// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// ficlone is FICLONE, the ioctl that asks a filesystem to share the source
// file's extents with the destination rather than copying them.
const ficlone = 0x40049409

// derefMode says which symbolic links cp follows: none of them (-P, and the
// default under -r), the ones named on the command line (-H), or all of them
// (-L, and the default for a non-recursive copy).
type derefMode int

const (
	derefNever derefMode = iota
	derefCommandLine
	derefAlways
)

// fileKey identifies one inode, so --preserve=links can recreate a hard link
// inside the tree rather than copying its contents twice.
type fileKey struct {
	dev uint64
	ino uint64
}

type copier struct {
	recursive      bool
	force          bool
	verbose        bool
	interactive    bool
	removeDest     bool
	noClobber      bool
	update         bool
	hardLink       bool // -l
	symbolicLink   bool // -s
	oneFileSystem  bool // -x
	parents        bool
	attributesOnly bool
	deref          derefMode
	derefSet       bool
	backup         backupMethod
	suffix         string
	preserveMode   bool
	preserveTimes  bool
	preserveOwner  bool
	preserveLinks  bool
	reflink        string
	input          *bufio.Reader
	// rootDev is the device the current operand lives on, which -x refuses to
	// leave; links maps an already-copied inode to the copy it produced.
	rootDev uint64
	links   map[fileKey]string
	status  int
	// operandPath and operandName differ only under --strip-trailing-slashes,
	// where cp works on the shortened path but still names the operand the way
	// it was written in its diagnostics.
	operandPath string
	operandName string
}

// fail reports one entry's failure and remembers it for the exit status. A
// declined prompt has already said all there is to say.
func (c *copier) fail(err error) {
	if !errors.Is(err, errCopyDeclined) {
		fatalf("cp", "%s", errText(err))
	}
	c.status = 1
}

// readDirByInode lists a directory in increasing inode order, which is the
// order the original's directory walk sorts entries into.
func readDirByInode(path string) ([]string, error) {
	names, err := readDirRaw(path)
	if err != nil {
		return nil, err
	}
	inodes := make(map[string]uint64, len(names))
	for _, name := range names {
		if info, statErr := os.Lstat(filepath.Join(path, name)); statErr == nil {
			if status, ok := info.Sys().(*syscall.Stat_t); ok {
				inodes[name] = status.Ino
			}
		}
	}
	sort.SliceStable(names, func(i, j int) bool { return inodes[names[i]] < inodes[names[j]] })
	return names, nil
}

// name is what a diagnostic calls this path.
func (c *copier) name(path string) string {
	if c.operandName != "" && path == c.operandPath {
		return c.operandName
	}
	return path
}

// preserve is the old spelling of --preserve=mode,ownership,timestamps, kept
// because the cross-device fallback in mv sets it.
func (c *copier) setPreserveAll(on bool) {
	c.preserveMode, c.preserveTimes, c.preserveOwner, c.preserveLinks = on, on, on, on
}

func cpUsage(format string, a ...interface{}) int {
	fatalf("cp", format, a...)
	fmt.Fprintln(os.Stderr, "Try 'cp --help' for more information.")
	return 1
}

// cpInvalidArgument reports an unusable value for one of the options that take
// a fixed word, listing what would have been accepted, as the original does.
func cpInvalidArgument(option, value string, valid []string) int {
	fatalf("cp", "invalid argument %s for %s", quoteLocaleName(value), quoteLocaleName(option))
	fmt.Fprintln(os.Stderr, "Valid arguments are:")
	for _, name := range valid {
		fmt.Fprintf(os.Stderr, "  - %s\n", quoteLocaleName(name))
	}
	fmt.Fprintln(os.Stderr, "Try 'cp --help' for more information.")
	return 1
}

// errCopyDeclined marks a copy the caller refused at the -i prompt: nothing is
// printed for it, but cp still exits 1.
var errCopyDeclined = errors.New("declined")

var cpPreserveAttributes = []string{"mode", "timestamps", "ownership", "links", "context", "xattr", "all"}

// setPreserveAttribute applies one name from a --preserve or --no-preserve
// list.
func (c *copier) setPreserveAttribute(name string, on bool) bool {
	switch name {
	case "mode":
		c.preserveMode = on
	case "ownership":
		c.preserveOwner = on
	case "timestamps":
		c.preserveTimes = on
	case "links":
		c.preserveLinks = on
	case "context", "xattr":
		// No SELinux policy and no extended attributes are carried here.
	case "all":
		c.setPreserveAll(on)
	default:
		return false
	}
	return true
}

