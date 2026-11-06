package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	wmTray        = wmApp + 30
	wmAppActivate = wmApp + 31
	wmSelfCheck   = wmApp + 32
	wndClassName  = "OpenListMounterMsgClass"
)

type folderState struct {
	Mounted bool
	Status  string
}

type app struct {
	mu     sync.RWMutex
	cfg    appConfig
	pass   string
	states map[string]*folderState
	procs  map[string]*mountProc

	hwnd      syscall.Handle
	hInst     syscall.Handle
	hIcon     syscall.Handle
	nid       notifyIconData
	trayTheme string
	trayIcon  iconTheme
	working   int32

	port  int
	token string
	srv   *http.Server

	auto    bool
	noElev  bool
	replace bool

	shutdownOnce sync.Once

	logMu sync.Mutex
	logs  []string

	logPath string
}

// shutdownForceExit 是退出的兜底时间：投递 WM_CLOSE 之后这么久还没结束进程，
// 就自己 os.Exit。测试里会调大，避免掩盖「消息循环没退出」这种失败。
var shutdownForceExit = 5 * time.Second

var theApp *app

func newApp(auto, noElev, replace bool) *app {
	a := &app{
		auto:    auto,
		noElev:  noElev,
		replace: replace,
		states:  map[string]*folderState{},
		logPath: filepath.Join(filepath.Dir(configPath()), "log.txt"),
	}
	cfg := loadConfig()
	a.cfg = cfg
	if cfg.PassEnc != "" {
		if p, err := dpapiUnprotect(cfg.PassEnc); err == nil {
			a.pass = p
		}
	}
	return a
}

// ---------------------------------------------------------------- window

var wndProcCallback = syscall.NewCallback(wndProc)

// taskbarCreatedMsg 是 explorer 启动时广播的「任务栏已重建」消息，
// 收到后要把托盘图标重新注册，否则 explorer 重启后图标就丢了。
var taskbarCreatedMsg uint32

// 消息窗口做两件事：单实例唤醒（再次双击程序时把面板叫出来），以及托盘图标的点击回调。
func wndProc(hwnd syscall.Handle, m uint32, w, l uintptr) uintptr {
	a := theApp

	if taskbarCreatedMsg != 0 && m == taskbarCreatedMsg {
		if a != nil {
			th := a.trayIcon
			if th.name == "" {
				th = iconBlue
			}
			a.addTrayIcon(th)
			a.refreshTrayState()
		}
		return 0
	}

	switch m {
	case wmTray:
		if a == nil {
			return 0
		}
		// 没有右键菜单：不管左键右键、单击双击，点一下就打开网页面板
		switch int(l & 0xFFFF) {
		case wmLButtonUp, wmLButtonDblClk, wmRButtonUp, wmContextMenu:
			a.openAdminPage()
		}
		return 0
	case wmAppActivate:
		if a != nil {
			a.openAdminPage()
		}
		return 0
	case wmSelfCheck:
		// 新实例用它判断旧实例是否还能干活：snapshot 要拿配置锁，
		// 读得出来说明没卡死；卡死的实例会一直挡住这个调用。
		if a != nil {
			_ = a.snapshot()
		}
		return 1
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	case wmClose:
		// 这里是唯一的销毁入口：WM_CLOSE 只会被 Dispatch 到窗口线程，
		// 在本线程调 DestroyWindow 才是合法的（跨线程调用返回 0，什么也不做）。
		// DestroyWindow 成功后会同步发出 WM_DESTROY，再由上面那条投 WM_QUIT。
		r, _, callErr := procDestroyWindow.Call(uintptr(hwnd))
		if r == 0 && a != nil {
			a.logf("销毁窗口失败：%v（错误码 %d）", callErr, getLastError())
		}
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(m), w, l)
	return r
}

