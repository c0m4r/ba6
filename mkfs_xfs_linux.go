// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"time"
)

// This formatter writes one conservative XFS profile: a version 5 (CRC)
// filesystem with 4 KiB blocks, 512-byte sectors and inodes, four equally
// sized allocation groups, and an internal log. Every optional feature that
// would add another on-disk btree - reverse mapping, reflink, the free inode
// btree, and sparse inode chunks - is left off, so each allocation group holds
// exactly one free extent and the layout is fully determined by its size.
const (
	xfsBlockSize      = 4096
	xfsBlockLog       = 12
	xfsSectorSize     = 512
	xfsSectorLog      = 9
	xfsInodeSize      = 512
	xfsInodeLog       = 9
	xfsInodesPerBlock = xfsBlockSize / xfsInodeSize
	xfsInodesPerBlkLg = 3
	// An inode chunk is always 64 inodes, and version 5 filesystems align
	// chunks to four blocks so that a cluster read never straddles one.
	xfsInodesPerChunk = 64
	xfsChunkBlocks    = xfsInodesPerChunk * xfsInodeSize / xfsBlockSize
	xfsInodeAlign     = 4
	xfsAgCount        = 4
	// The three btree roots follow the four header sectors in block zero.
	xfsAgReserved = 4
	// mkfs.xfs seeds every allocation group's free list with four blocks.
	xfsAgflBlocks = 4
	// mkfs.xfs picks a 64 MiB internal log for every filesystem this
	// formatter can create, and the kernel rejects logs below 10 MiB.
	xfsLogBlocks = 16384
	// An allocation group must hold at least 16 MiB, but one of them also
	// holds the log, so the smallest supported filesystem is 320 MiB.
	xfsMinAgBlocks = 20480
	xfsMaxAgBlocks = 268435455
	xfsMaxLabel    = 12

	xfsSuperMagic = 0x58465342 // XFSB
	xfsAgfMagic   = 0x58414746 // XAGF
	xfsAgiMagic   = 0x58414749 // XAGI
	xfsAgflMagic  = 0x5841464c // XAFL
	xfsBnobtMagic = 0x41423342 // AB3B
	xfsCntbtMagic = 0x41423343 // AB3C
	xfsInobtMagic = 0x49414233 // IAB3
	xfsInodeMagic = 0x494e     // IN
	xfsLogMagic   = 0xfeedbabe

	// sb_versionnum records version 5 plus the long-standing format bits
	// that every modern filesystem carries, and sb_features2 adds the lazy
	// superblock counters, attr2 inodes, 32-bit project ids, and CRCs.
	xfsVersionNum    = 0xb4a5
	xfsFeatures2     = 0x18a
	xfsIncompatFtype = 0x1

	xfsNullAgBlock = 0xffffffff
	xfsNullAgIno   = 0xffffffff
	// Reserved inodes: the root directory followed by the realtime bitmap
	// and summary inodes, which stay empty without a realtime section.
	xfsRootIno    = 64
	xfsUsedInodes = 3

	xfsSbCrcOffset    = 0xe0
	xfsAgfCrcOffset   = 0xd8
	xfsAgiCrcOffset   = 0x138
	xfsAgflCrcOffset  = 0x20
	xfsAgflBnoOffset  = 0x24
	xfsBtreeCrcOffset = 0x34
	xfsBtreeHeaderLen = 56
	xfsInodeCrcOffset = 0x64
	xfsInodeForkOff   = 176
)

func cmdMkfsXfs(args []string) int {
	const prog = "mkfs.xfs"
	request, ok := parseFormatArgs(prog, args, xfsMaxLabel)
	if !ok {
		return 1
	}
	file, bytesAvailable, status := openFormatTarget(prog, request.device, request.force)
	if file == nil {
		return status
	}
	defer file.Close()
	if request.kibibytes != 0 {
		if request.kibibytes > bytesAvailable/1024 {
			fatalf(prog, "requested %d KiB but the target holds %d KiB", request.kibibytes, bytesAvailable/1024)
			return 1
		}
		bytesAvailable = request.kibibytes * 1024
	}
	geometry, err := newXfsGeometry(bytesAvailable/xfsBlockSize, request.label)
	if err != nil {
		fatalf(prog, "%v", err)
		return 1
	}
	if err := writeXfsFilesystem(file, geometry); err != nil {
		fatalf(prog, "%s: %v", request.device, err)
		return 1
	}
	return 0
}

