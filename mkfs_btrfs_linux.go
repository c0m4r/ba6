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

// This formatter writes the smallest complete btrfs a kernel will mount: one
// device, three single-profile block groups, and the eight trees the filesystem
// always needs. Every tree is a single leaf, so no interior nodes are involved
// and the whole layout is fixed regardless of device size. Optional features
// that would add trees of their own - free space tree, quotas, and the block
// group tree - are left off.
const (
	btrfsSuperOffset = 65536
	btrfsSuperSize   = 4096
	btrfsMagic       = "_BHRfS_M"
	btrfsNodeSize    = 16384
	btrfsSectorSize  = 4096
	btrfsStripeLen   = 65536
	btrfsCsumSize    = 32
	btrfsHeaderSize  = 101
	btrfsItemSize    = 25
	btrfsUUIDSize    = 16
	btrfsLabelSize   = 256
	btrfsMaxLabel    = 255

	// The first mebibyte is reserved so that boot sectors and the primary
	// superblock never fall inside an allocated chunk.
	btrfsReserved      = 1 << 20
	btrfsSystemBytes   = 4 << 20
	btrfsMetadataBytes = 8 << 20
	btrfsDataBytes     = 8 << 20
	// Seven trees live in the metadata chunk and the chunk tree lives in
	// the system chunk.
	btrfsMetadataNodes = 7
	// The 64 MiB superblock mirror must fall inside the device, and the
	// block groups above already claim the first 21 MiB.
	btrfsMinBytes = 128 << 20

	btrfsRootTreeID      = 1
	btrfsExtentTreeID    = 2
	btrfsChunkTreeID     = 3
	btrfsDevTreeID       = 4
	btrfsFsTreeID        = 5
	btrfsRootTreeDirID   = 6
	btrfsCsumTreeID      = 7
	btrfsUUIDTreeID      = 9
	btrfsDataRelocTreeID = ^uint64(0) - 8 // -9
	btrfsDevItemsID      = 1
	btrfsFirstChunkID    = 256
	btrfsFirstFreeID     = 256
	btrfsDevStatsID      = 0

	btrfsInodeItemKey      = 1
	btrfsInodeRefKey       = 12
	btrfsDirItemKey        = 84
	btrfsRootItemKey       = 132
	btrfsMetadataItemKey   = 169
	btrfsTreeBlockRefKey   = 176
	btrfsBlockGroupItemKey = 192
	btrfsDevExtentKey      = 204
	btrfsDevItemKey        = 216
	btrfsChunkItemKey      = 228
	btrfsPersistentItemKey = 249
	btrfsUUIDSubvolKey     = 251

	btrfsBlockGroupData     = 1 << 0
	btrfsBlockGroupSystem   = 1 << 1
	btrfsBlockGroupMetadata = 1 << 2

	// Written, plus backref revision 1 in the top byte.
	btrfsHeaderFlags   = 1 | 1<<56
	btrfsExtentTreeFlg = 2 // the extent describes a tree block
	btrfsFileTypeDir   = 2

	// MIXED_BACKREF, BIG_METADATA, EXTENDED_IREF, and SKINNY_METADATA: the
	// set every current mkfs.btrfs enables by default.
	btrfsIncompatFlags = 0x1 | 0x20 | 0x40 | 0x100

	btrfsRootItemLen  = 439
	btrfsInodeItemLen = 160
	btrfsDevItemLen   = 98
	btrfsChunkItemLen = 80
	btrfsDevExtentLen = 48
	btrfsBlockGroupLn = 24
	btrfsDevStatsLen  = 40
	btrfsMetadataLen  = 33
)

// btrfsSuperMirrors are the offsets btrfs keeps superblock copies at. A copy is
// only written when it fits entirely inside the device.
var btrfsSuperMirrors = []uint64{btrfsSuperOffset, 64 << 20, 256 << 30}

var btrfsCastagnoli = crc32.MakeTable(crc32.Castagnoli)

