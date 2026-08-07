// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// cmdCp implements cp(1): copy files and directories. Flags: -r/-R (recursive),
// -f (force/overwrite), -i (prompt), -p (preserve mode & times), -v (verbose).
// Multiple sources require the destination to be a directory.
func cmdCp(args []string) int {
	var (
		recursive   bool
		force       bool
		preserve    bool
		verbose     bool
		interactive bool
		srcs        []string
	)

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case a == "--recursive":
			recursive = true
		case a == "--force":
			force = true
		case a == "--preserve":
			preserve = true
		case a == "--verbose":
			verbose = true
		case a == "--interactive":
			interactive, force = true, false
		case len(a) > 1 && a[0] == '-':
			for _, c := range a[1:] {
				switch c {
				case 'r', 'R', 'a':
					recursive = true
					if c == 'a' {
						preserve = true
					}
				case 'f':
					force, interactive = true, false
				case 'p':
					preserve = true
				case 'v':
					verbose = true
				case 'i':
					interactive, force = true, false
				default:
					fatalf("cp", "invalid option -- '%c'", c)
					return 1
				}
			}
		default:
			srcs = append(srcs, a)
		}
	}
rest:
	srcs = append(srcs, args[i:]...)
	if len(srcs) < 2 {
		fatalf("cp", "missing destination file operand")
		return 1
	}

	dst := srcs[len(srcs)-1]
	srcs = srcs[:len(srcs)-1]

	c := &copier{
		recursive: recursive, force: force, preserve: preserve, verbose: verbose,
		interactive: interactive, input: bufio.NewReader(os.Stdin),
	}

	dstInfo, dstErr := os.Stat(dst)
	dstIsDir := dstErr == nil && dstInfo.IsDir()

	if len(srcs) > 1 && !dstIsDir {
		fatalf("cp", "target '%s' is not a directory", dst)
		return 1
	}

	status := 0
	for _, src := range srcs {
		target := dst
		if dstIsDir {
			target = filepath.Join(dst, filepath.Base(src))
		}
		if err := c.copyPath(src, target); err != nil {
			fatalf("cp", "%v", err)
			status = 1
		}
	}
	return status
}

type copier struct {
	recursive   bool
	force       bool
	preserve    bool
	verbose     bool
	interactive bool
	input       *bufio.Reader
}

func (c *copier) copyPath(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}

	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return c.copySymlink(src, dst)
	case info.IsDir():
		if !c.recursive {
			return fmt.Errorf("-r not specified; omitting directory '%s'", src)
		}
		if err := rejectCopyIntoSelf(src, dst); err != nil {
			return err
		}
		return c.copyDir(src, dst, info)
	default:
		return c.copyFile(src, dst, info)
	}
}

func (c *copier) copyDir(src, dst string, info os.FileInfo) error {
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := c.copyPath(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	if c.preserve {
		if err := os.Chmod(dst, info.Mode().Perm()); err != nil {
			return err
		}
		if err := os.Chtimes(dst, atimeOf(info), info.ModTime()); err != nil {
			return err
		}
	}
	if c.verbose {
		if _, err := fmt.Fprintf(os.Stdout, "'%s' -> '%s'\n", src, dst); err != nil {
			return err
		}
	}
	return nil
}

func (c *copier) copyFile(src, dst string, info os.FileInfo) (retErr error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := in.Close(); retErr == nil && closeErr != nil {
			retErr = closeErr
		}
	}()

	if dstInfo, statErr := os.Stat(dst); statErr == nil {
		if os.SameFile(info, dstInfo) {
			return fmt.Errorf("'%s' and '%s' are the same file", src, dst)
		}
		if c.interactive {
			confirmed, confirmErr := confirm(c.input, fmt.Sprintf("cp: overwrite '%s'? ", dst))
			if confirmErr != nil {
				return confirmErr
			}
			if !confirmed {
				return nil
			}
		}
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil && c.force {
		if removeErr := os.Remove(dst); removeErr == nil {
			out, err = os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
		}
	}
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}

	if c.preserve {
		if err := os.Chmod(dst, info.Mode().Perm()); err != nil {
			return err
		}
		if err := os.Chtimes(dst, atimeOf(info), info.ModTime()); err != nil {
			return err
		}
	}
	if c.verbose {
		if _, err := fmt.Fprintf(os.Stdout, "'%s' -> '%s'\n", src, dst); err != nil {
			return err
		}
	}
	return nil
}

func (c *copier) copySymlink(src, dst string) error {
	target, err := os.Readlink(src)
	if err != nil {
		return err
	}
	if srcInfo, srcErr := os.Lstat(src); srcErr == nil {
		if dstInfo, dstErr := os.Lstat(dst); dstErr == nil {
			if os.SameFile(srcInfo, dstInfo) {
				return fmt.Errorf("'%s' and '%s' are the same file", src, dst)
			}
			if c.interactive {
				confirmed, confirmErr := confirm(c.input, fmt.Sprintf("cp: overwrite '%s'? ", dst))
				if confirmErr != nil {
					return confirmErr
				}
				if !confirmed {
					return nil
				}
			}
		}
	}
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Symlink(target, dst); err != nil {
		return err
	}
	if c.verbose {
		if _, err := fmt.Fprintf(os.Stdout, "'%s' -> '%s'\n", src, dst); err != nil {
			return err
		}
	}
	return nil
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
		return fmt.Errorf("cannot copy directory '%s' into itself, '%s'", src, dst)
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