// cmdCp implements cp(1): copy files, directories and symbolic links.
//
//nolint:gocyclo // one option table; splitting it would only scatter the command line.
func cmdCp(args []string) int {
	c := &copier{suffix: defaultBackupSuffix(), input: bufio.NewReader(os.Stdin), links: map[fileKey]string{}}
	var operands []string
	targetDir, haveTargetDir, noTargetDir, stripSlashes := "", false, false, false
	parsing := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !parsing || arg == "-" || !strings.HasPrefix(arg, "-") {
			operands = append(operands, arg)
			continue
		}
		if arg == "--" {
			parsing = false
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, value, hasValue := arg, "", false
			if eq := strings.IndexByte(arg, '='); eq >= 0 {
				name, value, hasValue = arg[:eq], arg[eq+1:], true
			}
			needValue := func() (string, bool) {
				if hasValue {
					return value, true
				}
				i++
				if i >= len(args) {
					cpUsage("option '%s' requires an argument", name)
					return "", false
				}
				return args[i], true
			}
			switch name {
			case "--recursive":
				c.recursive = true
			case "--archive":
				c.recursive, c.deref, c.derefSet = true, derefNever, true
				c.setPreserveAll(true)
			case "--force":
				c.force, c.interactive, c.noClobber = true, false, false
			case "--interactive":
				c.interactive, c.force, c.noClobber = true, false, false
			case "--no-clobber":
				c.noClobber, c.force, c.interactive = true, false, false
			case "--update":
				c.update = true
			case "--verbose":
				c.verbose = true
			case "--remove-destination":
				c.removeDest = true
			case "--link":
				c.hardLink = true
			case "--symbolic-link":
				c.symbolicLink = true
			case "--dereference":
				c.deref, c.derefSet = derefAlways, true
			case "--no-dereference":
				c.deref, c.derefSet = derefNever, true
			case "--one-file-system":
				c.oneFileSystem = true
			case "--parents":
				c.parents = true
			case "--strip-trailing-slashes":
				stripSlashes = true
			case "--attributes-only":
				c.attributesOnly = true
			case "--context":
				// SELinux labelling; nothing to do without a policy.
			case "--sparse":
				text, ok := needValue()
				if !ok {
					return 1
				}
				// Holes are neither detected nor created; the copy is faithful
				// in content either way.
				if text != "never" && text != "auto" && text != "always" {
					return cpInvalidArgument(name, text, []string{"never", "auto", "always"})
				}
			case "--reflink":
				c.reflink = "always"
				if hasValue {
					c.reflink = value
				}
				if c.reflink != "auto" && c.reflink != "always" && c.reflink != "never" {
					return cpInvalidArgument(name, c.reflink, []string{"auto", "always", "never"})
				}
			case "--no-target-directory":
				noTargetDir = true
			case "--target-directory":
				text, ok := needValue()
				if !ok {
					return 1
				}
				targetDir, haveTargetDir = text, true
			case "--suffix":
				text, ok := needValue()
				if !ok {
					return 1
				}
				c.suffix = text
				if c.backup == backupNone {
					c.backup = backupExisting
				}
			case "--backup":
				// An empty "--backup=" reads as a bare --backup.
				if !hasValue || value == "" {
					method, ok := defaultBackupMethod("cp")
					if !ok {
						return 1
					}
					c.backup = method
					continue
				}
				method, ok := parseBackupControl(value)
				if !ok {
					return backupUsageError("cp", "backup type", value)
				}
				c.backup = method
			case "--preserve", "--no-preserve":
				on := name == "--preserve"
				if !hasValue {
					if !on {
						cpUsage("option '--no-preserve' requires an argument")
						return 1
					}
					c.preserveMode, c.preserveTimes, c.preserveOwner = true, true, true
					continue
				}
				for _, attribute := range strings.Split(value, ",") {
					if !c.setPreserveAttribute(attribute, on) {
						return cpInvalidArgument(name, attribute, cpPreserveAttributes)
					}
				}
			default:
				return cpUsage("unrecognized option '%s'", arg)
			}
			continue
		}
		cluster := arg[1:]
		for len(cluster) > 0 {
			flag := cluster[0]
			cluster = cluster[1:]
			switch flag {
			case 'r', 'R':
				c.recursive = true
			case 'a':
				c.recursive, c.deref, c.derefSet = true, derefNever, true
				c.setPreserveAll(true)
			case 'f':
				c.force, c.interactive, c.noClobber = true, false, false
			case 'i':
				c.interactive, c.force, c.noClobber = true, false, false
			case 'n':
				c.noClobber, c.force, c.interactive = true, false, false
			case 'u':
				c.update = true
			case 'p':
				c.preserveMode, c.preserveTimes, c.preserveOwner = true, true, true
			case 'v':
				c.verbose = true
			case 'l':
				c.hardLink = true
			case 's':
				c.symbolicLink = true
			case 'd':
				// -d is --no-dereference --preserve=links.
				c.deref, c.derefSet, c.preserveLinks = derefNever, true, true
			case 'P':
				c.deref, c.derefSet = derefNever, true
			case 'L':
				c.deref, c.derefSet = derefAlways, true
			case 'H':
				c.deref, c.derefSet = derefCommandLine, true
			case 'x':
				c.oneFileSystem = true
			case 'T':
				noTargetDir = true
			case 'Z', 'c':
				// SELinux labelling; nothing to do without a policy.
			case 'b':
				method, ok := defaultBackupMethod("cp")
				if !ok {
					return 1
				}
				c.backup = method
			case 'S', 't':
				value := cluster
				cluster = ""
				if value == "" {
					i++
					if i >= len(args) {
						return cpUsage("option requires an argument -- '%c'", flag)
					}
					value = args[i]
				}
				if flag == 'S' {
					c.suffix = value
					if c.backup == backupNone {
						c.backup = backupExisting
					}
					continue
				}
				targetDir, haveTargetDir = value, true
			default:
				return cpUsage("invalid option -- '%c'", flag)
			}
		}
	}
	// A recursive copy leaves symbolic links alone unless told otherwise; a
	// plain one follows them, and so does -l, which links what a symbolic
	// source points at rather than the link.
	if !c.derefSet {
		c.deref = derefAlways
		if c.recursive && !c.hardLink {
			c.deref = derefNever
		}
	}

	sources, destination := operands, ""
	destinationIsDir := true
	switch {
	case haveTargetDir:
		destination = targetDir
		if len(operands) == 0 {
			return cpUsage("missing file operand")
		}
		info, err := os.Stat(destination)
		if err == nil && !info.IsDir() {
			err = syscall.ENOTDIR
		}
		if err != nil {
			fatalf("cp", "target directory '%s': %s", destination, errText(err))
			return 1
		}
	case len(operands) == 0:
		return cpUsage("missing file operand")
	case len(operands) == 1:
		return cpUsage("missing destination file operand after '%s'", operands[0])
	default:
		sources, destination = operands[:len(operands)-1], operands[len(operands)-1]
		info, err := os.Stat(destination)
		destinationIsDir = !noTargetDir && err == nil && info.IsDir()
		if (len(sources) > 1 || c.parents) && !destinationIsDir {
			if err == nil {
				err = syscall.ENOTDIR
			}
			fatalf("cp", "target '%s': %s", destination, errText(err))
			return 1
		}
	}

	status := 0
	for _, source := range sources {
		c.operandPath, c.operandName = "", ""
		if stripSlashes {
			if trimmed := strings.TrimRight(source, "/"); trimmed != "" && trimmed != source {
				c.operandPath, c.operandName = trimmed, source
				source = trimmed
			}
		}
		target := destination
		switch {
		case c.parents:
			// --parents rebuilds the source's own path under the destination.
			relative := filepath.Clean(source)
			if filepath.IsAbs(relative) {
				relative = strings.TrimPrefix(relative, string(os.PathSeparator))
			}
			target = filepath.Join(destination, relative)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:gosec // G301: --parents rebuilds directories that must stay traversable, as the original leaves them.
				fatalf("cp", "cannot make directory '%s': %s", filepath.Dir(target), errText(err))
				status = 1
				continue
			}
		case destinationIsDir:
			// cp appends the operand's own last component, so "cp -r . dir"
			// names the destination "dir/." the way the original does.
			target = strings.TrimRight(destination, "/") + "/" + filepath.Base(source)
		}
		c.rootDev = 0
		if err := c.copyPath(source, target, true); err != nil {
			// A declined prompt is a refusal, not a fault: cp says nothing
			// beyond the prompt but still fails.
			c.fail(err)
		}
	}
	if c.status != 0 {
		status = c.status
	}
	return status
}

