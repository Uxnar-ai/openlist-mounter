package main

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	nifInfo  = 0x10
	niifInfo = 0x1
)

// 托盘只剩「一个会变色的图标」：
//   - 图标颜色反映挂载状态（灰/蓝/绿/黄/红）
//   - 点一下就把网页面板叫出来
//   - 没有任何右键菜单，所有操作（含开机自启、退出）都在网页里
func trayIconSize() int {
	sz, _, _ := procGetSystemMetrics.Call(smCxSmallIcon)
	if int32(sz) > 0 {
		return int(sz)
	}
	return 16
}

// ---------------------------------------------------------------- tray shell

func (a *app) addTrayIcon(th iconTheme) {
	a.trayIcon = th

	// 可能是 explorer 重启后的重新注册，先清掉旧句柄
	if a.nid.cbSize != 0 {
		procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&a.nid)))
		a.nid.cbSize = 0
	}
	if a.hIcon != 0 {
		procDestroyIcon.Call(uintptr(a.hIcon))
		a.hIcon = 0
	}

	a.hIcon = makeTrayIcon(trayIconSize(), th)
	if a.hIcon == 0 {
		a.hIcon = makeTrayIcon(16, th)
	}
	a.trayTheme = th.name

	nid := notifyIconData{
		hWnd:             a.hwnd,
		uID:              1,
		uFlags:           nifMessage | nifIcon | nifTip,
		uCallbackMessage: wmTray,
		hIcon:            a.hIcon,
	}
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	copyUTF16(nid.szTip[:], "OpenList 盘符挂载")
	procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	a.nid = nid
}

// setTrayTheme 按挂载状态换图标颜色（灰/绿/黄/红）。
func (a *app) setTrayTheme(th iconTheme) {
	if a.nid.cbSize == 0 || a.trayTheme == th.name {
		return
	}
	icon := makeTrayIcon(trayIconSize(), th)
	if icon == 0 {
		return
	}
	old := a.hIcon
	a.hIcon = icon
	a.trayTheme = th.name

	a.nid.uFlags = nifIcon
	a.nid.hIcon = icon
	procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&a.nid)))
	a.nid.uFlags = nifMessage | nifIcon | nifTip

	if old != 0 {
		procDestroyIcon.Call(uintptr(old))
	}
}

func (a *app) updateTrayTip(s string) {
	if a.nid.cbSize == 0 {
		return
	}
	a.nid.uFlags = nifTip
	copyUTF16(a.nid.szTip[:], s)
	procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&a.nid)))
	a.nid.uFlags = nifMessage | nifIcon | nifTip
}

// notify 弹一个气泡。没有菜单之后，这是唯一的即时反馈渠道，
// 内容同时也会写进网页日志。
func (a *app) notify(title, text string) {
	a.logf("[提示] %s：%s", title, text)
	if a.nid.cbSize == 0 {
		return
	}
	nid := a.nid
	nid.uFlags = nifInfo
	copyUTF16(nid.szInfoTitle[:], title)
	copyUTF16(nid.szInfo[:], text)
	nid.dwInfoFlags = niifInfo
	procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

func (a *app) removeTrayIcon() {
	if a.nid.cbSize != 0 {
		procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&a.nid)))
		a.nid.cbSize = 0
	}
	if a.hIcon != 0 {
		procDestroyIcon.Call(uintptr(a.hIcon))
		a.hIcon = 0
	}
}

// ---------------------------------------------------------------- autostart

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

func autoStartEnabled() bool {
	var h syscall.Handle
	r, _, _ := procRegOpenKeyExW.Call(0x80000001, ptrOf(u16(runKeyPath)), 0,
		keyRead, uintptr(unsafe.Pointer(&h)))
	if r != 0 {
		return false
	}
	defer procRegCloseKey.Call(uintptr(h))
	var typ, size uint32 = 0, 1024
	buf := make([]uint16, 512)
	r, _, _ = procRegQueryValueExW.Call(uintptr(h), ptrOf(u16("OpenListMounter")), 0,
		uintptr(unsafe.Pointer(&typ)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)))
	return r == 0 && size > 0
}

func setAutoStart(exePath string, enable bool) error {
	var h syscall.Handle
	r, _, _ := procRegCreateKeyExW.Call(0x80000001, ptrOf(u16(runKeyPath)), 0, 0, 0,
		uintptr(keyAllAccess), 0, uintptr(unsafe.Pointer(&h)), 0)
	if r != 0 {
		return syscall.Errno(r)
	}
	defer procRegCloseKey.Call(uintptr(h))

	if !enable {
		procRegDeleteValueW.Call(uintptr(h), ptrOf(u16("OpenListMounter")))
		return nil
	}
	val := `"` + exePath + `" --auto`
	u := syscall.StringToUTF16(val)
	r, _, _ = procRegSetValueExW.Call(uintptr(h), ptrOf(u16("OpenListMounter")), 0, 1,
		uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)*2))
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// autoStartCommand 读出 Run 键里的完整命令行（含引号和参数）。
// 用来判断「当年写进去的 exe 路径还是不是现在这个」。
func autoStartCommand() string {
	var h syscall.Handle
	if r, _, _ := procRegOpenKeyExW.Call(0x80000001, ptrOf(u16(runKeyPath)), 0,
		keyRead, uintptr(unsafe.Pointer(&h))); r != 0 {
		return ""
	}
	defer procRegCloseKey.Call(uintptr(h))

	buf := make([]uint16, 2048)
	size := uint32(len(buf) * 2)
	var typ uint32
	r, _, _ := procRegQueryValueExW.Call(uintptr(h), ptrOf(u16("OpenListMounter")), 0,
		uintptr(unsafe.Pointer(&typ)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)))
	if r != 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

func exePath() (string, error) { return os.Executable() }
