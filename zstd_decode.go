// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"encoding/binary"
	"errors"
)

// The Zstandard compressed-block decoder: the reversed bitstream reader, the
// FSE and Huffman entropy stages, and the sequence execution that rebuilds the
// block from literals and matches. Together these let unzstd read what the
// real zstd produces rather than only stored and RLE blocks.
//
// Structure and naming follow RFC 8878.

var errZstdData = errors.New("corrupt Zstandard data")

const (
	zstdMaxHuffLog     = 11
	zstdMaxLitLenLog   = 9
	zstdMaxMatchLenLog = 9
	zstdMaxOffsetLog   = 8
	zstdMaxBlockSize   = 1 << 17
)

// zstdBitReader reads a Zstandard bitstream, which runs backwards: the last
// byte carries a set padding bit marking the start, and fields are consumed
// from the most significant end downwards.
type zstdBitReader struct {
	data   []byte
	bitPos int // bits still unread, counted from the front of data
	// forwardPos tracks the separate low-to-high cursor used for the table
	// descriptions, which are laid out in the opposite direction.
	forwardPos int
	corrupt    bool
}

func newZstdBitReader(data []byte) (*zstdBitReader, error) {
	if len(data) == 0 {
		return nil, errZstdData
	}
	last := data[len(data)-1]
	if last == 0 {
		return nil, errZstdData
	}
	// The highest set bit of the final byte is padding and not part of the
	// stream, so the stream ends just below it.
	padding := 0
	for bit := 7; bit >= 0; bit-- {
		if last&(1<<uint(bit)) != 0 {
			padding = 8 - bit
			break
		}
	}
	return &zstdBitReader{data: data, bitPos: len(data)*8 - padding}, nil
}

// read takes count bits from the current position, moving backwards. Reading
// past the start of the stream yields zeros rather than failing, which is what
// the format's final states rely on.
func (b *zstdBitReader) read(count int) uint32 {
	if count == 0 {
		return 0
	}
	if count > 32 {
		b.corrupt = true
		return 0
	}
	var value uint32
	for i := 0; i < count; i++ {
		b.bitPos--
		var bit byte
		if b.bitPos >= 0 {
			bit = (b.data[b.bitPos>>3] >> uint(b.bitPos&7)) & 1
		}
		value = value<<1 | uint32(bit)
	}
	return value
}

// exhausted reports that the stream has been read past its start. A state
// update landing exactly on the start is not the end: the trailing states can
// still emit symbols whose updates consume no bits.
func (b *zstdBitReader) exhausted() bool { return b.bitPos < 0 }

// alignForward advances the forward cursor to the next byte boundary.
func (b *zstdBitReader) alignForward() {
	b.forwardPos = (b.forwardPos + 7) &^ 7
}

// zstdFSETable is a decoded finite-state-entropy table: for each state, the
// symbol it emits and how to reach the next state.
type zstdFSETable struct {
	log     int
	symbol  []byte
	numBits []byte
	newarr  []uint16
}

// readFSETable reads the normalised counts that describe an FSE table and
// expands them into the per-state form used while decoding.
func readFSETable(reader *zstdBitReader, maxSymbol, maxLog int) (*zstdFSETable, error) {
	// The table description is itself read forwards through a bit reader that
	// runs low-to-high, so it uses its own small accessor.
	accuracy := int(reader.readForward(4)) + 5
	if accuracy > maxLog {
		return nil, errZstdData
	}
	size := 1 << uint(accuracy)
	remaining := size + 1
	counts := make([]int16, maxSymbol+1)
	symbol := 0
	previousZero := false

	for remaining > 1 && symbol <= maxSymbol {
		if previousZero {
			// A run of zero-probability symbols, coded two bits at a time.
			zeros := 0
			for {
				pair := int(reader.readForward(2))
				zeros += pair
				if pair != 3 {
					break
				}
			}
			for i := 0; i < zeros && symbol <= maxSymbol; i++ {
				counts[symbol] = 0
				symbol++
			}
			previousZero = false
			continue
		}
		// Counts below the cutoff fit in one bit fewer than the rest, which is
		// what lets the table description stay compact.
		bits := bitsFor(remaining)
		half := 1 << uint(bits-1)
		cutoff := (1 << uint(bits)) - 1 - remaining
		value := int(reader.readForward(bits - 1))
		if value >= cutoff {
			value |= int(reader.readForward(1)) << uint(bits-1)
			if value >= half {
				value -= cutoff
			}
		}
		count := value - 1
		if count >= 0 {
			remaining -= count
		} else {
			remaining--
		}
		if symbol > maxSymbol {
			return nil, errZstdData
		}
		counts[symbol] = int16(count) //nolint:gosec // G115: bounded by the table size above.
		symbol++
		previousZero = count == 0
		if remaining < 1 {
			return nil, errZstdData
		}
	}
	if reader.corrupt || remaining != 1 {
		return nil, errZstdData
	}
	return buildFSETable(counts[:symbol], accuracy)
}

