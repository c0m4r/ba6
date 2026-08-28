// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"encoding/binary"
	"math/bits"
)

// XXH64, the hash a Zstandard frame stores as its integrity check. Only the
// low 32 bits are kept in the frame, but the whole value has to be computed to
// get them.

const (
	xxh64Prime1 uint64 = 0x9e3779b185ebca87
	xxh64Prime2 uint64 = 0xc2b2ae3d27d4eb4f
	xxh64Prime3 uint64 = 0x165667b19e3779f9
	xxh64Prime4 uint64 = 0x85ebca77c2b2ae63
	xxh64Prime5 uint64 = 0x27d4eb2f165667c5
)

// xxh64 accumulates a hash over data supplied in arbitrary pieces.
type xxh64 struct {
	v1, v2, v3, v4 uint64
	buf            [32]byte
	buffered       int
	total          uint64
}

func newXXH64() *xxh64 {
	h := &xxh64{}
	h.reset()
	return h
}

func (h *xxh64) reset() {
	// Held in variables so the seeding arithmetic wraps at run time rather
	// than overflowing as an untyped constant expression.
	p1, p2 := xxh64Prime1, xxh64Prime2
	h.v1 = p1 + p2
	h.v2 = p2
	h.v3 = 0
	h.v4 = -p1
	h.buffered = 0
	h.total = 0
}

func xxh64Round(acc, input uint64) uint64 {
	acc += input * xxh64Prime2
	acc = bits.RotateLeft64(acc, 31)
	return acc * xxh64Prime1
}

func xxh64MergeRound(acc, val uint64) uint64 {
	acc ^= xxh64Round(0, val)
	return acc*xxh64Prime1 + xxh64Prime4
}

func (h *xxh64) Write(data []byte) {
	h.total += uint64(len(data))
	// Top up the pending stripe first; a full one is consumed immediately.
	if h.buffered > 0 {
		n := copy(h.buf[h.buffered:], data)
		h.buffered += n
		data = data[n:]
		if h.buffered < 32 {
			return
		}
		h.consume(h.buf[:])
		h.buffered = 0
	}
	for len(data) >= 32 {
		h.consume(data[:32])
		data = data[32:]
	}
	if len(data) > 0 {
		h.buffered = copy(h.buf[:], data)
	}
}

func (h *xxh64) consume(stripe []byte) {
	h.v1 = xxh64Round(h.v1, binary.LittleEndian.Uint64(stripe[0:8]))
	h.v2 = xxh64Round(h.v2, binary.LittleEndian.Uint64(stripe[8:16]))
	h.v3 = xxh64Round(h.v3, binary.LittleEndian.Uint64(stripe[16:24]))
	h.v4 = xxh64Round(h.v4, binary.LittleEndian.Uint64(stripe[24:32]))
}

func (h *xxh64) Sum64() uint64 {
	var acc uint64
	if h.total >= 32 {
		acc = bits.RotateLeft64(h.v1, 1) + bits.RotateLeft64(h.v2, 7) +
			bits.RotateLeft64(h.v3, 12) + bits.RotateLeft64(h.v4, 18)
		acc = xxh64MergeRound(acc, h.v1)
		acc = xxh64MergeRound(acc, h.v2)
		acc = xxh64MergeRound(acc, h.v3)
		acc = xxh64MergeRound(acc, h.v4)
	} else {
		acc = xxh64Prime5
	}
	acc += h.total

	tail := h.buf[:h.buffered]
	for len(tail) >= 8 {
		acc ^= xxh64Round(0, binary.LittleEndian.Uint64(tail[:8]))
		acc = bits.RotateLeft64(acc, 27)*xxh64Prime1 + xxh64Prime4
		tail = tail[8:]
	}
	if len(tail) >= 4 {
		acc ^= uint64(binary.LittleEndian.Uint32(tail[:4])) * xxh64Prime1
		acc = bits.RotateLeft64(acc, 23)*xxh64Prime2 + xxh64Prime3
		tail = tail[4:]
	}
	for _, b := range tail {
		acc ^= uint64(b) * xxh64Prime5
		acc = bits.RotateLeft64(acc, 11) * xxh64Prime1
	}

	acc ^= acc >> 33
	acc *= xxh64Prime2
	acc ^= acc >> 29
	acc *= xxh64Prime3
	acc ^= acc >> 32
	return acc
}
