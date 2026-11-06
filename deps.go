package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// 依赖管理：rclone.exe 是本程序下载到自己数据目录的，删掉重下即可；
// WinFsp 是系统级驱动，安装/卸载都得动 HKLM 和 MSI，必须管理员权限。

const hklm = 0x80000002

const (
	depRcloneReinstall = "rclone-reinstall"
	depRcloneRemove    = "rclone-remove"
	depWinFspInstall   = "winfsp-install"
	depWinFspUninstall = "winfsp-uninstall"
)

type depInfo struct {
	RclonePath    string `json:"rclone_path"`
	RcloneOK      bool   `json:"rclone_ok"`
	RcloneMB      int64  `json:"rclone_mb"`
	WinFspOK      bool   `json:"winfsp_ok"`
	WinFspVersion string `json:"winfsp_version"`
	Elevated      bool   `json:"elevated"`
	Busy          bool   `json:"busy"`
}

var depBusy int32

func depStatus() depInfo {
	d := depInfo{
		RclonePath: rclonePath(),
		WinFspOK:   winfspInstalled(),
		Elevated:   isElevated(),
		Busy:       atomic.LoadInt32(&depBusy) != 0,
	}
	if st, err := os.Stat(d.RclonePath); err == nil && !st.IsDir() {
		d.RcloneMB = st.Size() >> 20
		d.RcloneOK = st.Size() > 10<<20
	}
	_, d.WinFspVersion, _ = winfspInfo()
	return d
}

// depLogf 独立进程（--dep）也要能写日志，所以不走 app.logf。
func depLogf(format string, args ...any) {
	line := time.Now().Format("15:04:05") + "  " + fmt.Sprintf(format, args...)
	f, err := os.OpenFile(filepath.Join(dataDir(), "log.txt"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	fmt.Fprintln(f, line)
	f.Close()
}

// ---------------------------------------------------------------- 注册表小工具

func regString(root uintptr, path, name string) string {
	var h syscall.Handle
	if r, _, _ := procRegOpenKeyExW.Call(root, ptrOf(u16(path)), 0, keyRead,
		uintptr(unsafe.Pointer(&h))); r != 0 {
		return ""
	}
	defer procRegCloseKey.Call(uintptr(h))

	size := uint32(2048)
	buf := make([]uint16, 1024)
	var typ uint32
	r, _, _ := procRegQueryValueExW.Call(uintptr(h), ptrOf(u16(name)), 0,
		uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)))
	if r != 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

func regSubKeys(root uintptr, path string) []string {
	var h syscall.Handle
	if r, _, _ := procRegOpenKeyExW.Call(root, ptrOf(u16(path)), 0, keyRead,
		uintptr(unsafe.Pointer(&h))); r != 0 {
		return nil
	}
	defer procRegCloseKey.Call(uintptr(h))

	var out []string
	for i := 0; ; i++ {
		buf := make([]uint16, 256)
		size := uint32(len(buf))
		r, _, _ := procRegEnumKeyExW.Call(uintptr(h), uintptr(i),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)),
			0, 0, 0, 0)
		if r != 0 {
			break
		}
		out = append(out, syscall.UTF16ToString(buf[:size]))
	}
	return out
}