// bitsFor returns the width needed to hold value, which selects how many bits
// the next normalised count occupies.
func bitsFor(value int) int {
	bits := 0
	for 1<<uint(bits) <= value {
		bits++
	}
	return bits
}

// buildFSETable spreads the symbols across the state table in the order the
// format prescribes, then records the next-state arithmetic for each entry.
func buildFSETable(counts []int16, accuracy int) (*zstdFSETable, error) {
	size := 1 << uint(accuracy)
	table := &zstdFSETable{
		log:     accuracy,
		symbol:  make([]byte, size),
		numBits: make([]byte, size),
		newarr:  make([]uint16, size),
	}
	highThreshold := size - 1
	// Symbols with a "less than one" probability are placed at the end of the
	// table, working downwards.
	for i, count := range counts {
		if count == -1 {
			table.symbol[highThreshold] = byte(i) //nolint:gosec // G115: i indexes at most 255 symbols.
			highThreshold--
		}
	}
	position := 0
	mask := size - 1
	step := size>>1 + size>>3 + 3
	for i, count := range counts {
		for j := int16(0); j < count; j++ {
			table.symbol[position] = byte(i) //nolint:gosec // G115: i indexes at most 255 symbols.
			position = (position + step) & mask
			for position > highThreshold {
				position = (position + step) & mask
			}
		}
	}
	if position != 0 {
		return nil, errZstdData
	}
	// Each symbol's occurrences are numbered in table order, which fixes how
	// many bits that state reads and where it jumps to.
	next := make([]uint16, len(counts))
	for i, count := range counts {
		if count == -1 {
			next[i] = 1
		} else {
			next[i] = uint16(count) //nolint:gosec // G115: counts are bounded by the table size.
		}
	}
	for i := 0; i < size; i++ {
		sym := table.symbol[i]
		if int(sym) >= len(next) {
			return nil, errZstdData
		}
		value := next[sym]
		next[sym]++
		bits := accuracy - bitsFor(int(value)) + 1
		if bits < 0 || bits > accuracy {
			return nil, errZstdData
		}
		table.numBits[i] = byte(bits)                           //nolint:gosec // G115: bits is within 0..accuracy.
		table.newarr[i] = uint16(int(value)<<uint(bits) - size) //nolint:gosec // G115: the product is bounded by the table size.
	}
	return table, nil
}

// readForward reads count bits low-to-high, which is how the FSE table
// description and the Huffman weight header are laid out.
func (b *zstdBitReader) readForward(count int) uint32 {
	if count == 0 {
		return 0
	}
	if count > 32 || b.forwardPos+count > len(b.data)*8 {
		b.corrupt = true
		return 0
	}
	var value uint32
	for i := 0; i < count; i++ {
		bit := (b.data[b.forwardPos>>3] >> uint(b.forwardPos&7)) & 1
		value |= uint32(bit) << uint(i)
		b.forwardPos++
	}
	return value
}

// zstdHuffman is a canonical Huffman table flattened to a direct lookup over
// maxBits, which is how the format intends it to be decoded.
type zstdHuffman struct {
	maxBits int
	symbol  []byte
	bits    []byte
}