// copyPath copies one path. commandLine says whether this operand was named on
// the command line, which is what -H keys off.
func (c *copier) copyPath(src, dst string, commandLine bool) error {
	info, err := os.Lstat(src)
	if err != nil {
		// The caller prints this verbatim, so the sentence is built where the
		// operation and the operand are both known.
		return fmt.Errorf("cannot stat '%s': %s", c.name(src), errText(err))
	}
	if info.Mode()&os.ModeSymlink != 0 &&
		(c.deref == derefAlways || (c.deref == derefCommandLine && commandLine)) {
		followed, followErr := os.Stat(src)
		if followErr != nil {
			return fmt.Errorf("cannot stat '%s': %s", src, errText(followErr))
		}
		info = followed
	}
	if c.oneFileSystem {
		if status, ok := info.Sys().(*syscall.Stat_t); ok {
			if c.rootDev == 0 {
				c.rootDev = status.Dev
			} else if status.Dev != c.rootDev {
				// -x stops at the mount point rather than descending into it.
				return nil
			}
		}
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return c.copySymlink(src, dst)
	case info.IsDir():
		if !c.recursive {
			return fmt.Errorf("-r not specified; omitting directory '%s'", c.name(src))
		}
		if err := rejectCopyIntoSelf(src, dst); err != nil {
			return err
		}
		return c.copyDir(src, dst, info)
	default:
		return c.copyFile(src, dst, info)
	}
}

