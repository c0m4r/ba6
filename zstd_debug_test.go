// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux && zstddebug

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"testing"
)

// TestZstdDebugFile decodes the file named by BA6_ZSTD_DEBUG and reports where
// the decode diverges. Built only under the zstddebug tag.
func TestZstdDebugFile(t *testing.T) {
	path := os.Getenv("BA6_ZSTD_DEBUG")
	if path == "" {
		t.Skip("set BA6_ZSTD_DEBUG")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(os.Getenv("BA6_ZSTD_EXPECT"))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := newZstdReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("header: %v", err)
	}
	got, err := io.ReadAll(reader)
	fmt.Printf("decoded %d bytes (want %d), err=%v\n", len(got), len(want), err)
	limit := min(len(got), len(want))
	for i := 0; i < limit; i++ {
		if got[i] != want[i] {
			fmt.Printf("first difference at %d: got %#02x want %#02x\n", i, got[i], want[i])
			lo := max(0, i-16)
			fmt.Printf("  got  %x\n", got[lo:min(len(got), i+16)])
			fmt.Printf("  want %x\n", want[lo:min(len(want), i+16)])
			t.FailNow()
		}
	}
	if len(got) != len(want) {
		t.Fatalf("length differs: %d vs %d (prefix matches)", len(got), len(want))
	}
}
