// shot 用 GDI 截取当前屏幕，用于排查界面显示问题（不依赖 .NET / Add-Type）。
// 用法: shot <out.png>
package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"syscall"
	"unsafe"
)

var (
	modUser32 = syscall.NewLazyDLL("user32.dll")
	modGdi32  = syscall.NewLazyDLL("gdi32.dll")
)

type bmiHeader struct {
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
	hdr    bmiHeader
	colors [4]uint32
}

func main() {
	out := "screen.png"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}

	smCX, _, _ := modUser32.NewProc("GetSystemMetrics").Call(0)
	smCY, _, _ := modUser32.NewProc("GetSystemMetrics").Call(1)
	fullW, fullH := int(smCX), int(smCY)
	if fullW <= 0 || fullH <= 0 {
		return
	}

	// 可选：shot out.png x y w h  只截指定区域
	x, y, w, h := 0, 0, fullW, fullH
	if len(os.Args) >= 6 {
		fmt.Sscanf(os.Args[2], "%d", &x)
		fmt.Sscanf(os.Args[3], "%d", &y)
		fmt.Sscanf(os.Args[4], "%d", &w)
		fmt.Sscanf(os.Args[5], "%d", &h)
	}

	hdcScreen, _, _ := modUser32.NewProc("GetDC").Call(0)
	defer modUser32.NewProc("ReleaseDC").Call(0, hdcScreen)

	hdcMem, _, _ := modGdi32.NewProc("CreateCompatibleDC").Call(hdcScreen)
	defer modGdi32.NewProc("DeleteDC").Call(hdcMem)

	hbm, _, _ := modGdi32.NewProc("CreateCompatibleBitmap").Call(hdcScreen, uintptr(w), uintptr(h))
	if hbm == 0 {
		return
	}
	defer modGdi32.NewProc("DeleteObject").Call(hbm)
	modGdi32.NewProc("SelectObject").Call(hdcMem, hbm)

	const srccopy = 0x00CC0020
	modGdi32.NewProc("BitBlt").Call(hdcMem, 0, 0, uintptr(w), uintptr(h),
		hdcScreen, uintptr(x), uintptr(y), srccopy)

	var bi bitmapInfo
	bi.hdr.biSize = uint32(unsafe.Sizeof(bi.hdr))
	bi.hdr.biWidth = int32(w)
	bi.hdr.biHeight = -int32(h) // 自顶向下
	bi.hdr.biPlanes = 1
	bi.hdr.biBitCount = 32
	bi.hdr.biCompression = 0

	buf := make([]byte, w*h*4)
	n, _, _ := modGdi32.NewProc("GetDIBits").Call(
		hdcMem, hbm, 0, uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&bi)), 0)
	if n == 0 {
		return
	}

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := (y*w + x) * 4
			d := img.PixOffset(x, y)
			img.Pix[d+0] = buf[s+2]
			img.Pix[d+1] = buf[s+1]
			img.Pix[d+2] = buf[s+0]
			img.Pix[d+3] = 255
		}
	}

	f, err := os.Create(out)
	if err != nil {
		return
	}
	defer f.Close()
	png.Encode(f, img)
}
