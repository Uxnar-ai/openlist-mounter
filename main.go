package main

import (
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"
)

var (
	instanceMutex uintptr
	tookOver      bool
)

func main() {
	// 必须最先做：确定落盘目录（exe 同目录优先）并把旧版 %APPDATA% 里的配置搬过来，
	// 后面的 --set-server / --dep / 正常启动都要用同一个根目录。
	ensureDataDir()

	if trySetServer(os.Args[1:]) {
		return
	}
	// --dep <动作>：给 runas 拉起的提权子进程用（装/卸 WinFsp 这类动作），做完就退
	if action, ok := takeDepArg(os.Args[1:]); ok {
		os.Exit(depActionMain(action))
	}

	// 必须把主 goroutine 钉在一个 OS 线程上，而且要在建窗口之前锁。
	// Win32 的消息只投递到「创建该窗口的那个线程」的消息队列里，而 Go 的 goroutine
	// 会在 OS 线程之间迁移：一旦主 goroutine 漂到别的线程，GetMessage 就取不到
	// 托盘回调消息，表现为右键点了没反应（或者时好时坏）。
	runtime.LockOSThread()

	auto, noElev, replace := false, false, false
	for _, arg := range os.Args[1:] {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "--auto":
			auto = true
		case "--no-elevate":
			noElev = true
		case "--replace":
			replace = true
		}
	}

	enableDPIAwareness()

	if !ensureSingleInstance(replace) {
		hwnd, _, _ := procFindWindowW.Call(ptrOf(u16(wndClassName)), 0)
		if hwnd == 0 {
			messageBox(0, "OpenList 盘符挂载已经在运行。", "提示", mbOK|mbIconInfo)
			return
		}
		// 旧实例还活着就把它唤出来；卡死（比如死在扫描里）就直接接管，
		// 否则用户会遇到「关不掉、也重开不了」。注意不能因为对方是管理员就无脑接管，
		// 那会在提权/降权切换时叠出好几个实例。
		if windowResponsive(syscall.Handle(hwnd)) {
			procPostMessageW.Call(hwnd, wmAppActivate, 0, 0)
			return
		}
		tookOver = true
	}

	theApp = newApp(auto, noElev, replace)
	if err := theApp.createMessageWindow(); err != nil {
		messageBox(0, err.Error(), "启动失败", mbOK|mbIconWarning)
		return
	}
	if tookOver {
		theApp.logf("接管启动：上一次的实例已卡死或运行在管理员上下文，本次新建实例接管盘符")
	}

	if err := theApp.startWeb(); err != nil {
		messageBox(0, err.Error(), "管理页面启动失败", mbOK|mbIconWarning)
	}

	theApp.addTrayIcon(iconBlue)
	go theApp.bootstrap()

	messageLoop()
	theApp.removeTrayIcon()
	closeInstanceMutex()
}

// ensureSingleInstance 用命名互斥体防多开。replace 模式下会短暂等待旧进程退出，
// 用于「以管理员身份重启」这种交接场景。
//
// 注意：当已存在的互斥体属于更高完整性级别（例如管理员实例）时，
// CreateMutexW 返回的是 ERROR_ACCESS_DENIED 而不是 ERROR_ALREADY_EXISTS。
// 早期版本只判断后者，导致普通实例以为「没有别的实例」而放行，进而多开、
// 多个实例抢同一个盘符、rclone 报 ERROR_FILE_EXISTS。
func ensureSingleInstance(replace bool) bool {
	name := ptrOf(u16(`Local\OpenListMounter`))
	for i := 0; ; i++ {
		h, _, err := procCreateMutexW.Call(0, 0, name)
		e, isErrno := err.(syscall.Errno)

		if h != 0 && (!isErrno || e == 0) {
			instanceMutex = h
			return true
		}
		if h != 0 {
			procCloseHandle.Call(h)
		}

		denied := isErrno && e == syscall.ERROR_ACCESS_DENIED
		already := isErrno && (e == syscall.Errno(errorAlreadyExists) || denied)
		if !already {
			return true // 意料之外的错误，放行，别把自己挡死
		}
		if !replace || i >= 24 {
			return false
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func closeInstanceMutex() {
	if instanceMutex != 0 {
		procCloseHandle.Call(instanceMutex)
		instanceMutex = 0
	}
}

// takeDepArg 解析 `--dep <动作>`。
func takeDepArg(args []string) (string, bool) {
	for i, a := range args {
		if strings.EqualFold(a, "--dep") && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// trySetServer 处理 `--set-server <地址> <用户名> <密码>`，供脚本或排查时直接写配置。
// 成功时不弹窗（避免干扰自动化），失败才提示。
func trySetServer(args []string) bool {
	for i, a := range args {
		if !strings.EqualFold(a, "--set-server") {
			continue
		}
		if i+3 >= len(args) {
			messageBox(0, "用法：OpenListMounter.exe --set-server <地址> <用户名> <密码>",
				"参数不足", mbOK|mbIconWarning)
			return true
		}
		u, err := normalizeURL(args[i+1])
		if err != nil {
			messageBox(0, err.Error(), "地址无效", mbOK|mbIconWarning)
			return true
		}
		enc, err := dpapiProtect(args[i+3])
		if err != nil {
			messageBox(0, "密码加密失败："+err.Error(), "错误", mbOK|mbIconWarning)
			return true
		}
		cfg := loadConfig()
		cfg.URL = u
		cfg.User = args[i+2]
		cfg.PassEnc = enc
		if err := saveConfig(cfg); err != nil {
			messageBox(0, "保存失败："+err.Error(), "配置写入失败", mbOK|mbIconWarning)
			return true
		}
		return true
	}
	return false
}
