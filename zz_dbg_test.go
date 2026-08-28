//go:build linux && zstddebug

package main

import (
	"fmt"
	"os"
	"testing"
)

func TestZstdSeqDebug(t *testing.T) {
	raw, err := os.ReadFile(os.Getenv("BA6_ZSTD_DEBUG"))
	if err != nil {
		t.Skip(err)
	}
	p := 4
	desc := raw[p]
	p++
	if desc&0x20 == 0 {
		p++
	}
	fcs := 0
	switch desc >> 6 {
	case 0:
		if desc&0x20 != 0 {
			fcs = 1
		}
	case 1:
		fcs = 2
	case 2:
		fcs = 4
	case 3:
		fcs = 8
	}
	p += fcs
	hdr := uint32(raw[p]) | uint32(raw[p+1])<<8 | uint32(raw[p+2])<<16
	p += 3
	src := raw[p : p+int(hdr>>3)]
	d := newZstdBlockDecoder()
	lits, used, _ := d.decodeLiterals(src)
	rest := src[used:]
	fmt.Printf("literals n=%d; seq section %d bytes\n", len(lits), len(rest))
	count := int(rest[0])
	modes := rest[1]
	fmt.Printf("count=%d modes=%#02x -> ll=%d of=%d ml=%d\n", count, modes,
		(modes>>6)&3, (modes>>2)&3, (modes>>4)&3)
	fw := &zstdBitReader{data: rest[2:]}
	llT, _ := d.sequenceTable((modes>>6)&3, fw, 35, zstdMaxLitLenLog, zstdPredefLitLen, nil)
	ofT, _ := d.sequenceTable((modes>>2)&3, fw, 31, zstdMaxOffsetLog, zstdPredefOffset, nil)
	mlT, err2 := d.sequenceTable((modes>>4)&3, fw, 52, zstdMaxMatchLenLog, zstdPredefMatchLen, nil)
	fmt.Printf("tables: ll.log=%d of.log=%d ml.log=%v err=%v\n", llT.log, ofT.log, mlT, err2)
	if mlT != nil {
		fmt.Printf("ml.log=%d ml size=%d\n", mlT.log, len(mlT.symbol))
	}
	fmt.Printf("forwardPos=%d bits -> consumed %d bytes\n", fw.forwardPos, (fw.forwardPos+7)/8)
	br, _ := newZstdBitReader(rest[2+(fw.forwardPos+7)/8:])
	fmt.Printf("bitstream %d bytes, bitPos=%d\n", len(br.data), br.bitPos)
	s1 := br.read(llT.log)
	s2 := br.read(ofT.log)
	s3 := br.read(mlT.log)
	fmt.Printf("init states: ll=%d of=%d ml=%d\n", s1, s2, s3)
	fmt.Printf("symbols: ll=%d of=%d ml=%d\n", llT.symbol[s1], ofT.symbol[s2], mlT.symbol[s3])
}