// buildHuffman turns per-symbol code lengths into the flattened lookup table.
func buildHuffman(weights []byte, maxBits int) (*zstdHuffman, error) {
	if maxBits < 1 || maxBits > zstdMaxHuffLog {
		return nil, errZstdData
	}
	size := 1 << uint(maxBits)
	table := &zstdHuffman{maxBits: maxBits, symbol: make([]byte, size), bits: make([]byte, size)}
	position := 0
	// Entries are laid out in ascending weight, so the longest codes -- the
	// smallest weights -- occupy the low end of the table.
	for weight := 1; weight <= maxBits; weight++ {
		// A weight w codes a symbol in maxBits+1-w bits, so it covers 2^(w-1)
		// entries of the flattened table.
		width := 1 << uint(weight-1)
		for sym, w := range weights {
			if int(w) != weight {
				continue
			}
			if position+width > size {
				return nil, errZstdData
			}
			for i := 0; i < width; i++ {
				table.symbol[position+i] = byte(sym) //nolint:gosec // G115: at most 255 symbols.
				//nolint:gosec // G115: maxBits is at most 11, so the code length fits a byte.
				table.bits[position+i] = byte(maxBits - weight + 1)
			}
			position += width
		}
	}
	if position != size {
		return nil, errZstdData
	}
	return table, nil
}

// readHuffmanTable reads a literals Huffman description, which encodes each
// symbol's weight either directly in nibbles or through its own FSE stage.
func readHuffmanTable(data []byte) (*zstdHuffman, int, error) {
	if len(data) == 0 {
		return nil, 0, errZstdData
	}
	header := data[0]
	weights := make([]byte, 256)
	count := 0
	used := 0

	if header >= 128 {
		// Direct: one nibble per symbol, header-127 symbols in total.
		count = int(header) - 127
		used = 1 + (count+1)/2
		if used > len(data) {
			return nil, 0, errZstdData
		}
		for i := 0; i < count; i++ {
			b := data[1+i/2]
			if i%2 == 0 {
				weights[i] = b >> 4
			} else {
				weights[i] = b & 0x0f
			}
		}
	} else {
		// FSE-compressed weights.
		used = int(header) + 1
		if used > len(data) {
			return nil, 0, errZstdData
		}
		var err error
		count, err = readFSEWeights(data[1:used], weights)
		if err != nil {
			return nil, 0, err
		}
	}

	// The final symbol's weight is whatever is needed to complete the power of
	// two, and is not stored.
	total := 0
	maxWeight := 0
	for i := 0; i < count; i++ {
		if weights[i] > zstdMaxHuffLog {
			return nil, 0, errZstdData
		}
		if weights[i] > 0 {
			total += 1 << uint(weights[i]-1)
		}
		if int(weights[i]) > maxWeight {
			maxWeight = int(weights[i])
		}
	}
	if total == 0 {
		return nil, 0, errZstdData
	}
	maxBits := bitsFor(total)
	if maxBits < 1 || maxBits > zstdMaxHuffLog {
		return nil, 0, errZstdData
	}
	rest := (1 << uint(maxBits)) - total
	if rest <= 0 || rest&(rest-1) != 0 {
		return nil, 0, errZstdData
	}
	if count >= 256 {
		return nil, 0, errZstdData
	}
	weights[count] = byte(bitsFor(rest)) //nolint:gosec // G115: rest is a power of two below 1<<11.
	count++

	// A weight of w means a code of maxBits+1-w bits; zero means unused.
	// buildHuffman wants the weights themselves; unused symbols keep weight 0.
	lengths := make([]byte, count)
	copy(lengths, weights[:count])
	table, err := buildHuffman(lengths, maxBits)
	if err != nil {
		return nil, 0, err
	}
	return table, used, nil
}

// readFSEWeights decodes the Huffman weight list from its own FSE stream,
// which interleaves two states across the shared bitstream.
func readFSEWeights(data []byte, weights []byte) (int, error) {
	forward := &zstdBitReader{data: data}
	table, err := readFSETable(forward, 255, 6)
	if err != nil {
		return 0, err
	}
	offset := (forward.forwardPos + 7) / 8
	if offset > len(data) {
		return 0, errZstdData
	}
	reader, err := newZstdBitReader(data[offset:])
	if err != nil {
		return 0, err
	}
	state1 := int(reader.read(table.log))
	state2 := int(reader.read(table.log))
	count := 0
	for {
		if count >= 255 || reader.corrupt {
			return 0, errZstdData
		}
		weights[count] = table.symbol[state1]
		count++
		state1 = int(table.newarr[state1]) + int(reader.read(int(table.numBits[state1])))
		if reader.exhausted() {
			// The paired state still holds one final symbol.
			weights[count] = table.symbol[state2]
			count++
			break
		}
		weights[count] = table.symbol[state2]
		count++
		state2 = int(table.newarr[state2]) + int(reader.read(int(table.numBits[state2])))
		if reader.exhausted() {
			weights[count] = table.symbol[state1]
			count++
			break
		}
	}
	if reader.corrupt {
		return 0, errZstdData
	}
	return count, nil
}

