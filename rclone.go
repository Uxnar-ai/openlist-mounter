package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// rclone 后端：用 WinFsp + rclone mount 提供文件系统，替代系统 WebDAV 重定向器。
// 优势是分块预读、VFS 缓存、可分片上传大文件，以及能主动上报容量。

// 国内只有南京大学镜像了 rclone，其余（中科大/清华/阿里/腾讯）都没有收录。
var rcloneSources = []string{
	"https://mirrors.nju.edu.cn/rclone/rclone-current-windows-amd64.zip",
	"https://downloads.rclone.org/rclone-current-windows-amd64.zip",
}

const winfspMSIURL = "https://github.com/winfsp/winfsp/releases/download/v2.1/winfsp-2.1.25156.msi"

var errNeedWinFsp = errors.New("需要安装 WinFsp 驱动")

// mountProc 记录一个由本程序拉起的 rclone mount 子进程。
type mountProc struct {
	cmd   *exec.Cmd
	drive string
	port  int          // rclone 的 --rc 端口，用于优雅退出
	done  chan error   // cmd.Wait 的结果
}

// 落盘根目录统一由 config.go 的 dataDir() 定义，这里不再另起一份同名函数——
// 两处同名实现迟早会分叉，配置和 rclone 会跑到不同目录去。
func rclonePath() string     { return filepath.Join(dataDir(), "rclone.exe") }
func rcloneConfPath() string { return filepath.Join(dataDir(), "rclone.conf") }

// rcloneCacheDir 故意不放程序目录：缓存上限 50G，
// 塞进便携目录（可能被拷到 U 盘、被同步盘打包）会很难看。
func rcloneCacheDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "OpenListMounter", "vfs-cache")
}

func rcloneSection(i int) string { return fmt.Sprintf("olm%d", i) }

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func winfspInstalled() bool {
	for _, p := range []string{
		`C:\Program Files (x86)\WinFsp\bin\winfsp-x64.dll`,
		`C:\Program Files\WinFsp\bin\winfsp-x64.dll`,
	} {
		if fileExists(p) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- 下载与解压

func httpDownload(url, dest string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return err
	}
	if n < 1<<20 {
		return fmt.Errorf("下载内容异常（只有 %d 字节）", n)
	}
	return nil
}

func unzipRclone(zipPath, dest string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), "rclone.exe") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, rc)
		return err
	}
	return errors.New("压缩包里没有找到 rclone.exe")
}

// ensureRclone 保证 rclone.exe 就位，没有就按镜像顺序下载。
func (a *app) ensureRclone() (string, error) {
	exe := rclonePath()
	if st, err := os.Stat(exe); err == nil && st.Size() > 10<<20 {
		return exe, nil
	}
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return "", err
	}

	a.logf("首次使用 rclone 后端，开始下载（约 30MB）")
	tmp := filepath.Join(os.TempDir(), "olm-rclone.zip")
	defer os.Remove(tmp)

	var lastErr error
	for _, src := range rcloneSources {
		a.logf("  下载源：%s", src)
		start := time.Now()
		if err := httpDownload(src, tmp); err != nil {
			lastErr = err
			a.logf("  失败：%v", err)
			continue
		}
		if err := unzipRclone(tmp, exe); err != nil {
			lastErr = err
			a.logf("  解压失败：%v", err)
			continue
		}
		a.logf("  rclone 就绪（用时 %.1fs）", time.Since(start).Seconds())
		return exe, nil
	}
	return "", fmt.Errorf("所有下载源都失败了：%v", lastErr)
}

// ensureWinFsp 检查驱动；缺了就下载安装包并以管理员身份静默安装。
func (a *app) ensureWinFsp() error {
	if winfspInstalled() {
		return nil
	}
	if !isElevated() {
		return errNeedWinFsp
	}

	a.logf("正在下载 WinFsp 驱动（约 10MB）")
	msi := filepath.Join(os.TempDir(), "olm-winfsp.msi")
	defer os.Remove(msi)
	if err := httpDownload(winfspMSIURL, msi); err != nil {
		return fmt.Errorf("下载 WinFsp 失败：%v", err)
	}

	a.logf("正在静默安装 WinFsp…")
	err := exec.Command("msiexec", "/i", msi, "/quiet", "/norestart").Run()
	if err != nil {
		return fmt.Errorf("安装 WinFsp 失败：%v", err)
	}
	if !winfspInstalled() {
		return errors.New("WinFsp 安装后仍未检测到，请手动安装后重试")
	}
	a.logf("WinFsp 安装完成")
	return nil
}

