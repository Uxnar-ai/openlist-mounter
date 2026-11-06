package main

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

//go:embed web/index.html
var indexHTML []byte

const fallbackHTML = `<!doctype html><meta charset="utf-8">
<body style="font-family:system-ui;padding:40px;background:#111;color:#eee">
<h2>请从系统托盘打开</h2>
<p>这个页面的访问令牌无效。右键点击托盘里的 OpenList 图标 →「打开管理页面」。</p>
</body>`

type stateResp struct {
	URL         string       `json:"url"`
	User        string       `json:"user"`
	HasPass     bool         `json:"has_pass"`
	Folders     []folderView `json:"folders"`
	FreeDrives  []string     `json:"free_drives"`
	Elevated    bool         `json:"elevated"`
	WebClient   bool         `json:"webclient"`
	AutoStart   bool         `json:"auto_start"`
	Port        int          `json:"port"`
	ConfigDir   string       `json:"config_dir"`
	Logs        []string     `json:"logs"`
	Backend     string       `json:"backend"`
	RcloneReady bool         `json:"rclone_ready"`
	WinFspReady bool         `json:"winfsp_ready"`
	Deps        depInfo      `json:"deps"`
}

func (a *app) startWeb() error {
	ln, port, err := listenLocal()
	if err != nil {
		return err
	}
	a.port = port
	a.token = randomToken()

	srv := &http.Server{
		Handler:           a.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	a.srv = srv
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.logf("管理服务退出：%v", err)
		}
	}()
	// 带上 token，方便从日志直接打开面板（也方便排查时直接调 API）
	a.logf("管理页面已启动：http://127.0.0.1:%d/?t=%s", port, a.token)
	return nil
}

// stopWeb 停止 HTTP 服务。退出时先断掉它，前端才能靠「接口不可达」判断进程真的没了。
func (a *app) stopWeb() {
	if a.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := a.srv.Shutdown(ctx); err != nil {
		a.logf("关闭管理服务：%v", err)
	}
}

func listenLocal() (net.Listener, int, error) {
	for p := 18234; p <= 18280; p++ {
		if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			return ln, p, nil
		}
	}
	return nil, 0, errors.New("18234-18280 端口全被占用，无法启动管理页面")
}

func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------- routing

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/api/state", a.guard(a.handleState))
	mux.HandleFunc("/api/server", a.guard(a.handleServer))
	mux.HandleFunc("/api/scan", a.guard(a.handleScan))
	mux.HandleFunc("/api/folder", a.guard(a.handleFolder))
	mux.HandleFunc("/api/mount", a.guard(a.handleMount))
	mux.HandleFunc("/api/unmount", a.guard(a.handleUnmount))
	mux.HandleFunc("/api/mount-all", a.guard(a.handleMountAll))
	mux.HandleFunc("/api/unmount-all", a.guard(a.handleUnmountAll))
	mux.HandleFunc("/api/autostart", a.guard(a.handleAutoStart))
	mux.HandleFunc("/api/elevate", a.guard(a.handleElevate))
	mux.HandleFunc("/api/refresh", a.guard(a.handleRefresh))
	mux.HandleFunc("/api/quit", a.guard(a.handleQuit))
	mux.HandleFunc("/api/deps", a.guard(a.handleDeps))
	return mux
}

// handleDeps 依赖（rclone / WinFsp）状态与安装卸载。
func (a *app) handleDeps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, depStatus())
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, errors.New("请求格式错误"))
		return
	}
	if atomic.LoadInt32(&depBusy) != 0 {
		writeErr(w, errors.New("上一个依赖操作还没结束"))
		return
	}
	atomic.StoreInt32(&depBusy, 1)
	go func() {
		defer atomic.StoreInt32(&depBusy, 0)
		if err := a.runDepAction(body.Action); err != nil {
			a.logf("[依赖] 操作失败：%v", err)
		}
	}()
	writeJSON(w, map[string]string{"msg": "已开始，过程会写进下面的日志"})
}

// handleQuit 退出程序。没有托盘菜单了，这是唯一的退出入口。
func (a *app) handleQuit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"msg": "正在退出，这个页面可以关掉了"})
	go func() {
		time.Sleep(300 * time.Millisecond)
		a.shutdown()
	}()
}

func (a *app) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Restart bool `json:"restart"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	if body.Restart {
		go a.restartExplorer()
		writeJSON(w, map[string]string{"msg": "正在重启资源管理器，桌面会闪一下"})
		return
	}
	a.refreshShell()
	writeJSON(w, map[string]string{"msg": "已通知资源管理器刷新驱动器列表"})
}

func (a *app) handleElevate(w http.ResponseWriter, r *http.Request) {
	if isElevated() {
		writeErr(w, errors.New("已经是管理员权限了"))
		return
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		a.relaunchAsAdmin()
	}()
	writeJSON(w, map[string]string{"msg": "正在请求管理员权限，请在系统弹窗中点「是」"})
}

func (a *app) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.token == "" || r.Header.Get("X-Token") != a.token {
			http.Error(w, "token 无效", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		h(w, r)
	}
}

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if a.token == "" || r.URL.Query().Get("t") != a.token {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(fallbackHTML))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(indexHTML)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func writeOK(w http.ResponseWriter) {
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------- handlers

func (a *app) handleState(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	url, user, backend := a.cfg.URL, a.cfg.User, a.cfg.Backend
	hasPass := a.pass != ""
	a.mu.RUnlock()
	if backend == "" {
		backend = backendRclone
	}

	writeJSON(w, stateResp{
		URL:         url,
		User:        user,
		HasPass:     hasPass,
		Folders:     a.snapshot(),
		FreeDrives:  freeDriveLetters(),
		Elevated:    isElevated(),
		WebClient:   webClientRunning(),
		AutoStart:   autoStartEnabled(),
		Port:        a.port,
		ConfigDir:   dataDir(),
		Logs:        a.logLines(),
		Backend:     backend,
		RcloneReady: fileExists(rclonePath()),
		WinFspReady: winfspInstalled(),
		Deps:        depStatus(),
	})
}

func (a *app) handleServer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL  string `json:"url"`
		User string `json:"user"`
		Pass string `json:"pass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, errors.New("请求格式错误"))
		return
	}
	if err := a.setServer(body.URL, body.User, body.Pass); err != nil {
		writeErr(w, err)
		return
	}
	a.logf("服务器已更新：%s", body.URL)
	if err := a.scan(); err != nil {
		writeErr(w, err)
		return
	}
	go a.mountAllEnabled()
	writeOK(w)
}