// prepareDestination applies the rules that decide whether an existing
// destination may be replaced: -n, -u, -i, the backup, and --remove-destination.
// It reports the backup's name, and whether the copy should go ahead at all.
func (c *copier) prepareDestination(src, dst string, info os.FileInfo) (backup string, proceed bool, err error) {
	dstInfo, statErr := os.Lstat(dst)
	if statErr != nil {
		return "", true, nil
	}
	if os.SameFile(info, dstInfo) {
		return "", false, fmt.Errorf("'%s' and '%s' are the same file", src, dst)
	}
	switch {
	case c.noClobber:
		return "", false, nil
	case c.update && !info.ModTime().After(dstInfo.ModTime()):
		return "", false, nil
	case c.interactive:
		confirmed, confirmErr := confirm(c.input, fmt.Sprintf("cp: overwrite '%s'? ", dst))
		if confirmErr != nil {
			return "", false, confirmErr
		}
		if !confirmed {
			return "", false, errCopyDeclined
		}
	}
	backup, err = makeBackup(dst, c.backup, c.suffix)
	if err != nil {
		return "", false, err
	}
	if backup == "" && c.removeDest {
		// Unlike -f's fallback, this happens before the destination is
		// opened. It creates a new inode even when the old one is writable,
		// so copying over a mapped shared library cannot truncate its live
		// mapping in place.
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return "", false, err
		}
	}
	return backup, true, nil
}

func (c *copier) announce(src, dst, backup string) error {
	if !c.verbose {
		return nil
	}
	suffix := ""
	if backup != "" {
		suffix = fmt.Sprintf(" (backup: '%s')", backup)
	}
	_, err := fmt.Fprintf(os.Stdout, "'%s' -> '%s'%s\n", src, dst, suffix)
	return err
}

func (c *copier) copyDir(src, dst string, info os.FileInfo) error {
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	// cp announces a directory on the way in, before its contents.
	if err := c.announce(src, dst, ""); err != nil {
		return err
	}
	// The entries are walked in inode order, which is the order cp's own
	// directory walk uses and therefore the order -v announces them in.
	names, err := readDirByInode(src)
	if err != nil {
		return err
	}
	for _, name := range names {
		// A failure inside a directory is reported where it happens and the
		// walk carries on, as the original's does.
		if err := c.copyPath(filepath.Join(src, name), filepath.Join(dst, name), false); err != nil {
			c.fail(err)
		}
	}
	return c.applyAttributes(dst, info)
}

// applyAttributes copies over whatever --preserve asked for. A chown that the
// caller has no privilege for is not an error: the original tolerates it too.
func (c *copier) applyAttributes(dst string, info os.FileInfo) error {
	if c.preserveOwner {
		if status, ok := info.Sys().(*syscall.Stat_t); ok {
			if err := os.Lchown(dst, int(status.Uid), int(status.Gid)); err != nil &&
				!errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EINVAL) {
				return fmt.Errorf("failed to preserve ownership for '%s': %s", dst, errText(err))
			}
		}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// The mode and times of a symbolic link belong to the link itself and
		// cannot be set through these calls.
		return nil
	}
	if c.preserveMode {
		if err := os.Chmod(dst, info.Mode().Perm()); err != nil {
			return err
		}
	}
	if c.preserveTimes {
		if err := os.Chtimes(dst, atimeOf(info), info.ModTime()); err != nil {
			return err
		}
	}
	return nil
}

