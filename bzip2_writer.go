// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"fmt"
	"io"
	"sort"
)

const (
	bzip2BlockSize      = 100_000
	bzip2InputBlockSize = bzip2BlockSize * 4 / 5
)

// bzip2Writer emits valid BZh1 streams using a deliberately simple fixed
// Huffman coding. The BWT and move-to-front stages still make repeated input
// compact, while keeping this self-contained implementation auditable.
type bzip2Writer struct {
	output      *bufio.Writer
	bits        bzip2BitWriter
	block       []byte
	combinedCRC uint32
	closed      bool
}

func newBzip2Writer(output io.Writer) (*bzip2Writer, error) {
	buffered := bufio.NewWriter(output)
	if _, err := io.WriteString(buffered, "BZh1"); err != nil {
		return nil, err
	}
	writer := &bzip2Writer{output: buffered}
	writer.bits.output = buffered
	return writer, nil
}

func (writer *bzip2Writer) Write(data []byte) (int, error) {
	if writer.closed {
		return 0, fmt.Errorf("write after close")
	}
	written := len(data)
	for len(data) > 0 {
		// The first RLE stage can grow a four-byte run to five bytes. Keep
		// raw input blocks at 80 KiB so the post-RLE BWT block always stays
		// within the 100 KiB BZh1 limit.
		space := bzip2InputBlockSize - len(writer.block)
		if space == 0 {
			if err := writer.writeBlock(); err != nil {
				return written - len(data), err
			}
			space = bzip2InputBlockSize
		}
		amount := min(space, len(data))
		writer.block = append(writer.block, data[:amount]...)
		data = data[amount:]
	}
	return written, nil
}

func (writer *bzip2Writer) Close() error {
	if writer.closed {
		return nil
	}
	writer.closed = true
	if err := writer.writeBlock(); err != nil {
		return err
	}
	writer.bits.writeBits(24, 0x177245)
	writer.bits.writeBits(24, 0x385090)
	writer.bits.writeBits(32, writer.combinedCRC)
	if err := writer.bits.flush(); err != nil {
		return err
	}
	return writer.output.Flush()
}

func (writer *bzip2Writer) writeBlock() error {
	if len(writer.block) == 0 {
		return nil
	}
	crc := bzip2CRC(writer.block)
	writer.combinedCRC = (writer.combinedCRC << 1) | (writer.combinedCRC >> 31)
	writer.combinedCRC ^= crc
	encoded := bzip2RLE1(writer.block)
	if len(encoded) > bzip2BlockSize {
		return fmt.Errorf("internal bzip2 block exceeds the BZh1 limit")
	}
	bwt, original := bzip2BWT(encoded)
	symbols, used := bzip2MTF(bwt)
	writer.bits.writeBits(24, 0x314159)
	writer.bits.writeBits(24, 0x265359)
	writer.bits.writeBits(32, crc)
	writer.bits.writeBits(1, 0)                 // randomized blocks are obsolete and never emitted.
	writer.bits.writeBits(24, uint32(original)) //nolint:gosec // original is an index into a <=100 KiB BWT block.
	bzip2WriteInUse(&writer.bits, used)
	alphaSize := len(used) + 2
	codeLengths := bzip2FixedCodeLengths(alphaSize)
	codes := bzip2CanonicalCodes(codeLengths)
	selectorCount := (len(symbols) + 49) / 50
	if selectorCount == 0 {
		selectorCount = 1
	}
	writer.bits.writeBits(3, 2) // The format requires two to six groups.
	writer.bits.writeBits(15, uint32(selectorCount))
	for range selectorCount {
		writer.bits.writeBits(1, 0) // group 0 is first in the selector MTF list.
	}
	for range 2 {
		bzip2WriteCodeLengths(&writer.bits, codeLengths)
	}
	for _, symbol := range symbols {
		writer.bits.writeBits(uint(codeLengths[symbol]), codes[symbol])
	}
	writer.block = writer.block[:0]
	return writer.bits.err
}

type bzip2BitWriter struct {
	output io.Writer
	value  uint64
	bits   uint
	err    error
}

func (writer *bzip2BitWriter) writeBits(count uint, value uint32) {
	if writer.err != nil || count == 0 {
		return
	}
	writer.value = writer.value<<count | uint64(value)&((uint64(1)<<count)-1)
	writer.bits += count
	for writer.bits >= 8 {
		byteValue := byte((writer.value >> (writer.bits - 8)) & 0xff)
		if _, err := writer.output.Write([]byte{byteValue}); err != nil {
			writer.err = err
			return
		}
		writer.bits -= 8
		if writer.bits == 0 {
			writer.value = 0
		} else {
			writer.value &= (uint64(1) << writer.bits) - 1
		}
	}
}

func (writer *bzip2BitWriter) flush() error {
	if writer.err != nil {
		return writer.err
	}
	if writer.bits != 0 {
		if _, err := writer.output.Write([]byte{byte((writer.value << (8 - writer.bits)) & 0xff)}); err != nil {
			return err
		}
		writer.value, writer.bits = 0, 0
	}
	return nil
}

func bzip2RLE1(data []byte) []byte {
	output := make([]byte, 0, len(data))
	for start := 0; start < len(data); {
		end := start + 1
		for end < len(data) && data[end] == data[start] && end-start < 259 {
			end++
		}
		count := end - start
		if count < 4 {
			for range count {
				output = append(output, data[start])
			}
		} else {
			output = append(output, data[start], data[start], data[start], data[start], byte(count-4))
		}
		start = end
	}
	return output
}

