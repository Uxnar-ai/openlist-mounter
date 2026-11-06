// refresh 强制让资源管理器重新枚举驱动器，把已经摘掉的盘符从「此电脑」里刷走。
package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

func main() {
	proc := syscall.NewLazyDLL("shell32.dll").NewProc("SHChangeNotify")
	const (
		shcneNetUnshare   = 0x00004000
		shcneDriveRemoved = 0x08000000
		shcneUpdateDir    = 0x00001000
		shcnfPathW        = 0x0005
	)

	// 让「此电脑」整个目录重新枚举
	drives, _ := syscall.UTF16PtrFromString("::{20D04FE0-3AEA-1069-A2D8-08002B30309D}")
	proc.Call(shcneUpdateDir, shcnfPathW, uintptr(unsafe.Pointer(drives)), 0)

	// 逐个盘符发移除通知
	for c := 'A'; c <= 'Z'; c++ {
		p, _ := syscall.UTF16PtrFromString(string(c) + `:\`)
		proc.Call(shcneNetUnshare, shcnfPathW, uintptr(unsafe.Pointer(p)), 0)
		proc.Call(shcneDriveRemoved, shcnfPathW, uintptr(unsafe.Pointer(p)), 0)
	}
	fmt.Println("shell refresh sent")
}