func cmdMkfsBtrfs(args []string) int {
	const prog = "mkfs.btrfs"
	request, ok := parseFormatArgs(prog, args, btrfsMaxLabel)
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
	if bytesAvailable < btrfsMinBytes {
		fatalf(prog, "filesystem must be at least %s", humanSizeUint64(btrfsMinBytes))
		return 1
	}
	layout, err := newBtrfsLayout(bytesAvailable&^(btrfsSectorSize-1), request.label)
	if err != nil {
		fatalf(prog, "%v", err)
		return 1
	}
	if err := writeBtrfsFilesystem(file, layout); err != nil {
		fatalf(prog, "%s: %v", request.device, err)
		return 1
	}
	return 0
}

// btrfsLayout fixes where every chunk and tree block lives. Logical addresses
// and device offsets are identical here because the three chunks are laid out
// consecutively from the start of the device.
type btrfsLayout struct {
	total     uint64
	system    uint64
	metadata  uint64
	data      uint64
	chunkRoot uint64
	trees     map[uint64]uint64
	label     string
	fsid      []byte
	chunkUUID []byte
	devUUID   []byte
	fsUUID    []byte
	seconds   uint64
}

// btrfsMetadataTrees lists the trees stored in the metadata chunk, in the order
// their blocks are allocated.
var btrfsMetadataTrees = []uint64{
	btrfsRootTreeID, btrfsExtentTreeID, btrfsDevTreeID, btrfsFsTreeID,
	btrfsCsumTreeID, btrfsUUIDTreeID, btrfsDataRelocTreeID,
}

func newBtrfsLayout(total uint64, label string) (btrfsLayout, error) {
	layout := btrfsLayout{
		total:    total,
		system:   btrfsReserved,
		metadata: btrfsReserved + btrfsSystemBytes,
		data:     btrfsReserved + btrfsSystemBytes + btrfsMetadataBytes,
		trees:    make(map[uint64]uint64, len(btrfsMetadataTrees)),
		label:    label,
		seconds:  uint64(time.Now().Unix()), //nolint:gosec // Unix seconds are positive.
	}
	layout.chunkRoot = layout.system + btrfsNodeSize
	for index, tree := range btrfsMetadataTrees {
		layout.trees[tree] = layout.metadata + uint64(index)*btrfsNodeSize
	}
	for _, target := range []*[]byte{&layout.fsid, &layout.chunkUUID, &layout.devUUID, &layout.fsUUID} {
		value := make([]byte, btrfsUUIDSize)
		if _, err := rand.Read(value); err != nil {
			return btrfsLayout{}, fmt.Errorf("generate UUID: %w", err)
		}
		value[6] = value[6]&0x0f | 0x40
		value[8] = value[8]&0x3f | 0x80
		*target = value
	}
	return layout, nil
}

// deviceUsed is the part of the device the three chunks claim.
func (l btrfsLayout) deviceUsed() uint64 {
	return btrfsSystemBytes + btrfsMetadataBytes + btrfsDataBytes
}

// bytesUsed is the space the tree blocks occupy inside those chunks.
func (l btrfsLayout) bytesUsed() uint64 {
	return (1 + btrfsMetadataNodes) * btrfsNodeSize
}