// xfsGeometry is the complete description of the filesystem to write. All four
// allocation groups have the same length, so the usable size is rounded down to
// a multiple of the group size.
type xfsGeometry struct {
	blocks   uint64
	agBlocks uint32
	agBlkLog uint8
	logAg    uint32
	logAgBno uint32
	logStart uint64
	free     uint64
	label    string
	uuid     []byte
	seconds  uint32
}

func newXfsGeometry(available uint64, label string) (xfsGeometry, error) {
	agBlocks := available / xfsAgCount
	if agBlocks < xfsMinAgBlocks {
		return xfsGeometry{}, fmt.Errorf("filesystem must be at least %s",
			humanSizeUint64(xfsMinAgBlocks*xfsAgCount*xfsBlockSize))
	}
	if agBlocks > xfsMaxAgBlocks {
		return xfsGeometry{}, fmt.Errorf("filesystem must be at most %s",
			humanSizeUint64(xfsMaxAgBlocks*xfsAgCount*xfsBlockSize))
	}
	geometry := xfsGeometry{
		agBlocks: uint32(agBlocks), //nolint:gosec // Bounded by xfsMaxAgBlocks above.
		blocks:   agBlocks * xfsAgCount,
		logAg:    xfsAgCount / 2,
		logAgBno: xfsAgReserved,
		label:    label,
		uuid:     make([]byte, 16),
		seconds:  uint32(time.Now().Unix()), //nolint:gosec // Legacy XFS timestamps are 32-bit.
	}
	for geometry.agBlocks > 1<<geometry.agBlkLog {
		geometry.agBlkLog++
	}
	geometry.logStart = uint64(geometry.logAg)<<geometry.agBlkLog | uint64(geometry.logAgBno)
	for group := uint32(0); group < xfsAgCount; group++ {
		_, _, freeCount := geometry.groupLayout(group)
		// Blocks parked on a group's free list still count as free space
		// in the superblock even though the btrees no longer track them.
		geometry.free += uint64(freeCount) + xfsAgflBlocks
	}
	if _, err := rand.Read(geometry.uuid); err != nil {
		return xfsGeometry{}, fmt.Errorf("generate UUID: %w", err)
	}
	geometry.uuid[6] = geometry.uuid[6]&0x0f | 0x40
	geometry.uuid[8] = geometry.uuid[8]&0x3f | 0x80
	return geometry, nil
}

// groupLayout returns the first block of the group's free-list reserve, the
// first block of its inode chunk when it has one, and the length of the single
// free extent that follows them. Reserved blocks are laid out in a fixed order
// so that every group ends with exactly one run of free space.
func (g xfsGeometry) groupLayout(group uint32) (agfl, inodes, freeCount uint32) {
	next := uint32(xfsAgReserved)
	if group == g.logAg {
		next += xfsLogBlocks
	}
	agfl = next
	next += xfsAgflBlocks
	if group == 0 {
		next = (next + xfsInodeAlign - 1) &^ (xfsInodeAlign - 1)
		inodes = next
		next += xfsChunkBlocks
	}
	return agfl, inodes, g.agBlocks - next
}

// daddr converts an allocation group block number to the 512-byte sector
// address that version 5 metadata blocks record in their headers.
func (g xfsGeometry) daddr(group, block uint32) uint64 {
	return (uint64(group)*uint64(g.agBlocks) + uint64(block)) * (xfsBlockSize / xfsSectorSize)
}

// groupOffset is the byte offset of a block within an allocation group.
func (g xfsGeometry) groupOffset(group, block uint32) int64 {
	//nolint:gosec // Group and block numbers are bounded by xfsMaxAgBlocks.
	return int64((uint64(group)*uint64(g.agBlocks) + uint64(block)) * xfsBlockSize)
}

func writeXfsFilesystem(file *os.File, geometry xfsGeometry) error {
	// The log is written first because it is the only region that must be
	// cleared of stale records. Writing the headers that point at it only
	// after it is readable keeps a partial write from producing a
	// filesystem that claims to have a log it cannot parse.
	if err := writeXfsLog(file, geometry); err != nil {
		return err
	}
	for group := uint32(0); group < xfsAgCount; group++ {
		if err := writeXfsGroup(file, geometry, group); err != nil {
			return fmt.Errorf("allocation group %d: %w", group, err)
		}
	}
	return file.Sync()
}

