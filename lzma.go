// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// An LZMA decoder, and the LZMA2 chunk layer built on it. Together these are
// what lets unxz read a stream produced by the real xz rather than only the
// stored-chunk subset ba6 writes.
//
// The range coder, probability model and state machine follow the reference
// description in LzmaSpec.cpp. Names are kept close to that description so the
// two can be read side by side.

const (
	lzmaStates           = 12
	lzmaPosBitsMax       = 4
	lzmaLenToPosStates   = 4
	lzmaAlignBits        = 4
	lzmaEndPosModelIndex = 14
	lzmaFullDistances    = 1 << (lzmaEndPosModelIndex >> 1)
	lzmaMatchMinLen      = 2

	lzmaModelTotalBits = 11
	lzmaProbInit       = 1 << (lzmaModelTotalBits - 1)
	lzmaMoveBits       = 5
	lzmaTopValue       = 1 << 24

	// lzmaMaxProps bounds the packed (pb*5+lp)*9+lc byte.
	lzmaMaxProps = 9*5*5 - 1
)

var (
	errLZMAData      = errors.New("corrupt LZMA data")
	errLZMAEndMarker = errors.New("lzma end marker")
)

// rangeDecoder is the binary range coder every LZMA symbol is read through. It
// works over one complete chunk, so a short buffer is corruption rather than a
// request for more input.
type rangeDecoder struct {
	data    []byte
	pos     int
	rng     uint32
	code    uint32
	corrupt bool
}

func (d *rangeDecoder) init() error {
	if len(d.data) < 5 || d.data[0] != 0 {
		return errLZMAData
	}
	d.rng = 0xFFFFFFFF
	d.code = binary.BigEndian.Uint32(d.data[1:5])
	d.pos = 5
	return nil
}

// finished reports whether the coder ended in the only state a well-formed
// chunk can: all input consumed and the code word drained.
func (d *rangeDecoder) finished() bool {
	return !d.corrupt && d.code == 0
}

func (d *rangeDecoder) nextByte() uint32 {
	if d.pos >= len(d.data) {
		d.corrupt = true
		return 0
	}
	b := d.data[d.pos]
	d.pos++
	return uint32(b)
}

func (d *rangeDecoder) normalize() {
	if d.rng < lzmaTopValue {
		d.rng <<= 8
		d.code = d.code<<8 | d.nextByte()
	}
}

// decodeBit normalizes after the symbol, not before. The decoded bits are the
// same either way, but only this order leaves the coder having consumed the
// encoder's whole flush, which is what the end-of-chunk check tests for.
func (d *rangeDecoder) decodeBit(prob *uint16) uint32 {
	bound := (d.rng >> lzmaModelTotalBits) * uint32(*prob)
	var bit uint32
	if d.code < bound {
		*prob += (1<<lzmaModelTotalBits - *prob) >> lzmaMoveBits
		d.rng = bound
	} else {
		*prob -= *prob >> lzmaMoveBits
		d.code -= bound
		d.rng -= bound
		bit = 1
	}
	d.normalize()
	return bit
}

func (d *rangeDecoder) decodeDirectBits(count int) uint32 {
	var result uint32
	for ; count > 0; count-- {
		d.rng >>= 1
		d.code -= d.rng
		// t is all-ones when the subtraction borrowed, which encodes a zero bit.
		t := 0 - (d.code >> 31)
		d.code += d.rng & t
		d.normalize()
		result = result<<1 + t + 1
	}
	return result
}

func (d *rangeDecoder) bitTreeDecode(probs []uint16, numBits int) uint32 {
	m := uint32(1)
	for i := 0; i < numBits; i++ {
		m = m<<1 + d.decodeBit(&probs[m])
	}
	return m - 1<<numBits
}

func (d *rangeDecoder) bitTreeReverseDecode(probs []uint16, numBits int) uint32 {
	m := uint32(1)
	var symbol uint32
	for i := 0; i < numBits; i++ {
		bit := d.decodeBit(&probs[m])
		m = m<<1 + bit
		symbol |= bit << uint(i)
	}
	return symbol
}