func writeBtrfsFilesystem(file *os.File, layout btrfsLayout) error {
	// Clearing the reserved area and both metadata chunks removes any
	// superblock or tree block left by a previous filesystem, so nothing
	// stale can be mistaken for part of the new one.
	if err := clearRange(file, 0, btrfsReserved+btrfsSystemBytes+btrfsMetadataBytes); err != nil {
		return fmt.Errorf("clear metadata area: %w", err)
	}
	blocks := map[uint64][]byte{
		layout.chunkRoot:                   layout.chunkTree(),
		layout.trees[btrfsRootTreeID]:      layout.rootTree(),
		layout.trees[btrfsExtentTreeID]:    layout.extentTree(),
		layout.trees[btrfsDevTreeID]:       layout.devTree(),
		layout.trees[btrfsFsTreeID]:        layout.directoryTree(btrfsFsTreeID),
		layout.trees[btrfsCsumTreeID]:      layout.newLeaf(btrfsCsumTreeID).at(layout.trees[btrfsCsumTreeID]).finish(),
		layout.trees[btrfsUUIDTreeID]:      layout.uuidTree(),
		layout.trees[btrfsDataRelocTreeID]: layout.directoryTree(btrfsDataRelocTreeID),
	}
	for offset, block := range blocks {
		if block == nil {
			return fmt.Errorf("tree block at %d overflowed a leaf", offset)
		}
		//nolint:gosec // Offsets are bounded by the device size.
		if _, err := file.WriteAt(block, int64(offset)); err != nil {
			return err
		}
	}
	for _, mirror := range btrfsSuperMirrors {
		if mirror+btrfsSuperSize > layout.total {
			break
		}
		//nolint:gosec // Mirror offsets were checked against the device size.
		if _, err := file.WriteAt(layout.superblock(mirror), int64(mirror)); err != nil {
			return err
		}
	}
	return file.Sync()
}

// clearRange zeroes a region of the target in bounded writes.
func clearRange(file *os.File, offset, length uint64) error {
	const chunk = 1 << 20
	zero := make([]byte, chunk)
	for written := uint64(0); written < length; written += chunk {
		size := uint64(chunk)
		if remaining := length - written; remaining < size {
			size = remaining
		}
		//nolint:gosec // Callers clear regions near the start of the device.
		if _, err := file.WriteAt(zero[:size], int64(offset+written)); err != nil {
			return err
		}
	}
	return nil
}

// btrfsLeaf builds one tree block. btrfs stores item headers in ascending order
// from the front of the block and their payloads in descending order from the
// back, so callers must add items in key order.
type btrfsLeaf struct {
	block    []byte
	items    uint32
	dataEnd  int
	overflow bool
}

func (l btrfsLayout) newLeaf(owner uint64) *btrfsLeaf {
	leaf := &btrfsLeaf{block: make([]byte, btrfsNodeSize), dataEnd: btrfsNodeSize - btrfsHeaderSize}
	copy(leaf.block[32:48], l.fsid)
	binary.LittleEndian.PutUint64(leaf.block[56:64], btrfsHeaderFlags)
	copy(leaf.block[64:80], l.chunkUUID)
	binary.LittleEndian.PutUint64(leaf.block[80:88], 1) // generation
	binary.LittleEndian.PutUint64(leaf.block[88:96], owner)
	return leaf
}

// add reserves size bytes for one item and returns its payload area.
func (l *btrfsLeaf) add(objectid uint64, keyType uint8, offset uint64, size int) []byte {
	slot := btrfsHeaderSize + int(l.items)*btrfsItemSize
	if slot+btrfsItemSize > btrfsHeaderSize+l.dataEnd-size {
		l.overflow = true
		return make([]byte, size)
	}
	binary.LittleEndian.PutUint64(l.block[slot:slot+8], objectid)
	l.block[slot+8] = keyType
	binary.LittleEndian.PutUint64(l.block[slot+9:slot+17], offset)
	l.dataEnd -= size
	//nolint:gosec // Item offsets and sizes are bounded by the node size.
	payload, length := uint32(l.dataEnd), uint32(size)
	binary.LittleEndian.PutUint32(l.block[slot+17:slot+21], payload)
	binary.LittleEndian.PutUint32(l.block[slot+21:slot+25], length)
	l.items++
	start := btrfsHeaderSize + l.dataEnd
	return l.block[start : start+size]
}

// finish stamps the item count and checksum. It returns nil if any item
// overflowed the leaf.
func (l *btrfsLeaf) finish() []byte {
	if l.overflow {
		return nil
	}
	binary.LittleEndian.PutUint32(l.block[96:100], l.items)
	btrfsSetChecksum(l.block)
	return l.block
}

