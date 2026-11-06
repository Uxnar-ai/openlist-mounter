package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// errNeedAdmin 表示当前操作需要管理员权限才能完成系统级配置。
var errNeedAdmin = errors.New("需要管理员权限")

const (
	scManagerConnect      = 0x0001
	serviceQueryStatus    = 0x0004
	serviceStart          = 0x0010
	serviceChangeConfig   = 0x0002
	serviceAllAccess      = 0xF01FF
	serviceNoChange       = 0xFFFFFFFF
	serviceAutoStart      = 0x00000002
	serviceControlStop    = 0x00000001
	serviceStopped        = 0x00000001
	serviceStartPending   = 0x00000002
	serviceStopPending    = 0x00000003
	serviceRunning        = 0x00000004
	keyRead               = 0x20019
	keyAllAccess          = 0xF003F
	regDword              = 4
	webClientParamsKey    = `SYSTEM\CurrentControlSet\Services\WebClient\Parameters`
	errorServiceNotActive = 1062
	errorServiceExists    = 1056
	errorAlreadyRunning   = 1056
	maxFileSize           = 0xFFFFFFFF
	maxFileAttributes     = 10000000
)

func itoa(i int) string { return strconv.Itoa(i) }

// ---------------------------------------------------------------- registry

func regReadDword(hKey syscall.Handle, name string) (uint32, bool) {
	var typ, size uint32 = 0, 4
	var val uint32
	r, _, _ := procRegQueryValueExW.Call(
		uintptr(hKey), ptrOf(u16(name)), 0,
		uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&val)),
		uintptr(unsafe.Pointer(&size)))
	if r != 0 || typ != regDword {
		return 0, false
	}
	return val, true
}

