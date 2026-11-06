package main

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"os"
	"testing"
)

// TestVerifyExeIcon 直接解析 exe 的 PE 资源目录，确认图标真的被链进去了，
// 并把 RT_ICON 里的位图导出成 PNG，做视觉校验。
func TestVerifyExeIcon(t *testing.T) {
	data, err := os.ReadFile("build/OpenListMounter.exe")
	if err != nil {
		t.Skip("exe 不存在，跳过")
	}
	pe, err := parsePE(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf(".rsrc 节: vaddr=0x%x size=%d", pe.rsrc.vaddr, pe.rsrc.vsize)
	for _, s := range pe.sections {
		t.Logf("  %-10s vaddr=0x%08x vsize=%-9d raw=0x%x rawsize=%d", s.name, s.vaddr, s.vsize, s.rawOff, s.rawSize)
	}

	entries, err := pe.listIcons(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("资源里没有图标")
	}
	for _, e := range entries {
		t.Logf("RT_ICON id=%d %dx%d bytes=%d", e.id, e.w, e.h, len(e.data))
	}
	// 导出最大的那张，确认像素正常
	best := entries[0]
	for _, e := range entries {
		if e.w > best.w {
			best = e
		}
	}
	img, err := dibToImage(best.data)
	if err != nil {
		t.Fatalf("解出 %dx%d 失败: %v", best.w, best.h, err)
	}
	out, _ := os.Create("preview/exe_icon.png")
	defer out.Close()
	if err := png.Encode(out, img); err != nil {
		t.Fatal(err)
	}
	t.Logf("已导出 preview/exe_icon.png (%dx%d)", best.w, best.h)
}

// ---------------------------------------------------------------- PE

type peSection struct {
	name   string
	vaddr  uint32
	vsize  uint32
	rawOff uint32
	rawSize uint32
}

type peInfo struct {
	sections []peSection
	rsrc     peSection
	resDirRVA uint32
}

func parsePE(data []byte) (*peInfo, error) {
	if len(data) < 0x40 || data[0] != 'M' || data[1] != 'Z' {
		return nil, fmt.Errorf("不是 PE 文件")
	}
	le := binary.LittleEndian
	peOff := int(le.Uint32(data[0x3C:]))
	if peOff+24 > len(data) || string(data[peOff:peOff+4]) != "PE\x00\x00" {
		return nil, fmt.Errorf("PE 头无效")
	}
	numSec := int(le.Uint16(data[peOff+6:]))
	optSize := int(le.Uint16(data[peOff+20:]))
	optOff := peOff + 24

	magic := le.Uint16(data[optOff:])
	var ddOff int
	switch magic {
	case 0x20B:
		ddOff = optOff + 112
	case 0x10B:
		ddOff = optOff + 96
	default:
		return nil, fmt.Errorf("未知 optional header magic 0x%x", magic)
	}

	info := &peInfo{}
	info.resDirRVA = le.Uint32(data[ddOff+2*8:]) // directory[2] = resource

	secOff := optOff + optSize
	for i := 0; i < numSec; i++ {
		b := data[secOff+i*40:]
		name := string(b[:8])
		for j := 0; j < len(name); j++ {
			if name[j] == 0 {
				name = name[:j]
				break
			}
		}
		s := peSection{
			name:   name,
			vsize:  le.Uint32(b[8:]),
			vaddr:  le.Uint32(b[12:]),
			rawSize: le.Uint32(b[16:]),
			rawOff: le.Uint32(b[20:]),
		}
		info.sections = append(info.sections, s)
		if name == ".rsrc" {
			info.rsrc = s
		}
	}
	if info.rsrc.vaddr == 0 {
		return nil, fmt.Errorf("没有 .rsrc 节")
	}
	return info, nil
}

func (p *peInfo) rva2off(rva uint32) (int, bool) {
	for _, s := range p.sections {
		if rva >= s.vaddr && rva < s.vaddr+s.vsize {
			return int(s.rawOff + (rva - s.vaddr)), true
		}
	}
	return 0, false
}

type iconEntry struct {
	id   int
	w, h int
	data []byte
}

// resourceDataEntry 沿着目录项往下走，直到拿到 IMAGE_RESOURCE_DATA_ENTRY 的文件偏移。
//
// 资源树理论上三层（类型 → 名称 → 语言 → 数据项），但 iconpatch 生成的是两层：
// 名称层那一项的高位没有置上，直接指向数据项。两种布局都要认，
// 否则测试会因为「多跳了一层」把数据项当成目录，读到垃圾（组图标 RVA 0x3 就是这么来的）。
func resourceDataEntry(data []byte, base int, entryOff uint32) (int, bool) {
	le := binary.LittleEndian
	for i := 0; i < 3 && entryOff&0x80000000 != 0; i++ {
		dir := base + int(entryOff&0x7FFFFFFF)
		if dir+20 > len(data) {
			return 0, false
		}
		if int(le.Uint16(data[dir+12:]))+int(le.Uint16(data[dir+14:])) == 0 {
			return 0, false
		}
		entryOff = le.Uint32(data[dir+16+4:])
	}
	if entryOff&0x80000000 != 0 {
		return 0, false
	}
	off := base + int(entryOff&0x7FFFFFFF)
	if off+8 > len(data) {
		return 0, false
	}
	return off, true
}

func (p *peInfo) listIcons(data []byte) ([]iconEntry, error) {
	le := binary.LittleEndian
	base := int(p.rsrc.rawOff)

	// 根目录 -> 找 RT_GROUP_ICON(14) 和 RT_ICON(3)
	root := base
	count := int(le.Uint16(data[root+12:])) + int(le.Uint16(data[root+14:]))
	typeDirs := map[uint32]int{}
	for i := 0; i < count; i++ {
		e := root + 16 + i*8
		id := le.Uint32(data[e:])
		off := le.Uint32(data[e+4:])
		if off&0x80000000 == 0 {
			continue
		}
		typeDirs[id] = base + int(off&0x7FFFFFFF)
	}

	// 读 RT_GROUP_ICON 得到顺序与尺寸，再按 id 找 RT_ICON 数据
	grpDir, ok := typeDirs[rtGroupIcon]
	if !ok {
		return nil, fmt.Errorf("缺少 RT_GROUP_ICON")
	}
	grpEntry, ok := resourceDataEntry(data, base, le.Uint32(data[grpDir+16+4:]))
	if !ok {
		return nil, fmt.Errorf("定位组图标数据项失败")
	}
	grpRVA := le.Uint32(data[grpEntry:])
	grpSize := le.Uint32(data[grpEntry+4:])
	grpOff, ok := p.rva2off(grpRVA)
	if !ok {
		return nil, fmt.Errorf("组图标 RVA 0x%x 无法映射到文件偏移（重定位可能错了）", grpRVA)
	}
	grp := data[grpOff : grpOff+int(grpSize)]
	n := int(le.Uint16(grp[4:]))

	iconTypeDir, ok := typeDirs[rtIcon]
	if !ok {
		return nil, fmt.Errorf("缺少 RT_ICON")
	}
	// 类型层下面是「图标 ID」层，每个 ID 再往下（语言层 / 直接数据项）才到数据。
	// 层数各版本不一致，所以逐项用 resourceDataEntry 走到底，别写死偏移。
	iconCount := int(le.Uint16(data[iconTypeDir+12:])) + int(le.Uint16(data[iconTypeDir+14:]))
	iconByName := map[uint32]int{}
	for i := 0; i < iconCount; i++ {
		e := iconTypeDir + 16 + i*8
		id := le.Uint32(data[e:])
		de, ok := resourceDataEntry(data, base, le.Uint32(data[e+4:]))
		if !ok {
			return nil, fmt.Errorf("RT_ICON %d 的数据项定位失败", id)
		}
		iconByName[id] = de
	}

	var out []iconEntry
	for i := 0; i < n; i++ {
		b := grp[6+14*i:]
		w, h := int(b[0]), int(b[1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		size := int(le.Uint32(b[8:]))
		id := uint32(le.Uint16(b[12:]))
		de, ok := iconByName[id]
		if !ok {
			return nil, fmt.Errorf("组图标引用了不存在的 RT_ICON %d", id)
		}
		rva := le.Uint32(data[de:])
		declared := le.Uint32(data[de+4:])
		off, ok := p.rva2off(rva)
		if !ok {
			return nil, fmt.Errorf("RT_ICON %d 的 RVA 0x%x 无法映射（重定位可能错了）", id, rva)
		}
		if int(declared) != size {
			t := fmt.Sprintf("RT_ICON %d 大小不一致: 目录=%d 组=%d", id, declared, size)
			return nil, fmt.Errorf("%s", t)
		}
		out = append(out, iconEntry{id: int(id), w: w, h: h, data: data[off : off+size]})
	}
	return out, nil
}

// dibToImage 把 ICONIMAGE（BITMAPINFOHEADER + BGRA）解成 image.NRGBA。
func dibToImage(b []byte) (*image.NRGBA, error) {
	if len(b) < 40 {
		return nil, fmt.Errorf("数据太短")
	}
	le := binary.LittleEndian
	w := int(int32(le.Uint32(b[4:])))
	h := int(int32(le.Uint32(b[8:]))) / 2
	if w <= 0 || h <= 0 || w > 1024 || h > 1024 {
		return nil, fmt.Errorf("尺寸异常 %dx%d", w, h)
	}
	need := 40 + w*h*4
	if len(b) < need {
		return nil, fmt.Errorf("像素数据不足: %d < %d", len(b), need)
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	px := b[40:]
	for y := 0; y < h; y++ {
		src := (h - 1 - y) * w * 4
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i+0] = px[src+x*4+2]
			img.Pix[i+1] = px[src+x*4+1]
			img.Pix[i+2] = px[src+x*4+0]
			img.Pix[i+3] = px[src+x*4+3]
		}
	}
	return img, nil
}
