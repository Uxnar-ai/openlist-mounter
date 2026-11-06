// iconpatch 把 .ico 作为 RT_GROUP_ICON/RT_ICON 资源追加进 PE 文件。
// 用法: iconpatch <exe> <ico>
//
// 之所以不用 .syso：Go 链接器对外部 COFF 的 .rsrc 段做 RVA 重定位时会产出
// 结构损坏的 PE。编译完成后再补一节，完全不经过链接器，稳定得多。
package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

const (
	rtIcon      = 3
	rtGroupIcon = 14
	resLang     = 0x0409

	// IMAGE_RESOURCE_DIRECTORY 头是 16 字节：4+4+2+2+2+2
	dirHeaderSize = 16
	dirEntrySize  = 8
	dataEntrySize = 16
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: iconpatch <exe> <ico>")
		os.Exit(2)
	}
	exePath, icoPath := os.Args[1], os.Args[2]

	ico, err := os.ReadFile(icoPath)
	if err != nil {
		fail(err)
	}
	images, widths, heights, err := parseICO(ico)
	if err != nil {
		fail(err)
	}

	exe, err := os.ReadFile(exePath)
	if err != nil {
		fail(err)
	}

	out, err := patch(exe, images, widths, heights)
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(exePath, out, 0o755); err != nil {
		fail(err)
	}
	fmt.Printf("ok: %d 张图标写入，文件 %d -> %d 字节\n", len(images), len(exe), len(out))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "失败:", err)
	os.Exit(1)
}

func align(v, a uint32) uint32 {
	if a == 0 {
		return v
	}
	if rem := v % a; rem != 0 {
		return v + a - rem
	}
	return v
}

func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// ---------------------------------------------------------------- ICO