// decodeHuffmanStream reads exactly want symbols from one Huffman bitstream.
func decodeHuffmanStream(table *zstdHuffman, data []byte, out []byte) error {
	reader, err := newZstdBitReader(data)
	if err != nil {
		return err
	}
	mask := 1<<uint(table.maxBits) - 1
	for i := range out {
		if reader.bitPos <= 0 {
			return errZstdData
		}
		// Peek maxBits, padding with zeros past the end of the stream.
		peek := reader.peek(table.maxBits) & uint32(mask) //nolint:gosec // G115: mask is 2^maxBits-1 with maxBits <= 11.
		symbol := table.symbol[peek]
		bits := int(table.bits[peek])
		if bits > reader.bitPos {
			return errZstdData
		}
		reader.bitPos -= bits
		out[i] = symbol
	}
	return nil
}

// peek returns the next count bits without consuming them, treating bits past
// the start of the stream as zero.
func (b *zstdBitReader) peek(count int) uint32 {
	var value uint32
	pos := b.bitPos
	for i := 0; i < count; i++ {
		pos--
		var bit byte
		if pos >= 0 {
			bit = (b.data[pos>>3] >> uint(pos&7)) & 1
		}
		value = value<<1 | uint32(bit)
	}
	return value
}

// zstdSequence is one copy instruction: literals to emit, then a match.
type zstdSequence struct {
	literalLength int
	matchLength   int
	offset        int
}

// Baselines and extra-bit widths for the three sequence code alphabets.
var (
	zstdLitLenBaseline = [36]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
		16, 18, 20, 22, 24, 28, 32, 40, 48, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536}
	zstdLitLenExtra = [36]int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		1, 1, 1, 1, 2, 2, 3, 3, 4, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	zstdMatchLenBaseline = [53]int{3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18,
		19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 37, 39, 41, 43, 47, 51, 59,
		67, 83, 99, 131, 259, 515, 1027, 2051, 4099, 8195, 16387, 32771, 65539}
	zstdMatchLenExtra = [53]int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 3, 3,
		4, 4, 5, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
)

// Default distributions used when a sequence table is in "predefined" mode.
var (
	zstdDefaultLitLen = []int16{4, 3, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1,
		2, 2, 2, 2, 2, 2, 2, 2, 2, 3, 2, 1, 1, 1, 1, 1, -1, -1, -1, -1}
	zstdDefaultMatchLen = []int16{1, 4, 3, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, -1, -1, -1, -1, -1, -1, -1}
	zstdDefaultOffset = []int16{1, 1, 1, 1, 1, 1, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, -1, -1, -1, -1, -1}
)

func predefinedFSE(counts []int16, accuracy int) *zstdFSETable {
	table, err := buildFSETable(counts, accuracy)
	if err != nil {
		panic("ba6: predefined Zstandard table is malformed: " + err.Error())
	}
	return table
}

var (
	zstdPredefLitLen   = predefinedFSE(zstdDefaultLitLen, 6)
	zstdPredefMatchLen = predefinedFSE(zstdDefaultMatchLen, 6)
	zstdPredefOffset   = predefinedFSE(zstdDefaultOffset, 5)
)

// zstdBlockDecoder carries the state a compressed block may inherit from the
// blocks before it: the repeat offsets and any tables kept for reuse.
type zstdBlockDecoder struct {
	repeat    [3]int
	huffman   *zstdHuffman
	litLenFSE *zstdFSETable
	matchFSE  *zstdFSETable
	offsetFSE *zstdFSETable
}

func newZstdBlockDecoder() *zstdBlockDecoder {
	return &zstdBlockDecoder{repeat: [3]int{1, 4, 8}}
}

