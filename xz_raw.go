// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
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

type xzReader struct {
	input        *bufio.Reader
	blockHeader  uint64
	dataBytes    uint64
	remaining    uint64
	finished     bool
	initialChunk bool
}

func newXZReader(input io.Reader) (io.Reader, error) {
	reader := &xzReader{input: bufio.NewReader(input)}
	if err := reader.readHeaders(); err != nil {
		return nil, err
	}
	return reader, nil
}

func (reader *xzReader) readHeaders() error {
	streamHeader := make([]byte, 12)
	if _, err := io.ReadFull(reader.input, streamHeader); err != nil {
		return err
	}
	if !bytes.Equal(streamHeader[:6], xzStreamMagic) || streamHeader[6] != 0 || streamHeader[7] != 0 {
		return fmt.Errorf("unsupported XZ stream")
	}
	if binary.LittleEndian.Uint32(streamHeader[8:]) != crc32.ChecksumIEEE(streamHeader[6:8]) {
		return fmt.Errorf("invalid XZ stream header")
	}
	first, err := reader.input.ReadByte()
	if err != nil {
		return err
	}
	if first == 0 {
		return fmt.Errorf("empty XZ streams are unsupported")
	}
	reader.blockHeader = uint64(first+1) * 4
	if reader.blockHeader < 12 || reader.blockHeader > 1024 {
		return fmt.Errorf("invalid XZ block header")
	}
	rest := make([]byte, reader.blockHeader-1)
	if _, err := io.ReadFull(reader.input, rest); err != nil {
		return err
	}
	header := append([]byte{first}, rest[:len(rest)-4]...)
	if binary.LittleEndian.Uint32(rest[len(rest)-4:]) != crc32.ChecksumIEEE(header) {
		return fmt.Errorf("invalid XZ block header")
	}
	if len(header) < 5 || header[1] != 0 || header[2] != 0x21 || header[3] != 1 {
		return fmt.Errorf("unsupported XZ filter chain")
	}
	for _, value := range header[5:] {
		if value != 0 {
			return fmt.Errorf("invalid XZ block header padding")
		}
	}
	return nil
}

func (reader *xzReader) Read(output []byte) (int, error) {
	for reader.remaining == 0 && !reader.finished {
		control, err := reader.input.ReadByte()
		if err != nil {
			return 0, err
		}
		reader.dataBytes++
		switch control {
		case 0:
			if err := reader.finishBlock(); err != nil {
				return 0, err
			}
			reader.finished = true
			return 0, io.EOF
		case 1, 2:
			if control == 2 && !reader.initialChunk {
				return 0, fmt.Errorf("XZ raw chunk lacks an initial dictionary reset")
			}
			sizes := []byte{0, 0}
			if _, err := io.ReadFull(reader.input, sizes); err != nil {
				return 0, err
			}
			reader.dataBytes += 2
			reader.remaining = uint64(binary.BigEndian.Uint16(sizes)) + 1
			reader.initialChunk = true
		default:
			return 0, fmt.Errorf("compressed LZMA2 chunks are unsupported")
		}
	}
	if reader.finished {
		return 0, io.EOF
	}
	amount := min(uint64(len(output)), reader.remaining)
	read, err := reader.input.Read(output[:amount])
	if read < 0 {
		return 0, fmt.Errorf("XZ reader returned a negative byte count")
	}
	readCount := uint64(read)
	reader.remaining -= readCount
	reader.dataBytes += readCount
	if err != nil && err != io.EOF {
		return read, err
	}
	if read == 0 && err == io.EOF {
		return 0, io.ErrUnexpectedEOF
	}
	return read, nil
}

func (reader *xzReader) finishBlock() error {
	return skipXZPadding(reader.input, reader.blockHeader+reader.dataBytes)
}

func skipXZPadding(input io.Reader, size uint64) error {
	padding := (4 - size%4) % 4
	if padding == 0 {
		return nil
	}
	_, err := io.CopyN(io.Discard, input, int64(padding))
	return err
}