// at records the logical address a finished block will be written to, which
// btrfs stores inside the block so a misdirected read is detected.
func (l *btrfsLeaf) at(bytenr uint64) *btrfsLeaf {
	binary.LittleEndian.PutUint64(l.block[48:56], bytenr)
	return l
}

func (l btrfsLayout) chunkTree() []byte {
	leaf := l.newLeaf(btrfsChunkTreeID).at(l.chunkRoot)
	l.writeDevItem(leaf.add(btrfsDevItemsID, btrfsDevItemKey, 1, btrfsDevItemLen))
	for _, chunk := range []struct {
		start  uint64
		length uint64
		flags  uint64
	}{
		{l.system, btrfsSystemBytes, btrfsBlockGroupSystem},
		{l.metadata, btrfsMetadataBytes, btrfsBlockGroupMetadata},
		{l.data, btrfsDataBytes, btrfsBlockGroupData},
	} {
		item := leaf.add(btrfsFirstChunkID, btrfsChunkItemKey, chunk.start, btrfsChunkItemLen)
		l.writeChunkItem(item, chunk.start, chunk.length, chunk.flags)
	}
	return leaf.finish()
}

func (l btrfsLayout) writeDevItem(item []byte) {
	binary.LittleEndian.PutUint64(item[0:8], 1) // devid
	binary.LittleEndian.PutUint64(item[8:16], l.total)
	binary.LittleEndian.PutUint64(item[16:24], l.deviceUsed())
	binary.LittleEndian.PutUint32(item[24:28], btrfsSectorSize) // io_align
	binary.LittleEndian.PutUint32(item[28:32], btrfsSectorSize) // io_width
	binary.LittleEndian.PutUint32(item[32:36], btrfsSectorSize)
	copy(item[66:82], l.devUUID)
	copy(item[82:98], l.fsid)
}

func (l btrfsLayout) writeChunkItem(item []byte, start, length, flags uint64) {
	binary.LittleEndian.PutUint64(item[0:8], length)
	binary.LittleEndian.PutUint64(item[8:16], btrfsExtentTreeID) // chunk owner
	binary.LittleEndian.PutUint64(item[16:24], btrfsStripeLen)
	binary.LittleEndian.PutUint64(item[24:32], flags)
	// The bootstrap system chunk is aligned to a sector; the chunks
	// allocated afterwards are aligned to a full stripe.
	alignment, subStripes := uint32(btrfsSectorSize), uint16(0)
	if flags != btrfsBlockGroupSystem {
		alignment, subStripes = btrfsStripeLen, 1
	}
	binary.LittleEndian.PutUint32(item[32:36], alignment)
	binary.LittleEndian.PutUint32(item[36:40], alignment)
	binary.LittleEndian.PutUint32(item[40:44], btrfsSectorSize)
	binary.LittleEndian.PutUint16(item[44:46], 1) // one stripe on one device
	binary.LittleEndian.PutUint16(item[46:48], subStripes)
	binary.LittleEndian.PutUint64(item[48:56], 1)     // stripe devid
	binary.LittleEndian.PutUint64(item[56:64], start) // stripe device offset
	copy(item[64:80], l.devUUID)
}