// decodeBlock expands one compressed block, appending to window and returning
// the bytes it produced. window supplies the match history.
func (d *zstdBlockDecoder) decodeBlock(src []byte, window []byte, windowStart int) ([]byte, error) {
	literals, used, err := d.decodeLiterals(src)
	if err != nil {
		return nil, err
	}
	sequences, err := d.decodeSequences(src[used:])
	if err != nil {
		return nil, err
	}
	return d.execute(literals, sequences, window, windowStart)
}

// decodeLiterals reads the literals section, which may be stored, a single
// repeated byte, or Huffman coded in one or four streams.
func (d *zstdBlockDecoder) decodeLiterals(src []byte) ([]byte, int, error) {
	if len(src) < 1 {
		return nil, 0, errZstdData
	}
	kind := src[0] & 0x03
	sizeFormat := (src[0] >> 2) & 0x03

	switch kind {
	case 0, 1: // raw or RLE
		var regenerated, headerSize int
		switch sizeFormat {
		case 0, 2:
			regenerated = int(src[0] >> 3)
			headerSize = 1
		case 1:
			if len(src) < 2 {
				return nil, 0, errZstdData
			}
			regenerated = int(src[0]>>4) | int(src[1])<<4
			headerSize = 2
		default:
			if len(src) < 3 {
				return nil, 0, errZstdData
			}
			regenerated = int(src[0]>>4) | int(src[1])<<4 | int(src[2])<<12
			headerSize = 3
		}
		if regenerated > zstdMaxBlockSize {
			return nil, 0, errZstdData
		}
		if kind == 0 {
			end := headerSize + regenerated
			if end > len(src) {
				return nil, 0, errZstdData
			}
			return src[headerSize:end], end, nil
		}
		if headerSize >= len(src) {
			return nil, 0, errZstdData
		}
		literals := make([]byte, regenerated)
		for i := range literals {
			literals[i] = src[headerSize]
		}
		return literals, headerSize + 1, nil

	case 2, 3: // Huffman coded, with a new table or the previous one
		if len(src) < 3 {
			return nil, 0, errZstdData
		}
		var regenerated, compressed, headerSize, streams int
		value := uint32(src[0]) | uint32(src[1])<<8 | uint32(src[2])<<16
		switch sizeFormat {
		case 0:
			streams, headerSize = 1, 3
			regenerated = int(value>>4) & 0x3ff
			compressed = int(value>>14) & 0x3ff
		case 1:
			streams, headerSize = 4, 3
			regenerated = int(value>>4) & 0x3ff
			compressed = int(value>>14) & 0x3ff
		case 2:
			if len(src) < 4 {
				return nil, 0, errZstdData
			}
			streams, headerSize = 4, 4
			value |= uint32(src[3]) << 24
			regenerated = int(value>>4) & 0x3fff
			compressed = int(value>>18) & 0x3fff
		default:
			if len(src) < 5 {
				return nil, 0, errZstdData
			}
			// 18-bit sizes: regenerated in bits 4..21, compressed in 22..39,
			// so the top eight bits of the latter come from the fifth byte.
			streams, headerSize = 4, 5
			value |= uint32(src[3]) << 24
			regenerated = int(value>>4) & 0x3ffff
			compressed = int(value>>22) | int(src[4])<<10
		}
		if regenerated > zstdMaxBlockSize || headerSize+compressed > len(src) {
			return nil, 0, errZstdData
		}
		body := src[headerSize : headerSize+compressed]
		if kind == 2 {
			table, used, err := readHuffmanTable(body)
			if err != nil {
				return nil, 0, err
			}
			d.huffman = table
			body = body[used:]
		}
		if d.huffman == nil {
			return nil, 0, errZstdData
		}
		literals := make([]byte, regenerated)
		if streams == 1 {
			if err := decodeHuffmanStream(d.huffman, body, literals); err != nil {
				return nil, 0, err
			}
		} else if err := d.decodeFourStreams(body, literals); err != nil {
			return nil, 0, err
		}
		return literals, headerSize + compressed, nil
	}
	return nil, 0, errZstdData
}

