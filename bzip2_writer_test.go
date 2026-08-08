// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bytes"
	"compress/bzip2"
	"io"
	"testing"
)

func TestBzip2WriterRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{name: "text", input: bytes.Repeat([]byte("banana bandana\n"), 400)},
		{name: "periodic", input: bytes.Repeat([]byte("round trip\n"), 100)},
		{name: "long-run", input: bytes.Repeat([]byte{'A'}, bzip2BlockSize)},
		{name: "rle-expansion", input: bytes.Repeat([]byte("AAAAB"), 20_000)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var compressed bytes.Buffer
			writer, err := newBzip2Writer(&compressed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(test.input); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			output, err := io.ReadAll(bzip2.NewReader(&compressed))
			if err != nil {
				t.Fatalf("standard decoder rejected output: %v", err)
			}
			if !bytes.Equal(output, test.input) {
				t.Fatalf("round trip = %d bytes, want %d", len(output), len(test.input))
			}
		})
	}
}

func TestXZRawWriterRoundTrip(t *testing.T) {
	input := bytes.Repeat([]byte("xz raw stream\n"), 5_000)
	var compressed bytes.Buffer
	writer, err := newXZWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(input); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := newXZReader(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output, input) {
		t.Fatalf("round trip = %d bytes, want %d", len(output), len(input))
	}
}

func TestZstdRawWriterRoundTrip(t *testing.T) {
	input := append(bytes.Repeat([]byte("zstd raw stream\n"), 12_000), bytes.Repeat([]byte{'A'}, 1_000)...)
	var compressed bytes.Buffer
	writer, err := newZstdWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(input); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if stream := compressed.Bytes(); len(stream) < 6 || stream[5] != 0x38 {
		t.Fatalf("Zstandard window descriptor = %x, want 38", stream)
	}
	reader, err := newZstdReader(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output, input) {
		t.Fatalf("round trip = %d bytes, want %d", len(output), len(input))
	}
}
