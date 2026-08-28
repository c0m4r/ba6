// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"io"
	"os"
)

// cksumTable is the CRC-32/CKSUM table: the classic POSIX cksum(1) algorithm,
// which is a non-reflected CRC-32 (MSB-first, no final XOR complement other
// than the trailing bitwise NOT). It differs from the far more common
// zlib/IEEE 802.3 CRC-32 (used by gzip, zip, ...), which processes bits in
// the opposite bit order.
var cksumTable = func() [256]uint32 {
	const poly = uint32(0x04c11db7)
	var table [256]uint32
	for i := range table {
		r := uint32(i) << 24
		for range 8 {
			if r&0x80000000 != 0 {
				r = (r << 1) ^ poly
			} else {
				r <<= 1
			}
		}
		table[i] = r
	}
	return table
}()

// cksumCompute implements the POSIX cksum(1) algorithm: a CRC-32/CKSUM over
// the byte stream, followed by the stream's own length encoded as a
// variable-length sequence of bytes (least significant first, stopping after
// the last non-zero byte; nothing is appended for a zero-length stream), then
// the running CRC is bitwise complemented.
func cksumCompute(r io.Reader) (uint32, uint64, error) {
	var crc uint32
	var length uint64
	buf := make([]byte, 64*1024)
	for {
		n, err := r.Read(buf)
		for _, b := range buf[:n] {
			crc = (crc << 8) ^ cksumTable[byte(crc>>24)^b]
		}
		length += uint64(n)
		if err != nil {
			if err == io.EOF {
				break
			}
			return 0, 0, err
		}
	}
	for l := length; l != 0; l >>= 8 {
		crc = (crc << 8) ^ cksumTable[byte(crc>>24)^byte(l)]
	}
	return ^crc, length, nil
}

// cmdCksum implements cksum(1): print the CRC-32/CKSUM checksum and byte
// count of each file, or standard input with no operands.
func cmdCksum(args []string) int {
	var files []string
	parsing := true
	for _, arg := range args {
		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && len(arg) > 1 && arg[0] == '-':
			fatalf("cksum", "invalid option %q", arg)
			return 1
		default:
			files = append(files, arg)
		}
	}
	if len(files) == 0 {
		files = []string{"-"}
	}

	status := 0
	for _, name := range files {
		input, err := openInput(name)
		if err != nil {
			fatalf("cksum", "%s: %s", name, errText(err))
			status = 1
			continue
		}
		crc, length, err := cksumCompute(input)
		input.Close()
		if err != nil {
			fatalf("cksum", "%s: %s", name, errText(err))
			status = 1
			continue
		}
		var writeErr error
		if name == "-" {
			_, writeErr = fmt.Fprintf(os.Stdout, "%d %d\n", crc, length)
		} else {
			_, writeErr = fmt.Fprintf(os.Stdout, "%d %d %s\n", crc, length, name)
		}
		if writeErr != nil {
			fatalf("cksum", "write error: %v", writeErr)
			return 1
		}
	}
	return status
}