// ---------------------------------------------------------------- 配置与挂载

func rcloneObscure(exe, pass string) (string, error) {
	if pass == "" {
		return "", nil
	}
	cmd := exec.Command(exe, "obscure", pass)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// writeRcloneConf 为当前所有目录生成一份临时 rclone 配置。
// section 名用序号，避免目录名里的符号破坏 ini 格式。
func (a *app) writeRcloneConf(exe string) (map[string]string, error) {
	a.mu.RLock()
	folders := make([]folderMount, len(a.cfg.Folders))
	copy(folders, a.cfg.Folders)
	base, user, pass := a.cfg.URL, a.cfg.User, a.pass
	a.mu.RUnlock()

	obscured, err := rcloneObscure(exe, pass)
	if err != nil {
		return nil, fmt.Errorf("密码加扰失败：%v", err)
	}

	sections := map[string]string{}
	var b strings.Builder
	b.WriteString("# 由 OpenListMounter 自动生成，请勿手改\n\n")
	for i, f := range folders {
		sec := rcloneSection(i)
		sections[f.Name] = sec
		fmt.Fprintf(&b, "[%s]\ntype = webdav\nurl = %s\nvendor = other\n", sec, joinURL(base, f.Name))
		if user != "" {
			fmt.Fprintf(&b, "user = %s\n", user)
		}
		if obscured != "" {
			fmt.Fprintf(&b, "pass = %s\n", obscured)
		}
		b.WriteString("\n")
	}

	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(rcloneConfPath(), []byte(b.String()), 0o600); err != nil {
		return nil, err
	}
	return sections, nil
}

// normalizeCapacity 把用户填的容量规范成 rclone 认的写法。
// rclone 的 SizeSuffix 只认 100P / 500G / 1.5T，写成 100PB / 500GB 会直接报
// "invalid argument ... parsing \"100P\" as fs.SizeSuffix failed"，rclone 起不来。
func normalizeCapacity(v string) string {
	v = strings.ToUpper(strings.TrimSpace(v))
	for strings.HasSuffix(v, "B") {
		v = strings.TrimSuffix(v, "B")
	}
	if v == "" {
		return ""
	}
	switch v[len(v)-1] {
	case 'K', 'M', 'G', 'T', 'P':
	default:
		v += "T"
	}
	return v
}

func rcloneMountArgs(section, drive, volname, capacity string) []string {
	capacity = normalizeCapacity(capacity)
	if capacity == "" {
		capacity = "10T" // OpenList 不上报 quota，只能给个占位值
	}
	return []string{
		"mount", section + ":", drive,
		"--config", rcloneConfPath(),
		"--network-mode",
		"--vfs-cache-mode", "full",
		"--vfs-cache-max-size", "50G",
		"--vfs-cache-max-age", "24h",
		"--cache-dir", rcloneCacheDir(),
		"--vfs-disk-space-total-size", capacity,
		"--dir-cache-time", "30s",
		"--buffer-size", "32M",
		"--vfs-read-chunk-size", "16M",
		"--vfs-read-chunk-size-limit", "off",
		"--volname", volname,
		"--log-level", "ERROR",
	}
}

// startRcloneMount 拉起一个 rclone mount 进程，并等到盘符真正出现。
func (a *app) startRcloneMount(name, drive, capacity string) error {
	exe, err := a.ensureRclone()
	if err != nil {
		return err
	}
	if err := a.ensureWinFsp(); err != nil {
		return err
	}
	sections, err := a.writeRcloneConf(exe)
	if err != nil {
		return err
	}
	sec, ok := sections[name]
	if !ok {
		return errors.New("找不到该目录的 rclone 配置，请重新扫描")
	}

	args := rcloneMountArgs(sec, drive, name, capacity)

	// 开一个本地 RC 端口，停止时用 core/quit 让它自己卸载，
	// 直接 Kill 会导致 WinFsp 来不及注销，资源管理器里留下带红叉的僵尸图标。
	port := freeLocalPort()
	if port > 0 {
		args = append(args, "--rc", "--rc-no-auth", "--rc-addr", fmt.Sprintf("127.0.0.1:%d", port))
	}

	cmd := exec.Command(exe, args...)
	// HideWindow 只是让窗口隐藏，控制台仍会被创建并闪一下；
	// CREATE_NO_WINDOW 才是根本不创建控制台。
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 rclone 失败：%v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// 持续把 rclone 的错误输出转进日志，但限制行数，避免参数错误时把 help 全文刷进去
	go func() {
		buf := make([]byte, 4096)
		lineCount := 0
		for {
			n, err := stderr.Read(buf)
			if n > 0 && lineCount < 30 {
				for _, line := range strings.Split(strings.TrimSpace(string(buf[:n])), "\n") {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					// OpenList 的根在 rclone 看来像个符号链接，每次列目录都会报这条，
					// 属于已知噪音，不影响功能。
					if strings.Contains(line, "symlinks not supported") {
						continue
					}
					lineCount++
					if lineCount <= 30 {
						a.logf("  [rclone] %s", line)
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// 盘符在启动前是不是已经有映射。有的话说明是残留，
	// 后面必须看到映射「换成新的」才算挂载成功，否则会把残留误判成成功。
	preRemote := strings.ToLower(driveRemotePath(drive))
	if preRemote != "" {
		a.logf("%s 上已有映射（%s），本次必须看到它被换掉才算挂载成功", drive, preRemote)
	}

	// 等盘符出现，最多 20 秒
	for i := 0; i < 80; i++ {
		remote := strings.ToLower(driveRemotePath(drive))
		if driveExists(drive) && remote != "" && remote != preRemote {
			a.trackProc(name, cmd, drive, port, done)
			return nil
		}
		select {
		case <-done:
			return rcloneExitError(cmd)
		default:
		}
		time.Sleep(250 * time.Millisecond)
	}
	cmd.Process.Kill()
	return errors.New("rclone 启动后 20 秒内盘符没有真正出现（盘符上可能还挂着旧的残留映射），请查看日志")
}

func rcloneExitError(cmd *exec.Cmd) error {
	if cmd.ProcessState == nil {
		return errors.New("rclone 意外退出")
	}
	code := cmd.ProcessState.ExitCode()
	if code == 1 {
		return errors.New("rclone 启动失败，请查看日志中的 [rclone] 行")
	}
	return fmt.Errorf("rclone 退出，代码 %d", code)
}

func (a *app) trackProc(name string, cmd *exec.Cmd, drive string, port int, done chan error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.procs == nil {
		a.procs = map[string]*mountProc{}
	}
	a.procs[name] = &mountProc{cmd: cmd, drive: drive, port: port, done: done}
}

// freeLocalPort 找一个空闲的回环端口给 rclone 的 RC 用。
func freeLocalPort() int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// rcloneQuit 让 rclone 自己卸载并退出。
func rcloneQuit(port int) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/core/quit", port),
		"application/json", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("rc 返回 %s", resp.Status)
	}
	return nil
}

func (a *app) stopRcloneMount(name string) {
	a.mu.Lock()
	p := a.procs[name]
	delete(a.procs, name)
	a.mu.Unlock()
	if p == nil || p.cmd.Process == nil {
		return
	}

	// 优先让它优雅退出：WinFsp 会正常注销挂载点，资源管理器不会留下僵尸图标
	graceful := p.port > 0 && rcloneQuit(p.port) == nil
	if graceful {
		select {
		case <-p.done:
		case <-time.After(6 * time.Second):
			p.cmd.Process.Kill()
		}
	} else {
		p.cmd.Process.Kill()
	}

	// 给 WinFsp 一点时间释放盘符
	for i := 0; i < 30; i++ {
		if !driveExists(p.drive) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (a *app) stopAllRclone() {
	a.mu.Lock()
	procs := a.procs
	a.procs = nil
	a.mu.Unlock()

	// 并发优雅退出，让 WinFsp 有机会逐个注销，否则退出后盘符会留一堆红叉
	var wg sync.WaitGroup
	for _, p := range procs {
		wg.Add(1)
		go func(p *mountProc) {
			defer wg.Done()
			if p.port > 0 && rcloneQuit(p.port) == nil {
				select {
				case <-p.done:
					return
				case <-time.After(5 * time.Second):
				}
			}
			if p.cmd.Process != nil {
				p.cmd.Process.Kill()
			}
		}(p)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
	}
}

func (a *app) hasRcloneProc(name string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	p := a.procs[name]
	return p != nil && p.cmd.Process != nil
}