// winfspInfo 从「程序和功能」列表里找出 WinFsp 的名字/版本/卸载命令。
func winfspInfo() (name, version, uninstall string) {
	roots := []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
		`SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`,
	}
	for _, root := range roots {
		for _, key := range regSubKeys(hklm, root) {
			full := root + `\` + key
			n := regString(hklm, full, "DisplayName")
			if n == "" || !strings.Contains(strings.ToLower(n), "winfsp") {
				continue
			}
			return n, regString(hklm, full, "DisplayVersion"), regString(hklm, full, "UninstallString")
		}
	}
	return "", "", ""
}

// ---------------------------------------------------------------- 动作实现

func runInstaller(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

func removeRclone() error {
	exe := rclonePath()
	if _, err := os.Stat(exe); os.IsNotExist(err) {
		return nil
	}
	// 正在挂载时 exe 被锁住，先删不掉：调用方负责先停挂载
	if err := os.Remove(exe); err != nil {
		return fmt.Errorf("删除 rclone.exe 失败（可能还有挂载在用它）：%v", err)
	}
	return nil
}

func installWinFsp() error {
	if winfspInstalled() {
		return nil
	}
	if !isElevated() {
		return errors.New("需要管理员权限")
	}
	depLogf("[依赖] 开始下载 WinFsp 安装包…")
	msi := filepath.Join(os.TempDir(), "olm-winfsp.msi")
	defer os.Remove(msi)
	if err := httpDownload(winfspMSIURL, msi); err != nil {
		return fmt.Errorf("下载 WinFsp 失败：%v", err)
	}
	depLogf("[依赖] 正在静默安装 WinFsp…")
	if err := runInstaller("msiexec", "/i", msi, "/quiet", "/norestart"); err != nil {
		return fmt.Errorf("安装 WinFsp 失败：%v", err)
	}
	if !winfspInstalled() {
		return errors.New("安装完成但没检测到 winfsp-x64.dll")
	}
	depLogf("[依赖] WinFsp 安装完成")
	return nil
}

func uninstallWinFsp() error {
	if !isElevated() {
		return errors.New("需要管理员权限")
	}
	if !winfspInstalled() {
		return errors.New("没有检测到 WinFsp")
	}
	_, _, uninst := winfspInfo()
	depLogf("[依赖] 卸载命令：%s", uninst)

	// 「程序和功能」里的写法通常是 MsiExec.exe /I{GUID} 或 /X{GUID}
	i := strings.Index(uninst, "{")
	j := strings.Index(uninst, "}")
	if i >= 0 && j > i {
		guid := uninst[i : j+1]
		if err := runInstaller("msiexec", "/x", guid, "/quiet", "/norestart"); err != nil {
			return fmt.Errorf("卸载 WinFsp 失败（可能需要重启后再卸）：%v", err)
		}
	} else if uninst != "" {
		if err := runInstaller(uninst, "/S"); err != nil {
			return fmt.Errorf("卸载 WinFsp 失败：%v", err)
		}
	} else {
		return errors.New("没找到 WinFsp 的卸载项，请到「程序和功能」里手动卸载")
	}

	for i := 0; i < 20 && winfspInstalled(); i++ {
		time.Sleep(300 * time.Millisecond)
	}
	if winfspInstalled() {
		return errors.New("卸载命令已执行，但驱动还在，通常需要重启后再生效")
	}
	depLogf("[依赖] WinFsp 已卸载")
	return nil
}

// depHelper 需要管理员的动作：用 runas 拉起自己的 --dep 子命令（会弹 UAC）。
func depHelper(action string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return shellExec("runas", exe, "--dep "+action, swShowNormal)
}

// runDepAction 在运行中的实例里执行动作。需要管理员且当前不是管理员时，
// 交给 --dep 子进程去做（UAC）。
func (a *app) runDepAction(action string) error {
	switch action {
	case depRcloneRemove, depRcloneReinstall:
		// 有 rclone 挂载在用时先停掉，否则 exe 被锁
		a.stopAllRclone()
		if err := removeRclone(); err != nil {
			return err
		}
		a.logf("[依赖] 已删除 rclone.exe")
		if action == depRcloneRemove {
			return nil
		}
		if _, err := a.ensureRclone(); err != nil {
			return err
		}
		a.logf("[依赖] rclone 已重新下载")
		return nil
	case depWinFspInstall, depWinFspUninstall:
		if !isElevated() {
			a.logf("[依赖] 需要管理员权限，正在请求提权…")
			return depHelper(action)
		}
		var err error
		if action == depWinFspInstall {
			err = installWinFsp()
		} else {
			err = uninstallWinFsp()
		}
		if err != nil {
			a.logf("[依赖] %v", err)
		}
		return err
	}
	return errors.New("未知操作")
}

// depActionMain 处理 `--dep <动作>`：给 runas 拉起的提权子进程用，做完就退。
func depActionMain(action string) int {
	switch action {
	case depWinFspInstall:
		depLogf("[依赖] 提权子进程：安装 WinFsp")
		if err := installWinFsp(); err != nil {
			depLogf("[依赖] 安装失败：%v", err)
			messageBox(0, err.Error(), "安装 WinFsp 失败", mbOK|mbIconWarning)
			return 1
		}
		messageBox(0, "WinFsp 已安装完成，可以回面板继续挂载。", "完成", mbOK|mbIconInfo)
		return 0
	case depWinFspUninstall:
		depLogf("[依赖] 提权子进程：卸载 WinFsp")
		if err := uninstallWinFsp(); err != nil {
			depLogf("[依赖] 卸载失败：%v", err)
			messageBox(0, err.Error(), "卸载 WinFsp 失败", mbOK|mbIconWarning)
			return 1
		}
		messageBox(0, "WinFsp 已卸载。重启后更彻底。", "完成", mbOK|mbIconInfo)
		return 0
	case depRcloneReinstall:
		depLogf("[依赖] 提权子进程：重装 rclone")
		if err := removeRclone(); err != nil {
			depLogf("[依赖] %v", err)
			return 1
		}
		if _, err := ensureRcloneStanalone(); err != nil {
			depLogf("[依赖] 下载失败：%v", err)
			return 1
		}
		return 0
	}
	depLogf("[依赖] 未知动作：%s", action)
	return 2
}

// ensureRcloneStanalone 给 --dep 子进程用（没有 app 上下文）。
func ensureRcloneStanalone() (string, error) {
	exe := rclonePath()
	if st, err := os.Stat(exe); err == nil && st.Size() > 10<<20 {
		return exe, nil
	}
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return "", err
	}
	tmp := filepath.Join(os.TempDir(), "olm-rclone.zip")
	defer os.Remove(tmp)
	var lastErr error
	for _, src := range rcloneSources {
		depLogf("[依赖] 下载源：%s", src)
		if err := httpDownload(src, tmp); err != nil {
			lastErr = err
			continue
		}
		if err := unzipRclone(tmp, exe); err != nil {
			lastErr = err
			continue
		}
		depLogf("[依赖] rclone 已就绪")
		return exe, nil
	}
	return "", fmt.Errorf("所有下载源都失败：%v", lastErr)
}