func (a *app) createMessageWindow() error {
	inst, _, _ := procGetModuleHandleW.Call(0)
	a.hInst = syscall.Handle(inst)

	if r, _, _ := procRegisterWindowMsg.Call(ptrOf(u16("TaskbarCreated"))); r != 0 {
		taskbarCreatedMsg = uint32(r)
	}

	wc := wndClassExW{
		style:         0,
		lpfnWndProc:   wndProcCallback,
		hInstance:     a.hInst,
		lpszClassName: u16(wndClassName),
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if cursor, _, _ := procLoadCursorW.Call(0, idcArrow); cursor != 0 {
		wc.hCursor = syscall.Handle(cursor)
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		ptrOf(u16(wndClassName)),
		ptrOf(u16("OpenListMounter")),
		0, 0, 0, 0, 0,
		0, 0, inst, 0)
	if hwnd == 0 {
		return fmt.Errorf("创建窗口失败（错误码 %d）", getLastError())
	}
	a.hwnd = syscall.Handle(hwnd)

	// 管理员身份运行时，explorer（中权限）发来的托盘回调会被 UIPI 静默丢弃，
	// 表现为图标在但点了没反应。必须显式放行这两条消息。
	errTray := allowMsgFromLower(a.hwnd, wmTray)
	errTaskbar := allowMsgFromLower(a.hwnd, taskbarCreatedMsg)
	if isElevated() {
		a.logf("以管理员身份运行；托盘消息过滤：wmTray=%v taskbarCreated=%v", errTray, errTaskbar)
	}
	return nil
}

func messageLoop() {
	var m struct {
		hwnd    syscall.Handle
		message uint32
		wParam  uintptr
		lParam  uintptr
		time    uint32
		pt      pointT
	}
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// ---------------------------------------------------------------- logging

func (a *app) logf(format string, args ...any) {
	line := time.Now().Format("15:04:05") + "  " + fmt.Sprintf(format, args...)
	a.logMu.Lock()
	defer a.logMu.Unlock()
	a.logs = append(a.logs, line)
	if len(a.logs) > 400 {
		a.logs = a.logs[len(a.logs)-400:]
	}
	a.appendLogFile(line)
}

// appendLogFile 把日志同时落盘，方便用户回传排查。超过 512KB 就重开一个。
func (a *app) appendLogFile(line string) {
	if a.logPath == "" {
		return
	}
	// 首次运行 / 还没保存过配置时目录并不存在，open 会静默失败，日志就成了空文件。
	os.MkdirAll(filepath.Dir(a.logPath), 0o755)
	_, statErr := os.Stat(a.logPath)
	isNew := os.IsNotExist(statErr)

	f, err := os.OpenFile(a.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	if st, err := f.Stat(); err == nil && st.Size() > 512*1024 {
		f.Close()
		os.Remove(a.logPath)
		f, err = os.OpenFile(a.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		isNew = true
	}
	if isNew {
		f.WriteString("\ufeff") // BOM：记事本才认得出这是 UTF-8
	}
	fmt.Fprintln(f, line)
	f.Close()
}

func (a *app) logLines() []string {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	out := make([]string, len(a.logs))
	copy(out, a.logs)
	return out
}

// ---------------------------------------------------------------- views

type folderView struct {
	Name     string `json:"name"`
	Drive    string `json:"drive"`
	Enabled  bool   `json:"enabled"`
	Mounted  bool   `json:"mounted"`
	Status   string `json:"status"`
	Backend  string `json:"backend"`
	Capacity string `json:"capacity"`
}

func (a *app) snapshot() []folderView {
	a.mu.RLock()
	defer a.mu.RUnlock()
	defBackend := a.cfg.Backend
	if defBackend == "" {
		defBackend = backendRclone
	}
	out := make([]folderView, 0, len(a.cfg.Folders))
	for _, f := range a.cfg.Folders {
		b := f.Backend
		if b == "" {
			b = defBackend
		}
		v := folderView{Name: f.Name, Drive: f.Drive, Enabled: f.Enabled, Backend: b, Capacity: f.Capacity}
		if f.Drive != "" && driveExists(f.Drive) {
			if remote := driveRemotePath(f.Drive); remote != "" {
				v.Mounted = true
				v.Status = "已挂载"
			} else {
				v.Status = "盘符被本地占用"
			}
		} else if st, ok := a.states[f.Name]; ok && st.Status != "" {
			v.Status = st.Status
		} else {
			v.Status = "未挂载"
		}
		out = append(out, v)
	}
	return out
}

func (a *app) hasServer() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg.URL != ""
}

// ---------------------------------------------------------------- config

func (a *app) persist() {
	a.mu.Lock()
	cfg := a.cfg
	pass := a.pass
	a.mu.Unlock()

	// 加密失败时绝不能把空串写回去：那等于「密码没保存」，
	// 下次启动 has_pass=false，用户还得重输。
	if enc, err := dpapiProtect(pass); err != nil {
		a.logf("密码加密失败：%v（保留原密文）", err)
	} else {
		cfg.PassEnc = enc
	}
	if err := saveConfig(cfg); err != nil {
		a.logf("保存配置失败：%v", err)
	}
}

// setServer 保存服务器信息，不触发扫描。
func (a *app) setServer(rawURL, user, pass string) error {
	u, err := normalizeURL(rawURL)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.cfg.URL = u
	a.cfg.User = user
	if pass != "" {
		a.pass = pass
	}
	a.mu.Unlock()
	a.persist()
	return nil
}

// ---------------------------------------------------------------- drives

// mountBelongsToUs 判断某个盘符是不是本程序挂上去的。
func (a *app) mountBelongsToUs(letter string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.mountBelongsToUsLocked(letter)
}

// mountBelongsToUsLocked 同上，但要求调用方已经持锁。
//
// 必须分开写：assignDrivesLocked 是在写锁里被调用的，
// sync.RWMutex 不可重入，在里面再 RLock 会把自己锁死——
// 表现就是「盘符一旦真的挂上了，重新扫描就整个进程卡住」。
func (a *app) mountBelongsToUsLocked(letter string) bool {
	remote := driveRemotePath(letter)
	if remote == "" {
		return false
	}
	return sameHost(remote, a.cfg.URL)
}

func sameHost(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return strings.EqualFold(ua.Host, ub.Host) && strings.EqualFold(ua.Scheme, ub.Scheme)
}

// nextFreeDrive 从 Z 往前找一个没被占用、也没被本程序分配的盘符。
func nextFreeDrive(taken map[string]bool) string {
	for c := 'Z'; c >= 'D'; c-- {
		d := string(c) + ":"
		if taken[d] {
			continue
		}
		if driveExists(d) && driveRemotePath(d) == "" {
			continue // 本地磁盘
		}
		return d
	}
	return ""
}

// assignDrivesLocked 先沿用记忆里的盘符，冲突或新目录再顺延补位。调用方需持锁。
func (a *app) assignDrivesLocked() {
	taken := map[string]bool{}

	// 第一轮：记忆命中就保持
	for i := range a.cfg.Folders {
		f := &a.cfg.Folders[i]
		if f.Drive == "" || taken[f.Drive] {
			continue
		}
		if driveExists(f.Drive) && !a.mountBelongsToUsLocked(f.Drive) {
			continue // 被别的东西占了，交给第二轮顺延
		}
		taken[f.Drive] = true
	}
	// 第二轮：剩下的顺延补位
	for i := range a.cfg.Folders {
		f := &a.cfg.Folders[i]
		if f.Drive != "" && taken[f.Drive] {
			continue
		}
		d := nextFreeDrive(taken)
		if d == "" {
			f.Drive = ""
			continue
		}
		if f.Drive != d {
			a.logf("%s 的盘符调整为 %s", f.Name, d)
		}
		f.Drive = d
		taken[d] = true
	}
}

// ---------------------------------------------------------------- discovery

// scan 扫描远程根目录，合并进记忆，并按规则重新分配盘符。
func (a *app) scan() error {
	a.mu.RLock()
	base, user, pass := a.cfg.URL, a.cfg.User, a.pass
	a.mu.RUnlock()
	if base == "" {
		return errors.New("还没有填写 WebDAV 地址")
	}

	names, err := listTopFolders(base, user, pass, 20*time.Second)
	if err != nil {
		return err
	}
	a.logf("扫描到 %d 个目录：%s", len(names), strings.Join(names, "、"))

	a.mu.Lock()
	old := map[string]folderMount{}
	for _, f := range a.cfg.Folders {
		old[f.Name] = f
	}
	merged := make([]folderMount, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		seen[n] = true
		if f, ok := old[n]; ok {
			// 老目录原样保留：勾选/盘符/容量/通道都是用户意图，扫描没资格改。
			merged = append(merged, f)
			continue
		}
		// 新目录：默认启用；如果它以前出现过（服务器上被删过又回来了），
		// 按记忆还原盘符、容量、通道和勾选状态。
		nf := folderMount{Name: n, Enabled: true}
		if m, ok := a.cfg.memory(n); ok {
			nf.Drive, nf.Backend, nf.Capacity, nf.Enabled = m.Drive, m.Backend, m.Capacity, m.Enabled
		}
		a.cfg.forget(n)
		merged = append(merged, nf)
	}
	// 这次没列出来的目录：设置存进记忆，别让盘符/容量/通道跟着消失。
	for _, f := range a.cfg.Folders {
		if !seen[f.Name] {
			a.cfg.remember(f)
			if f.Drive != "" {
				a.logf("%s 已不在服务器上，盘符记忆保留（%s）", f.Name, f.Drive)
			}
		}
	}
	a.cfg.Folders = merged
	a.assignDrivesLocked()
	a.mu.Unlock()

	a.persist()
	a.refreshTrayState()
	return nil
}

// listTopFolders 只保留真正的目录，并去掉代表根自身的那一项。

// ---------------------------------------------------------------- mount ops

func joinURL(base, name string) string {
	return strings.TrimRight(base, "/") + "/" + url.PathEscape(name)
}

func (a *app) mountOne(name string) error {
	atomic.AddInt32(&a.working, 1)
	defer func() {
		atomic.AddInt32(&a.working, -1)
		a.refreshTrayState()
	}()

	a.mu.RLock()
	i, f := a.cfg.folder(name)
	var drive, base, user, pass, backend, capacity string
	backend = a.cfg.Backend
	if f != nil {
		drive = f.Drive
		capacity = f.Capacity
		if f.Backend != "" {
			backend = f.Backend
		}
	}
	if backend == "" {
		backend = backendRclone
	}
	base, user, pass = a.cfg.URL, a.cfg.User, a.pass
	a.mu.RUnlock()
	if i < 0 || f == nil {
		return errors.New("该目录不在列表里，请先重新扫描")
	}
	if drive == "" {
		return errors.New("没有可用的盘符了")
	}

	a.setStatus(name, "连接中…")
	a.logf("挂载 %s → %s（%s）", name, drive, backendName(backend))

	// 盘符可能被上一个实例或残留映射占着，先清干净再挂，
	// 否则 rclone 会报 Cannot create WinFsp-FUSE file system (ERROR_FILE_EXISTS)
	if driveExists(drive) {
		if old := driveRemotePath(drive); old != "" {
			a.logf("%s 已被占用（%s），先断开", drive, old)
			cleanupDriveMapping(drive)
			time.Sleep(300 * time.Millisecond)
			// 必须确认真的断开了：另一个权限上下文（管理员实例）挂上去的映射，
			// 中权限进程删不掉。不检查的话后面会把这条残留当成「挂载成功」，
			// 用户看到的就是「显示已挂载但资源管理器里没有盘」。
			if still := driveRemotePath(drive); still != "" {
				return fmt.Errorf("%s 上还挂着 %s，当前权限删不掉（多半是管理员实例留下的）："+
					"结束残留进程或重启电脑后再挂载", drive, still)
			}
		} else {
			return fmt.Errorf("%s 已被本地磁盘占用，请换一个盘符", drive)
		}
	}

	var err error
	if backend == backendRclone {
		err = a.startRcloneMount(name, drive, capacity)
	} else {
		target := joinURL(base, name)
		err = mountDrive(drive, target, user, pass, func(s string) { a.logf("  %s", s) })
	}
	if err != nil {
		msg := err.Error()
		switch {
		case errors.Is(err, errNeedAdmin):
			msg = "需要管理员权限完成系统配置（托盘菜单 → 以管理员身份重启）"
		case errors.Is(err, errNeedWinFsp):
			msg = "需要安装 WinFsp 驱动：以管理员身份重启后会自动安装"
		}
		a.setStatus(name, msg)
		return errors.New(msg)
	}
	a.setStatus(name, "已挂载")
	// 主动告诉外壳多了一个盘符，否则「此电脑」要等用户手动刷新才显示
	notifyShellDriveAdded(drive)
	return nil
}

func backendName(b string) string {
	if b == backendWebDAV {
		return "WebDAV"
	}
	return "rclone"
}

func (a *app) unmountOne(name string) error {
	atomic.AddInt32(&a.working, 1)
	defer func() {
		atomic.AddInt32(&a.working, -1)
		a.refreshTrayState()
	}()

	a.mu.RLock()
	i, f := a.cfg.folder(name)
	var drive string
	if f != nil {
		drive = f.Drive
	}
	a.mu.RUnlock()
	if i < 0 || drive == "" {
		return errors.New("该目录没有分配盘符")
	}

	if a.hasRcloneProc(name) {
		a.logf("停止 rclone 进程：%s", name)
		a.stopRcloneMount(name)
	}
	// 统一清理：WNet 断开 + 持久映射 + 通知资源管理器刷新
	cleanupDriveMapping(drive)

	a.logf("已断开 %s（%s）", name, drive)
	a.setStatus(name, "未挂载")
	return nil
}

func (a *app) toggleFolder(name string) {
	a.mu.RLock()
	_, f := a.cfg.folder(name)
	var drive string
	if f != nil {
		drive = f.Drive
	}
	a.mu.RUnlock()
	if f == nil {
		return
	}
	if drive != "" && driveRemotePath(drive) != "" {
		if err := a.unmountOne(name); err != nil {
			a.logf("%v", err)
			a.notify("断开失败", err.Error())
		}
		return
	}
	if err := a.mountOne(name); err != nil {
		a.logf("挂载 %s 失败：%v", name, err)
		a.notify("挂载失败："+name, err.Error())
	}
}

func (a *app) setStatus(name, s string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.states[name] == nil {
		a.states[name] = &folderState{}
	}
	a.states[name].Status = s
}

func (a *app) mountAllEnabled() {
	a.mu.RLock()
	var list []string
	var base string
	for _, f := range a.cfg.Folders {
		if f.Enabled {
			list = append(list, f.Name)
		}
	}
	base = a.cfg.URL
	a.mu.RUnlock()

	if len(list) == 0 {
		a.logf("没有勾选要挂载的目录")
		return
	}

	// 按通道分别做一次可用性判断：同类目录里的第一个失败后就不再逐个重试
	useHTTP := strings.HasPrefix(strings.ToLower(base), "http://")
	blocked := map[string]string{}

	a.logf("开始挂载 %d 个目录", len(list))
	ok := 0
	for _, n := range list {
		b := a.backendOf(n)
		if reason, bad := blocked[b]; bad {
			a.logf("%s 跳过：%s", n, reason)
			continue
		}
		if ready, reason := a.backendReady(b, useHTTP); !ready {
			blocked[b] = reason
			a.logf("%s 跳过：%s", n, reason)
			a.setStatus(n, reason)
			continue
		}
		if err := a.mountOne(n); err != nil {
			a.logf("%s 挂载失败：%v", n, err)
			continue
		}
		ok++
	}
	a.logf("挂载完成：%d/%d", ok, len(list))
	a.refreshTrayState()

	if len(blocked) > 0 {
		for _, reason := range blocked {
			a.notify("还差一步", reason)
			break
		}
	} else if len(list) > 0 && !a.auto {
		a.notify("OpenList", fmt.Sprintf("已挂载 %d/%d 个目录", ok, len(list)))
	}
}

// backendOf 返回某个目录实际生效的后端。
func (a *app) backendOf(name string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, f := a.cfg.folder(name); f != nil && f.Backend != "" {
		return f.Backend
	}
	if a.cfg.Backend != "" {
		return a.cfg.Backend
	}
	return backendRclone
}

// backendReady 判断该通道当前能不能用；不能就返回可操作的说明。
func (a *app) backendReady(backend string, useHTTP bool) (bool, string) {
	if backend == backendRclone {
		if winfspInstalled() {
			return true, ""
		}
		if isElevated() {
			return true, "" // 管理员身份下会在挂载时自动安装
		}
		return false, "rclone 通道缺少 WinFsp 驱动：以管理员身份重启后会自动安装"
	}
	if !isElevated() {
		if st := checkSystem(useHTTP); st.needsFix {
			return false, "WebDAV 通道需要管理员权限做一次性配置：" + strings.Join(st.reasons, "；")
		}
	}
	return true, ""
}

// relaunchAsAdmin 释放单实例锁后以管理员身份重新拉起自己。
func (a *app) relaunchAsAdmin() {
	exe, err := os.Executable()
	if err != nil {
		a.notify("提权失败", err.Error())
		return
	}
	a.logf("正在请求管理员权限…")
	a.persist()
	closeInstanceMutex()
	time.Sleep(150 * time.Millisecond)

	params := "--replace"
	if a.auto {
		params = "--auto --replace"
	}
	if err := shellExec("runas", exe, params, swShowNormal); err != nil {
		a.logf("提权被取消：%v", err)
		a.notify("提权已取消", "没有管理员授权，就无法安装驱动或配置系统 WebDAV 客户端")
		ensureSingleInstance(false)  // 忽略错误：拿不回来也要继续跑
		return
	}
	a.shutdown()
}

// relaunchAsUser 从提权实例退回普通权限实例。
//
// 提权进程无法给自己降权：直接 CreateProcess 开出来的子进程会继承高完整性令牌。
// 借 explorer.exe（中权限）当父进程拉起，子进程才是中权限——否则 WebDAV 映射出来的
// 盘符依旧只存在于管理员上下文，资源管理器里根本看不见，挂载等于白做。
//
// 注意两点：explorer 不能可靠转发命令行参数，所以这里不带任何参数（单实例互斥体
// 已在 spawn 之前释放，新实例不需要 --replace）；explorer 只认纯路径。
func (a *app) relaunchAsUser() {
	exe, err := os.Executable()
	if err != nil {
		a.logf("切换普通身份失败：%v", err)
		return
	}
	a.persist()
	closeInstanceMutex()
	if err := shellExec("open", "explorer.exe", `"`+exe+`"`, swShowNormal); err != nil {
		a.logf("拉起普通身份实例失败：%v；改由提权实例继续挂载，盘符在资源管理器里可能不可见", err)
		ensureSingleInstance(false)  // 忽略错误：拿不回来也要继续跑
		a.mountAllEnabled()
		return
	}
	a.logf("已交回普通权限实例挂载")
	a.shutdown()
}

func (a *app) unmountAll() {
	a.mu.RLock()
	var drives []string
	var names []string
	for _, f := range a.cfg.Folders {
		if f.Drive != "" {
			drives = append(drives, f.Drive)
			names = append(names, f.Name)
		}
	}
	a.mu.RUnlock()
	for i, d := range drives {
		if driveRemotePath(d) == "" {
			continue
		}
		if err := netCancelConnection(d); err != nil {
			a.logf("断开 %s 失败：%s", names[i], netErrText(errnoErr(err)))
			continue
		}
		a.setStatus(names[i], "未挂载")
		a.logf("已断开 %s", names[i])
	}
	a.refreshTrayState()
}

// refreshTrayState 根据当前挂载情况刷新托盘图标颜色与提示文字。
func (a *app) refreshTrayState() {
	views := a.snapshot()
	enabled, mounted := 0, 0
	failed := false
	for _, v := range views {
		if !v.Enabled {
			continue
		}
		enabled++
		if v.Mounted {
			mounted++
		}
		for _, bad := range []string{"失败", "错误", "拒绝", "超时", "不可用"} {
			if strings.Contains(v.Status, bad) {
				failed = true
			}
		}
	}

	theme := iconBlue
	switch {
	case atomic.LoadInt32(&a.working) > 0:
		theme = iconYellow
	case failed:
		theme = iconRed
	case mounted > 0:
		theme = iconGreen
	case enabled == 0:
		theme = iconGray
	}
	if theme.name != a.trayTheme {
		a.logf("托盘状态 → %s（已挂载 %d/%d）", theme.name, mounted, enabled)
	}
	a.setTrayTheme(theme)
	a.updateTrayTip(fmt.Sprintf("OpenList · 已挂载 %d/%d", mounted, enabled))
}

// bootstrap 是启动后的恢复流程：扫描目录 -> 挂载记忆里勾选的那些。
func (a *app) bootstrap() {
	a.refreshTrayState()
	a.logf("配置目录：%s", dataDir())
	a.syncAutoStart()
	if !a.hasServer() {
		a.logf("还没有配置服务器，请先填写 WebDAV 地址")
		if !a.auto {
			a.openAdminPage()
		}
		return
	}

	// 缺少系统级配置时自动提权重启一次（UAC 静默模式下用户几乎无感）。
	// replace 标记用来防止提权后再次判断失败导致无限重启。
	a.mu.RLock()
	base := a.cfg.URL
	a.mu.RUnlock()
	a.logf("启动恢复：%s", base)

	if err := a.scan(); err != nil {
		a.logf("扫描失败：%v", err)
		a.notify("无法连接 OpenList", err.Error())
		a.refreshTrayState()
		return
	}

	// 必须等扫描出目录之后才能判断：needsElevation 是按启用的目录逐个看的。
	if !a.noElev && !a.replace && a.needsElevation() {
		// 防振荡：提权实例补不齐系统配置（最常见是 WinFsp 下载超时）时会退回普通身份，
		// 普通实例又判定缺配置 → 再提权……两边互相拉起，用户看到 UAC 每 20 秒弹一次。
		// 同一个时间窗内只自动提权一次，超时就本次在普通身份下继续（挂载会报缺什么）。
		if tooSoon, left := elevateTooSoon(); tooSoon {
			a.logf("最近 %s 内已经自动提权过一次但没补齐配置，本次跳过自动提权（还需等待 %s）",
				elevateGuardWindow, left.Round(time.Second))
			a.notify("系统配置没补齐", "刚试过提权但没成功，本次在普通权限下继续；"+
				"可以先到管理页「依赖组件」里手动安装 WinFsp")
		} else {
			a.logf("缺少系统级配置（驱动或 WebDAV 客户端），正在以管理员身份重启…")
			markElevateAttempt()
			a.relaunchAsAdmin()
			return
		}
	}

	// 盘符必须在普通权限实例里挂：WebDAV 走 WNet 映射，rclone 用 --network-mode
	// （WinFsp 的网络驱动器模式），两者都是「按完整性上下文隔离」的网络驱动器。
	// 管理员进程挂出来的盘符，资源管理器（中权限）看不见——状态显示已挂载，但「此电脑」里没有。
	// 所以提权实例只负责补齐一次性系统配置，补完立刻退回普通权限再挂载。
	if isElevated() {
		hasWebDAV, hasRclone, anyEnabled := a.backendsInUse()
		if anyEnabled {
			useHTTP := strings.HasPrefix(strings.ToLower(base), "http://")
			if hasRclone && !winfspInstalled() {
				a.logf("以管理员身份安装 WinFsp 驱动…")
				if err := a.ensureWinFsp(); err != nil {
					a.logf("WinFsp 安装失败：%v", err)
					a.notify("WinFsp 安装失败", err.Error())
				}
			}
			if hasWebDAV {
				if st := checkSystem(useHTTP); st.needsFix {
					a.logf("以管理员身份补齐 WebDAV 客户端配置：%s", strings.Join(st.reasons, "；"))
					if err := applySystemConfig(useHTTP,
						func(s string) { a.logf("  %s", s) }); err != nil {
						a.logf("系统配置失败：%v", err)
						a.notify("系统配置失败", err.Error())
					}
				}
			}
			if a.systemConfigMissing() {
				// 兜底：配置还是没补齐（比如 WinFsp 下载失败）。
				// 这时绝不能退回普通身份，否则会被再次判定需要提权 → 无限循环。
				a.logf("系统配置仍未补齐，本次在管理员身份下继续挂载（盘符可能不出现在资源管理器里）")
			} else {
				a.relaunchAsUser()
				return
			}
		}
	}

	// 启动就把面板打开（开机自启时不弹，免得每次开机都跳浏览器）
	if !a.auto {
		a.openAdminPage()
	}
	a.mountAllEnabled()
}

// backendsInUse 统计「启用的目录里实际用到的后端」。
func (a *app) backendsInUse() (hasWebDAV, hasRclone, anyEnabled bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	def := a.cfg.Backend
	if def == "" {
		def = backendRclone
	}
	for _, f := range a.cfg.Folders {
		if !f.Enabled {
			continue
		}
		anyEnabled = true
		b := f.Backend
		if b == "" {
			b = def
		}
		if b == backendWebDAV {
			hasWebDAV = true
		} else {
			hasRclone = true
		}
	}
	return
}

// elevateGuardWindow 是「自动提权」的最小间隔。提权实例补不齐系统配置时会退回
// 普通身份，普通身份又判定缺配置——不设间隔就会来回拉起，无限弹 UAC。
const elevateGuardWindow = 3 * time.Minute

func elevateGuardPath() string { return filepath.Join(dataDir(), "elevate-attempt.txt") }

// elevateTooSoon 返回上次自动提权是否还在冷却窗内，以及还剩多久。
func elevateTooSoon() (bool, time.Duration) {
	data, err := os.ReadFile(elevateGuardPath())
	if err != nil {
		return false, 0
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || ts <= 0 {
		return false, 0
	}
	elapsed := time.Since(time.Unix(ts, 0))
	if elapsed < 0 {
		return false, 0 // 时钟被改过，当作没有记录
	}
	if elapsed < elevateGuardWindow {
		return true, elevateGuardWindow - elapsed
	}
	return false, 0
}

func markElevateAttempt() {
	if err := writeFileAtomic(elevateGuardPath(),
		[]byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o600); err != nil {
		// 写不进去就算了：这只是防振荡的辅助信息，不能挡住正常提权。
	}
}

// needsElevation 判断「需要管理员才能补齐的系统配置」还缺不缺，且当前不是管理员。
func (a *app) needsElevation() bool {
	if isElevated() {
		return false
	}
	return a.systemConfigMissing()
}

// systemConfigMissing 只看配置齐不齐，不看当前权限。
// 提权实例补完配置后要用它确认「真的补齐了」才敢退回普通身份，
// 否则会陷入 普通→提权→普通→提权 的死循环。
func (a *app) systemConfigMissing() bool {
	a.mu.RLock()
	base := a.cfg.URL
	def := a.cfg.Backend
	folders := make([]folderMount, len(a.cfg.Folders))
	copy(folders, a.cfg.Folders)
	a.mu.RUnlock()

	if base == "" {
		return false
	}
	if def == "" {
		def = backendRclone
	}
	useHTTP := strings.HasPrefix(strings.ToLower(base), "http://")
	checkedWebdav := false

	for _, f := range folders {
		if !f.Enabled {
			continue
		}
		b := f.Backend
		if b == "" {
			b = def
		}
		if b == backendRclone && !winfspInstalled() {
			return true
		}
		if b == backendWebDAV && !checkedWebdav {
			checkedWebdav = true
			if checkSystem(useHTTP).needsFix {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------- misc

// openAdminPage 打开网页面板。
// 必须异步：ShellExecute 走 DDE 唤起已在运行的浏览器时会阻塞好几秒，
// 同步调用会把启动流程和消息循环一起卡住（挂载都轮不到）。
func (a *app) openAdminPage() {
	target := fmt.Sprintf("http://127.0.0.1:%d/?t=%s", a.port, a.token)
	go func() {
		if err := shellExec("open", target, "", swShowNormal); err != nil {
			a.logf("打开管理页面失败：%v", err)
		}
	}()
}

func (a *app) toggleAutoStart() { a.setAutoStartEnabled(!autoStartEnabled()) }

// setAutoStartEnabled 写注册表 + 同步配置里的镜像字段。
func (a *app) setAutoStartEnabled(on bool) {
	exe, err := os.Executable()
	if err != nil {
		a.notify("设置失败", err.Error())
		return
	}
	if err := setAutoStart(exe, on); err != nil {
		a.notify("设置失败", err.Error())
		return
	}
	a.mu.Lock()
	a.cfg.AutoStart = on
	a.mu.Unlock()
	a.persist()
	if on {
		a.logf("已开启开机自启动")
	} else {
		a.logf("已关闭开机自启动")
	}
}

// syncAutoStart 让注册表 Run 键和「当前 exe 路径」对齐。
//
// Run 键里存的是写进去那天的 exe 绝对路径，程序一移动/改名/换目录，
// 开机自启就静默失效，而界面读的也是注册表，所以看起来一切正常。
// 启动时对一次，不一致就重写；配置里的镜像字段也一并校正。
func (a *app) syncAutoStart() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	reg := autoStartCommand()
	a.mu.RLock()
	cfgOn := a.cfg.AutoStart
	a.mu.RUnlock()

	if reg == "" {
		if cfgOn {
			a.logf("配置记着要开机自启，但注册表里没有（可能被清理软件删了），以注册表为准关闭")
			a.mu.Lock()
			a.cfg.AutoStart = false
			a.mu.Unlock()
			a.persist()
		}
		return
	}

	want := `"` + exe + `" --auto`
	if !strings.EqualFold(strings.TrimSpace(reg), want) {
		if err := setAutoStart(exe, true); err != nil {
			a.logf("校正开机自启路径失败：%v", err)
		} else {
			a.logf("开机自启路径与当前 exe 不一致，已重写：%s", want)
		}
	}
	if !cfgOn {
		a.mu.Lock()
		a.cfg.AutoStart = true
		a.mu.Unlock()
		a.persist()
	}
}

// refreshShell 让资源管理器重新枚举驱动器，界面上残留的旧盘符图标会消失。
func (a *app) refreshShell() {
	refreshDrivesView()
	a.logf("已通知资源管理器刷新驱动器列表")
}

// restartExplorer 彻底重启资源管理器。会关掉已打开的文件夹窗口，桌面闪一下。
//
// 两个坑：
//  1. taskkill /F 之后不能指望系统的 AutoRestartShell 一定拉起新 shell——很多机器上
//     根本不触发，结果就是桌面没了。必须自己轮询确认并在缺失时补一次启动。
//  2. 提权实例启动的 explorer 会是「管理员桌面」（高完整性），比不重启更糟。
//     所以提权场景直接拒绝，退化成只刷新驱动器图标。
func (a *app) restartExplorer() {
	if isElevated() {
		a.logf("管理员身份下不重启资源管理器（会拉起高权限桌面），改为刷新驱动器图标")
		a.refreshShell()
		a.notify("已刷新驱动器图标", "管理员身份下不重启资源管理器，避免得到一个管理员桌面")
		return
	}

	a.logf("正在重启资源管理器…")
	cmd := exec.Command("taskkill", "/F", "/IM", "explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		a.logf("重启资源管理器失败：%s", msg)
		a.notify("重启失败", msg)
		return
	}

	go a.reviveShell()
	a.notify("资源管理器", "正在重启，桌面和托盘会闪一下")
}

// reviveShell 等旧 shell 彻底退出，确认没有 shell 就自己拉起一个（普通权限），
// 然后把托盘图标补回去。否则用户会看到一个没有桌面和任务栏的屏幕。
func (a *app) reviveShell() {
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && shellWindow() != 0 {
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(400 * time.Millisecond)

	if shellWindow() == 0 {
		a.logf("没有检测到 shell，主动拉起 explorer.exe")
		c := exec.Command("explorer.exe")
		c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
		if err := c.Start(); err != nil {
			a.logf("拉起资源管理器失败：%v", err)
			a.notify("桌面没能恢复", "请用任务管理器 → 运行新任务 → explorer.exe")
		}
	}

	// 等新 shell 起来再把托盘图标加回去
	for i := 0; i < 30 && shellWindow() == 0; i++ {
		time.Sleep(300 * time.Millisecond)
	}
	a.addTrayIcon(a.trayIcon)
	a.refreshTrayState()
	a.logf("资源管理器已恢复，托盘图标已重新注册")
}

// shutdown 退出程序。幂等：多个入口（网页、托盘、提权交接）同时调用也只走一次。
func (a *app) shutdown() {
	a.shutdownOnce.Do(a.doShutdown)
}

func (a *app) doShutdown() {
	a.logf("退出")

	// 先停掉管理页面，让 /api/state 尽快不可达——前端就是靠这个判断「真的退出了」。
	a.stopWeb()

	a.stopAllRclone()

	// 退出前逐个通知外壳刷新，否则资源管理器会继续画着已经摘掉的盘符
	a.mu.RLock()
	drives := make([]string, 0, len(a.cfg.Folders))
	for _, f := range a.cfg.Folders {
		if f.Drive != "" {
			drives = append(drives, f.Drive)
		}
	}
	a.mu.RUnlock()
	for _, d := range drives {
		notifyShellDriveRemoved(d)
	}

	a.removeTrayIcon()

	// 关键：销毁窗口的动作必须回到窗口线程。
	//   - PostQuitMessage 只往「调用它的线程」的队列里投 WM_QUIT，这里是 HTTP 的 goroutine，
	//     GetMessage 跑在主线程，消息直接丢失；
	//   - DestroyWindow 不能跨线程，返回 0（ERROR_ACCESS_DENIED），窗口没销毁，
	//     WM_DESTROY 也就永远不会来。
	// 所以改成 PostMessage(WM_CLOSE)：跨线程安全，主线程取出后由 wndProc
	// 在正确的线程里 DestroyWindow → WM_DESTROY → PostQuitMessage → GetMessage 返回 0。
	r, _, callErr := procPostMessageW.Call(uintptr(a.hwnd), wmClose, 0, 0)
	if r == 0 {
		a.logf("向窗口线程投递退出消息失败：%v（错误码 %d），直接结束进程", callErr, getLastError())
		os.Exit(0)
	}

	// 兜底：消息循环万一还是没退（比如窗口线程卡在某个 RPC 上），到点自己走。
	// os.Exit 会跳过 main 尾部收尾，这里可以接受：托盘图标已删、rclone 已杀、
	// 互斥体随进程释放。
	if d := shutdownForceExit; d > 0 {
		time.AfterFunc(d, func() {
			a.logf("%s 内没等到消息循环退出，强制结束进程", d)
			os.Exit(0)
		})
	}
}