func resetProbs(probs []uint16) {
	for i := range probs {
		probs[i] = lzmaProbInit
	}
}

// lzmaLenDecoder reads a match length as one of three ranges, selected by two
// choice bits: 2..9, 10..17, or 18..273.
type lzmaLenDecoder struct {
	choice  uint16
	choice2 uint16
	low     [1 << lzmaPosBitsMax][8]uint16
	mid     [1 << lzmaPosBitsMax][8]uint16
	high    [256]uint16
}

func (l *lzmaLenDecoder) reset() {
	l.choice, l.choice2 = lzmaProbInit, lzmaProbInit
	for i := range l.low {
		resetProbs(l.low[i][:])
		resetProbs(l.mid[i][:])
	}
	resetProbs(l.high[:])
}

func (l *lzmaLenDecoder) decode(d *rangeDecoder, posState uint32) uint32 {
	if d.decodeBit(&l.choice) == 0 {
		return d.bitTreeDecode(l.low[posState][:], 3)
	}
	if d.decodeBit(&l.choice2) == 0 {
		return 8 + d.bitTreeDecode(l.mid[posState][:], 3)
	}
	return 16 + d.bitTreeDecode(l.high[:], 8)
}

// lzmaDecoder holds everything that survives between LZMA2 chunks: the
// probability model, the state machine, the recent distances, and the position
// the literal and position contexts are derived from.
type lzmaDecoder struct {
	lc, lp, pb uint32

	litProbs []uint16

	isMatch    [lzmaStates << lzmaPosBitsMax]uint16
	isRep      [lzmaStates]uint16
	isRepG0    [lzmaStates]uint16
	isRepG1    [lzmaStates]uint16
	isRepG2    [lzmaStates]uint16
	isRep0Long [lzmaStates << lzmaPosBitsMax]uint16

	posSlotDecoder [lzmaLenToPosStates][64]uint16
	alignDecoder   [1 << lzmaAlignBits]uint16
	posDecoders    [1 + lzmaFullDistances - lzmaEndPosModelIndex]uint16

	lenDecoder    lzmaLenDecoder
	repLenDecoder lzmaLenDecoder

	state                  uint32
	rep0, rep1, rep2, rep3 uint32

	// pos counts output bytes since the last dictionary reset. It drives the
	// position contexts, and bounds how far back a match may reach.
	pos uint64
}

// setProps unpacks the (pb*5+lp)*9+lc byte that selects the context sizes.
func (dec *lzmaDecoder) setProps(value byte) error {
	if value > lzmaMaxProps {
		return fmt.Errorf("invalid LZMA properties byte %#02x", value)
	}
	remainder := uint32(value)
	dec.lc = remainder % 9
	remainder /= 9
	dec.lp = remainder % 5
	dec.pb = remainder / 5
	if dec.lc+dec.lp > 4 {
		// Wider contexts are legal in LZMA but no xz encoder emits them, and
		// allowing them would make the literal table enormous.
		return fmt.Errorf("unsupported LZMA lc=%d lp=%d", dec.lc, dec.lp)
	}
	// lc+lp is capped at 4 above, so the table is at most 0x3000 entries.
	size := 0x300 << (dec.lc + dec.lp)
	if len(dec.litProbs) != size {
		dec.litProbs = make([]uint16, size)
	}
	return nil
}

// resetState returns the probability model, state machine and distances to
// their starting values, which an LZMA2 chunk can request independently of a
// dictionary reset.
func (dec *lzmaDecoder) resetState() {
	dec.state = 0
	dec.rep0, dec.rep1, dec.rep2, dec.rep3 = 0, 0, 0, 0
	resetProbs(dec.litProbs)
	resetProbs(dec.isMatch[:])
	resetProbs(dec.isRep[:])
	resetProbs(dec.isRepG0[:])
	resetProbs(dec.isRepG1[:])
	resetProbs(dec.isRepG2[:])
	resetProbs(dec.isRep0Long[:])
	for i := range dec.posSlotDecoder {
		resetProbs(dec.posSlotDecoder[i][:])
	}
	resetProbs(dec.alignDecoder[:])
	resetProbs(dec.posDecoders[:])
	dec.lenDecoder.reset()
	dec.repLenDecoder.reset()
}

