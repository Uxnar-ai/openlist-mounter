package main

import (
	"syscall"
	"unsafe"
)

var (
	modUser32   = syscall.NewLazyDLL("user32.dll")
	modGdi32    = syscall.NewLazyDLL("gdi32.dll")
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")
	modMpr      = syscall.NewLazyDLL("mpr.dll")
	modAdvapi32 = syscall.NewLazyDLL("advapi32.dll")
	modShell32  = syscall.NewLazyDLL("shell32.dll")
	modCrypt32  = syscall.NewLazyDLL("crypt32.dll")
)

var (
	// user32
	procRegisterClassExW  = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW   = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW    = modUser32.NewProc("DefWindowProcW")
	procDestroyWindow     = modUser32.NewProc("DestroyWindow")
	procShowWindow        = modUser32.NewProc("ShowWindow")
	procGetMessageW       = modUser32.NewProc("GetMessageW")
	procTranslateMessage  = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW  = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage   = modUser32.NewProc("PostQuitMessage")
	procPostMessageW      = modUser32.NewProc("PostMessageW")
	procSendMessageW      = modUser32.NewProc("SendMessageW")
	procMessageBoxW       = modUser32.NewProc("MessageBoxW")
	procLoadCursorW       = modUser32.NewProc("LoadCursorW")
	procGetDC             = modUser32.NewProc("GetDC")
	procReleaseDC         = modUser32.NewProc("ReleaseDC")
	procSetProcessDPICtx  = modUser32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware = modUser32.NewProc("SetProcessDPIAware")
	procGetSystemMetrics  = modUser32.NewProc("GetSystemMetrics")
	procFindWindowW       = modUser32.NewProc("FindWindowW")
	procSetForegroundWindow = modUser32.NewProc("SetForegroundWindow")
	procSendMessageTimeout = modUser32.NewProc("SendMessageTimeoutW")
	procChangeMsgFilterEx  = modUser32.NewProc("ChangeWindowMessageFilterEx")
	procCreateIconIndirect = modUser32.NewProc("CreateIconIndirect")
	procDestroyIcon       = modUser32.NewProc("DestroyIcon")
	procRegisterWindowMsg = modUser32.NewProc("RegisterWindowMessageW")

	// gdi32
	procGetDeviceCaps   = modGdi32.NewProc("GetDeviceCaps")
	procCreateDIBSection = modGdi32.NewProc("CreateDIBSection")
	procCreateBitmap    = modGdi32.NewProc("CreateBitmap")
	procDeleteObject    = modGdi32.NewProc("DeleteObject")

	// kernel32
	procGetModuleHandleW = modKernel32.NewProc("GetModuleHandleW")
	procGetCurrentThreadID = modKernel32.NewProc("GetCurrentThreadId")
	procGetLogicalDrives = modKernel32.NewProc("GetLogicalDrives")
	procGetDriveTypeW    = modKernel32.NewProc("GetDriveTypeW")
	procLocalFree        = modKernel32.NewProc("LocalFree")
	procCreateMutexW     = modKernel32.NewProc("CreateMutexW")
	procCloseHandle      = modKernel32.NewProc("CloseHandle")

	// mpr
	procWNetAddConnection2W    = modMpr.NewProc("WNetAddConnection2W")
	procWNetCancelConnection2W = modMpr.NewProc("WNetCancelConnection2W")
	procWNetGetConnectionW     = modMpr.NewProc("WNetGetConnectionW")

	// advapi32
	procOpenSCManagerW       = modAdvapi32.NewProc("OpenSCManagerW")
	procOpenServiceW         = modAdvapi32.NewProc("OpenServiceW")
	procCloseServiceHandle   = modAdvapi32.NewProc("CloseServiceHandle")
	procStartServiceW        = modAdvapi32.NewProc("StartServiceW")
	procControlService       = modAdvapi32.NewProc("ControlService")
	procQueryServiceStatus   = modAdvapi32.NewProc("QueryServiceStatus")
	procChangeServiceConfigW = modAdvapi32.NewProc("ChangeServiceConfigW")
	procRegOpenKeyExW        = modAdvapi32.NewProc("RegOpenKeyExW")
	procRegCreateKeyExW      = modAdvapi32.NewProc("RegCreateKeyExW")
	procRegSetValueExW       = modAdvapi32.NewProc("RegSetValueExW")
	procRegQueryValueExW     = modAdvapi32.NewProc("RegQueryValueExW")
	procRegEnumKeyExW        = modAdvapi32.NewProc("RegEnumKeyExW")
	procRegDeleteValueW      = modAdvapi32.NewProc("RegDeleteValueW")
	procRegDeleteKeyW        = modAdvapi32.NewProc("RegDeleteKeyW")
	procRegCloseKey          = modAdvapi32.NewProc("RegCloseKey")

	// shell32
	procIsUserAnAdmin   = modShell32.NewProc("IsUserAnAdmin")
	procShellExecuteW   = modShell32.NewProc("ShellExecuteW")
	procShellNotifyIcon = modShell32.NewProc("Shell_NotifyIconW")
	procSHChangeNotify  = modShell32.NewProc("SHChangeNotify")

	// crypt32
	procCryptProtectData   = modCrypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = modCrypt32.NewProc("CryptUnprotectData")
)