func (l btrfsLayout) rootTree() []byte {
	leaf := l.newLeaf(btrfsRootTreeID).at(l.trees[btrfsRootTreeID])
	l.writeRootItem(leaf.add(btrfsExtentTreeID, btrfsRootItemKey, 0, btrfsRootItemLen),
		l.trees[btrfsExtentTreeID], 0, nil)
	l.writeRootItem(leaf.add(btrfsDevTreeID, btrfsRootItemKey, 0, btrfsRootItemLen),
		l.trees[btrfsDevTreeID], 0, nil)
	// The default subvolume is reached through a directory entry in the
	// root tree, so the root tree directory owns a reference to it.
	writeBtrfsInodeRef(leaf.add(btrfsFsTreeID, btrfsInodeRefKey, btrfsRootTreeDirID, 10+len("default")), "default")
	l.writeRootItem(leaf.add(btrfsFsTreeID, btrfsRootItemKey, 0, btrfsRootItemLen),
		l.trees[btrfsFsTreeID], btrfsFirstFreeID, l.fsUUID)

	l.writeInodeItem(leaf.add(btrfsRootTreeDirID, btrfsInodeItemKey, 0, btrfsInodeItemLen))
	writeBtrfsInodeRef(leaf.add(btrfsRootTreeDirID, btrfsInodeRefKey, btrfsRootTreeDirID, 10+len("..")), "..")
	entry := leaf.add(btrfsRootTreeDirID, btrfsDirItemKey, uint64(btrfsNameHash("default")), 30+len("default"))
	binary.LittleEndian.PutUint64(entry[0:8], btrfsFsTreeID)
	entry[8] = btrfsRootItemKey
	binary.LittleEndian.PutUint64(entry[9:17], ^uint64(0)) // newest root item
	binary.LittleEndian.PutUint64(entry[17:25], 1)         // transid
	binary.LittleEndian.PutUint16(entry[27:29], uint16(len("default")))
	entry[29] = btrfsFileTypeDir
	copy(entry[30:], "default")

	l.writeRootItem(leaf.add(btrfsCsumTreeID, btrfsRootItemKey, 0, btrfsRootItemLen),
		l.trees[btrfsCsumTreeID], 0, nil)
	l.writeRootItem(leaf.add(btrfsUUIDTreeID, btrfsRootItemKey, 0, btrfsRootItemLen),
		l.trees[btrfsUUIDTreeID], 0, nil)
	l.writeRootItem(leaf.add(btrfsDataRelocTreeID, btrfsRootItemKey, 0, btrfsRootItemLen),
		l.trees[btrfsDataRelocTreeID], btrfsFirstFreeID, nil)
	return leaf.finish()
}

// writeRootItem describes one tree: where its root block is, how much space it
// uses, and - for subvolumes - the directory inode and UUID it carries.
func (l btrfsLayout) writeRootItem(item []byte, bytenr, dirid uint64, uuid []byte) {
	// The embedded inode is vestigial but tools still read its mode.
	binary.LittleEndian.PutUint64(item[0:8], 1)   // inode generation
	binary.LittleEndian.PutUint64(item[16:24], 3) // inode size
	binary.LittleEndian.PutUint64(item[24:32], btrfsNodeSize)
	binary.LittleEndian.PutUint32(item[40:44], 1) // nlink
	binary.LittleEndian.PutUint32(item[52:56], 0o40755)

	binary.LittleEndian.PutUint64(item[160:168], 1) // generation
	binary.LittleEndian.PutUint64(item[168:176], dirid)
	binary.LittleEndian.PutUint64(item[176:184], bytenr)
	binary.LittleEndian.PutUint64(item[192:200], btrfsNodeSize) // bytes_used
	binary.LittleEndian.PutUint32(item[216:220], 1)             // refs
	item[238] = 0                                               // a single leaf is level zero
	binary.LittleEndian.PutUint64(item[239:247], 1)             // generation_v2
	if uuid != nil {
		copy(item[247:263], uuid)
		binary.LittleEndian.PutUint64(item[295:303], 1) // ctransid
		binary.LittleEndian.PutUint64(item[303:311], 1) // otransid
		binary.LittleEndian.PutUint64(item[327:335], l.seconds)
		binary.LittleEndian.PutUint64(item[339:347], l.seconds)
	}
}

