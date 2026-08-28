// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
)

var xzStreamMagic = []byte{0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00}

// xzWriter writes an XZ stream whose LZMA2 filter uses uncompressed chunks.
// It is interoperable with normal XZ decoders while avoiding a large codec
// dependency. The reader accepts the same recoverable raw-chunk subset.
type xzWriter struct {
	output       *bufio.Writer
	compressed   uint64
	uncompressed uint64
	closed       bool
}

func newXZWriter(output io.Writer) (*xzWriter, error) {
	writer := &xzWriter{output: bufio.NewWriter(output)}
	// The stream flags select no block check. The four following bytes are the
	// little-endian CRC32 of those flags.
	flags := []byte{0, 0}
	crc := crc32.ChecksumIEEE(flags)
	var checksum [4]byte
	binary.LittleEndian.PutUint32(checksum[:], crc)
	if _, err := writer.output.Write(xzStreamMagic); err != nil {
		return nil, err
	}
	if _, err := writer.output.Write(flags); err != nil {
		return nil, err
	}
	if _, err := writer.output.Write(checksum[:]); err != nil {
		return nil, err
	}
	if err := writer.writeBlockHeader(); err != nil {
		return nil, err
	}
	return writer, nil
}

func (writer *xzWriter) writeBlockHeader() error {
	// A 12-byte header: one LZMA2 filter (ID 0x21), one-byte dictionary
	// property (0, meaning 4 KiB), no sizes, and three padding bytes.
	header := []byte{0x02, 0x00, 0x21, 0x01, 0x00, 0x00, 0x00, 0x00}
	crc := crc32.ChecksumIEEE(header)
	var checksum [4]byte
	binary.LittleEndian.PutUint32(checksum[:], crc)
	if _, err := writer.output.Write(header); err != nil {
		return err
	}
	_, err := writer.output.Write(checksum[:])
	return err
}

func (writer *xzWriter) Write(data []byte) (int, error) {
	if writer.closed {
		return 0, fmt.Errorf("write after close")
	}
	written := len(data)
	for len(data) > 0 {
		amount := min(len(data), 65_536)
		control := byte(0x02)
		if writer.compressed == 0 {
			control = 0x01
		}
		encodedLength := uint16(amount - 1) //nolint:gosec // amount is capped at 65,536 bytes above.
		if _, err := writer.output.Write([]byte{control, byte(encodedLength >> 8), byte(encodedLength & 0xff)}); err != nil {
			return written - len(data), err
		}
		if _, err := writer.output.Write(data[:amount]); err != nil {
			return written - len(data), err
		}
		writer.compressed += uint64(amount + 3)
		writer.uncompressed += uint64(amount)
		data = data[amount:]
	}
	return written, nil
}

func (writer *xzWriter) Close() error {
	if writer.closed {
		return nil
	}
	writer.closed = true
	if _, err := writer.output.Write([]byte{0}); err != nil {
		return err
	}
	writer.compressed++
	blockUnpadded := uint64(12) + writer.compressed
	if err := writeXZPadding(writer.output, blockUnpadded); err != nil {
		return err
	}
	index := []byte{0, 1}
	index = append(index, xzVLI(blockUnpadded)...)
	index = append(index, xzVLI(writer.uncompressed)...)
	for len(index)%4 != 0 {
		index = append(index, 0)
	}
	if _, err := writer.output.Write(index); err != nil {
		return err
	}
	var indexCRC [4]byte
	binary.LittleEndian.PutUint32(indexCRC[:], crc32.ChecksumIEEE(index))
	if _, err := writer.output.Write(indexCRC[:]); err != nil {
		return err
	}
	indexSize := len(index) + len(indexCRC)
	footerBody := make([]byte, 6)
	binary.LittleEndian.PutUint32(footerBody[:4], uint32(indexSize/4-1)) //nolint:gosec // index is bounded by one block.
	footerBody[4], footerBody[5] = 0, 0
	var footerCRC [4]byte
	binary.LittleEndian.PutUint32(footerCRC[:], crc32.ChecksumIEEE(footerBody))
	if _, err := writer.output.Write(footerCRC[:]); err != nil {
		return err
	}
	if _, err := writer.output.Write(footerBody); err != nil {
		return err
	}
	if _, err := writer.output.Write([]byte{'Y', 'Z'}); err != nil {
		return err
	}
	return writer.output.Flush()
}