func regWriteDword(hKey syscall.Handle, name string, val uint32) error {
	r, _, _ := procRegSetValueExW.Call(
		uintptr(hKey), ptrOf(u16(name)), 0, regDword,
		uintptr(unsafe.Pointer(&val)), 4)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

func openParamsKey(access uint32) (syscall.Handle, error) {
	var h syscall.Handle
	r, _, _ := procRegOpenKeyExW.Call(
		0x80000002, // HKEY_LOCAL_MACHINE
		ptrOf(u16(webClientParamsKey)),
		0, uintptr(access), uintptr(unsafe.Pointer(&h)))
	if r != 0 {
		return 0, syscall.Errno(r)
	}
	return h, nil
}

// webClientParams 是当前系统上 WebDAV 客户端的相关配置。
type webClientParams struct {
	basicAuth     uint32
	fileSize      uint32
	fileAttrLimit uint32
}

func readWebClientParams() webClientParams {
	var p webClientParams
	h, err := openParamsKey(keyRead)
	if err != nil {
		return p
	}
	defer procRegCloseKey.Call(uintptr(h))
	if v, ok := regReadDword(h, "BasicAuthLevel"); ok {
		p.basicAuth = v
	}
	if v, ok := regReadDword(h, "FileSizeLimitInBytes"); ok {
		p.fileSize = v
	}
	if v, ok := regReadDword(h, "FileAttributesLimitInBytes"); ok {
		p.fileAttrLimit = v
	}
	return p
}

// ---------------------------------------------------------------- service

func openWebClientService(access uint32) (uintptr, uintptr, error) {
	hSCM, _, _ := procOpenSCManagerW.Call(0, 0, uintptr(scManagerConnect))
	if hSCM == 0 {
		return 0, 0, syscall.Errno(5)
	}
	hSvc, _, _ := procOpenServiceW.Call(hSCM, ptrOf(u16("WebClient")), uintptr(access))
	if hSvc == 0 {
		procCloseServiceHandle.Call(hSCM)
		return 0, 0, syscall.Errno(1060)
	}
	return hSCM, hSvc, nil
}

func queryServiceState() (uint32, error) {
	hSCM, hSvc, err := openWebClientService(serviceQueryStatus)
	if err != nil {
		return 0, err
	}
	defer procCloseServiceHandle.Call(hSvc)
	defer procCloseServiceHandle.Call(hSCM)

	var st serviceStatus
	r, _, _ := procQueryServiceStatus.Call(
		uintptr(hSvc), uintptr(unsafe.Pointer(&st)))
	if r == 0 {
		return 0, syscall.Errno(5)
	}
	return st.dwCurrentState, nil
}

func webClientRunning() bool {
	st, err := queryServiceState()
	return err == nil && st == serviceRunning
}

func startWebClient() error {
	hSCM, hSvc, err := openWebClientService(serviceStart | serviceQueryStatus | serviceChangeConfig)
	if err != nil {
		return err
	}
	defer procCloseServiceHandle.Call(hSvc)
	defer procCloseServiceHandle.Call(hSCM)

	// 启动类型改成自动，否则重启后又回到 Manual。
	procChangeServiceConfigW.Call(
		uintptr(hSvc), uintptr(serviceNoChange), uintptr(serviceAutoStart),
		uintptr(serviceNoChange), 0, 0, 0, 0, 0, 0, 0)

	r, _, _ := procStartServiceW.Call(uintptr(hSvc), 0, 0)
	if r == 0 {
		code := syscall.Errno(getLastError())
		if code == errorAlreadyRunning {
			return nil
		}
		if code == syscall.ERROR_ACCESS_DENIED {
			return errNeedAdmin
		}
		return code
	}
	// 等待进入 RUNNING。
	for i := 0; i < 40; i++ {
		st, _ := queryServiceState()
		if st == serviceRunning {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("WebClient 服务启动超时")
}

func stopWebClient() error {
	hSCM, hSvc, err := openWebClientService(serviceStart | serviceQueryStatus)
	if err != nil {
		return err
	}
	defer procCloseServiceHandle.Call(hSvc)
	defer procCloseServiceHandle.Call(hSCM)

	var st serviceStatus
	r, _, _ := procControlService.Call(
		uintptr(hSvc), uintptr(serviceControlStop), uintptr(unsafe.Pointer(&st)))
	if r == 0 {
		code := syscall.Errno(getLastError())
		if code == errorServiceNotActive {
			return nil
		}
		if code == syscall.ERROR_ACCESS_DENIED {
			return errNeedAdmin
		}
		return code
	}
	for i := 0; i < 60; i++ {
		s, _ := queryServiceState()
		if s == serviceStopped {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return errors.New("WebClient 服务停止超时")
}

// ---------------------------------------------------------------- prepare

// systemStatus 描述本机 WebDAV 客户端是否已满足挂载条件。
type systemStatus struct {
	running       bool
	basicAuth     uint32
	fileSize      uint32
	fileAttrLimit uint32
	needsFix      bool
	needsRestart  bool
	reasons       []string
}

func checkSystem(useHTTP bool) systemStatus {
	var s systemStatus
	p := readWebClientParams()
	s.basicAuth = p.basicAuth
	s.fileSize = p.fileSize
	s.fileAttrLimit = p.fileAttrLimit
	s.running = webClientRunning()

	wantAuth := uint32(1)
	if useHTTP {
		wantAuth = 2
	}
	if p.basicAuth < wantAuth {
		s.needsFix = true
		if useHTTP {
			s.reasons = append(s.reasons, fmt.Sprintf(
				"BasicAuthLevel=%d，明文 HTTP 需要 ≥2", p.basicAuth))
		} else {
			s.reasons = append(s.reasons, fmt.Sprintf(
				"BasicAuthLevel=%d，需要 ≥1", p.basicAuth))
		}
	}
	if p.fileSize < maxFileSize {
		s.needsFix = true
		s.reasons = append(s.reasons, fmt.Sprintf(
			"单文件上限 %s，需要提升到 4GB", humanBytes(uint64(p.fileSize))))
	}
	if p.fileAttrLimit < maxFileAttributes {
		s.needsFix = true
		s.reasons = append(s.reasons, fmt.Sprintf(
			"目录属性上限 %s，大目录会列不全", humanBytes(uint64(p.fileAttrLimit))))
	}
	if !s.running {
		s.needsFix = true
		s.reasons = append(s.reasons, "WebClient 服务未运行")
	}
	return s
}

// applySystemConfig 修正注册表参数并（必要时）重启 WebClient 服务。
// 需要管理员权限，权限不足时返回 errNeedAdmin。
func applySystemConfig(useHTTP bool, log func(string)) error {
	p := readWebClientParams()
	wantAuth := uint32(1)
	if useHTTP {
		wantAuth = 2
	}
	needWrite := p.basicAuth < wantAuth || p.fileSize < maxFileSize || p.fileAttrLimit < maxFileAttributes

	changed := false
	if needWrite {
		h, err := openParamsKey(keyAllAccess)
		if err != nil {
			if errnoErr(err) == syscall.ERROR_ACCESS_DENIED {
				return errNeedAdmin
			}
			return err
		}
		auth := p.basicAuth
		if auth < wantAuth {
			auth = wantAuth
			if err := regWriteDword(h, "BasicAuthLevel", auth); err != nil {
				procRegCloseKey.Call(uintptr(h))
				return err
			}
			log(fmt.Sprintf("已设置 BasicAuthLevel = %d", auth))
			changed = true
		}
		if p.fileSize < maxFileSize {
			if err := regWriteDword(h, "FileSizeLimitInBytes", maxFileSize); err != nil {
				procRegCloseKey.Call(uintptr(h))
				return err
			}
			log("已解除单文件 50MB 限制（提升到 4GB）")
			changed = true
		}
		if p.fileAttrLimit < maxFileAttributes {
			if err := regWriteDword(h, "FileAttributesLimitInBytes", maxFileAttributes); err != nil {
				procRegCloseKey.Call(uintptr(h))
				return err
			}
			log("已提高目录属性上限，避免大目录列不全")
			changed = true
		}
		procRegCloseKey.Call(uintptr(h))
	}

	if changed && webClientRunning() {
		log("重启 WebClient 服务使配置生效…")
		if err := stopWebClient(); err != nil {
			return err
		}
	}

	if !webClientRunning() {
		log("启动 WebClient 服务…")
		if err := startWebClient(); err != nil {
			return err
		}
	}
	return nil
}

func errnoErr(err error) syscall.Errno {
	var e syscall.Errno
	if errors.As(err, &e) {
		return e
	}
	return 0
}

func humanBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d B", n)
}

// ---------------------------------------------------------------- drives

func letterFromIndex(i int) string { return string(rune('A'+i)) + ":" }

func allDrives() []string {
	mask, _, _ := procGetLogicalDrives.Call()
	var out []string
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, letterFromIndex(i))
		}
	}
	return out
}

func freeDriveLetters() []string {
	mask, _, _ := procGetLogicalDrives.Call()
	var out []string
	for i := 2; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			out = append(out, letterFromIndex(i))
		}
	}
	return out
}