func (l btrfsLayout) extentTree() []byte {
	leaf := l.newLeaf(btrfsExtentTreeID).at(l.trees[btrfsExtentTreeID])
	// Items are added in key order: ascending logical address, and within
	// one address the metadata item sorts before the block group item.
	l.writeBlockGroup(leaf.add(l.system, btrfsBlockGroupItemKey, btrfsSystemBytes, btrfsBlockGroupLn),
		btrfsNodeSize, btrfsBlockGroupSystem)
	l.writeMetadataItem(leaf.add(l.chunkRoot, btrfsMetadataItemKey, 0, btrfsMetadataLen), btrfsChunkTreeID)
	for index, tree := range btrfsMetadataTrees {
		block := l.trees[tree]
		l.writeMetadataItem(leaf.add(block, btrfsMetadataItemKey, 0, btrfsMetadataLen), tree)
		if index == 0 {
			// The metadata block group item shares its key objectid
			// with the first block in the chunk, the root tree.
			l.writeBlockGroup(leaf.add(l.metadata, btrfsBlockGroupItemKey, btrfsMetadataBytes, btrfsBlockGroupLn),
				btrfsMetadataNodes*btrfsNodeSize, btrfsBlockGroupMetadata)
		}
	}
	l.writeBlockGroup(leaf.add(l.data, btrfsBlockGroupItemKey, btrfsDataBytes, btrfsBlockGroupLn),
		0, btrfsBlockGroupData)
	return leaf.finish()
}

func (l btrfsLayout) writeBlockGroup(item []byte, used, flags uint64) {
	binary.LittleEndian.PutUint64(item[0:8], used)
	binary.LittleEndian.PutUint64(item[8:16], btrfsFirstChunkID)
	binary.LittleEndian.PutUint64(item[16:24], flags)
}

// writeMetadataItem records that one tree block is allocated and referenced
// only by the tree that owns it. The skinny format keeps the block's level in
// the key rather than in the item.
func (l btrfsLayout) writeMetadataItem(item []byte, owner uint64) {
	binary.LittleEndian.PutUint64(item[0:8], 1) // refs
	binary.LittleEndian.PutUint64(item[8:16], 1)
	binary.LittleEndian.PutUint64(item[16:24], btrfsExtentTreeFlg)
	item[24] = btrfsTreeBlockRefKey
	binary.LittleEndian.PutUint64(item[25:33], owner)
}

func (l btrfsLayout) devTree() []byte {
	leaf := l.newLeaf(btrfsDevTreeID).at(l.trees[btrfsDevTreeID])
	leaf.add(btrfsDevStatsID, btrfsPersistentItemKey, 1, btrfsDevStatsLen)
	for _, chunk := range []struct {
		start  uint64
		length uint64
	}{
		{l.system, btrfsSystemBytes},
		{l.metadata, btrfsMetadataBytes},
		{l.data, btrfsDataBytes},
	} {
		item := leaf.add(1, btrfsDevExtentKey, chunk.start, btrfsDevExtentLen)
		binary.LittleEndian.PutUint64(item[0:8], btrfsChunkTreeID)
		binary.LittleEndian.PutUint64(item[8:16], btrfsFirstChunkID)
		binary.LittleEndian.PutUint64(item[16:24], chunk.start)
		binary.LittleEndian.PutUint64(item[24:32], chunk.length)
		copy(item[32:48], l.chunkUUID)
	}
	return leaf.finish()
}

// directoryTree builds a subvolume holding nothing but its own empty root
// directory, which is what both the default subvolume and the data relocation
// tree start out as.
func (l btrfsLayout) directoryTree(tree uint64) []byte {
	leaf := l.newLeaf(tree).at(l.trees[tree])
	l.writeInodeItem(leaf.add(btrfsFirstFreeID, btrfsInodeItemKey, 0, btrfsInodeItemLen))
	writeBtrfsInodeRef(leaf.add(btrfsFirstFreeID, btrfsInodeRefKey, btrfsFirstFreeID, 10+len("..")), "..")
	return leaf.finish()
}

func (l btrfsLayout) uuidTree() []byte {
	leaf := l.newLeaf(btrfsUUIDTreeID).at(l.trees[btrfsUUIDTreeID])
	// The subvolume UUID is split into the two halves of the key so that a
	// lookup by UUID is an ordinary btree search.
	item := leaf.add(binary.LittleEndian.Uint64(l.fsUUID[0:8]), btrfsUUIDSubvolKey,
		binary.LittleEndian.Uint64(l.fsUUID[8:16]), 8)
	binary.LittleEndian.PutUint64(item, btrfsFsTreeID)
	return leaf.finish()
}

