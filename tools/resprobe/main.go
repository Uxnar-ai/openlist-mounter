// resprobe 用 Windows 自己的资源 API 探测 exe 里的图标资源，
// 判断问题出在「找不到资源」还是「图标数据本身无效」。
package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")
	modUser32   = syscall.NewLazyDLL("user32.dll")
)

const (
	loadLibraryAsDatafile       = 0x00000002
	loadLibraryAsImageResource  = 0x00000020
	rtIcon                      = 3
	rtGroupIcon                 = 14
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: resprobe <exe>")
		os.Exit(2)
	}
	p, _ := syscall.UTF16PtrFromString(os.Args[1])

	loadLib := modKernel32.NewProc("LoadLibraryExW")
	h, _, err := loadLib.Call(
		uintptr(unsafe.Pointer(p)), 0,
		loadLibraryAsDatafile|loadLibraryAsImageResource)
	if h == 0 {
		fmt.Printf("LoadLibraryEx 失败: %v\n", err)
		os.Exit(1)
	}
	defer modKernel32.NewProc("FreeLibrary").Call(h)
	fmt.Printf("模块句柄 = 0x%x\n", h)

	findRes := modKernel32.NewProc("FindResourceW")
	sizeRes := modKernel32.NewProc("SizeofResource")
	loadRes := modKernel32.NewProc("LoadResource")

	check := func(name uintptr, typ uintptr, label string) {
		r, _, _ := findRes.Call(h, name, typ)
		if r == 0 {
			fmt.Printf("  %-28s 未找到\n", label)
			return
		}
		sz, _, _ := sizeRes.Call(h, r)
		d, _, _ := loadRes.Call(h, r)
		fmt.Printf("  %-28s 找到 size=%d data=0x%x\n", label, sz, d)
	}

	fmt.Println("FindResource 探测：")
	check(1, rtGroupIcon, "RT_GROUP_ICON #1")
	check(1, rtIcon, "RT_ICON #1")
	check(9, rtIcon, "RT_ICON #9")

	// 用 LoadImage 试着取图标，这一步和 Explorer 一样
	loadImage := modUser32.NewProc("LoadImageW")
	hi, _, _ := loadImage.Call(h, 1, 1 /*IMAGE_ICON*/, 0, 0, 0x0000 /*LR_DEFAULTCOLOR*/)
	fmt.Printf("LoadImage(MAKEINTRESOURCE(1), IMAGE_ICON) = 0x%x\n", hi)
}
