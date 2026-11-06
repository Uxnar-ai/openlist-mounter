// icontest 走 Windows 真正的图标加载路径（ExtractIconEx），
// 验证 exe 里的资源能不能被系统识别并渲染出来。
// 用法: icontest <exe> <out.png>
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
	modShell32 = syscall.NewLazyDLL("shell32.dll")
	modUser32  = syscall.NewLazyDLL("user32.dll")
	modGdi32   = syscall.NewLazyDLL("gdi32.dll")
)

type iconInfo struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	hbmMask  syscall.Handle
	hbmColor syscall.Handle
}

type bitmapT struct {
	bmType       int32
	bmWidth      int32
	bmHeight     int32
	bmWidthBytes int32
	bmPlanes     uint16
	bmBitsPixel  uint16
	bmBits       uintptr
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

type bitmapInfoT struct {
	bmiHeader bitmapInfoHeader
	bmiColors [4]uint32
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: icontest <exe> [out.png]")
		os.Exit(2)
	}
	exe := os.Args[1]
	out := "icon_out.png"
	if len(os.Args) > 2 {
		out = os.Args[2]
	}

	p, err := syscall.UTF16PtrFromString(exe)
	if err != nil {
		panic(err)
	}

	var hLarge, hSmall syscall.Handle
	extract := modShell32.NewProc("ExtractIconExW")
	n, _, _ := extract.Call(
		uintptr(unsafe.Pointer(p)),
		0,
		uintptr(unsafe.Pointer(&hLarge)),
		uintptr(unsafe.Pointer(&hSmall)),
		1)
	fmt.Printf("ExtractIconEx 返回图标数 = %d (large=%v small=%v)\n", int32(n), hLarge, hSmall)
	if int32(n) <= 0 || hLarge == 0 {
		fmt.Println("结果：系统读不到任何图标资源")
		os.Exit(1)
	}

	getIconInfo := modUser32.NewProc("GetIconInfo")
	var ii iconInfo
	r, _, _ := getIconInfo.Call(uintptr(hLarge), uintptr(unsafe.Pointer(&ii)))
	if r == 0 {
		fmt.Println("GetIconInfo 失败")
		os.Exit(1)
	}
	fmt.Printf("hbmColor=%v hbmMask=%v\n", ii.hbmColor, ii.hbmMask)

	getObject := modGdi32.NewProc("GetObjectW")
	var bm bitmapT
	r, _, _ = getObject.Call(uintptr(ii.hbmColor), unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm)))
	if r == 0 {
		fmt.Println("GetObject 失败")
		os.Exit(1)
	}
	fmt.Printf("位图 %dx%d, %d bpp\n", bm.bmWidth, bm.bmHeight, bm.bmBitsPixel)

	// 用 DrawIconEx 渲染到 32bpp DIB，检查真实绘制结果（含 alpha）
	w, h := int(bm.bmWidth), int(bm.bmHeight)
	hdcScreen, _, _ := modUser32.NewProc("GetDC").Call(0)
	defer modUser32.NewProc("ReleaseDC").Call(0, hdcScreen)
	hdcMem, _, _ := modGdi32.NewProc("CreateCompatibleDC").Call(hdcScreen)
	defer modGdi32.NewProc("DeleteDC").Call(hdcMem)

	var bi bitmapInfoT
	bi.bmiHeader.biSize = uint32(unsafe.Sizeof(bi.bmiHeader))
	bi.bmiHeader.biWidth = int32(w)
	bi.bmiHeader.biHeight = int32(-h) // 自顶向下
	bi.bmiHeader.biPlanes = 1
	bi.bmiHeader.biBitCount = 32
	bi.bmiHeader.biCompression = 0

	var bits unsafe.Pointer
	hbmDib, _, _ := modGdi32.NewProc("CreateDIBSection").Call(
		hdcScreen, uintptr(unsafe.Pointer(&bi)), 0,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hbmDib == 0 {
		fmt.Println("CreateDIBSection 失败")
		os.Exit(1)
	}
	old, _, _ := modGdi32.NewProc("SelectObject").Call(hdcMem, hbmDib)

	// 画个洋红底，方便看出透明区域
	brush, _, _ := modGdi32.NewProc("CreateSolidBrush").Call(0x00FF00FF)
	var rc = [4]int32{0, 0, int32(w), int32(h)}
	modUser32.NewProc("FillRect").Call(hdcMem, uintptr(unsafe.Pointer(&brush)), uintptr(unsafe.Pointer(&rc[0])))
	modGdi32.NewProc("DeleteObject").Call(brush)

	drawIcon := modUser32.NewProc("DrawIconEx")
	drawIcon.Call(hdcMem, 0, 0, uintptr(hLarge), uintptr(w), uintptr(h), 0, 0, 3) // DI_NORMAL
	modGdi32.NewProc("SelectObject").Call(hdcMem, old)

	px := unsafe.Slice((*byte)(bits), w*h*4)
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	opaque, colored := 0, 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := (y*w + x) * 4
			d := img.PixOffset(x, y)
			img.Pix[d+0] = px[s+2]
			img.Pix[d+1] = px[s+1]
			img.Pix[d+2] = px[s+0]
			img.Pix[d+3] = 255
			if px[s] != 255 || px[s+1] != 0 || px[s+2] != 255 {
				colored++
			}
			if px[s+3] != 0 {
				opaque++
			}
		}
	}
	f, _ := os.Create(out)
	png.Encode(f, img)
	f.Close()
	fmt.Printf("导出 %s：非洋红像素 %d/%d（说明图标确实画上去了）\n", out, colored, w*h)
}