func lzmaLiteralNextState(state uint32) uint32 {
	switch {
	case state < 4:
		return 0
	case state < 10:
		return state - 3
	default:
		return state - 6
	}
}

func lzmaMatchNextState(state uint32) uint32 {
	if state < 7 {
		return 7
	}
	return 10
}

func lzmaRepNextState(state uint32) uint32 {
	if state < 7 {
		return 8
	}
	return 11
}

func lzmaShortRepNextState(state uint32) uint32 {
	if state < 7 {
		return 9
	}
	return 11
}

// decodeDistance reads the match distance, which is coded as a slot plus a
// varying number of direct and context-coded low bits.
func (dec *lzmaDecoder) decodeDistance(d *rangeDecoder, length uint32) uint32 {
	lenState := length - lzmaMatchMinLen
	if lenState > lzmaLenToPosStates-1 {
		lenState = lzmaLenToPosStates - 1
	}
	posSlot := d.bitTreeDecode(dec.posSlotDecoder[lenState][:], 6)
	if posSlot < 4 {
		return posSlot
	}
	numDirectBits := int(posSlot>>1) - 1
	dist := (2 | (posSlot & 1)) << numDirectBits
	if posSlot < lzmaEndPosModelIndex {
		dist += d.bitTreeReverseDecode(dec.posDecoders[dist-posSlot:], numDirectBits)
		return dist
	}
	dist += d.decodeDirectBits(numDirectBits-lzmaAlignBits) << lzmaAlignBits
	dist += d.bitTreeReverseDecode(dec.alignDecoder[:], lzmaAlignBits)
	return dist
}

