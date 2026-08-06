//go:build linux

package main

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// formatImage creates a sparse image of the requested size and formats it with
// the given applet, returning the resulting bytes.
func formatImage(t *testing.T, fn applet, name string, size int64, args ...string) []byte {
	t.Helper()
	image := filepath.Join(t.TempDir(), name)
	file, err := os.Create(image)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, fn, append(args, image), "")
	if status != 0 {
		t.Fatalf("%s status=%d stderr=%q", name, status, stderr)
	}
	data, err := os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestMkfsExt3WritesCleanJournal(t *testing.T) {
	const size = 32 * 1024 * 1024
	data := formatImage(t, cmdMkfsExt3, "root.ext3", size, "-F", "-L", "rescue")
	super := data[1024:2048]
	if binary.LittleEndian.Uint16(super[56:58]) != 0xef53 {
		t.Fatal("missing ext superblock magic")
	}
	if binary.LittleEndian.Uint32(super[92:96])&0x4 == 0 {
		t.Fatal("has_journal was not set")
	}
	if binary.LittleEndian.Uint32(super[96:100])&0x40 != 0 {
		t.Fatal("ext3 must not advertise extents")
	}
	if binary.LittleEndian.Uint32(super[224:228]) != 8 {
		t.Fatal("journal inode is not inode 8")
	}

	layout := extProfiles["ext3"].layout()
	journal := data[uint64(layout.journal)*ext2BlockSize:]
	if binary.BigEndian.Uint32(journal[0:4]) != extJournalMagic {
		t.Fatal("missing JBD2 superblock magic")
	}
	if got := binary.BigEndian.Uint32(journal[16:20]); got != extJournalBlocks {
		t.Fatalf("journal length %d, want %d", got, extJournalBlocks)
	}
	if binary.BigEndian.Uint32(journal[28:32]) != 0 {
		t.Fatal("journal is not empty")
	}

	// Inode 8 must map every journal block: twelve direct pointers and one
	// indirect block covering the rest.
	inode := data[uint64(layout.inodeTable)*ext2BlockSize+7*ext2InodeSize:]
	if got := binary.LittleEndian.Uint32(inode[4:8]); got != extJournalBlocks*ext2BlockSize {
		t.Fatalf("journal inode size %d, want %d", got, extJournalBlocks*ext2BlockSize)
	}
	for index := uint32(0); index < 12; index++ {
		if got := binary.LittleEndian.Uint32(inode[40+index*4 : 44+index*4]); got != layout.journal+index {
			t.Fatalf("direct block %d is %d, want %d", index, got, layout.journal+index)
		}
	}
	indirect := data[uint64(binary.LittleEndian.Uint32(inode[88:92]))*ext2BlockSize:]
	for index := uint32(12); index < extJournalBlocks; index++ {
		want := layout.journal + index
		if got := binary.LittleEndian.Uint32(indirect[(index-12)*4 : (index-12)*4+4]); got != want {
			t.Fatalf("indirect block %d is %d, want %d", index, got, want)
		}
	}

	image := filepath.Join(t.TempDir(), "check.ext3")
	if err := os.WriteFile(image, data, 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdFsck, []string{"-t", "ext3", image}, "")
	if status != 0 || !strings.Contains(stdout, ": clean") {
		t.Fatalf("fsck status=%d out=%q stderr=%q", status, stdout, stderr)
	}
}

