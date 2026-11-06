package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

func main() {
	d, _ := os.ReadFile(os.Args[1])
	le := binary.LittleEndian
	pe := int(le.Uint32(d[0x3C:]))
	opt := pe + 24
	optSize := int(le.Uint16(d[pe+20:]))
	numSec := int(le.Uint16(d[pe+6:]))
	fmt.Printf("PE off=0x%x optSize=%d numSec=%d magic=0x%x\n", pe, optSize, numSec, le.Uint16(d[opt:]))
	fmt.Printf("SizeOfImage=0x%x SizeOfHeaders=0x%x SectionAlign=0x%x FileAlign=0x%x\n",
		le.Uint32(d[opt+56:]), le.Uint32(d[opt+60:]), le.Uint32(d[opt+32:]), le.Uint32(d[opt+36:]))
	fmt.Printf("NumberOfRvaAndSizes=%d\n", le.Uint32(d[opt+108:]))
	dd := opt + 112
	for i := 0; i < 4; i++ {
		fmt.Printf("  dir[%d] rva=0x%x size=%d\n", i, le.Uint32(d[dd+i*8:]), le.Uint32(d[dd+i*8+4:]))
	}
	sec := opt + optSize
	fmt.Println("sections:")
	for i := 0; i < numSec; i++ {
		b := d[sec+i*40:]
		name := ""
		for j := 0; j < 8 && b[j] != 0; j++ { name += string(b[j]) }
		fmt.Printf("  %-10s vsize=%-9d va=0x%08x rawsize=%-9d raw=0x%-8x chars=0x%x\n",
			name, le.Uint32(b[8:]), le.Uint32(b[12:]), le.Uint32(b[16:]), le.Uint32(b[20:]), le.Uint32(b[36:]))
	}
}