func bzip2BWT(data []byte) ([]byte, int) {
	length := len(data)
	positions := make([]int, length)
	ranks, next := make([]int, length), make([]int, length)
	for index := range positions {
		positions[index] = index
		ranks[index] = int(data[index])
	}
	sort.Slice(positions, func(left, right int) bool {
		if ranks[positions[left]] != ranks[positions[right]] {
			return ranks[positions[left]] < ranks[positions[right]]
		}
		return positions[left] < positions[right]
	})
	classes := 0
	for index, position := range positions {
		if index == 0 || data[position] != data[positions[index-1]] {
			classes++
		}
		next[position] = classes - 1
	}
	copy(ranks, next)
	for step := 1; step < length && classes < length; step *= 2 {
		sort.Slice(positions, func(left, right int) bool {
			first, second := positions[left], positions[right]
			if ranks[first] != ranks[second] {
				return ranks[first] < ranks[second]
			}
			if ranks[(first+step)%length] != ranks[(second+step)%length] {
				return ranks[(first+step)%length] < ranks[(second+step)%length]
			}
			return first < second
		})
		newClasses := 0
		for index, position := range positions {
			if index == 0 || ranks[position] != ranks[positions[index-1]] ||
				ranks[(position+step)%length] != ranks[(positions[index-1]+step)%length] {
				newClasses++
			}
			next[position] = newClasses - 1
		}
		if newClasses == classes {
			break
		}
		classes = newClasses
		copy(ranks, next)
	}
	output := make([]byte, length)
	original := 0
	for index, position := range positions {
		output[index] = data[(position+length-1)%length]
		if position == 0 {
			original = index
		}
	}
	return output, original
}

func bzip2MTF(data []byte) ([]uint16, []byte) {
	present := [256]bool{}
	for _, value := range data {
		present[value] = true
	}
	used := make([]byte, 0, 256)
	for value, exists := range present {
		if exists {
			used = append(used, byte(value))
		}
	}
	list := append([]byte(nil), used...)
	symbols := make([]uint16, 0, len(data)+1)
	zeros := 0
	flushZeros := func() {
		if zeros == 0 {
			return
		}
		symbols = append(symbols, bzip2ZeroRun(zeros)...)
		zeros = 0
	}
	for _, value := range data {
		position := 0
		for list[position] != value {
			position++
		}
		if position == 0 {
			zeros++
			continue
		}
		flushZeros()
		symbols = append(symbols, uint16(position+1))
		copy(list[1:position+1], list[:position])
		list[0] = value
	}
	flushZeros()
	symbols = append(symbols, uint16(len(used)+1)) //nolint:gosec // used holds at most one entry per byte value.
	return symbols, used
}

func bzip2ZeroRun(length int) []uint16 {
	values := []uint16{}
	for value := length - 1; ; value = (value - 2) >> 1 {
		if value&1 == 0 {
			values = append(values, 0)
		} else {
			values = append(values, 1)
		}
		if value < 2 {
			return values
		}
	}
}

func bzip2WriteInUse(writer *bzip2BitWriter, used []byte) {
	present := [256]bool{}
	groups := [16]bool{}
	for _, value := range used {
		present[value] = true
		groups[value/16] = true
	}
	for _, group := range groups {
		if group {
			writer.writeBits(1, 1)
		} else {
			writer.writeBits(1, 0)
		}
	}
	for group, exists := range groups {
		if !exists {
			continue
		}
		for value := group * 16; value < group*16+16; value++ {
			if present[value] {
				writer.writeBits(1, 1)
			} else {
				writer.writeBits(1, 0)
			}
		}
	}
}

func bzip2FixedCodeLengths(alphaSize int) []uint8 {
	base, shortLength := 1, 0
	for base*2 <= alphaSize {
		base *= 2
		shortLength++
	}
	shortCount := 2*base - alphaSize
	lengths := make([]uint8, alphaSize)
	for index := range lengths {
		lengths[index] = uint8(shortLength + 1)
		if index < shortCount {
			lengths[index] = uint8(shortLength)
		}
	}
	return lengths
}

func bzip2WriteCodeLengths(writer *bzip2BitWriter, lengths []uint8) {
	current := lengths[0]
	writer.writeBits(5, uint32(current))
	for _, length := range lengths {
		for current < length {
			writer.writeBits(1, 1)
			writer.writeBits(1, 0)
			current++
		}
		for current > length {
			writer.writeBits(1, 1)
			writer.writeBits(1, 1)
			current--
		}
		writer.writeBits(1, 0)
	}
}

func bzip2CanonicalCodes(lengths []uint8) []uint32 {
	maximum := uint8(0)
	for _, length := range lengths {
		if length > maximum {
			maximum = length
		}
	}
	codes := make([]uint32, len(lengths))
	code := uint32(0)
	for length := uint8(1); length <= maximum; length++ {
		for index, symbolLength := range lengths {
			if symbolLength == length {
				codes[index] = code
				code++
			}
		}
		code <<= 1
	}
	return codes
}

func bzip2CRC(data []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, value := range data {
		crc = crc<<8 ^ bzip2CRCTable[byte(crc>>24)^value]
	}
	return ^crc
}

var bzip2CRCTable = makeBzip2CRCTable()

func makeBzip2CRCTable() [256]uint32 {
	var table [256]uint32
	for value := range table {
		crc := uint32(value) << 24
		for bit := 0; bit < 8; bit++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
		table[value] = crc
	}
	return table
}