// writeXfsGroup writes one allocation group: the four header sectors, the three
// btree roots, and - for group zero - the inode chunk holding the root
// directory and the two realtime inodes.
func writeXfsGroup(file *os.File, geometry xfsGeometry, group uint32) error {
	agfl, inodes, freeCount := geometry.groupLayout(group)
	firstFree := geometry.agBlocks - freeCount

	headers := make([]byte, 4*xfsSectorSize)
	geometry.writeSuperblock(headers[0:xfsSectorSize])
	geometry.writeAgf(headers[xfsSectorSize:2*xfsSectorSize], group, freeCount)
	geometry.writeAgi(headers[2*xfsSectorSize:3*xfsSectorSize], group)
	geometry.writeAgfl(headers[3*xfsSectorSize:4*xfsSectorSize], group, agfl)
	if _, err := file.WriteAt(headers, geometry.groupOffset(group, 0)); err != nil {
		return err
	}

	trees := make([]byte, 3*xfsBlockSize)
	for index, magic := range []uint32{xfsBnobtMagic, xfsCntbtMagic} {
		block := trees[index*xfsBlockSize : (index+1)*xfsBlockSize]
		//nolint:gosec // Free space btree roots sit at group blocks 1 and 2.
		records := geometry.writeBtreeHeader(block, magic, group, uint32(index)+1, 1)
		binary.BigEndian.PutUint32(records[0:4], firstFree)
		binary.BigEndian.PutUint32(records[4:8], freeCount)
		xfsSetChecksum(block, xfsBtreeCrcOffset)
	}
	inobt := trees[2*xfsBlockSize : 3*xfsBlockSize]
	inodeRecords := uint16(0)
	if group == 0 {
		inodeRecords = 1
	}
	records := geometry.writeBtreeHeader(inobt, xfsInobtMagic, group, 3, inodeRecords)
	if inodeRecords == 1 {
		binary.BigEndian.PutUint32(records[0:4], xfsRootIno)
		binary.BigEndian.PutUint32(records[4:8], xfsInodesPerChunk-xfsUsedInodes)
		// Each cleared bit marks an allocated inode, lowest inode first.
		binary.BigEndian.PutUint64(records[8:16], ^uint64(1<<xfsUsedInodes-1))
	}
	xfsSetChecksum(inobt, xfsBtreeCrcOffset)
	if _, err := file.WriteAt(trees, geometry.groupOffset(group, 1)); err != nil {
		return err
	}

	if group != 0 {
		return nil
	}
	return geometry.writeInodeChunk(file, inodes)
}

func (g xfsGeometry) writeSuperblock(target []byte) {
	be16 := func(offset int, value uint16) { binary.BigEndian.PutUint16(target[offset:offset+2], value) }
	be32 := func(offset int, value uint32) { binary.BigEndian.PutUint32(target[offset:offset+4], value) }
	be64 := func(offset int, value uint64) { binary.BigEndian.PutUint64(target[offset:offset+8], value) }
	be32(0x00, xfsSuperMagic)
	be32(0x04, xfsBlockSize)
	be64(0x08, g.blocks)
	copy(target[0x20:0x30], g.uuid)
	be64(0x30, g.logStart)
	be64(0x38, xfsRootIno)
	be64(0x40, xfsRootIno+1) // realtime bitmap inode
	be64(0x48, xfsRootIno+2) // realtime summary inode
	be32(0x50, 1)            // rextsize
	be32(0x54, g.agBlocks)
	be32(0x58, xfsAgCount)
	be32(0x60, xfsLogBlocks)
	be16(0x64, xfsVersionNum)
	be16(0x66, xfsSectorSize)
	be16(0x68, xfsInodeSize)
	be16(0x6a, xfsInodesPerBlock)
	copy(target[0x6c:0x78], g.label)
	target[0x78] = xfsBlockLog
	target[0x79] = xfsSectorLog
	target[0x7a] = xfsInodeLog
	target[0x7b] = xfsInodesPerBlkLg
	target[0x7c] = g.agBlkLog
	target[0x7f] = 25 // imax_pct
	be64(0x80, xfsInodesPerChunk)
	be64(0x88, xfsInodesPerChunk-xfsUsedInodes)
	be64(0x90, g.free)
	be32(0xb4, xfsInodeAlign)
	be32(0xc4, 1) // logsunit: version 2 logs record a single-byte stripe unit
	be32(0xc8, xfsFeatures2)
	be32(0xcc, xfsFeatures2) // bad_features2 mirrors features2
	be32(0xd8, xfsIncompatFtype)
	xfsSetChecksum(target, xfsSbCrcOffset)
}

