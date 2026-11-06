package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestShutdownExitsMessageLoop 直接盯 BUG 1。
//
// 手法和线上一样：从一个「不是窗口线程」的 goroutine（相当于 HTTP handler）
// 调 shutdown()，然后看主线程的 messageLoop 会不会返回。
//
// 老写法（PostQuitMessage 打在错的线程 + 跨线程 DestroyWindow）在这里必然超时，
// 因为没人能在别的线程上销毁窗口，WM_DESTROY 永远不会来。
func TestShutdownExitsMessageLoop(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	oldForce := shutdownForceExit
	shutdownForceExit = 0 // 关掉兜底 os.Exit，否则失败会被它悄悄掩盖成退出码 0
	defer func() { shutdownForceExit = oldForce }()

	a := &app{
		states:  map[string]*folderState{},
		logPath: filepath.Join(t.TempDir(), "log.txt"),
	}
	theApp = a
	defer func() { theApp = nil }()

	if err := a.createMessageWindow(); err != nil {
		t.Fatalf("创建消息窗口失败：%v", err)
	}
	if a.hwnd == 0 {
		t.Fatal("窗口句柄为 0")
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		a.shutdown()
	}()

	loopDone := make(chan struct{})
	go func() {
		select {
		case <-loopDone:
		case <-time.After(5 * time.Second):
			t.Errorf("shutdown() 之后消息循环没有退出（BUG 1 复现）")
			// 别把整个测试进程挂死：补一条 WM_CLOSE 再等，还不行就直接退出
			procPostMessageW.Call(uintptr(a.hwnd), wmClose, 0, 0)
			select {
			case <-loopDone:
			case <-time.After(2 * time.Second):
				os.Exit(1)
			}
		}
	}()

	messageLoop()
	close(loopDone)
}

// TestShutdownIdempotent 网页连点两次「退出」、或提权交接时重复调用，不能死锁。
func TestShutdownIdempotent(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	oldForce := shutdownForceExit
	shutdownForceExit = 0
	defer func() { shutdownForceExit = oldForce }()

	a := &app{
		states:  map[string]*folderState{},
		logPath: filepath.Join(t.TempDir(), "log.txt"),
	}
	theApp = a
	defer func() { theApp = nil }()

	if err := a.createMessageWindow(); err != nil {
		t.Fatalf("创建消息窗口失败：%v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.shutdown()
		a.shutdown()
		a.shutdown()
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("重复调用 shutdown() 卡住了")
	}

	// 窗口还在（WM_CLOSE 还排在队列里没人取），这里把消息循环跑起来收尾
	loopDone := make(chan struct{})
	go func() {
		select {
		case <-loopDone:
		case <-time.After(5 * time.Second):
			t.Error("重复 shutdown() 之后消息循环没有退出")
			os.Exit(1)
		}
	}()
	messageLoop()
	close(loopDone)
}