const (
	swShowNormal = 1
	idcArrow     = 32512
	logPixelsX   = 88

	wmApp = 0x8000

	// CreateProcess 标志：完全不创建控制台窗口。
	// 只设 HideWindow 的话控制台仍会被创建，启动瞬间会闪一下黑框。
	createNoWindow = 0x08000000

	wmDestroy = 0x0002
	wmClose   = 0x0010

	smCxSmallIcon = 49
	smCxScreen    = 0
	smCyScreen    = 1

	mbOK           = 0x00000000
	mbYesNo        = 0x00000004
	mbIconWarning  = 0x00000030
	mbIconQuestion = 0x00000020
	mbIconInfo     = 0x00000040
	idYes          = 6

	driveNoRootDir   = 1
	driveRemote      = 4
	resourceTypeDisk = 1

	// 托盘
	nimAdd    = 0
	nimModify = 1
	nimDelete = 2
	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4

	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmContextMenu   = 0x007B

	// SendMessageTimeout
	smtoAbortIfHung = 0x0002
	wmNull         = 0x0000

	msgfltAllow = 1 // ChangeWindowMessageFilterEx: MSGFLT_ALLOW

	dibRGBColors = 0
	biRGB        = 0
	errorAlreadyExists = 183
)

type pointT struct {
	x, y int32
}

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     syscall.Handle
	hIcon         syscall.Handle
	hCursor       syscall.Handle
	hbrBackground syscall.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       syscall.Handle
}

type netResourceW struct {
	scope       uint32
	resType     uint32
	displayType uint32
	usage       uint32
	localName   *uint16
	remoteName  *uint16
	comment     *uint16
	provider    *uint16
}

type dataBlob struct {
	cbData uint32
	pbData *byte
}

type serviceStatus struct {
	dwServiceType             uint32
	dwCurrentState            uint32
	dwControlsAccepted        uint32
	dwWin32ExitCode           uint32
	dwServiceSpecificExitCode uint32
	dwCheckPoint              uint32
	dwWaitHint                uint32
}

type notifyIconData struct {
	cbSize           uint32
	hWnd             syscall.Handle
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            syscall.Handle
	szTip            [128]uint16
	dwState          uint32
	szState          [256]uint16
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         syscall.GUID
	hBalloonIcon     syscall.Handle
}

type iconInfo struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	hbmMask  syscall.Handle
	hbmColor syscall.Handle
}

type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type bitmapInfo struct {
	bmiHeader bitmapInfoHeader
	bmiColors [1]uint32
}

// ---------------------------------------------------------------- helpers

func u16(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		p, _ = syscall.UTF16PtrFromString("")
	}
	return p
}

func ptrOf(p *uint16) uintptr { return uintptr(unsafe.Pointer(p)) }

func getLastError() uintptr {
	r, _, _ := modKernel32.NewProc("GetLastError").Call()
	return r
}

func messageBox(owner syscall.Handle, text, caption string, flags uint32) int32 {
	r, _, _ := procMessageBoxW.Call(
		uintptr(owner), ptrOf(u16(text)), ptrOf(u16(caption)), uintptr(flags))
	return int32(r)
}

func sendMsg(h syscall.Handle, m uint32, w, l uintptr) uintptr {
	r, _, _ := procSendMessageW.Call(uintptr(h), uintptr(m), w, l)
	return r
}

func shellExec(verb, file, params string, show int32) error {
	r, _, _ := procShellExecuteW.Call(
		0, ptrOf(u16(verb)), ptrOf(u16(file)), ptrOf(u16(params)), 0, uintptr(show))
	if r <= 32 {
		return errno(r)
	}
	return nil
}

type errno uintptr

func (e errno) Error() string { return "ShellExecute 错误码 " + itoa(int(uintptr(e))) }

func isElevated() bool {
	r, _, _ := procIsUserAnAdmin.Call()
	return r != 0
}

func enableDPIAwareness() {
	if r, _, _ := procSetProcessDPICtx.Call(^uintptr(3)); r != 0 {
		return
	}
	procSetProcessDPIAware.Call()
}

func copyUTF16(dst []uint16, s string) {
	src := syscall.StringToUTF16(s)
	n := len(src)
	if n > len(dst) {
		n = len(dst)
		src[n-1] = 0
	}
	copy(dst[:n], src[:n])
}

// ---------------------------------------------------------------- win32 helpers

// shellWindow 返回任务栏窗口句柄；为 0 说明 shell（explorer）没在跑。
func shellWindow() syscall.Handle {
	h, _, _ := procFindWindowW.Call(ptrOf(u16("Shell_TrayWnd")), 0)
	return syscall.Handle(h)
}

// windowResponsive 判断旧实例是不是还能正常工作。
//
// 只测 WM_NULL 不够：卡在读配置锁的实例，界面线程照样能回 WM_NULL，
// 但所有真正要读状态的操作都会永远挡住。所以改发 wmSelfCheck，
// 让对端做一个必须拿锁的 snapshot。
func windowResponsive(hwnd syscall.Handle) bool {
	if hwnd == 0 {
		return false
	}
	var res uintptr
	r, _, _ := procSendMessageTimeout.Call(uintptr(hwnd), wmSelfCheck, 0, 0,
		smtoAbortIfHung, 2000, uintptr(unsafe.Pointer(&res)))
	return r != 0
}

// allowMsgFromLower 放行一条来自「较低完整性级别进程」的窗口消息。
//
// 程序会用管理员身份重启（装驱动 / 配置 WebDAV 客户端），此时本进程是 High，
// 而发托盘回调的 explorer 是 Medium。UIPI 默认会静默丢掉 Medium → High 的消息，
// 表现就是：托盘图标在，右键点了完全没反应，连日志都没有一行。
// 必须显式把自定义回调消息和 TaskbarCreated 加进窗口消息过滤白名单。
func allowMsgFromLower(hwnd syscall.Handle, msg uint32) error {
	if hwnd == 0 || msg == 0 {
		return nil
	}
	r, _, err := procChangeMsgFilterEx.Call(uintptr(hwnd), uintptr(msg), msgfltAllow, 0)
	if r == 0 {
		return err
	}
	return nil
}