func (g xfsGeometry) writeAgf(target []byte, group, freeCount uint32) {
	be32 := func(offset int, value uint32) { binary.BigEndian.PutUint32(target[offset:offset+4], value) }
	be32(0x00, xfsAgfMagic)
	be32(0x04, 1) // versionnum
	be32(0x08, group)
	be32(0x0c, g.agBlocks)
	be32(0x10, 1) // free space by block btree root
	be32(0x14, 2) // free space by count btree root
	be32(0x1c, 1) // both free space btrees are a single leaf
	be32(0x20, 1)
	be32(0x28, 0)               // flfirst
	be32(0x2c, xfsAgflBlocks-1) // fllast
	be32(0x30, xfsAgflBlocks)   // flcount
	be32(0x34, freeCount)       // freeblks excludes the free list
	be32(0x38, freeCount)       // longest: groups hold one free extent
	copy(target[0x40:0x50], g.uuid)
	xfsSetChecksum(target, xfsAgfCrcOffset)
}

func (g xfsGeometry) writeAgi(target []byte, group uint32) {
	be32 := func(offset int, value uint32) { binary.BigEndian.PutUint32(target[offset:offset+4], value) }
	be32(0x00, xfsAgiMagic)
	be32(0x04, 1) // versionnum
	be32(0x08, group)
	be32(0x0c, g.agBlocks)
	be32(0x14, 3) // the inode btree root always follows the free space roots
	be32(0x18, 1) // level
	newino := uint32(xfsNullAgIno)
	if group == 0 {
		be32(0x10, xfsInodesPerChunk)
		be32(0x1c, xfsInodesPerChunk-xfsUsedInodes)
		newino = xfsRootIno
	}
	be32(0x20, newino)
	be32(0x24, xfsNullAgIno) // dirino
	for bucket := 0; bucket < 64; bucket++ {
		be32(0x28+bucket*4, xfsNullAgIno)
	}
	copy(target[0x128:0x138], g.uuid)
	xfsSetChecksum(target, xfsAgiCrcOffset)
}

func (g xfsGeometry) writeAgfl(target []byte, group, first uint32) {
	binary.BigEndian.PutUint32(target[0x00:0x04], xfsAgflMagic)
	binary.BigEndian.PutUint32(target[0x04:0x08], group)
	copy(target[0x08:0x18], g.uuid)
	entries := (len(target) - xfsAgflBnoOffset) / 4
	for index := 0; index < entries; index++ {
		offset := xfsAgflBnoOffset + index*4
		block := uint32(xfsNullAgBlock)
		if index < xfsAgflBlocks {
			block = first + uint32(index) //nolint:gosec // xfsAgflBlocks is a small constant.
		}
		binary.BigEndian.PutUint32(target[offset:offset+4], block)
	}
	xfsSetChecksum(target, xfsAgflCrcOffset)
}

// writeBtreeHeader fills a short-form version 5 btree block header and returns
// the record area that follows it. The caller sets the checksum once the
// records are in place.
func (g xfsGeometry) writeBtreeHeader(block []byte, magic, group, agBlock uint32, records uint16) []byte {
	binary.BigEndian.PutUint32(block[0x00:0x04], magic)
	binary.BigEndian.PutUint16(block[0x04:0x06], 0) // leaf level
	binary.BigEndian.PutUint16(block[0x06:0x08], records)
	binary.BigEndian.PutUint32(block[0x08:0x0c], xfsNullAgBlock)
	binary.BigEndian.PutUint32(block[0x0c:0x10], xfsNullAgBlock)
	binary.BigEndian.PutUint64(block[0x10:0x18], g.daddr(group, agBlock))
	copy(block[0x20:0x30], g.uuid)
	binary.BigEndian.PutUint32(block[0x30:0x34], group)
	return block[xfsBtreeHeaderLen:]
}

// writeInodeChunk writes the 64-inode chunk of allocation group zero. Inodes
// beyond the three reserved ones are unallocated but still carry a valid
// header, exactly as the kernel initialises a freshly allocated chunk.
func (g xfsGeometry) writeInodeChunk(file *os.File, first uint32) error {
	chunk := make([]byte, xfsChunkBlocks*xfsBlockSize)
	for index := uint64(0); index < xfsInodesPerChunk; index++ {
		g.writeInode(chunk[index*xfsInodeSize:(index+1)*xfsInodeSize], xfsRootIno+index)
	}
	_, err := file.WriteAt(chunk, g.groupOffset(0, first))
	return err
}