func writeXZPadding(output io.Writer, size uint64) error {
	padding := (4 - size%4) % 4
	if padding == 0 {
		return nil
	}
	_, err := output.Write(make([]byte, padding))
	return err
}

func xzVLI(value uint64) []byte {
	output := []byte{}
	for {
		byteValue := byte(value & 0x7f)
		value >>= 7
		if value != 0 {
			byteValue |= 0x80
		}
		output = append(output, byteValue)
		if value == 0 {
			return output
		}
	}
}

// xzReader decodes a complete XZ stream: every block of it, each block's
// LZMA2 filter, the integrity check the stream declares, and any further
// streams concatenated after the first.
type xzReader struct {
	input     *bufio.Reader
	checkType byte
	check     *xzChecker
	block     *lzma2Reader
	finished  bool
}

func newXZReader(input io.Reader) (io.Reader, error) {
	reader := &xzReader{input: bufio.NewReader(input)}
	if err := reader.readStreamHeader(); err != nil {
		return nil, err
	}
	return reader, nil
}

func (reader *xzReader) readStreamHeader() error {
	header := make([]byte, 12)
	if _, err := io.ReadFull(reader.input, header); err != nil {
		return unexpectedEOF(err)
	}
	if !bytes.Equal(header[:6], xzStreamMagic) {
		return fmt.Errorf("unsupported XZ stream")
	}
	if header[6] != 0 || header[7]&0xf0 != 0 {
		return fmt.Errorf("unsupported XZ stream flags")
	}
	if binary.LittleEndian.Uint32(header[8:]) != crc32.ChecksumIEEE(header[6:8]) {
		return fmt.Errorf("invalid XZ stream header")
	}
	reader.checkType = header[7] & 0x0f
	check, err := newXZChecker(reader.checkType)
	if err != nil {
		return err
	}
	reader.check = check
	return nil
}

func (reader *xzReader) Read(output []byte) (int, error) {
	for {
		if reader.finished {
			return 0, io.EOF
		}
		// Finishing a stream may roll straight into another one concatenated
		// after it, so keep going until a block is actually open.
		for reader.block == nil && !reader.finished {
			if err := reader.startBlock(); err != nil {
				return 0, err
			}
		}
		if reader.finished {
			return 0, io.EOF
		}
		n, err := reader.block.Read(output)
		if n > 0 {
			reader.check.Write(output[:n])
			return n, nil
		}
		if err == nil {
			continue
		}
		if !errors.Is(err, io.EOF) {
			return 0, err
		}
		if err := reader.finishBlock(); err != nil {
			return 0, err
		}
	}
}

// startBlock reads the next block header, or hands over to the index when the
// stream's blocks are done.
func (reader *xzReader) startBlock() error {
	first, err := reader.input.ReadByte()
	if err != nil {
		return unexpectedEOF(err)
	}
	if first == 0 {
		return reader.finishStream()
	}
	size := (int(first) + 1) * 4
	header := make([]byte, size)
	header[0] = first
	if _, err := io.ReadFull(reader.input, header[1:]); err != nil {
		return unexpectedEOF(err)
	}
	body := header[:size-4]
	if binary.LittleEndian.Uint32(header[size-4:]) != crc32.ChecksumIEEE(body) {
		return fmt.Errorf("invalid XZ block header")
	}
	dictSize, err := parseXZBlockHeader(body)
	if err != nil {
		return err
	}
	reader.block = newLZMA2Reader(reader.input, dictSize)
	reader.check.reset()
	return nil
}

