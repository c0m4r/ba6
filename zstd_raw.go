// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
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

// zstdReader decodes a complete Zstandard stream: every frame in it, all three
// block types including entropy-coded ones, the sliding window matches reach
// back through, and any skippable frames in between.
type zstdReader struct {
	input    *bufio.Reader
	dec      *zstdBlockDecoder
	window   []byte
	frameAt  int // index in window where the current frame's history starts
	consumed int
	winSize  int
	checksum bool
	finished bool
}

func newZstdReader(input io.Reader) (io.Reader, error) {
	reader := &zstdReader{input: bufio.NewReader(input)}
	if err := reader.readFrameHeader(); err != nil {
		return nil, err
	}
	return reader, nil
}

// readFrameHeader reads one frame header, skipping over any skippable frames
// that precede it. It reports io.EOF when the input is exhausted.
func (reader *zstdReader) readFrameHeader() error {
	for {
		magic := make([]byte, 4)
		if _, err := io.ReadFull(reader.input, magic); err != nil {
			return err
		}
		value := binary.LittleEndian.Uint32(magic)
		if value&0xfffffff0 == 0x184d2a50 {
			// A skippable frame: a four-byte length then that much payload.
			var size [4]byte
			if _, err := io.ReadFull(reader.input, size[:]); err != nil {
				return unexpectedEOF(err)
			}
			skip := int64(binary.LittleEndian.Uint32(size[:]))
			if _, err := io.CopyN(io.Discard, reader.input, skip); err != nil {
				return unexpectedEOF(err)
			}
			continue
		}
		if !bytes.Equal(magic, zstdFrameMagic) {
			return fmt.Errorf("not a Zstandard frame")
		}
		break
	}

	descriptor, err := reader.input.ReadByte()
	if err != nil {
		return unexpectedEOF(err)
	}
	if descriptor&0x08 != 0 {
		return fmt.Errorf("invalid Zstandard frame header")
	}
	reader.checksum = descriptor&0x04 != 0
	singleSegment := descriptor&0x20 != 0

	windowSize := 0
	if !singleSegment {
		windowByte, err := reader.input.ReadByte()
		if err != nil {
			return unexpectedEOF(err)
		}
		exponent := int(windowByte>>3) + 10
		mantissa := int(windowByte & 0x07)
		if exponent > 40 {
			return fmt.Errorf("unsupported Zstandard window size")
		}
		base := 1 << uint(exponent)
		windowSize = base + (base/8)*mantissa
	}

	dictionaryIDSize := []int{0, 1, 2, 4}[descriptor&0x03]
	if dictionaryIDSize > 0 {
		return fmt.Errorf("dictionary-compressed Zstandard frames are unsupported")
	}
	contentSizeSize := 0
	switch descriptor >> 6 {
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
	var contentSize uint64
	if contentSizeSize > 0 {
		field := make([]byte, contentSizeSize)
		if _, err := io.ReadFull(reader.input, field); err != nil {
			return unexpectedEOF(err)
		}
		switch contentSizeSize {
		case 1:
			contentSize = uint64(field[0])
		case 2:
			contentSize = uint64(binary.LittleEndian.Uint16(field)) + 256
		case 4:
			contentSize = uint64(binary.LittleEndian.Uint32(field))
		default:
			contentSize = binary.LittleEndian.Uint64(field)
		}
	}
	if singleSegment {
		// The whole frame is one segment, so the window must span its content.
		windowSize = int(min(contentSize, 1<<31))
	}
	if windowSize < 1024 {
		windowSize = 1024
	}
	reader.winSize = windowSize
	reader.dec = newZstdBlockDecoder()
	reader.frameAt = len(reader.window)
	return nil
}

func (reader *zstdReader) Read(output []byte) (int, error) {
	for reader.consumed == len(reader.window) {
		if reader.finished {
			return 0, io.EOF
		}
		reader.compact()
		if err := reader.readBlock(); err != nil {
			return 0, err
		}
	}
	n := copy(output, reader.window[reader.consumed:])
	reader.consumed += n
	return n, nil
}

// compact drops history no match can reach any more, bounding memory for a
// long stream. Only bytes already handed to the caller are eligible.
func (reader *zstdReader) compact() {
	keep := reader.winSize
	if len(reader.window) <= keep {
		return
	}
	drop := len(reader.window) - keep
	if drop > reader.consumed {
		drop = reader.consumed
	}
	if drop < 1<<20 {
		return
	}
	copy(reader.window, reader.window[drop:])
	reader.window = reader.window[:len(reader.window)-drop]
	reader.consumed -= drop
	reader.frameAt -= drop
	if reader.frameAt < 0 {
		reader.frameAt = 0
	}
}

// readBlock decodes one block, or closes the frame and moves on to the next.
func (reader *zstdReader) readBlock() error {
	var header [3]byte
	if _, err := io.ReadFull(reader.input, header[:]); err != nil {
		return unexpectedEOF(err)
	}
	value := uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16
	last := value&1 != 0
	blockType := (value >> 1) & 0x03
	size := int(value >> 3)
	if size > zstdMaxBlockSize {
		return errZstdData
	}

	switch blockType {
	case 0: // stored
		start := len(reader.window)
		reader.window = append(reader.window, make([]byte, size)...)
		if _, err := io.ReadFull(reader.input, reader.window[start:]); err != nil {
			reader.window = reader.window[:start]
			return unexpectedEOF(err)
		}
	case 1: // one byte repeated
		value, err := reader.input.ReadByte()
		if err != nil {
			return unexpectedEOF(err)
		}
		for range size {
			reader.window = append(reader.window, value)
		}
	case 2: // entropy coded
		body := make([]byte, size)
		if _, err := io.ReadFull(reader.input, body); err != nil {
			return unexpectedEOF(err)
		}
		expanded, err := reader.dec.decodeBlock(body, reader.window, reader.frameAt)
		if err != nil {
			return err
		}
		reader.window = expanded
	default:
		return fmt.Errorf("invalid Zstandard block type")
	}

	if !last {
		return nil
	}
	return reader.finishFrame()
}

// finishFrame consumes the frame checksum and looks for another frame after
// it, which zstd allows and produces when concatenating.
func (reader *zstdReader) finishFrame() error {
	if reader.checksum {
		if _, err := io.CopyN(io.Discard, reader.input, 4); err != nil {
			return unexpectedEOF(err)
		}
	}
	if _, err := reader.input.Peek(1); err != nil {
		reader.finished = true
		return nil
	}
	if err := reader.readFrameHeader(); err != nil {
		if errors.Is(err, io.EOF) {
			reader.finished = true
			return nil
		}
		return err
	}
	return nil
}