func driveExists(letter string) bool {
	p := u16(letter + "\\")
	r, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(p)))
	return uint32(r) != driveNoRootDir
}

func driveType(letter string) uint32 {
	p := u16(letter + "\\")
	r, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(p)))
	return uint32(r)
}

func driveRemotePath(letter string) string {
	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	r, _, _ := procWNetGetConnectionW.Call(
		uintptr(unsafe.Pointer(u16(letter))),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)))
	if r != 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:size])
}

// ---------------------------------------------------------------- mount

func netAddConnection(local, remote, user, pass string) error {
	nr := netResourceW{
		resType:    resourceTypeDisk,
		localName:  u16(local),
		remoteName: u16(remote),
	}
	var passPtr, userPtr uintptr
	if pass != "" {
		passPtr = ptrOf(u16(pass))
	}
	if user != "" {
		userPtr = ptrOf(u16(user))
	}
	r, _, _ := procWNetAddConnection2W.Call(
		uintptr(unsafe.Pointer(&nr)), passPtr, userPtr, 0)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

func netCancelConnection(local string) error {
	p := u16(local)
	r, _, _ := procWNetCancelConnection2W.Call(uintptr(unsafe.Pointer(p)), 0, 1)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// webdavUNC 把 WebDAV URL 转成 Windows 重定向器认识的 UNC 形式。
// https://host:443/dav  ->  \\host@SSL\dav
// http://host:5244/dav  ->  \\host@5244\dav
func webdavUNC(rawURL string) string {
	s := rawURL
	scheme := "http"
	if i := strings.Index(s, "://"); i >= 0 {
		scheme = strings.ToLower(s[:i])
		s = s[i+3:]
	}
	hostPort := s
	path := ""
	if i := strings.IndexByte(s, '/'); i >= 0 {
		hostPort = s[:i]
		path = s[i:]
	}
	host := hostPort
	port := ""
	if i := strings.LastIndex(hostPort, ":"); i >= 0 {
		host = hostPort[:i]
		port = hostPort[i+1:]
	}
	path = strings.ReplaceAll(path, "/", `\`)

	var b strings.Builder
	b.WriteString(`\\`)
	b.WriteString(host)
	if scheme == "https" {
		b.WriteString("@SSL")
		if port != "" && port != "443" {
			b.WriteString("@")
			b.WriteString(port)
		}
	} else if port != "" && port != "80" {
		b.WriteString("@")
		b.WriteString(port)
	}
	b.WriteString(path)
	return b.String()
}

// netErrText 把 Win32 网络错误码翻译成可操作的中文提示。
func netErrText(code syscall.Errno) string {
	switch code {
	case 5:
		return "拒绝访问：系统拒绝了此连接"
	case 53:
		return "找不到网络路径：地址不可达，或该地址不是 WebDAV 端点"
	case 67:
		return "网络名无效：路径可能少了 /dav 结尾"
	case 86:
		return "密码不正确"
	case 121:
		return "服务器响应超时"
	case 1326:
		return "用户名或密码错误"
	case 1203:
		return "无网络或路径错误（未指定网络提供程序）"
	case 1219:
		return "该服务器已存在其他账号的连接，请先断开旧连接再试"
	case 1231:
		return "网络不可达：请检查地址、端口与防火墙"
	case 1232:
		return "无法访问远程计算机"
	case 59:
		return "网络异常：服务器返回了 WebDAV 客户端无法识别的响应（可能是版本或路径不对）"
	case 1396:
		return "登录失败：账号或密码不被服务器接受"
	}
	return "网络错误（代码 " + itoa(int(code)) + "）"
}

// mountDrive 执行一次完整的挂载：系统配置 -> 断开旧连接 -> 建立映射。
func mountDrive(letter, rawURL, user, pass string, log func(string)) error {
	useHTTP := strings.HasPrefix(strings.ToLower(rawURL), "http://")

	log("检查系统 WebDAV 客户端…")
	if err := applySystemConfig(useHTTP, log); err != nil {
		return err
	}

	if driveExists(letter) {
		if old := driveRemotePath(letter); old != "" {
			log(fmt.Sprintf("%s 已被占用（%s），先断开", letter, old))
			if err := netCancelConnection(letter); err != nil {
				return fmt.Errorf("无法断开 %s 上的旧连接：%s", letter, netErrText(errnoErr(err)))
			}
		} else {
			return fmt.Errorf("%s 已被本地磁盘占用，请换一个盘符", letter)
		}
	}

	attempts := []struct {
		name   string
		target string
	}{
		{"URL 形式", rawURL},
		{"UNC 形式", webdavUNC(rawURL)},
	}
	var lastErr error
	for _, a := range attempts {
		log(fmt.Sprintf("正在映射 %s → %s（%s）", letter, a.target, a.name))
		err := netAddConnection(letter, a.target, user, pass)
		if err == nil {
			log("映射成功")
			return nil
		}
		lastErr = err
		log(fmt.Sprintf("  %s 失败：%s", a.name, netErrText(errnoErr(err))))
		// 凭据冲突/认证失败换形式也没用，直接返回。
		code := errnoErr(err)
		if code == 1219 || code == 1326 || code == 86 {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("未知错误")
	}
	return fmt.Errorf("挂载失败：%s", netErrText(errnoErr(lastErr)))
}

// ---------------------------------------------------------------- misc

func openInExplorer(path string) error {
	return shellExec("open", path, "", swShowNormal)
}

// cleanupDriveMapping 彻底拆掉一个盘符映射：WNet 断开 -> 清 HKCU\Network 持久项
// -> 通知资源管理器刷新。否则「此电脑」里会留下一个带红叉的僵尸图标。
func cleanupDriveMapping(letter string) {
	letter = strings.ToUpper(strings.TrimSuffix(letter, `\`))
	if !strings.HasSuffix(letter, ":") {
		letter += ":"
	}

	if driveRemotePath(letter) != "" {
		netCancelConnection(letter)
	}

	// 持久映射条目，删掉之后资源管理器才不会在下次启动时重建它
	key := `Network\` + strings.TrimSuffix(letter, ":")
	procRegDeleteKeyW.Call(0x80000001, ptrOf(u16(key)))

	notifyShellDriveRemoved(letter)
}

func notifyShellDriveRemoved(letter string) {
	const (
		shcneNetUnshare   = 0x00004000
		shcneDriveRemoved = 0x08000000
		shcneUpdateDir    = 0x00001000
		shcnfPathW        = 0x0005
	)
	p := u16(letter + `\`)
	procSHChangeNotify.Call(shcneNetUnshare, shcnfPathW, uintptr(unsafe.Pointer(p)), 0)
	procSHChangeNotify.Call(shcneDriveRemoved, shcnfPathW, uintptr(unsafe.Pointer(p)), 0)

	// 让「此电脑」重新枚举驱动器。只发上面两条的话，界面常常还挂着已摘除的盘符。
	drives := u16("::{20D04FE0-3AEA-1069-A2D8-08002B30309D}")
	procSHChangeNotify.Call(shcneUpdateDir, shcnfPathW, uintptr(unsafe.Pointer(drives)), 0)
}

// notifyShellDriveAdded 告诉外壳「多了一个盘符」。
//
// 不通知的话，Windows 10/11 的「此电脑」不会自动枚举新挂上的网络驱动器，
// 用户要点一下刷新才看得见——很容易被当成「挂载失败」。
func notifyShellDriveAdded(letter string) {
	const (
		shcneNetShare   = 0x00002000
		shcneDriveAdded = 0x80000000
		shcneUpdateDir  = 0x00001000
		shcnfPathW      = 0x0005
	)
	p := u16(letter + `\`)
	procSHChangeNotify.Call(shcneNetShare, shcnfPathW, uintptr(unsafe.Pointer(p)), 0)
	procSHChangeNotify.Call(shcneDriveAdded, shcnfPathW, uintptr(unsafe.Pointer(p)), 0)

	drives := u16("::{20D04FE0-3AEA-1069-A2D8-08002B30309D}")
	procSHChangeNotify.Call(shcneUpdateDir, shcnfPathW, uintptr(unsafe.Pointer(drives)), 0)
}

// refreshDrivesView 只让「此电脑」重新枚举驱动器，不碰具体盘符的移除事件。
// 用于用户手动触发刷新，不会误伤正在使用的盘符。
func refreshDrivesView() {
	const (
		shcneUpdateDir = 0x00001000
		shcnfPathW     = 0x0005
	)
	drives := u16("::{20D04FE0-3AEA-1069-A2D8-08002B30309D}")
	procSHChangeNotify.Call(shcneUpdateDir, shcnfPathW, uintptr(unsafe.Pointer(drives)), 0)
}