func (g xfsGeometry) writeInode(inode []byte, number uint64) {
	be16 := func(offset int, value uint16) { binary.BigEndian.PutUint16(inode[offset:offset+2], value) }
	be32 := func(offset int, value uint32) { binary.BigEndian.PutUint32(inode[offset:offset+4], value) }
	be64 := func(offset int, value uint64) { binary.BigEndian.PutUint64(inode[offset:offset+8], value) }
	be16(0x00, xfsInodeMagic)
	inode[0x04] = 3          // version 5 filesystems use version 3 inodes
	be32(0x60, xfsNullAgIno) // next_unlinked
	be64(0x98, number)
	copy(inode[0xa0:0xb0], g.uuid)
	switch number {
	case xfsRootIno:
		be16(0x02, 0o40755)
		inode[0x05] = 1 // local format: the directory lives in the inode
		be32(0x10, 2)   // the root directory links to itself
		be64(0x38, 6)   // an empty shortform directory is just its header
		inode[0x53] = 2 // an empty attribute fork is in extent format
		// Shortform directory header: no entries, no 64-bit inode
		// numbers, and a parent pointer back to the root itself.
		be32(xfsInodeForkOff+2, xfsRootIno)
	case xfsRootIno + 1, xfsRootIno + 2:
		be16(0x02, 0o100000) // a regular file with no permission bits
		inode[0x05] = 2      // extent format, holding no extents
		be32(0x10, 1)
		inode[0x53] = 2
		if number == xfsRootIno+1 {
			be16(0x5a, 0x04) // the realtime bitmap carries NEWRTBM
		}
	default:
		// Unallocated inodes keep only the header the kernel writes when
		// it initialises a chunk; the rest of the inode stays zero.
		xfsSetChecksum(inode, xfsInodeCrcOffset)
		return
	}
	for _, offset := range []int{0x20, 0x28, 0x30, 0x90} {
		be32(offset, g.seconds) // access, modify, change, and creation times
	}
	be64(0x68, 1) // changecount
	xfsSetChecksum(inode, xfsInodeCrcOffset)
}

// writeXfsLog clears the internal log and writes the unmount record that marks
// it clean, so that the filesystem mounts without recovery.
func writeXfsLog(file *os.File, geometry xfsGeometry) error {
	const clearBlocks = 256
	offset := geometry.groupOffset(geometry.logAg, geometry.logAgBno)
	zero := make([]byte, clearBlocks*xfsBlockSize)
	for written := uint32(0); written < xfsLogBlocks; written += clearBlocks {
		length := len(zero)
		if remaining := xfsLogBlocks - written; remaining < clearBlocks {
			length = int(remaining) * xfsBlockSize
		}
		if _, err := file.WriteAt(zero[:length], offset+int64(written)*xfsBlockSize); err != nil {
			return fmt.Errorf("clear log: %w", err)
		}
	}

	// The record is one 512-byte header followed by one 512-byte data
	// block. The first four bytes of every data block on disk hold the log
	// cycle number; the bytes they displace are kept in h_cycle_data.
	const savedTransaction = 0xb0c0d0d0
	record := make([]byte, 2*xfsSectorSize)
	header := record[:xfsSectorSize]
	binary.BigEndian.PutUint32(header[0x00:0x04], xfsLogMagic)
	binary.BigEndian.PutUint32(header[0x04:0x08], 1) // cycle
	binary.BigEndian.PutUint32(header[0x08:0x0c], 2) // version 2 log record
	binary.BigEndian.PutUint32(header[0x0c:0x10], xfsSectorSize)
	binary.BigEndian.PutUint64(header[0x10:0x18], 1<<32) // lsn: cycle 1, block 0
	binary.BigEndian.PutUint64(header[0x18:0x20], 1<<32) // tail_lsn
	binary.BigEndian.PutUint32(header[0x24:0x28], xfsNullAgBlock)
	binary.BigEndian.PutUint32(header[0x28:0x2c], 1) // one operation
	binary.BigEndian.PutUint32(header[0x2c:0x30], savedTransaction)
	binary.BigEndian.PutUint32(header[0x12c:0x130], 1) // little-endian Linux format
	copy(header[0x130:0x140], geometry.uuid)
	binary.BigEndian.PutUint32(header[0x140:0x144], 32768) // in-core log buffer size

	data := record[xfsSectorSize:]
	binary.BigEndian.PutUint32(data[0x00:0x04], 1) // cycle stamp
	binary.BigEndian.PutUint32(data[0x04:0x08], 8) // operation payload length
	data[0x08] = 0xaa                              // the log itself is the client
	data[0x09] = 0x20                              // unmount transaction
	binary.LittleEndian.PutUint16(data[0x0c:0x0e], 0x556e)
	_, err := file.WriteAt(record, offset)
	return err
}

// xfsSetChecksum stores the CRC32C of a metadata block over itself with the
// checksum field read as zero. XFS keeps the result little-endian.
func xfsSetChecksum(block []byte, offset int) {
	binary.LittleEndian.PutUint32(block[offset:offset+4], 0)
	sum := crc32.Checksum(block, crc32.MakeTable(crc32.Castagnoli))
	binary.LittleEndian.PutUint32(block[offset:offset+4], sum)
}