func parseICO(b []byte) (images [][]byte, widths, heights []int, err error) {
	le := binary.LittleEndian
	if len(b) < 6 {
		return nil, nil, nil, fmt.Errorf("ICO 太短")
	}
	if le.Uint16(b[2:]) != 1 {
		return nil, nil, nil, fmt.Errorf("不是图标类型")
	}
	n := int(le.Uint16(b[4:]))
	if n == 0 || len(b) < 6+16*n {
		return nil, nil, nil, fmt.Errorf("图标目录无效")
	}
	for i := 0; i < n; i++ {
		e := b[6+16*i:]
		w, h := int(e[0]), int(e[1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		size := int(le.Uint32(e[8:]))
		off := int(le.Uint32(e[12:]))
		if size <= 0 || off+size > len(b) {
			return nil, nil, nil, fmt.Errorf("第 %d 张图标数据越界", i)
		}
		images = append(images, b[off:off+size])
		widths = append(widths, w)
		heights = append(heights, h)
	}
	return images, widths, heights, nil
}

// ---------------------------------------------------------------- RSRC

func putDir(b []byte, off int, id uint32, subdir int) {
	le := binary.LittleEndian
	le.PutUint32(b[off:], id)
	le.PutUint32(b[off+4:], uint32(subdir)|0x80000000)
}

func putLeaf(b []byte, off int, id uint32, dataEntry int) {
	le := binary.LittleEndian
	le.PutUint32(b[off:], id)
	le.PutUint32(b[off+4:], uint32(dataEntry))
}

func putData(b []byte, off int, rva uint32, size int) {
	le := binary.LittleEndian
	le.PutUint32(b[off:], rva)
	le.PutUint32(b[off+4:], uint32(size))
	le.PutUint32(b[off+8:], 0)
	le.PutUint32(b[off+12:], 0)
}

// buildRSRC 构造资源树：Type → Name → Language → DataEntry，共三层目录。
func buildRSRC(images [][]byte, widths, heights []int, rvaBase uint32) []byte {
	le := binary.LittleEndian
	n := len(images)

	grp := make([]byte, 6+14*n)
	le.PutUint16(grp[0:], 0)
	le.PutUint16(grp[2:], 1)
	le.PutUint16(grp[4:], uint16(n))
	for i, img := range images {
		b := grp[6+14*i:]
		if widths[i] >= 256 {
			b[0] = 0
		} else {
			b[0] = byte(widths[i])
		}
		if heights[i] >= 256 {
			b[1] = 0
		} else {
			b[1] = byte(heights[i])
		}
		le.PutUint16(b[4:], 1)
		le.PutUint16(b[6:], 32)
		le.PutUint32(b[8:], uint32(len(img)))
		le.PutUint16(b[12:], uint16(i+1))
	}

	off := 0
	rootOff := off
	off += dirHeaderSize + 2*dirEntrySize // 根就是「类型层」

	iconNameOff := off // 名称层：每个图标一个 ID
	off += dirHeaderSize + n*dirEntrySize
	iconLangOff := make([]int, n) // 语言层
	for i := range iconLangOff {
		iconLangOff[i] = off
		off += dirHeaderSize + dirEntrySize
	}

	groupNameOff := off
	off += dirHeaderSize + dirEntrySize
	groupLangOff := off
	off += dirHeaderSize + dirEntrySize

	iconEntry := make([]int, n)
	for i := range iconEntry {
		iconEntry[i] = off
		off += dataEntrySize
	}
	groupEntryOff := off
	off += dataEntrySize

	iconData := make([]int, n)
	for i := range iconData {
		iconData[i] = off
		off += len(images[i])
	}
	groupDataOff := off
	off += len(grp)

	buf := make([]byte, off)

	// 根目录 = 类型层，直接指向名称层
	le.PutUint16(buf[rootOff+14:], 2)
	putDir(buf, rootOff+dirHeaderSize, rtIcon, iconNameOff)
	putDir(buf, rootOff+dirHeaderSize+dirEntrySize, rtGroupIcon, groupNameOff)

	// RT_ICON 名称层 -> 语言层
	le.PutUint16(buf[iconNameOff+14:], uint16(n))
	for i := 0; i < n; i++ {
		putDir(buf, iconNameOff+dirHeaderSize+i*dirEntrySize, uint32(i+1), iconLangOff[i])
	}
	for i := 0; i < n; i++ {
		le.PutUint16(buf[iconLangOff[i]+14:], 1)
		putLeaf(buf, iconLangOff[i]+dirHeaderSize, resLang, iconEntry[i])
	}
	for i := 0; i < n; i++ {
		putData(buf, iconEntry[i], rvaBase+uint32(iconData[i]), len(images[i]))
		copy(buf[iconData[i]:], images[i])
	}

	// RT_GROUP_ICON 名称层 -> 语言层
	le.PutUint16(buf[groupNameOff+14:], 1)
	putDir(buf, groupNameOff+dirHeaderSize, 1, groupLangOff)
	le.PutUint16(buf[groupLangOff+14:], 1)
	putLeaf(buf, groupLangOff+dirHeaderSize, resLang, groupEntryOff)
	putData(buf, groupEntryOff, rvaBase+uint32(groupDataOff), len(grp))
	copy(buf[groupDataOff:], grp)

	return buf
}

// ---------------------------------------------------------------- PE patch

func patch(data []byte, images [][]byte, widths, heights []int) ([]byte, error) {
	le := binary.LittleEndian
	if len(data) < 0x40 || data[0] != 'M' || data[1] != 'Z' {
		return nil, fmt.Errorf("不是 PE 文件")
	}
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

	sectionAlign := le.Uint32(data[optOff+32:])
	fileAlign := le.Uint32(data[optOff+36:])
	sizeOfImage := le.Uint32(data[optOff+56:])
	sizeOfHeaders := le.Uint32(data[optOff+60:])
	secOff := optOff + optSize

	hasRsrc, lastEnd := false, uint32(0)
	for i := 0; i < numSec; i++ {
		b := data[secOff+i*40:]
		if cstr(b[:8]) == ".rsrc" {
			hasRsrc = true
		}
		if end := le.Uint32(b[12:]) + le.Uint32(b[8:]); end > lastEnd {
			lastEnd = end
		}
	}
	if hasRsrc {
		return nil, fmt.Errorf("已存在 .rsrc 节，先跳过以免破坏现有资源")
	}

	newSecOff := secOff + numSec*40
	if newSecOff+40 > int(sizeOfHeaders) {
		return nil, fmt.Errorf("节表空间不足（headers=%d, 需要=%d）", sizeOfHeaders, newSecOff+40)
	}

	newVA := align(lastEnd, sectionAlign)
	rsrc := buildRSRC(images, widths, heights, newVA)
	newRaw := align(uint32(len(data)), fileAlign)
	newSize := uint32(len(rsrc))
	rawSize := align(newSize, fileAlign)

	out := make([]byte, int(newRaw+rawSize))
	copy(out, data)
	copy(out[newRaw:], rsrc)

	sh := out[newSecOff:]
	copy(sh[0:8], ".rsrc\x00\x00\x00")
	le.PutUint32(sh[8:], newSize)
	le.PutUint32(sh[12:], newVA)
	le.PutUint32(sh[16:], rawSize)
	le.PutUint32(sh[20:], newRaw)
	le.PutUint16(sh[32:], 0)
	le.PutUint16(sh[34:], 0)
	le.PutUint32(sh[36:], 0x40000040) // INITIALIZED_DATA | MEM_READ

	le.PutUint16(out[peOff+6:], uint16(numSec+1))

	if img := align(newVA+newSize, sectionAlign); img > sizeOfImage {
		le.PutUint32(out[optOff+56:], img)
	}

	// DataDirectory[2] = 资源表
	le.PutUint32(out[ddOff+2*8:], newVA)
	le.PutUint32(out[ddOff+2*8+4:], newSize)

	return out, nil
}