// decodeChunk appends exactly limit bytes to *out. Everything already in *out
// is available as match history, bounded by dec.pos so a match cannot reach
// across a dictionary reset. It returns errLZMAEndMarker when the stream's end
// marker is met, which LZMA2 permits at the close of a chunk.
func (dec *lzmaDecoder) decodeChunk(d *rangeDecoder, out *[]byte, limit int) error {
	buf := *out
	defer func() { *out = buf }()

	target := len(buf) + limit
	// The masks are at most 15, so the masked position always fits a uint32.
	pbMask := uint64(1)<<dec.pb - 1
	lpMask := uint64(1)<<dec.lp - 1

	for len(buf) < target {
		if d.corrupt {
			return errLZMAData
		}
		posState := uint32(dec.pos & pbMask) //nolint:gosec // G115: masked by pbMask <= 15.
		stateIndex := dec.state<<lzmaPosBitsMax + posState

		if d.decodeBit(&dec.isMatch[stateIndex]) == 0 {
			var prevByte byte
			if dec.pos > 0 {
				prevByte = buf[len(buf)-1]
			}
			//nolint:gosec // G115: masked by lpMask <= 15 before the shift.
			litState := uint32(dec.pos&lpMask)<<dec.lc + uint32(prevByte>>(8-dec.lc))
			probs := dec.litProbs[0x300*litState:]
			symbol := uint32(1)
			if dec.state >= 7 {
				// After a match, the literal is coded against the byte at the
				// last distance, bit by bit, until the two disagree.
				if !dec.canReach(dec.rep0, len(buf)) {
					return errLZMAData
				}
				matchByte := buf[len(buf)-int(dec.rep0)-1]
				for symbol < 0x100 {
					matchBit := uint32(matchByte>>7) & 1
					matchByte <<= 1
					bit := d.decodeBit(&probs[(1+matchBit)<<8+symbol])
					symbol = symbol<<1 | bit
					if matchBit != bit {
						break
					}
				}
			}
			for symbol < 0x100 {
				symbol = symbol<<1 | d.decodeBit(&probs[symbol])
			}
			buf = append(buf, byte(symbol))
			dec.pos++
			dec.state = lzmaLiteralNextState(dec.state)
			continue
		}

		var length uint32
		if d.decodeBit(&dec.isRep[dec.state]) != 0 {
			if dec.pos == 0 {
				return errLZMAData
			}
			if d.decodeBit(&dec.isRepG0[dec.state]) == 0 {
				if d.decodeBit(&dec.isRep0Long[stateIndex]) == 0 {
					if !dec.canReach(dec.rep0, len(buf)) {
						return errLZMAData
					}
					buf = append(buf, buf[len(buf)-int(dec.rep0)-1])
					dec.pos++
					dec.state = lzmaShortRepNextState(dec.state)
					continue
				}
			} else {
				var dist uint32
				if d.decodeBit(&dec.isRepG1[dec.state]) == 0 {
					dist = dec.rep1
				} else {
					if d.decodeBit(&dec.isRepG2[dec.state]) == 0 {
						dist = dec.rep2
					} else {
						dist = dec.rep3
						dec.rep3 = dec.rep2
					}
					dec.rep2 = dec.rep1
				}
				dec.rep1 = dec.rep0
				dec.rep0 = dist
			}
			length = dec.repLenDecoder.decode(d, posState) + lzmaMatchMinLen
			dec.state = lzmaRepNextState(dec.state)
		} else {
			dec.rep3, dec.rep2, dec.rep1 = dec.rep2, dec.rep1, dec.rep0
			length = dec.lenDecoder.decode(d, posState) + lzmaMatchMinLen
			dec.state = lzmaMatchNextState(dec.state)
			dist := dec.decodeDistance(d, length)
			if dist == 0xFFFFFFFF {
				return errLZMAEndMarker
			}
			dec.rep0 = dist
		}

		if !dec.canReach(dec.rep0, len(buf)) {
			return errLZMAData
		}
		if len(buf)+int(length) > target {
			// A chunk's uncompressed size is exact, so a match may not run past it.
			return errLZMAData
		}
		from := len(buf) - int(dec.rep0) - 1
		for i := uint32(0); i < length; i++ {
			buf = append(buf, buf[from])
			from++
		}
		dec.pos += uint64(length)
	}
	if d.corrupt {
		return errLZMAData
	}
	return nil
}

// canReach reports whether a match at distance dist has that many bytes behind
// it, both in the buffer we hold and since the last dictionary reset.
func (dec *lzmaDecoder) canReach(dist uint32, available int) bool {
	return uint64(dist) < dec.pos && int64(dist) < int64(available)
}

// lzma2Reader turns the LZMA2 chunk sequence into a plain byte stream. The
// window is the decoded output itself, trimmed back to the dictionary size
// once the caller has consumed enough of it.
type lzma2Reader struct {
	input    *bufio.Reader
	dec      lzmaDecoder
	buf      []byte
	consumed int
	dictSize int

	sawProps    bool
	sawDictInit bool
	finished    bool

	// compressed counts the bytes taken from the input, which the container
	// needs in order to find the block padding that follows.
	compressed int64
}

// lzma2MaxChunk is the largest uncompressed size one LZMA2 chunk can declare.
const lzma2MaxChunk = 1 << 21

func newLZMA2Reader(input *bufio.Reader, dictSize int) *lzma2Reader {
	if dictSize < 4096 {
		dictSize = 4096
	}
	return &lzma2Reader{input: input, dictSize: dictSize}
}

func (r *lzma2Reader) Read(output []byte) (int, error) {
	for r.consumed == len(r.buf) {
		if r.finished {
			return 0, io.EOF
		}
		r.compact()
		if err := r.readChunk(); err != nil {
			return 0, err
		}
	}
	n := copy(output, r.buf[r.consumed:])
	r.consumed += n
	return n, nil
}