// decodeFourStreams splits the four independently coded literal streams, whose
// first three compressed sizes are given in a small jump table.
func (d *zstdBlockDecoder) decodeFourStreams(body []byte, literals []byte) error {
	if len(body) < 6 {
		return errZstdData
	}
	size1 := int(binary.LittleEndian.Uint16(body[0:2]))
	size2 := int(binary.LittleEndian.Uint16(body[2:4]))
	size3 := int(binary.LittleEndian.Uint16(body[4:6]))
	rest := body[6:]
	if size1+size2+size3 > len(rest) {
		return errZstdData
	}
	// The first three streams decode a quarter each, rounded up; the last takes
	// whatever remains.
	quarter := (len(literals) + 3) / 4
	bounds := []int{0, 0, 0, 0}
	for i := range 3 {
		bounds[i] = quarter
	}
	bounds[3] = len(literals) - 3*quarter
	if bounds[3] < 0 {
		return errZstdData
	}
	segments := [][]byte{
		rest[:size1],
		rest[size1 : size1+size2],
		rest[size1+size2 : size1+size2+size3],
		rest[size1+size2+size3:],
	}
	offset := 0
	for i, segment := range segments {
		if err := decodeHuffmanStream(d.huffman, segment, literals[offset:offset+bounds[i]]); err != nil {
			return err
		}
		offset += bounds[i]
	}
	return nil
}

// decodeSequences reads the sequences section: the three FSE tables (or their
// predefined or retained forms) and then the interleaved sequence stream.
func (d *zstdBlockDecoder) decodeSequences(src []byte) ([]zstdSequence, error) {
	if len(src) == 0 {
		return nil, nil
	}
	count := int(src[0])
	pos := 1
	switch {
	case count == 0:
		return nil, nil
	case count < 128:
		// count is already correct
	case count < 255:
		if len(src) < 2 {
			return nil, errZstdData
		}
		count = (count-128)<<8 + int(src[1])
		pos = 2
	default:
		if len(src) < 3 {
			return nil, errZstdData
		}
		count = int(binary.LittleEndian.Uint16(src[1:3])) + 0x7f00
		pos = 3
	}
	if pos >= len(src) {
		return nil, errZstdData
	}
	modes := src[pos]
	pos++
	if modes&0x03 != 0 {
		return nil, errZstdData // reserved bits
	}

	forward := &zstdBitReader{data: src[pos:]}
	// Symbol_Compression_Modes: literals in bits 7-6, offsets in 5-4, match
	// lengths in 3-2. The tables are then read in that same order.
	litMode := (modes >> 6) & 3
	offsetMode := (modes >> 4) & 3
	matchMode := (modes >> 2) & 3

	var err error
	if d.litLenFSE, err = d.sequenceTable(litMode, forward, 35, zstdMaxLitLenLog, zstdPredefLitLen, d.litLenFSE); err != nil {
		return nil, err
	}
	if d.offsetFSE, err = d.sequenceTable(offsetMode, forward, 31, zstdMaxOffsetLog, zstdPredefOffset, d.offsetFSE); err != nil {
		return nil, err
	}
	if d.matchFSE, err = d.sequenceTable(matchMode, forward, 52, zstdMaxMatchLenLog, zstdPredefMatchLen, d.matchFSE); err != nil {
		return nil, err
	}

	consumed := (forward.forwardPos + 7) / 8
	if pos+consumed > len(src) {
		return nil, errZstdData
	}
	reader, err := newZstdBitReader(src[pos+consumed:])
	if err != nil {
		return nil, err
	}

	// The three states are primed in this order and updated in the reverse.
	litState := int(reader.read(d.litLenFSE.log))
	offsetState := int(reader.read(d.offsetFSE.log))
	matchState := int(reader.read(d.matchFSE.log))

	sequences := make([]zstdSequence, 0, count)
	for i := 0; i < count; i++ {
		if reader.corrupt {
			return nil, errZstdData
		}
		offsetCode := int(d.offsetFSE.symbol[offsetState])
		matchCode := int(d.matchFSE.symbol[matchState])
		litCode := int(d.litLenFSE.symbol[litState])
		if offsetCode > 31 || matchCode > 52 || litCode > 35 {
			return nil, errZstdData
		}

		// Additional bits are read offset first, then match, then literal.
		offsetValue := 1<<uint(offsetCode) + int(reader.read(offsetCode))
		matchLength := zstdMatchLenBaseline[matchCode] + int(reader.read(zstdMatchLenExtra[matchCode]))
		literalLength := zstdLitLenBaseline[litCode] + int(reader.read(zstdLitLenExtra[litCode]))

		offset, err := d.resolveOffset(offsetValue, literalLength)
		if err != nil {
			return nil, err
		}
		sequences = append(sequences, zstdSequence{
			literalLength: literalLength, matchLength: matchLength, offset: offset,
		})

		if i == count-1 {
			break
		}
		// States advance in the same order they were primed.
		litState = int(d.litLenFSE.newarr[litState]) + int(reader.read(int(d.litLenFSE.numBits[litState])))
		matchState = int(d.matchFSE.newarr[matchState]) + int(reader.read(int(d.matchFSE.numBits[matchState])))
		offsetState = int(d.offsetFSE.newarr[offsetState]) + int(reader.read(int(d.offsetFSE.numBits[offsetState])))
	}
	if reader.corrupt {
		return nil, errZstdData
	}
	return sequences, nil
}

