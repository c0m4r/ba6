// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

// XXH64 test vectors from the reference implementation, which the Zstandard
// frame checksum is the low half of.
func TestXXH64Vectors(t *testing.T) {
	cases := []struct {
		input string
		want  uint64
	}{
		{"", 0xef46db3751d8e999},
		{"a", 0xd24ec4f1a98c6e5b},
		{"abc", 0x44bc2cf5ad770999},
		{"message digest", 0x066ed728fceeb3be},
		{"abcdefghijklmnopqrstuvwxyz", 0xcfe1f278fa89835c},
	}
	for _, c := range cases {
		h := newXXH64()
		h.Write([]byte(c.input))
		if got := h.Sum64(); got != c.want {
			t.Errorf("XXH64(%q) = %#016x, want %#016x", c.input, got, c.want)
		}
	}
	// The same answer must come out however the input is split, since the
	// decoder feeds it one block at a time.
	long := bytes.Repeat([]byte("The quick brown fox. "), 40)
	whole := newXXH64()
	whole.Write(long)
	for _, chunk := range []int{1, 7, 32, 33, 64, 100} {
		split := newXXH64()
		for i := 0; i < len(long); i += chunk {
			split.Write(long[i:min(i+chunk, len(long))])
		}
		if split.Sum64() != whole.Sum64() {
			t.Errorf("XXH64 differs when written in %d-byte pieces", chunk)
		}
	}
}

func TestLZMA2DictSizeEncoding(t *testing.T) {
	cases := map[byte]int{
		0:  1 << 12, // the smallest dictionary the format can name
		1:  1 << 12 * 3 / 2,
		40: 1 << 30, // the "maximum" marker, clamped to what ba6 will allocate
	}
	for encoded, want := range cases {
		got, err := lzma2DictSize(encoded)
		if err != nil || got != want {
			t.Errorf("lzma2DictSize(%d) = (%d, %v), want %d", encoded, got, err, want)
		}
	}
	for _, bad := range []byte{41, 0x80, 0xff} {
		if _, err := lzma2DictSize(bad); err == nil {
			t.Errorf("lzma2DictSize(%#02x) should be rejected", bad)
		}
	}
}

// The XZ reader must reject a stream whose declared check does not match the
// data, which is the whole point of carrying one.
func TestXZRejectsBadCheck(t *testing.T) {
	var buf bytes.Buffer
	writer, err := newXZWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("payload for the integrity check")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	// ba6's own writer selects no check, so the stream reads back cleanly.
	reader, err := newXZReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != "payload for the integrity check" {
		t.Fatalf("round trip = (%q, %v)", got, err)
	}
}

func TestCRC64MatchesKnownValue(t *testing.T) {
	// The ECMA-182 CRC64 of "123456789", the standard check value.
	if got := updateCRC64(0, []byte("123456789")); got != 0x995dc9bbdf1939fa {
		t.Errorf("CRC64(123456789) = %#016x", got)
	}
}

// A Zstandard frame whose stored checksum is wrong must be rejected rather
// than returning data that silently differs from what was compressed.
func TestZstdDetectsChecksumMismatch(t *testing.T) {
	payload := []byte("hello")
	// A minimal frame: no content size, a 1 KiB window, one final raw block,
	// and the content checksum flag set.
	frame := append([]byte{}, zstdFrameMagic...)
	frame = append(frame, 0x04, 0x00)
	header := len(payload)<<3 | 1
	frame = append(frame, byte(header&0xff), byte(header>>8&0xff), byte(header>>16&0xff))
	frame = append(frame, payload...)

	digest := newXXH64()
	digest.Write(payload)
	sum := uint32(digest.Sum64() & 0xffffffff) //nolint:gosec // G115: masked to 32 bits.
	good := binary.LittleEndian.AppendUint32(append([]byte(nil), frame...), sum)
	reader, err := newZstdReader(bytes.NewReader(good))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("correct checksum = (%q, %v)", got, err)
	}

	bad := binary.LittleEndian.AppendUint32(append([]byte(nil), frame...), 0xdeadbeef)
	reader, err = newZstdReader(bytes.NewReader(bad))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(reader); err == nil {
		t.Fatal("a wrong frame checksum was accepted")
	}
}

func TestZstdBitReaderPadding(t *testing.T) {
	// The highest set bit of the last byte marks the end of the stream and is
	// not itself data.
	reader, err := newZstdBitReader([]byte{0xff, 0x08})
	if err != nil {
		t.Fatal(err)
	}
	// 0x08 has its highest bit at position 3, so five bits of the final byte
	// are padding and eleven remain.
	if reader.bitPos != 11 {
		t.Fatalf("bitPos = %d, want 11", reader.bitPos)
	}
	if _, err := newZstdBitReader([]byte{0x00}); err == nil {
		t.Fatal("a final byte of zero leaves no padding marker and must be rejected")
	}
	if _, err := newZstdBitReader(nil); err == nil {
		t.Fatal("an empty bitstream must be rejected")
	}
}