// finishBlock consumes the block padding and verifies the declared check over
// the block's uncompressed data.
func (reader *xzReader) finishBlock() error {
	compressed := reader.block.compressed
	if compressed < 0 {
		return fmt.Errorf("invalid XZ block size")
	}
	if err := skipXZPadding(reader.input, uint64(compressed)); err != nil {
		return err
	}
	if size := reader.check.size; size > 0 {
		stored := make([]byte, size)
		if _, err := io.ReadFull(reader.input, stored); err != nil {
			return unexpectedEOF(err)
		}
		if err := reader.check.verify(stored); err != nil {
			return err
		}
	}
	reader.block = nil
	return nil
}

// finishStream consumes the index and footer, then looks for another stream
// concatenated after this one, which xz allows and produces.
func (reader *xzReader) finishStream() error {
	if err := reader.skipIndex(); err != nil {
		return err
	}
	footer := make([]byte, 12)
	if _, err := io.ReadFull(reader.input, footer); err != nil {
		return unexpectedEOF(err)
	}
	if footer[10] != 'Y' || footer[11] != 'Z' {
		return fmt.Errorf("invalid XZ stream footer")
	}
	if err := reader.skipStreamPadding(); err != nil {
		return err
	}
	if _, err := reader.input.Peek(1); err != nil {
		reader.finished = true
		return nil
	}
	return reader.readStreamHeader()
}

// skipIndex walks the index records. The indicator byte has already been read.
func (reader *xzReader) skipIndex() error {
	consumed := uint64(1)
	records, width, err := readXZVLI(reader.input)
	if err != nil {
		return err
	}
	consumed += uint64(width) //nolint:gosec // G115: width is the 1..9 byte count of one VLI.
	if records > 1<<32 {
		return fmt.Errorf("invalid XZ index")
	}
	for i := uint64(0); i < records; i++ {
		for range 2 {
			_, width, err := readXZVLI(reader.input)
			if err != nil {
				return err
			}
			consumed += uint64(width) //nolint:gosec // G115: width is the 1..9 byte count of one VLI.
		}
	}
	if err := skipXZPadding(reader.input, consumed); err != nil {
		return err
	}
	// The index CRC32 follows its padding.
	_, err = io.CopyN(io.Discard, reader.input, 4)
	return unexpectedEOF(err)
}

// skipStreamPadding consumes the four-byte zero groups xz may insert between
// concatenated streams.
func (reader *xzReader) skipStreamPadding() error {
	for {
		next, err := reader.input.Peek(4)
		if err != nil || !bytes.Equal(next, []byte{0, 0, 0, 0}) {
			return nil
		}
		if _, err := reader.input.Discard(4); err != nil {
			return err
		}
	}
}

// parseXZBlockHeader validates the filter chain and returns the LZMA2
// dictionary size. Only a lone LZMA2 filter is supported: the BCJ and delta
// filters would each need their own decoder.
func parseXZBlockHeader(body []byte) (int, error) {
	if len(body) < 2 {
		return 0, fmt.Errorf("invalid XZ block header")
	}
	flags := body[1]
	if flags&0x3c != 0 {
		return 0, fmt.Errorf("invalid XZ block flags")
	}
	if filters := int(flags&0x03) + 1; filters != 1 {
		return 0, fmt.Errorf("unsupported XZ filter chain of %d filters", filters)
	}
	rest := bytes.NewReader(body[2:])
	if flags&0x40 != 0 { // compressed size present
		if _, _, err := readXZVLI(rest); err != nil {
			return 0, err
		}
	}
	if flags&0x80 != 0 { // uncompressed size present
		if _, _, err := readXZVLI(rest); err != nil {
			return 0, err
		}
	}
	id, _, err := readXZVLI(rest)
	if err != nil {
		return 0, err
	}
	if id != 0x21 {
		return 0, fmt.Errorf("unsupported XZ filter %#x", id)
	}
	propsSize, _, err := readXZVLI(rest)
	if err != nil {
		return 0, err
	}
	if propsSize != 1 {
		return 0, fmt.Errorf("invalid LZMA2 filter properties")
	}
	encoded, err := rest.ReadByte()
	if err != nil {
		return 0, fmt.Errorf("invalid LZMA2 filter properties")
	}
	return lzma2DictSize(encoded)
}

