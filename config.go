package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// folderMount 是 OpenList 根下一个顶层目录的挂载记录。
// OpenList 的一个「存储」在 WebDAV 里就是一个顶层目录，对应一个盘符。
type folderMount struct {
	Name     string `json:"name"`               // 目录名（= OpenList 里的挂载名）
	Drive    string `json:"drive"`              // 分配到的盘符，如 "Z:"
	Enabled  bool   `json:"enabled"`            // 用户是否希望它被挂载
	Backend  string `json:"backend,omitempty"`  // 空 = 跟随全局
	Capacity string `json:"capacity,omitempty"` // 盘符显示容量，如 "2T"；空 = 用默认值
}

// folderMemory 记住「服务器上暂时消失的目录」的设置。
// 目录在服务器上被删掉/改名时，盘符、容量、通道不能跟着丢，
// 等它再出现（同名）就按这里的记忆还原。
type folderMemory struct {
	Name     string `json:"name"`
	Drive    string `json:"drive,omitempty"`
	Backend  string `json:"backend,omitempty"`
	Capacity string `json:"capacity,omitempty"`
	Enabled  bool   `json:"enabled"`
}

const (
	backendRclone = "rclone"
	backendWebDAV = "webdav"

	// rememberedLimit 是「消失目录」记忆的上限，防止长期使用后无限膨胀。
	rememberedLimit = 80
)

type appConfig struct {
	URL        string         `json:"url"`
	User       string         `json:"user"`
	PassEnc    string         `json:"pass_enc"`
	Folders    []folderMount  `json:"folders"`
	Remembered []folderMemory `json:"remembered,omitempty"`
	AutoStart  bool           `json:"auto_start"`
	Backend    string         `json:"backend"` // 全局默认后端
}

func (c *appConfig) folder(name string) (int, *folderMount) {
	for i := range c.Folders {
		if c.Folders[i].Name == name {
			return i, &c.Folders[i]
		}
	}
	return -1, nil
}

// memory 返回某个目录的历史记忆。
func (c *appConfig) memory(name string) (folderMemory, bool) {
	for _, m := range c.Remembered {
		if m.Name == name {
			return m, true
		}
	}
	return folderMemory{}, false
}

// remember 记下一个消失的目录设置；已有的按名字覆盖。
func (c *appConfig) remember(f folderMount) {
	m := folderMemory{Name: f.Name, Drive: f.Drive, Backend: f.Backend, Capacity: f.Capacity, Enabled: f.Enabled}
	for i := range c.Remembered {
		if c.Remembered[i].Name == f.Name {
			c.Remembered[i] = m
			return
		}
	}
	c.Remembered = append(c.Remembered, m)
	if len(c.Remembered) > rememberedLimit {
		c.Remembered = c.Remembered[len(c.Remembered)-rememberedLimit:]
	}
}

// forget 在目录重新出现后清掉记忆，避免同一目录留两份。
func (c *appConfig) forget(name string) {
	out := c.Remembered[:0]
	for _, m := range c.Remembered {
		if m.Name != name {
			out = append(out, m)
		}
	}
	c.Remembered = out
}

func (c *appConfig) usedDrives(except string) map[string]bool {
	used := map[string]bool{}
	for _, f := range c.Folders {
		if f.Drive != "" && f.Name != except {
			used[strings.ToUpper(f.Drive)] = true
		}
	}
	return used
}

// ---------------------------------------------------------------- 落盘位置

var (
	dataDirOnce sync.Once
	dataDirPath string
)

// dataDir 是全项目唯一的落盘根目录。
//
// 优先级：exe 同目录（便携，拷走整个目录就能带走配置）→ %APPDATA%\OpenListMounter。
// exe 放在 Program Files 这类只读目录时，探测写权限失败会自动回落。
func dataDir() string {
	dataDirOnce.Do(func() { dataDirPath = resolveDataDir() })
	return dataDirPath
}

func resolveDataDir() string {
	if d := portableDir(); d != "" {
		return d
	}
	base := os.Getenv("APPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "OpenListMounter")
}

func portableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	if !dirWritable(dir) {
		return ""
	}
	return dir
}

