package main

import (
	"encoding/binary"
	"fmt"
	"image/png"
	"os"
	"testing"
)

// 这些测试只用于生成预览图 / 图标资源，不参与正式构建。

func TestRenderPreview(t *testing.T) {
	if err := os.MkdirAll("preview", 0o755); err != nil {
		t.Fatal(err)
	}
	themes := []iconTheme{iconBlue, iconGreen, iconYellow, iconRed, iconGray}
	for _, sz := range []int{16, 24, 32, 48, 128} {
		for _, th := range themes {
			f, err := os.Create(fmt.Sprintf("preview/%s_%d.png", th.name, sz))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, renderDiskIcon(sz, th)); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}
}

func TestWriteICO(t *testing.T) {
	data := encodeICO(iconSizes, iconBlue)
	if err := os.WriteFile("openlist.ico", data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("openlist.ico: %d bytes", len(data))
}

var iconSizes = []int{16, 20, 24, 32, 40, 48, 64, 128, 256}

// ---------------------------------------------------------------- ICO

func encodeICO(sizes []int, th iconTheme) []byte {
	type entry struct{ data []byte }
	var entries []entry
	for _, sz := range sizes {
		entries = append(entries, entry{icoImage(sz, th)})
	}

	le := binary.LittleEndian
	out := make([]byte, 6+16*len(entries))
	le.PutUint16(out[0:], 0)
	le.PutUint16(out[2:], 1)
	le.PutUint16(out[4:], uint16(len(entries)))

	offset := 6 + 16*len(entries)
	for i, e := range entries {
		b := out[6+16*i:]
		sz := sizes[i]
		if sz >= 256 {
			b[0], b[1] = 0, 0
		} else {
			b[0], b[1] = byte(sz), byte(sz)
		}
		le.PutUint16(b[4:], 1)
		le.PutUint16(b[6:], 32)
		le.PutUint32(b[8:], uint32(len(e.data)))
		le.PutUint32(b[12:], uint32(offset))
		offset += len(e.data)
	}
	for _, e := range entries {
		out = append(out, e.data...)
	}
	return out
}

// icoImage 返回 ICONIMAGE：BITMAPINFOHEADER + 自底向上 BGRA + AND 掩码。
func icoImage(sz int, th iconTheme) []byte {
	le := binary.LittleEndian
	px := toBGRA(renderDiskIcon(sz, th))

	hdr := make([]byte, 40)
	le.PutUint32(hdr[0:], 40)
	le.PutUint32(hdr[4:], uint32(sz))
	le.PutUint32(hdr[8:], uint32(sz*2))
	le.PutUint16(hdr[12:], 1)
	le.PutUint16(hdr[14:], 32)
	le.PutUint32(hdr[16:], 0) // BI_RGB
	le.PutUint32(hdr[20:], uint32(sz*sz*4))

	xor := make([]byte, sz*sz*4)
	for y := 0; y < sz; y++ {
		copy(xor[(sz-1-y)*sz*4:], px[y*sz*4:(y+1)*sz*4])
	}
	maskRow := ((sz + 31) / 32) * 4
	mask := make([]byte, maskRow*sz)

	out := make([]byte, 0, len(hdr)+len(xor)+len(mask))
	out = append(out, hdr...)
	out = append(out, xor...)
	out = append(out, mask...)
	return out
}

// ---------------------------------------------------------------- RSRC

const (
	rtIcon      = 3
	rtGroupIcon = 14
	resLang     = 0x0409

	dirHeaderSize = 14
	dirEntrySize  = 8
	dataEntrySize = 16
)

func putDirEntry(b []byte, off int, id uint32, subdir bool, target int) {
	le := binary.LittleEndian
	le.PutUint32(b[off:], id)
	v := uint32(target)
	if subdir {
		v |= 0x80000000
	}
	le.PutUint32(b[off+4:], v)
}

// putDataEntry 写一个数据项，返回需要重定位的字段在段内的偏移。
func putDataEntry(b []byte, off, dataOff, size int) int {
	le := binary.LittleEndian
	le.PutUint32(b[off:], uint32(dataOff)) // 加数：段内偏移，链接时补上段 RVA
	le.PutUint32(b[off+4:], uint32(size))
	le.PutUint32(b[off+8:], 0)
	le.PutUint32(b[off+12:], 0)
	return off
}

func buildRSRC(images [][]byte, ids []int) ([]byte, []int) {
	le := binary.LittleEndian
	n := len(images)

	// 组图标头
	grp := make([]byte, 6+14*n)
	le.PutUint16(grp[0:], 0)
	le.PutUint16(grp[2:], 1)
	le.PutUint16(grp[4:], uint16(n))
	for i, img := range images {
		b := grp[6+14*i:]
		w := le.Uint32(img[4:])
		h := le.Uint32(img[8:]) / 2
		if w >= 256 {
			b[0] = 0
		} else {
			b[0] = byte(w)
		}
		if h >= 256 {
			b[1] = 0
		} else {
			b[1] = byte(h)
		}
		le.PutUint16(b[4:], 1)
		le.PutUint16(b[6:], 32)
		le.PutUint32(b[8:], uint32(len(img)))
		le.PutUint16(b[12:], uint16(ids[i]))
	}

	// 布局
	off := 0
	rootOff := off
	off += dirHeaderSize + 2*dirEntrySize
	iconTypeOff := off
	off += dirHeaderSize + n*dirEntrySize
	groupTypeOff := off
	off += dirHeaderSize + dirEntrySize
	nameDirs := make([]int, n)
	for i := 0; i < n; i++ {
		nameDirs[i] = off
		off += dirHeaderSize + dirEntrySize
	}
	groupNameOff := off
	off += dirHeaderSize + dirEntrySize
	iconEntries := make([]int, n)
	for i := 0; i < n; i++ {
		iconEntries[i] = off
		off += dataEntrySize
	}
	groupEntryOff := off
	off += dataEntrySize
	iconData := make([]int, n)
	for i := 0; i < n; i++ {
		iconData[i] = off
		off += len(images[i])
	}
	groupDataOff := off
	off += len(grp)

	buf := make([]byte, off)

	// 根目录
	le.PutUint16(buf[rootOff+14:], 2)
	putDirEntry(buf, rootOff+dirHeaderSize, rtIcon, true, iconTypeOff)
	putDirEntry(buf, rootOff+dirHeaderSize+dirEntrySize, rtGroupIcon, true, groupTypeOff)

	// RT_ICON 类型目录
	le.PutUint16(buf[iconTypeOff+14:], uint16(n))
	for i := 0; i < n; i++ {
		putDirEntry(buf, iconTypeOff+dirHeaderSize+i*dirEntrySize, uint32(ids[i]), true, nameDirs[i])
	}

	// 每个图标的语言目录
	var relocs []int
	for i := 0; i < n; i++ {
		le.PutUint16(buf[nameDirs[i]+14:], 1)
		putDirEntry(buf, nameDirs[i]+dirHeaderSize, resLang, false, iconEntries[i])
	}

	for i := 0; i < n; i++ {
		relocs = append(relocs, putDataEntry(buf, iconEntries[i], iconData[i], len(images[i])))
		copy(buf[iconData[i]:], images[i])
	}

	// RT_GROUP_ICON
	le.PutUint16(buf[groupTypeOff+14:], 1)
	putDirEntry(buf, groupTypeOff+dirHeaderSize, rtGroupIcon, true, groupNameOff)
	le.PutUint16(buf[groupNameOff+14:], 1)
	putDirEntry(buf, groupNameOff+dirHeaderSize, resLang, false, groupEntryOff)
	relocs = append(relocs, putDataEntry(buf, groupEntryOff, groupDataOff, len(grp)))
	copy(buf[groupDataOff:], grp)

	return buf, relocs
}

// wrapCOFF 把资源段包成一个 COFF 对象，Go 链接器会把它并进 PE。
func wrapCOFF(rsrc []byte, relocs []int) []byte {
	le := binary.LittleEndian

	const (
		fileHdrSize = 20
		secHdrSize  = 40
		symSize     = 18
	)
	dataStart := fileHdrSize + secHdrSize
	relocStart := dataStart + len(rsrc)
	symStart := relocStart + 10*len(relocs)
	strStart := symStart + symSize
	total := strStart + 4

	out := make([]byte, total)

	// IMAGE_FILE_HEADER
	le.PutUint16(out[0:], 0x8664) // AMD64
	le.PutUint16(out[2:], 1)
	le.PutUint32(out[8:], uint32(symStart))
	le.PutUint32(out[12:], 1)

	// IMAGE_SECTION_HEADER
	sh := out[fileHdrSize:]
	copy(sh[0:8], ".rsrc\x00\x00\x00")
	le.PutUint32(sh[8:], uint32(len(rsrc)))
	le.PutUint32(sh[16:], uint32(len(rsrc)))
	le.PutUint32(sh[20:], uint32(dataStart))
	le.PutUint32(sh[24:], uint32(relocStart))
	le.PutUint16(sh[32:], uint16(len(relocs)))
	le.PutUint32(sh[36:], 0x40000040) // INITIALIZED_DATA | MEM_READ

	copy(out[dataStart:], rsrc)

	// 重定位：ADDR32NB（RVA）指向 .rsrc 段符号
	for i, off := range relocs {
		r := out[relocStart+10*i:]
		le.PutUint32(r[0:], uint32(off))
		le.PutUint32(r[4:], 0)
		le.PutUint16(r[8:], 3)
	}

	// 符号表：一个段符号
	s := out[symStart:]
	copy(s[0:8], ".rsrc\x00\x00\x00")
	le.PutUint32(s[8:], 0)
	le.PutUint16(s[12:], 1)
	s[16] = 2 // IMAGE_SYM_CLASS_STATIC
	s[17] = 0

	le.PutUint32(out[strStart:], 4)
	return out
}