// sequenceTable resolves one of the four table modes into a usable table.
func (d *zstdBlockDecoder) sequenceTable(mode byte, forward *zstdBitReader, maxSymbol, maxLog int,
	predefined *zstdFSETable, previous *zstdFSETable,
) (*zstdFSETable, error) {
	switch mode {
	case 0: // predefined
		return predefined, nil
	case 1: // RLE: a single byte gives the only symbol
		symbol := byte(forward.readForward(8) & 0xff)
		if forward.corrupt || int(symbol) > maxSymbol {
			return nil, errZstdData
		}
		return &zstdFSETable{
			log: 0, symbol: []byte{symbol}, numBits: []byte{0}, newarr: []uint16{0},
		}, nil
	case 2: // a new table description
		table, err := readFSETable(forward, maxSymbol, maxLog)
		if err != nil {
			return nil, err
		}
		// Each description occupies a whole number of bytes, so the next one
		// starts at the following byte boundary rather than mid-byte.
		forward.alignForward()
		return table, nil
	default: // repeat whatever was used last
		if previous == nil {
			return nil, errZstdData
		}
		return previous, nil
	}
}

// resolveOffset maps the coded offset onto the sliding repeat-offset window,
// whose update rules depend on whether any literals preceded the match.
func (d *zstdBlockDecoder) resolveOffset(value, literalLength int) (int, error) {
	if value > 3 {
		offset := value - 3
		d.repeat[2], d.repeat[1], d.repeat[0] = d.repeat[1], d.repeat[0], offset
		return offset, nil
	}
	index := value - 1
	if literalLength == 0 {
		// With no literals, the codes shift up by one and code 1 means "the
		// one before rep0".
		index++
	}
	switch index {
	case 0:
		return d.repeat[0], nil
	case 1:
		offset := d.repeat[1]
		d.repeat[1], d.repeat[0] = d.repeat[0], offset
		return offset, nil
	case 2:
		offset := d.repeat[2]
		d.repeat[2], d.repeat[1], d.repeat[0] = d.repeat[1], d.repeat[0], offset
		return offset, nil
	case 3:
		offset := d.repeat[0] - 1
		if offset < 1 {
			return 0, errZstdData
		}
		d.repeat[2], d.repeat[1], d.repeat[0] = d.repeat[1], d.repeat[0], offset
		return offset, nil
	}
	return 0, errZstdData
}

// execute replays the sequences against the literals and the window, which
// already holds every earlier byte of the frame.
func (d *zstdBlockDecoder) execute(literals []byte, sequences []zstdSequence, window []byte, windowStart int) ([]byte, error) {
	out := window
	litPos := 0
	for _, seq := range sequences {
		if litPos+seq.literalLength > len(literals) {
			return nil, errZstdData
		}
		out = append(out, literals[litPos:litPos+seq.literalLength]...)
		litPos += seq.literalLength
		// A match may reach back into earlier blocks, but never past the start
		// of this frame's history.
		if seq.offset <= 0 || seq.offset > len(out)-windowStart {
			return nil, errZstdData
		}
		from := len(out) - seq.offset
		for i := 0; i < seq.matchLength; i++ {
			out = append(out, out[from+i])
		}
	}
	if litPos < len(literals) {
		out = append(out, literals[litPos:]...)
	}
	return out, nil
}