func (c *copier) copyFile(src, dst string, info os.FileInfo) (retErr error) {
	backup, proceed, err := c.prepareDestination(src, dst, info)
	if err != nil || !proceed {
		return err
	}
	// --preserve=links recreates a hard link inside the tree instead of
	// copying the same inode twice.
	if c.preserveLinks && !c.hardLink && !c.symbolicLink {
		if status, ok := info.Sys().(*syscall.Stat_t); ok && status.Nlink > 1 {
			key := fileKey{dev: status.Dev, ino: status.Ino}
			if first, seen := c.links[key]; seen {
				if err := c.replaceWithLink(first, dst, false); err != nil {
					return err
				}
				return c.announce(src, dst, backup)
			}
			c.links[key] = dst
		}
	}
	if c.hardLink || c.symbolicLink {
		return c.linkInsteadOfCopy(src, dst, backup)
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := in.Close(); retErr == nil && closeErr != nil {
			retErr = closeErr
		}
	}()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil && c.force {
		if removeErr := os.Remove(dst); removeErr == nil {
			out, err = os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
		}
	}
	if err != nil {
		return err
	}
	if !c.attributesOnly {
		if err := c.copyContents(in, out); err != nil {
			out.Close()
			return err
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := c.applyAttributes(dst, info); err != nil {
		return err
	}
	return c.announce(src, dst, backup)
}

// copyContents moves the bytes across, asking the filesystem for a reflink
// first when --reflink was given. FICLONE is refused on filesystems that
// cannot share extents, which is why "auto" falls back to a plain copy.
func (c *copier) copyContents(in, out *os.File) error {
	if c.reflink != "" && c.reflink != "never" {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, out.Fd(), ficlone, in.Fd())
		if errno == 0 {
			return nil
		}
		if c.reflink == "always" {
			return fmt.Errorf("failed to clone '%s' from '%s': %s", out.Name(), in.Name(), errText(errno))
		}
	}
	_, err := io.Copy(out, in)
	return err
}

// linkInsteadOfCopy is what -l and -s do in place of copying the bytes.
func (c *copier) linkInsteadOfCopy(src, dst, backup string) error {
	if c.symbolicLink {
		// A relative source only makes sense when the link lands in the
		// directory the paths are relative to.
		if !filepath.IsAbs(src) && filepath.Dir(dst) != "." {
			return fmt.Errorf("%s: can make relative symbolic links only in current directory", dst)
		}
		if err := c.replaceWithLink(src, dst, true); err != nil {
			return err
		}
		return c.announce(src, dst, backup)
	}
	// The hard link is made to whatever the source resolves to when symbolic
	// links are being followed, and to the link itself when they are not.
	target := src
	if c.deref != derefNever {
		resolved, resolveErr := filepath.EvalSymlinks(src)
		if resolveErr != nil {
			return resolveErr
		}
		target = resolved
	}
	if err := c.replaceWithLink(target, dst, false); err != nil {
		return err
	}
	return c.announce(src, dst, backup)
}

// replaceWithLink puts a hard or symbolic link at dst, clearing whatever the
// backup step left behind.
func (c *copier) replaceWithLink(src, dst string, symbolic bool) error {
	if _, err := os.Lstat(dst); err == nil {
		if err := os.Remove(dst); err != nil {
			return err
		}
	}
	if symbolic {
		return os.Symlink(src, dst)
	}
	return os.Link(src, dst)
}

func (c *copier) copySymlink(src, dst string) error {
	target, err := os.Readlink(src)
	if err != nil {
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	backup, proceed, err := c.prepareDestination(src, dst, info)
	if err != nil || !proceed {
		return err
	}
	// -l and -s link to the symbolic link's own path rather than copying it.
	if c.hardLink || c.symbolicLink {
		return c.linkInsteadOfCopy(src, dst, backup)
	}
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Symlink(target, dst); err != nil {
		return err
	}
	if err := c.applyAttributes(dst, info); err != nil {
		return err
	}
	return c.announce(src, dst, backup)
}

func rejectCopyIntoSelf(src, dst string) error {
	srcPath, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	srcPath, err = filepath.Abs(srcPath)
	if err != nil {
		return err
	}
	dstPath, err := resolveProspectivePath(dst)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(srcPath, dstPath)
	if err != nil {
		return err
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
		return fmt.Errorf("cannot copy a directory, '%s', into itself, '%s'", src, dst)
	}
	return nil
}

func resolveProspectivePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	cur := abs
	var suffix []string
	for {
		resolved, resolveErr := filepath.EvalSymlinks(cur)
		if resolveErr == nil {
			parts := append([]string{resolved}, suffix...)
			return filepath.Join(parts...), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", resolveErr
		}
		suffix = append([]string{filepath.Base(cur)}, suffix...)
		cur = parent
	}
}
