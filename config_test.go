package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cfgTestDir 把落盘根目录指到临时目录，避免测试写坏真实配置。
// dataDir() 只解析一次，所以这里先把 once 消耗掉再直接赋值。
func cfgTestDir(t *testing.T) string {
	t.Helper()
	if dataDirPath == "" {
		d, err := os.MkdirTemp("", "olm-cfg-")
		if err != nil {
			t.Fatalf("创建临时目录失败：%v", err)
		}
		dataDirOnce.Do(func() {})
		dataDirPath = d
	}
	// 每个测试用干净的子目录，互不干扰
	d, err := os.MkdirTemp(dataDirPath, "case-")
	if err != nil {
		t.Fatalf("创建用例目录失败：%v", err)
	}
	dataDirPath = d
	return d
}

func TestSaveLoadConfigRoundTrip(t *testing.T) {
	dir := cfgTestDir(t)

	cfg := appConfig{
		URL:       "http://192.168.1.10:5244/dav",
		User:      "u",
		PassEnc:   "xxx",
		AutoStart: true,
		Backend:   backendRclone,
		Folders: []folderMount{
			{Name: "影片", Drive: "Z:", Enabled: false, Capacity: "2T"},
			{Name: "剧集", Drive: "Y:", Enabled: true, Backend: backendWebDAV},
		},
	}
	if err := saveConfig(cfg); err != nil {
		t.Fatalf("保存失败：%v", err)
	}

	// 不能留下 .tmp（原子写的中间产物）
	if _, err := os.Stat(filepath.Join(dir, "config.json.tmp")); err == nil {
		t.Error("残留了 config.json.tmp")
	}

	got := loadConfig()
	if got.URL != cfg.URL || got.User != cfg.User || got.PassEnc != cfg.PassEnc {
		t.Errorf("服务器信息没读回来：%+v", got)
	}
	if !got.AutoStart {
		t.Error("auto_start 没落盘")
	}
	if len(got.Folders) != 2 {
		t.Fatalf("目录数量不对：%d", len(got.Folders))
	}
	// 关键：用户取消勾选的状态必须原样回来（BUG 2.1 的表现就是这里变成 true）
	if got.Folders[0].Enabled {
		t.Error("停用状态没保持住")
	}
	if got.Folders[0].Capacity != "2T" || got.Folders[0].Drive != "Z:" {
		t.Errorf("容量/盘符丢了：%+v", got.Folders[0])
	}
	if got.Folders[1].Backend != backendWebDAV {
		t.Errorf("通道设置丢了：%+v", got.Folders[1])
	}
}

func TestLoadConfigQuarantinesCorruptFile(t *testing.T) {
	dir := cfgTestDir(t)
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{"url":"http://x","folders":[`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := loadConfig()
	if got.URL != "" {
		t.Errorf("坏配置应该按空配置返回，实际拿到 %+v", got)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("坏文件应该被移走，不该留在原地被下次 persist 覆盖")
	}
	entries, _ := os.ReadDir(dir)
	found := ""
	for _, e := range entries {
		if strings.Contains(e.Name(), "config.json.bad-") {
			found = e.Name()
		}
	}
	if found == "" {
		t.Fatalf("没有生成 config.json.bad-* 备份，目录里只有：%v", entries)
	}

	// 坏文件内容必须原样保留，否则「可恢复」就是空话
	data, err := os.ReadFile(filepath.Join(dir, found))
	if err != nil || !strings.Contains(string(data), "folders") {
		t.Errorf("坏文件备份内容不对：%q err=%v", string(data), err)
	}

	// 之后正常保存不能再出问题
	if err := saveConfig(appConfig{URL: "http://new"}); err != nil {
		t.Fatalf("坏文件之后无法保存：%v", err)
	}
	if got := loadConfig(); got.URL != "http://new" {
		t.Errorf("保存后读回不对：%+v", got)
	}
}

func TestFolderMemoryKeepsSettings(t *testing.T) {
	var c appConfig
	c.remember(folderMount{Name: "影片", Drive: "Z:", Capacity: "2T", Backend: backendWebDAV, Enabled: false})
	c.remember(folderMount{Name: "影片", Drive: "X:", Capacity: "1T"}) // 同名覆盖

	m, ok := c.memory("影片")
	if !ok {
		t.Fatal("记忆没写进去")
	}
	if m.Drive != "X:" || m.Capacity != "1T" {
		t.Errorf("同名应该覆盖而不是追加：%+v", m)
	}
	if len(c.Remembered) != 1 {
		t.Errorf("记忆条数不对：%d", len(c.Remembered))
	}

	c.forget("影片")
	if _, ok := c.memory("影片"); ok {
		t.Error("目录重新出现后应该清掉记忆")
	}

	// 上限要生效，否则长期用下去配置无限膨胀
	for i := 0; i < rememberedLimit+20; i++ {
		c.remember(folderMount{Name: string(rune('a' + i%26))})
	}
	if len(c.Remembered) > rememberedLimit {
		t.Errorf("记忆条数超限：%d > %d", len(c.Remembered), rememberedLimit)
	}
}

func TestPortableDirIsUsable(t *testing.T) {
	// 测试二进制所在目录必然可写，此时 dataDir 必须等于 exe 同目录
	exe, err := os.Executable()
	if err != nil {
		t.Skip("拿不到 exe 路径")
	}
	if d := portableDir(); d != "" && !strings.EqualFold(d, filepath.Dir(exe)) {
		t.Errorf("便携目录应该是 exe 所在目录：%s vs %s", d, filepath.Dir(exe))
	}
	if !dirWritable(filepath.Dir(exe)) {
		t.Skip("测试目录不可写，跳过")
	}
	if d := portableDir(); d == "" {
		t.Error("可写目录却判定为非便携，会退回 %APPDATA%")
	}
}