func TestMkfsExt4UsesExtentsAndWideInodes(t *testing.T) {
	const size = 32 * 1024 * 1024
	data := formatImage(t, cmdMkfsExt4, "root.ext4", size, "-F", "-L", "rescue")
	super := data[1024:2048]
	if binary.LittleEndian.Uint32(super[96:100])&0x40 == 0 {
		t.Fatal("extent feature was not set")
	}
	if got := binary.LittleEndian.Uint16(super[88:90]); got != ext4InodeSize {
		t.Fatalf("inode size %d, want %d", got, ext4InodeSize)
	}
	if binary.LittleEndian.Uint16(super[348:350]) != extExtraIsize {
		t.Fatal("s_min_extra_isize was not set for wide inodes")
	}

	layout := extProfiles["ext4"].layout()
	root := data[uint64(layout.inodeTable)*ext2BlockSize+ext4InodeSize:]
	if binary.LittleEndian.Uint32(root[32:36])&extInodeExtentsFlg == 0 {
		t.Fatal("root inode is not extent mapped")
	}
	if binary.LittleEndian.Uint16(root[40:42]) != extExtentMagic {
		t.Fatal("root inode has no extent header")
	}
	if got := binary.LittleEndian.Uint32(root[60:64]); got != layout.root {
		t.Fatalf("root extent starts at %d, want %d", got, layout.root)
	}

	// The journal is one contiguous extent rather than a block map.
	journalInode := data[uint64(layout.inodeTable)*ext2BlockSize+7*ext4InodeSize:]
	if got := binary.LittleEndian.Uint16(journalInode[56:58]); got != extJournalBlocks {
		t.Fatalf("journal extent covers %d blocks, want %d", got, extJournalBlocks)
	}

	image := filepath.Join(t.TempDir(), "check.ext4")
	if err := os.WriteFile(image, data, 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, _ := captureApplet(t, cmdFsck, []string{"-t", "ext4", image}, "")
	if status != 0 || !strings.Contains(stdout, ": clean") {
		t.Fatalf("fsck status=%d out=%q", status, stdout)
	}
}

func TestMkfsExtJournalProfilesRejectTinyTargets(t *testing.T) {
	for _, name := range []string{"ext3", "ext4"} {
		image := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(image, make([]byte, 4*1024*1024), 0o600); err != nil {
			t.Fatal(err)
		}
		status, _, _ := captureApplet(t, cmdMkfs, []string{"-t", name, "-F", image}, "")
		if status == 0 {
			t.Fatalf("%s accepted a target too small for its journal", name)
		}
	}
}

// xfsChecksumValid recomputes a version 5 metadata checksum the way the kernel
// verifies it: over the whole block with the stored checksum read as zero.
func xfsChecksumValid(block []byte, offset int) bool {
	buffer := append([]byte(nil), block...)
	stored := binary.LittleEndian.Uint32(buffer[offset : offset+4])
	binary.LittleEndian.PutUint32(buffer[offset:offset+4], 0)
	return crc32.Checksum(buffer, crc32.MakeTable(crc32.Castagnoli)) == stored
}

func TestMkfsXfsWritesCheckedGroupsAndCleanLog(t *testing.T) {
	const size = 512 * 1024 * 1024
	data := formatImage(t, cmdMkfsXfs, "root.xfs", size, "-f", "-L", "rescue")
	super := data[:xfsSectorSize]
	if binary.BigEndian.Uint32(super[0:4]) != xfsSuperMagic {
		t.Fatal("missing XFS superblock magic")
	}
	if !xfsChecksumValid(super, xfsSbCrcOffset) {
		t.Fatal("superblock checksum does not verify")
	}
	agBlocks := binary.BigEndian.Uint32(super[0x54:0x58])
	if got := binary.BigEndian.Uint32(super[0x58:0x5c]); got != xfsAgCount {
		t.Fatalf("allocation group count %d, want %d", got, xfsAgCount)
	}
	if got := binary.BigEndian.Uint64(super[0x08:0x10]); got != uint64(agBlocks)*xfsAgCount {
		t.Fatalf("dblocks %d does not cover %d groups of %d", got, xfsAgCount, agBlocks)
	}
	if strings.TrimRight(string(super[0x6c:0x78]), "\x00") != "rescue" {
		t.Fatal("label was not written")
	}

	// Every group must carry checksummed headers and a free extent that
	// starts after the reserved blocks and runs to the end of the group.
	freeTotal := uint64(0)
	for group := uint32(0); group < xfsAgCount; group++ {
		base := uint64(group) * uint64(agBlocks) * xfsBlockSize
		for _, header := range []struct {
			name   string
			sector uint64
			magic  uint32
			crc    int
		}{
			{"AGF", 1, xfsAgfMagic, xfsAgfCrcOffset},
			{"AGI", 2, xfsAgiMagic, xfsAgiCrcOffset},
			{"AGFL", 3, xfsAgflMagic, xfsAgflCrcOffset},
		} {
			block := data[base+header.sector*xfsSectorSize:][:xfsSectorSize]
			if binary.BigEndian.Uint32(block[0:4]) != header.magic {
				t.Fatalf("group %d has no %s magic", group, header.name)
			}
			if !xfsChecksumValid(block, header.crc) {
				t.Fatalf("group %d %s checksum does not verify", group, header.name)
			}
		}
		agf := data[base+xfsSectorSize:][:xfsSectorSize]
		freeBlocks := binary.BigEndian.Uint32(agf[0x34:0x38])
		freeTotal += uint64(freeBlocks) + xfsAgflBlocks
		bnobt := data[base+xfsBlockSize:][:xfsBlockSize]
		if binary.BigEndian.Uint32(bnobt[0:4]) != xfsBnobtMagic {
			t.Fatalf("group %d has no free space btree root", group)
		}
		if !xfsChecksumValid(bnobt, xfsBtreeCrcOffset) {
			t.Fatalf("group %d free space btree checksum does not verify", group)
		}
		start := binary.BigEndian.Uint32(bnobt[xfsBtreeHeaderLen : xfsBtreeHeaderLen+4])
		length := binary.BigEndian.Uint32(bnobt[xfsBtreeHeaderLen+4 : xfsBtreeHeaderLen+8])
		if length != freeBlocks || start+length != agBlocks {
			t.Fatalf("group %d free extent [%d,%d] does not reach the end of %d blocks",
				group, start, length, agBlocks)
		}
	}
	if got := binary.BigEndian.Uint64(super[0x90:0x98]); got != freeTotal {
		t.Fatalf("superblock counts %d free blocks, groups hold %d", got, freeTotal)
	}

	// The root inode is an empty shortform directory that parents itself.
	// Its address comes from decoding the inode number the way XFS does:
	// group, then block within the group, then slot within the block.
	inopblog, agblklog := super[0x7b], super[0x7c]
	agInode := binary.BigEndian.Uint64(super[0x38:0x40]) & (1<<(agblklog+inopblog) - 1)
	rootOffset := (agInode>>inopblog)*xfsBlockSize + (agInode&(1<<inopblog-1))*xfsInodeSize
	root := data[rootOffset:][:xfsInodeSize]
	if binary.BigEndian.Uint16(root[0:2]) != xfsInodeMagic || root[0x05] != 1 {
		t.Fatal("root inode is not a local-format inode")
	}
	if !xfsChecksumValid(root, xfsInodeCrcOffset) {
		t.Fatal("root inode checksum does not verify")
	}
	if root[xfsInodeForkOff] != 0 || root[xfsInodeForkOff+1] != 0 {
		t.Fatal("root directory is not empty")
	}
	if got := binary.BigEndian.Uint32(root[xfsInodeForkOff+2 : xfsInodeForkOff+6]); got != xfsRootIno {
		t.Fatalf("root directory parent is inode %d, want %d", got, xfsRootIno)
	}

	// The log must open with the unmount record that marks it recovered.
	logStart := binary.BigEndian.Uint64(super[0x30:0x38])
	logAg := logStart >> super[0x7c]
	logBno := logStart & (1<<super[0x7c] - 1)
	record := data[(logAg*uint64(agBlocks)+logBno)*xfsBlockSize:]
	if binary.BigEndian.Uint32(record[0:4]) != xfsLogMagic {
		t.Fatal("log does not start with a record header")
	}
	if binary.BigEndian.Uint32(record[0x28:0x2c]) != 1 {
		t.Fatal("log record does not hold exactly one operation")
	}
	if record[xfsSectorSize+0x09] != 0x20 {
		t.Fatal("log record is not an unmount transaction")
	}
}

func TestMkfsXfsRejectsUndersizedTarget(t *testing.T) {
	image := filepath.Join(t.TempDir(), "small.xfs")
	file, err := os.Create(image)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(64 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, cmdMkfsXfs, []string{"-f", image}, "")
	if status == 0 || !strings.Contains(stderr, "at least") {
		t.Fatalf("undersized target accepted: status=%d stderr=%q", status, stderr)
	}
}

func TestMkfsBtrfsWritesChecksummedTrees(t *testing.T) {
	const size = 256 * 1024 * 1024
	data := formatImage(t, cmdMkfsBtrfs, "root.btrfs", size, "-f", "-L", "rescue")
	super := data[btrfsSuperOffset:][:btrfsSuperSize]
	if string(super[64:72]) != btrfsMagic {
		t.Fatal("missing btrfs superblock magic")
	}
	if got := crc32.Checksum(super[btrfsCsumSize:], btrfsCastagnoli); got != binary.LittleEndian.Uint32(super[0:4]) {
		t.Fatal("superblock checksum does not verify")
	}
	if got := binary.LittleEndian.Uint64(super[48:56]); got != btrfsSuperOffset {
		t.Fatalf("superblock records address %d, want %d", got, btrfsSuperOffset)
	}
	if strings.TrimRight(string(super[299:555]), "\x00") != "rescue" {
		t.Fatal("label was not written")
	}
	if got := binary.LittleEndian.Uint64(super[112:120]); got != size {
		t.Fatalf("total_bytes %d, want %d", got, size)
	}

	// The 64 MiB mirror fits in this image and must describe itself.
	mirror := data[btrfsSuperMirrors[1]:][:btrfsSuperSize]
	if string(mirror[64:72]) != btrfsMagic {
		t.Fatal("second superblock mirror was not written")
	}
	if got := binary.LittleEndian.Uint64(mirror[48:56]); got != btrfsSuperMirrors[1] {
		t.Fatalf("mirror records address %d, want %d", got, btrfsSuperMirrors[1])
	}

	// The chunk root and every tree the root tree points at must be a
	// checksummed leaf that knows its own address and owning tree.
	chunkRoot := binary.LittleEndian.Uint64(super[88:96])
	checkLeaf := func(bytenr, owner uint64) uint32 {
		t.Helper()
		leaf := data[bytenr:][:btrfsNodeSize]
		if got := crc32.Checksum(leaf[btrfsCsumSize:], btrfsCastagnoli); got != binary.LittleEndian.Uint32(leaf[0:4]) {
			t.Fatalf("leaf at %d has a bad checksum", bytenr)
		}
		if got := binary.LittleEndian.Uint64(leaf[48:56]); got != bytenr {
			t.Fatalf("leaf at %d records address %d", bytenr, got)
		}
		if got := binary.LittleEndian.Uint64(leaf[88:96]); got != owner {
			t.Fatalf("leaf at %d is owned by %d, want %d", bytenr, got, owner)
		}
		if leaf[100] != 0 {
			t.Fatalf("leaf at %d is not a leaf", bytenr)
		}
		return binary.LittleEndian.Uint32(leaf[96:100])
	}
	if items := checkLeaf(chunkRoot, btrfsChunkTreeID); items != 4 {
		t.Fatalf("chunk tree holds %d items, want one device and three chunks", items)
	}
	rootTree := binary.LittleEndian.Uint64(super[80:88])
	if items := checkLeaf(rootTree, btrfsRootTreeID); items != 10 {
		t.Fatalf("root tree holds %d items, want 10", items)
	}
	for _, tree := range btrfsMetadataTrees {
		if tree == btrfsRootTreeID {
			continue
		}
		checkLeaf(btrfsRootItemFor(t, data, rootTree, tree), tree)
	}

	// The system chunk array must map the chunk root's own address.
	arraySize := binary.LittleEndian.Uint32(super[160:164])
	if arraySize != 17+btrfsChunkItemLen {
		t.Fatalf("system chunk array is %d bytes, want %d", arraySize, 17+btrfsChunkItemLen)
	}
	logical := binary.LittleEndian.Uint64(super[811+9 : 811+17])
	length := binary.LittleEndian.Uint64(super[811+17 : 811+25])
	if chunkRoot < logical || chunkRoot >= logical+length {
		t.Fatalf("chunk root %d falls outside the bootstrap chunk [%d,%d)", chunkRoot, logical, logical+length)
	}
}

// btrfsRootItemFor finds the root item for one tree in the root tree leaf and
// returns the address of that tree's root block.
func btrfsRootItemFor(t *testing.T, data []byte, rootTree, tree uint64) uint64 {
	t.Helper()
	leaf := data[rootTree:][:btrfsNodeSize]
	items := binary.LittleEndian.Uint32(leaf[96:100])
	for index := uint32(0); index < items; index++ {
		slot := btrfsHeaderSize + int(index)*btrfsItemSize
		if binary.LittleEndian.Uint64(leaf[slot:slot+8]) != tree || leaf[slot+8] != btrfsRootItemKey {
			continue
		}
		payload := btrfsHeaderSize + int(binary.LittleEndian.Uint32(leaf[slot+17:slot+21]))
		return binary.LittleEndian.Uint64(leaf[payload+176 : payload+184])
	}
	t.Fatalf("root tree has no root item for tree %d", tree)
	return 0
}

func TestMkfsBtrfsRejectsUndersizedTarget(t *testing.T) {
	image := filepath.Join(t.TempDir(), "small.btrfs")
	if err := os.WriteFile(image, make([]byte, 64*1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, cmdMkfsBtrfs, []string{"-f", image}, "")
	if status == 0 || !strings.Contains(stderr, "at least") {
		t.Fatalf("undersized target accepted: status=%d stderr=%q", status, stderr)
	}
}

func TestMkfsDispatchesEveryBundledType(t *testing.T) {
	for _, name := range []string{"ext2", "ext3", "ext4", "xfs", "btrfs"} {
		image := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(image, []byte("do not overwrite"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Without -F the formatter must refuse a regular file, which
		// proves dispatch reached it without touching the target.
		status, _, stderr := captureApplet(t, cmdMkfs, []string{"-t", name, image}, "")
		if status == 0 || !strings.Contains(stderr, "mkfs."+name) {
			t.Fatalf("mkfs -t %s status=%d stderr=%q", name, status, stderr)
		}
		got, err := os.ReadFile(image)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "do not overwrite" {
			t.Fatalf("mkfs -t %s modified a target it refused", name)
		}
	}
	status, _, stderr := captureApplet(t, cmdMkfs, []string{"-t", "reiserfs", "/dev/null"}, "")
	if status == 0 || !strings.Contains(stderr, "unsupported filesystem type") {
		t.Fatalf("unknown type accepted: status=%d stderr=%q", status, stderr)
	}
}