// compact drops history the dictionary can no longer refer to, so decoding a
// large stream does not hold all of it in memory. Only bytes the caller has
// already taken are eligible.
func (r *lzma2Reader) compact() {
	keep := r.dictSize
	if len(r.buf) <= keep {
		return
	}
	drop := len(r.buf) - keep
	if drop > r.consumed {
		drop = r.consumed
	}
	if drop < 1<<20 {
		return
	}
	copy(r.buf, r.buf[drop:])
	r.buf = r.buf[:len(r.buf)-drop]
	r.consumed -= drop
}

func (r *lzma2Reader) readChunk() error {
	control, err := r.input.ReadByte()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	r.compressed++
	switch {
	case control == 0:
		r.finished = true
		return nil
	case control == 1 || control == 2:
		return r.readStoredChunk(control == 1)
	case control >= 0x80:
		return r.readCompressedChunk(control)
	default:
		return fmt.Errorf("invalid LZMA2 control byte %#02x", control)
	}
}

func (r *lzma2Reader) readStoredChunk(dictReset bool) error {
	if dictReset {
		r.resetDictionary()
	} else if !r.sawDictInit {
		return fmt.Errorf("LZMA2 stored chunk before a dictionary reset")
	}
	size, err := r.readBE16()
	if err != nil {
		return err
	}
	length := int(size) + 1
	start := len(r.buf)
	r.buf = append(r.buf, make([]byte, length)...)
	if _, err := io.ReadFull(r.input, r.buf[start:]); err != nil {
		r.buf = r.buf[:start]
		return unexpectedEOF(err)
	}
	r.compressed += int64(length)
	r.dec.pos += uint64(length)
	// A stored chunk resets the coder state, so a following chunk may ask for
	// no reset of its own.
	r.dec.resetState()
	return nil
}

func (r *lzma2Reader) readCompressedChunk(control byte) error {
	high, err := r.readBE16()
	if err != nil {
		return err
	}
	unpackSize := int(uint32(control&0x1F)<<16|uint32(high)) + 1
	packed, err := r.readBE16()
	if err != nil {
		return err
	}
	packSize := int(packed) + 1
	if unpackSize > lzma2MaxChunk {
		return errLZMAData
	}

	switch (control >> 5) & 3 {
	case 0: // carry everything over
	case 1:
		r.dec.resetState()
	case 2:
		if err := r.readProps(); err != nil {
			return err
		}
	case 3:
		if err := r.readProps(); err != nil {
			return err
		}
		r.resetDictionary()
	}
	if !r.sawProps {
		return fmt.Errorf("LZMA2 chunk before any properties byte")
	}
	if !r.sawDictInit {
		return fmt.Errorf("LZMA2 chunk before a dictionary reset")
	}

	data := make([]byte, packSize)
	if _, err := io.ReadFull(r.input, data); err != nil {
		return unexpectedEOF(err)
	}
	r.compressed += int64(packSize)
	coder := &rangeDecoder{data: data}
	if err := coder.init(); err != nil {
		return err
	}
	start := len(r.buf)
	err = r.dec.decodeChunk(coder, &r.buf, unpackSize)
	switch {
	case errors.Is(err, errLZMAEndMarker):
		// The end marker may only close a chunk that is otherwise complete.
		if len(r.buf)-start != unpackSize {
			return errLZMAData
		}
	case err != nil:
		return err
	}
	if !coder.finished() || coder.pos != len(data) {
		return errLZMAData
	}
	return nil
}

func (r *lzma2Reader) readProps() error {
	value, err := r.input.ReadByte()
	if err != nil {
		return unexpectedEOF(err)
	}
	r.compressed++
	if err := r.dec.setProps(value); err != nil {
		return err
	}
	r.sawProps = true
	r.dec.resetState()
	return nil
}

func (r *lzma2Reader) resetDictionary() {
	r.dec.pos = 0
	r.sawDictInit = true
}

func (r *lzma2Reader) readBE16() (uint16, error) {
	var pair [2]byte
	if _, err := io.ReadFull(r.input, pair[:]); err != nil {
		return 0, unexpectedEOF(err)
	}
	r.compressed += 2
	return binary.BigEndian.Uint16(pair[:]), nil
}

func unexpectedEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