// lzma2DictSize expands the six-bit dictionary size the LZMA2 filter stores.
func lzma2DictSize(encoded byte) (int, error) {
	value := encoded & 0x3f
	if encoded&0xc0 != 0 || value > 40 {
		return 0, fmt.Errorf("invalid LZMA2 dictionary size")
	}
	if value == 40 {
		return 1 << 30, nil
	}
	size := (uint64(2) | uint64(value&1)) << (value/2 + 11)
	// The window is only ever as large as the data, so a huge declared size
	// costs nothing until that much output exists.
	if size > 1<<30 {
		size = 1 << 30
	}
	return int(size), nil
}

// readXZVLI reads one variable-length integer, returning its value and width.
func readXZVLI(input io.ByteReader) (uint64, int, error) {
	var value uint64
	for i := 0; i < 9; i++ {
		b, err := input.ReadByte()
		if err != nil {
			return 0, 0, unexpectedEOF(err)
		}
		value |= uint64(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			if b == 0 && i > 0 {
				return 0, 0, fmt.Errorf("invalid XZ variable-length integer")
			}
			return value, i + 1, nil
		}
	}
	return 0, 0, fmt.Errorf("invalid XZ variable-length integer")
}

func skipXZPadding(input io.Reader, size uint64) error {
	padding := (4 - size%4) % 4
	if padding == 0 {
		return nil
	}
	_, err := io.CopyN(io.Discard, input, int64(padding))
	return unexpectedEOF(err)
}

// xzChecker computes the integrity check a stream declares. Types xz defines
// but does not produce are accepted and their bytes consumed without
// verification, which is better than refusing to read the data at all.
type xzChecker struct {
	kind     byte
	size     int
	crc32Sum uint32
	crc64Sum uint64
	digest   hash.Hash
}

func newXZChecker(kind byte) (*xzChecker, error) {
	sizes := map[byte]int{0: 0, 1: 4, 2: 4, 3: 4, 4: 8, 5: 8, 6: 8,
		7: 16, 8: 16, 9: 16, 10: 32, 11: 32, 12: 32, 13: 64, 14: 64, 15: 64}
	size, ok := sizes[kind]
	if !ok {
		return nil, fmt.Errorf("unsupported XZ check type %#x", kind)
	}
	checker := &xzChecker{kind: kind, size: size}
	if kind == 10 {
		checker.digest = sha256.New()
	}
	return checker, nil
}

func (c *xzChecker) reset() {
	c.crc32Sum, c.crc64Sum = 0, 0
	if c.digest != nil {
		c.digest.Reset()
	}
}

func (c *xzChecker) Write(data []byte) {
	switch c.kind {
	case 1:
		c.crc32Sum = crc32.Update(c.crc32Sum, crc32.IEEETable, data)
	case 4:
		c.crc64Sum = updateCRC64(c.crc64Sum, data)
	case 10:
		c.digest.Write(data)
	}
}

func (c *xzChecker) verify(stored []byte) error {
	var computed []byte
	switch c.kind {
	case 1:
		computed = binary.LittleEndian.AppendUint32(nil, c.crc32Sum)
	case 4:
		computed = binary.LittleEndian.AppendUint64(nil, c.crc64Sum)
	case 10:
		computed = c.digest.Sum(nil)
	default:
		return nil // a check ba6 does not compute; its bytes are still consumed
	}
	if !bytes.Equal(computed, stored) {
		return fmt.Errorf("XZ integrity check failed")
	}
	return nil
}

// crc64Table holds the reflected ECMA-182 polynomial xz uses for its default
// check, built once on first use.
var crc64Table = func() [256]uint64 {
	const polynomial = 0xc96c5795d7870f42
	var table [256]uint64
	for i := range table {
		value := uint64(i)
		for range 8 {
			if value&1 != 0 {
				value = value>>1 ^ polynomial
			} else {
				value >>= 1
			}
		}
		table[i] = value
	}
	return table
}()

func updateCRC64(sum uint64, data []byte) uint64 {
	sum = ^sum
	for _, b := range data {
		sum = crc64Table[byte(sum&0xff)^b] ^ sum>>8
	}
	return ^sum
}