// dirWritable 用「建一个临时文件再删掉」判断目录是否可写。
// 只看权限位不可靠，Program Files 下真正的拦截来自 UAC 虚拟化/ACL。
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".olm-wtest-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

func configPath() string { return filepath.Join(dataDir(), "config.json") }

// legacyConfigPath 是旧版本写死的位置，只用于一次性迁移。
func legacyConfigPath() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		return ""
	}
	return filepath.Join(base, "OpenListMounter", "config.json")
}

// ensureDataDir 在启动最早期调用：确保目录存在，并把旧配置搬过来。
func ensureDataDir() {
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		depLogf("[配置] 创建数据目录失败：%v", err)
	}
	migrateLegacyConfig()
}

// migrateLegacyConfig 把 %APPDATA% 下的旧配置复制到便携目录。
//
// 用复制不用 move：出问题还能回退。DPAPI 密文绑定当前用户，
// 复制过来照样能解开，不用重新输密码。
func migrateLegacyConfig() {
	dst := configPath()
	if _, err := os.Stat(dst); err == nil {
		return
	}
	src := legacyConfigPath()
	if src == "" || strings.EqualFold(src, dst) {
		return
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return
	}
	if err := writeFileAtomic(dst, data, 0o600); err != nil {
		depLogf("[配置] 从 %s 迁移失败：%v", src, err)
		return
	}
	depLogf("[配置] 已把旧配置从 %s 复制到 %s", src, dst)
}

// ---------------------------------------------------------------- 读写

// loadConfig 读配置。解析失败时把坏文件改名留证，而不是静默当空配置——
// 否则下一次 persist() 就会把唯一的原件覆盖掉，设置永久丢失。
func loadConfig() appConfig {
	var c appConfig
	p := configPath()
	data, err := os.ReadFile(p)
	if err != nil {
		return c
	}
	if err := json.Unmarshal(data, &c); err != nil {
		bad := fmt.Sprintf("%s.bad-%s", p, time.Now().Format("20060102-150405"))
		if renameErr := os.Rename(p, bad); renameErr != nil {
			depLogf("[配置] 解析失败：%v；备份坏文件也失败：%v", err, renameErr)
		} else {
			depLogf("[配置] 解析失败：%v；坏文件已另存为 %s，本次按空配置启动", err, bad)
		}
		return appConfig{}
	}
	return c
}

// saveConfig 原子写：写临时文件 → fsync → rename 覆盖。
// 直接覆盖的话，断电/崩溃卡在写入中间会留下半个 JSON，下次启动就是「配置全没了」。
func saveConfig(c appConfig) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(configPath(), data, 0o600)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ---------------------------------------------------------------- DPAPI

const cryptProtectUIForbidden = 0x1

func dpapiProtect(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	in := []byte(plain)
	inBlob := dataBlob{cbData: uint32(len(in)), pbData: &in[0]}
	var outBlob dataBlob
	r, _, _ := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&inBlob)), 0, 0, 0, 0,
		cryptProtectUIForbidden, uintptr(unsafe.Pointer(&outBlob)))
	if r == 0 {
		return "", syscall.Errno(getLastError())
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(outBlob.pbData)))
	enc := unsafe.Slice(outBlob.pbData, outBlob.cbData)
	return base64.StdEncoding.EncodeToString(enc), nil
}

func dpapiUnprotect(enc string) (string, error) {
	enc = strings.TrimSpace(enc)
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw) == 0 {
		return "", err
	}
	inBlob := dataBlob{cbData: uint32(len(raw)), pbData: &raw[0]}
	var outBlob dataBlob
	r, _, _ := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&inBlob)), 0, 0, 0, 0,
		cryptProtectUIForbidden, uintptr(unsafe.Pointer(&outBlob)))
	if r == 0 {
		return "", syscall.Errno(getLastError())
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(outBlob.pbData)))
	out := unsafe.Slice(outBlob.pbData, outBlob.cbData)
	return string(out), nil
}