func (l btrfsLayout) writeInodeItem(item []byte) {
	binary.LittleEndian.PutUint64(item[0:8], 1)               // generation
	binary.LittleEndian.PutUint64(item[24:32], btrfsNodeSize) // nbytes
	binary.LittleEndian.PutUint32(item[40:44], 1)             // nlink
	binary.LittleEndian.PutUint32(item[52:56], 0o40755)
	for _, offset := range []int{112, 124, 136, 148} { // access, change, modify, creation
		binary.LittleEndian.PutUint64(item[offset:offset+8], l.seconds)
	}
}

func writeBtrfsInodeRef(item []byte, name string) {
	//nolint:gosec // Names written here are fixed and short.
	binary.LittleEndian.PutUint16(item[8:10], uint16(len(name)))
	copy(item[10:], name)
}

func (l btrfsLayout) superblock(offset uint64) []byte {
	super := make([]byte, btrfsSuperSize)
	copy(super[32:48], l.fsid)
	binary.LittleEndian.PutUint64(super[48:56], offset)
	binary.LittleEndian.PutUint64(super[56:64], 1) // written
	copy(super[64:72], btrfsMagic)
	binary.LittleEndian.PutUint64(super[72:80], 1) // generation
	binary.LittleEndian.PutUint64(super[80:88], l.trees[btrfsRootTreeID])
	binary.LittleEndian.PutUint64(super[88:96], l.chunkRoot)
	binary.LittleEndian.PutUint64(super[112:120], l.total)
	binary.LittleEndian.PutUint64(super[120:128], l.bytesUsed())
	binary.LittleEndian.PutUint64(super[128:136], btrfsRootTreeDirID)
	binary.LittleEndian.PutUint64(super[136:144], 1) // one device
	binary.LittleEndian.PutUint32(super[144:148], btrfsSectorSize)
	binary.LittleEndian.PutUint32(super[148:152], btrfsNodeSize)
	binary.LittleEndian.PutUint32(super[152:156], btrfsNodeSize) // deprecated leafsize
	binary.LittleEndian.PutUint32(super[156:160], btrfsSectorSize)
	binary.LittleEndian.PutUint64(super[164:172], 1) // chunk_root_generation
	binary.LittleEndian.PutUint64(super[188:196], btrfsIncompatFlags)
	binary.LittleEndian.PutUint16(super[196:198], 0) // crc32c
	l.writeDevItem(super[201 : 201+btrfsDevItemLen])
	copy(super[299:299+btrfsLabelSize], l.label)
	binary.LittleEndian.PutUint64(super[555:563], ^uint64(0)) // no free space cache

	// The system chunk array bootstraps chunk lookup: it maps the chunk
	// tree's own address before any tree can be read.
	array := super[811:]
	binary.LittleEndian.PutUint64(array[0:8], btrfsFirstChunkID)
	array[8] = btrfsChunkItemKey
	binary.LittleEndian.PutUint64(array[9:17], l.system)
	l.writeChunkItem(array[17:17+btrfsChunkItemLen], l.system, btrfsSystemBytes, btrfsBlockGroupSystem)
	binary.LittleEndian.PutUint32(super[160:164], 17+btrfsChunkItemLen)

	btrfsSetChecksum(super)
	return super
}

// btrfsSetChecksum stores the CRC32C of everything after the checksum field
// itself, little-endian, in the first four bytes of the block.
func btrfsSetChecksum(block []byte) {
	sum := crc32.Checksum(block[btrfsCsumSize:], btrfsCastagnoli)
	binary.LittleEndian.PutUint32(block[0:4], sum)
}

// btrfsNameHash is the CRC32C of a name seeded with ~1, the value btrfs uses as
// the offset of a directory entry's key.
func btrfsNameHash(name string) uint32 {
	return ^crc32.Update(^uint32(0xfffffffe), btrfsCastagnoli, []byte(name))
}
