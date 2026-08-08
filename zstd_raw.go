// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
)

var zstdFrameMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

const zstdRawBlockSize = 128 << 10

// zstdWriter produces ordinary Zstandard frames using raw blocks and the RLE
// block form for uniform data. This keeps the binary self-contained while the
// output remains readable by full Zstandard implementations.
type zstdWriter struct {
	output *bufio.Writer
	block  []byte
	closed bool
}

func newZstdWriter(output io.Writer) (*zstdWriter, error) {
	writer := &zstdWriter{output: bufio.NewWriter(output)}
	if _, err := writer.output.Write(zstdFrameMagic); err != nil {
		return nil, err
	}
	// No content size or checksum. Raw blocks can be 128 KiB, so advertise a
	// matching 128 KiB window (window descriptor 0x38) rather than the 1 KiB
	// minimum window.
	if _, err := writer.output.Write([]byte{0, 0x38}); err != nil {
		return nil, err
	}
	return writer, nil
}

func (writer *zstdWriter) Write(data []byte) (int, error) {
	if writer.closed {
		return 0, fmt.Errorf("write after close")
	}
	written := len(data)
	for len(data) > 0 {
		space := zstdRawBlockSize - len(writer.block)
		amount := min(space, len(data))
		writer.block = append(writer.block, data[:amount]...)
		data = data[amount:]
		if len(writer.block) == zstdRawBlockSize && len(data) > 0 {
			if err := writer.writeBlock(writer.block, false); err != nil {
				return written - len(data), err
			}
			writer.block = writer.block[:0]
		}
	}
	return written, nil
}

func (writer *zstdWriter) Close() error {
	if writer.closed {
		return nil
	}
	writer.closed = true
	if err := writer.writeBlock(writer.block, true); err != nil {
		return err
	}
	return writer.output.Flush()
}

func (writer *zstdWriter) writeBlock(data []byte, last bool) error {
	if len(data) > zstdRawBlockSize {
		return fmt.Errorf("zstandard raw block exceeds the 128 KiB limit")
	}
	blockType := uint32(0)
	payload := data
	if len(data) > 0 && zstdUniform(data) {
		blockType = 1
		payload = data[:1]
	}
	header := uint32(len(data))<<3 | blockType<<1 //nolint:gosec // data is bounded by zstdRawBlockSize above.
	if last {
		header |= 1
	}
	if _, err := writer.output.Write([]byte{byte(header & 0xff), byte((header >> 8) & 0xff), byte((header >> 16) & 0xff)}); err != nil {
		return err
	}
	_, err := writer.output.Write(payload)
	return err
}

func zstdUniform(data []byte) bool {
	for _, value := range data[1:] {
		if value != data[0] {
			return false
		}
	}
	return true
}

type zstdReader struct {
	input          *bufio.Reader
	remaining      uint32
	rle            byte
	rleBlock       bool
	lastAfterBlock bool
	checksum       bool
	finished       bool
}

func newZstdReader(input io.Reader) (io.Reader, error) {
	reader := &zstdReader{input: bufio.NewReader(input)}
	if err := reader.readFrameHeader(); err != nil {
		return nil, err
	}
	return reader, nil
}

func (reader *zstdReader) readFrameHeader() error {
	magic := make([]byte, len(zstdFrameMagic))
	if _, err := io.ReadFull(reader.input, magic); err != nil {
		return err
	}
	if !bytes.Equal(magic, zstdFrameMagic) {
		return fmt.Errorf("not a Zstandard frame")
	}
	descriptor, err := reader.input.ReadByte()
	if err != nil {
		return err
	}
	if descriptor&0x08 != 0 {
		return fmt.Errorf("invalid Zstandard frame header")
	}
	reader.checksum = descriptor&0x04 != 0
	singleSegment := descriptor&0x20 != 0
	if !singleSegment {
		if _, err := reader.input.ReadByte(); err != nil {
			return err
		}
	}
	dictionarySize := []int{0, 1, 2, 4}[descriptor&0x03]
	contentSizeFlag := descriptor >> 6
	contentSizeSize := 0
	switch contentSizeFlag {
	case 0:
		if singleSegment {
			contentSizeSize = 1
		}
	case 1:
		contentSizeSize = 2
	case 2:
		contentSizeSize = 4
	case 3:
		contentSizeSize = 8
	}
	if _, err := io.CopyN(io.Discard, reader.input, int64(dictionarySize+contentSizeSize)); err != nil {
		return err
	}
	return nil
}

func (reader *zstdReader) Read(output []byte) (int, error) {
	if len(output) == 0 {
		return 0, nil
	}
	for reader.remaining == 0 && !reader.finished {
		if reader.lastAfterBlock {
			if reader.checksum {
				if _, err := io.CopyN(io.Discard, reader.input, 4); err != nil {
					return 0, err
				}
			}
			reader.finished = true
			return 0, io.EOF
		}
		if err := reader.readBlockHeader(); err != nil {
			return 0, err
		}
	}
	if reader.finished {
		return 0, io.EOF
	}
	amount := len(output)
	if uint64(amount) > uint64(reader.remaining) {
		amount = int(reader.remaining)
	}
	if reader.rleBlock {
		for index := 0; index < amount; index++ {
			output[index] = reader.rle
		}
		reader.remaining -= uint32(amount) //nolint:gosec // amount is no greater than reader.remaining.
		return amount, nil
	}
	read, err := reader.input.Read(output[:amount])
	if read < 0 || read > amount {
		return 0, fmt.Errorf("zstandard reader returned an invalid byte count")
	}
	reader.remaining -= uint32(read) //nolint:gosec // read is nonnegative and no greater than amount <= remaining.
	if err != nil && err != io.EOF {
		return read, err
	}
	if read == 0 && err == io.EOF {
		return 0, io.ErrUnexpectedEOF
	}
	return read, nil
}

func (reader *zstdReader) readBlockHeader() error {
	headerBytes := []byte{0, 0, 0}
	if _, err := io.ReadFull(reader.input, headerBytes); err != nil {
		return err
	}
	header := uint32(headerBytes[0]) | uint32(headerBytes[1])<<8 | uint32(headerBytes[2])<<16
	reader.lastAfterBlock = header&1 != 0
	blockType := (header >> 1) & 0x03
	reader.remaining = header >> 3
	reader.rleBlock = false
	switch blockType {
	case 0:
		return nil
	case 1:
		value, err := reader.input.ReadByte()
		if err != nil {
			return err
		}
		reader.rle, reader.rleBlock = value, true
		return nil
	case 2:
		return fmt.Errorf("compressed Zstandard blocks are unsupported")
	default:
		return fmt.Errorf("invalid Zstandard block type")
	}
}