func (a *app) handleScan(w http.ResponseWriter, r *http.Request) {
	if err := a.scan(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w)
}

func (a *app) handleFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string  `json:"name"`
		Enabled  *bool   `json:"enabled"`
		Drive    *string `json:"drive"`
		Backend  *string `json:"backend"`
		Capacity *string `json:"capacity"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, errors.New("请求格式错误"))
		return
	}
	a.mu.Lock()
	_, f := a.cfg.folder(body.Name)
	if f == nil {
		a.mu.Unlock()
		writeErr(w, errors.New("目录不存在，请重新扫描"))
		return
	}
	capacityChanged := false
	if body.Capacity != nil {
		v := strings.TrimSpace(*body.Capacity)
		if v != "" && !validCapacity(v) {
			a.mu.Unlock()
			writeErr(w, errors.New("容量格式不对，示例：2T、500G、1.5T"))
			return
		}
		v = normalizeCapacity(v)
		if f.Capacity != v {
			f.Capacity = v
			capacityChanged = true
		}
	}
	if body.Backend != nil {
		if *body.Backend != backendRclone && *body.Backend != backendWebDAV {
			a.mu.Unlock()
			writeErr(w, errors.New("未知通道"))
			return
		}
		f.Backend = *body.Backend
	}
	if body.Enabled != nil {
		f.Enabled = *body.Enabled
	}
	if body.Drive != nil && *body.Drive != "" {
		want := *body.Drive
		for _, other := range a.cfg.Folders {
			if other.Name != body.Name && other.Drive == want {
				a.mu.Unlock()
				writeErr(w, fmt.Errorf("盘符 %s 已分配给 %s", want, other.Name))
				return
			}
		}
		f.Drive = want
	}
	name := f.Name
	enabled := f.Enabled
	drive := f.Drive
	a.mu.Unlock()

	a.persist()
	a.logf("%s：%s，%s", name, enabledText(enabled), drive)

	// 勾选状态变化立即反映到盘符上
	switch {
	case enabled && drive != "" && driveRemotePath(drive) == "":
		go a.mountOne(name)
	case !enabled && drive != "" && driveRemotePath(drive) != "":
		go a.unmountOne(name)
	case capacityChanged && drive != "" && driveRemotePath(drive) != "":
		// 容量是 rclone 的启动参数，改了必须重挂才生效
		go func() {
			a.unmountOne(name)
			a.mountOne(name)
		}()
	}
	writeOK(w)
}

// validCapacity 校验 "2T" / "500G" / "1.5T" 这类写法。
func validCapacity(s string) bool {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, "B")
	if s == "" {
		return false
	}
	switch s[len(s)-1] {
	case 'K', 'M', 'G', 'T', 'P':
		s = s[:len(s)-1]
	}
	_, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return err == nil
}

func enabledText(b bool) string {
	if b {
		return "启用"
	}
	return "停用"
}

func (a *app) handleMount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, errors.New("请求格式错误"))
		return
	}
	if err := a.mountOne(body.Name); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w)
}

func (a *app) handleUnmount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, errors.New("请求格式错误"))
		return
	}
	if err := a.unmountOne(body.Name); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w)
}

func (a *app) handleMountAll(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	n := 0
	for _, f := range a.cfg.Folders {
		if f.Enabled {
			n++
		}
	}
	a.mu.RUnlock()
	if n == 0 {
		writeErr(w, errors.New("没有勾选任何目录"))
		return
	}
	go a.mountAllEnabled()
	writeOK(w)
}

func (a *app) handleUnmountAll(w http.ResponseWriter, r *http.Request) {
	go a.unmountAll()
	writeOK(w)
}

func (a *app) handleAutoStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, errors.New("请求格式错误"))
		return
	}
	exe, err := os.Executable()
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := setAutoStart(exe, body.Enabled); err != nil {
		writeErr(w, err)
		return
	}
	// 注册表是真正生效的地方，配置里的 auto_start 只是镜像（启动时用来校正路径）。
	a.mu.Lock()
	a.cfg.AutoStart = body.Enabled
	a.mu.Unlock()
	a.persist()
	if body.Enabled {
		a.logf("已开启开机自启动")
	} else {
		a.logf("已关闭开机自启动")
	}
	writeOK(w)
}

// 保持 sort 被使用（列表排序在 webdav.go 里做，这里给盘符排序留个引用点）
var _ = sort.Strings
