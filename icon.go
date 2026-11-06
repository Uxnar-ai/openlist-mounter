package main

import (
	"image"
	"math"
	"syscall"
	"unsafe"
)

// 图标取自用户提供的 SVG（viewBox 1316x1024），这里按原始坐标重绘，
// 不依赖 SVG 渲染器，任意尺寸都能得到干净的边缘。
//
// 几何：一个上窄下宽的硬盘体 + 一个内凹槽 + 两颗指示灯（用主色覆盖形成挖空感）。

type iconTheme struct {
	name string
	main uint32 // 0xRRGGBB
	dark uint32
}

var (
	iconBlue   = iconTheme{"blue", 0x5B9BF0, 0x3E78D2}
	iconGray   = iconTheme{"gray", 0xC7CCD4, 0xA5ADBA}
	iconGreen  = iconTheme{"green", 0x62D07F, 0x50AD6D}
	iconYellow = iconTheme{"yellow", 0xFFDD37, 0xEEBE00}
	iconRed    = iconTheme{"red", 0xE15B53, 0xBA3A49}
)

var diskBody = [][2]float64{
	{1082.806857, 0},
	{232.886857, 0},
	{0, 500.662857},
	{0, 1001.325714},
	{1315.693714, 1001.325714},
	{1315.693714, 500.662857},
}

type diskBox struct{ x0, y0, x1, y1 float64 }

func (b diskBox) contains(x, y float64) bool {
	return x >= b.x0 && x <= b.x1 && y >= b.y0 && y <= b.y1
}

var (
	diskSlot = diskBox{100.900571, 543.451429, 1214.793143, 900.388571}
	diskLed1 = diskBox{706.340571, 648.118857, 853.832000, 795.610000}
	diskLed2 = diskBox{962.486857, 648.118857, 1110.000000, 795.610000}
)

func inPolygon(x, y float64, pts [][2]float64) bool {
	inside := false
	n := len(pts)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		xi, yi := pts[i][0], pts[i][1]
		xj, yj := pts[j][0], pts[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			inside = !inside
		}
	}
	return inside
}

func channel(c uint32, shift uint) float64 {
	return float64((c >> shift) & 0xFF)
}

// renderDiskIcon 输出 size×size 的图标，超采样 5×5。
func renderDiskIcon(size int, th iconTheme) *image.NRGBA {
	const ss = 5
	const vbW, vbH = 1316.0, 1024.0

	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	if size <= 0 {
		return img
	}

	margin := float64(size) * 0.055
	scale := math.Min((float64(size)-2*margin)/vbW, (float64(size)-2*margin)/vbH)
	ox := (float64(size) - vbW*scale) / 2
	oy := (float64(size) - vbH*scale) / 2

	mr, mg, mb := channel(th.main, 16), channel(th.main, 8), channel(th.main, 0)
	dr, dg, db := channel(th.dark, 16), channel(th.dark, 8), channel(th.dark, 0)
	samples := float64(ss * ss)

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var hits, sr, sg, sb float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/ss
					py := float64(y) + (float64(sy)+0.5)/ss
					vx := (px - ox) / scale
					vy := (py - oy) / scale
					if !inPolygon(vx, vy, diskBody) {
						continue
					}
					hits++
					r, g, b := mr, mg, mb
					if diskSlot.contains(vx, vy) {
						r, g, b = dr, dg, db
					}
					if diskLed1.contains(vx, vy) || diskLed2.contains(vx, vy) {
						r, g, b = mr, mg, mb
					}
					sr += r
					sg += g
					sb += b
				}
			}
			if hits == 0 {
				continue
			}
			i := img.PixOffset(x, y)
			img.Pix[i+0] = byte(sr/hits + 0.5)
			img.Pix[i+1] = byte(sg/hits + 0.5)
			img.Pix[i+2] = byte(sb/hits + 0.5)
			img.Pix[i+3] = byte(hits/samples*255 + 0.5)
		}
	}
	return img
}

// toBGRA 把 NRGBA 转成 DIB 需要的 BGRA 顺序（自顶向下）。
func toBGRA(img *image.NRGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	out := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			o := (y*w + x) * 4
			out[o+0] = img.Pix[i+2]
			out[o+1] = img.Pix[i+1]
			out[o+2] = img.Pix[i+0]
			out[o+3] = img.Pix[i+3]
		}
	}
	return out
}

// makeTrayIcon 生成一个 HICON。调用方负责在用完后 DestroyIcon。
func makeTrayIcon(size int, th iconTheme) syscall.Handle {
	if size < 8 {
		size = 16
	}
	px := toBGRA(renderDiskIcon(size, th))
	return makeHIcon(px, size, size)
}

func makeHIcon(px []byte, w, h int) syscall.Handle {
	if w <= 0 || h <= 0 || len(px) < w*h*4 {
		return 0
	}
	var bi bitmapInfo
	bi.bmiHeader.biSize = uint32(unsafe.Sizeof(bi.bmiHeader))
	bi.bmiHeader.biWidth = int32(w)
	bi.bmiHeader.biHeight = int32(h) // 正数：自底向上
	bi.bmiHeader.biPlanes = 1
	bi.bmiHeader.biBitCount = 32
	bi.bmiHeader.biCompression = biRGB
	bi.bmiHeader.biSizeImage = uint32(len(px))

	var bits unsafe.Pointer
	hbm, _, _ := procCreateDIBSection.Call(
		0, uintptr(unsafe.Pointer(&bi)), dibRGBColors,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hbm == 0 || bits == nil {
		return 0
	}
	dst := unsafe.Slice((*byte)(bits), len(px))
	row := w * 4
	for y := 0; y < h; y++ {
		copy(dst[(h-1-y)*row:], px[y*row:(y+1)*row])
	}

	hMask, _, _ := procCreateBitmap.Call(uintptr(w), uintptr(h), 1, 1, 0)
	ii := iconInfo{
		fIcon:    1,
		hbmMask:  syscall.Handle(hMask),
		hbmColor: syscall.Handle(hbm),
	}
	hIcon, _, _ := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	procDeleteObject.Call(hbm)
	procDeleteObject.Call(hMask)
	return syscall.Handle(hIcon)
}
