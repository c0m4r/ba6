// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

type miniEditor struct {
	lines                          [][]byte
	row, col, rowOffset, colOffset int
	rows, cols                     int
	filename, message              string
	dirty                          bool
}

func cmdNano(args []string) int {
	if len(args) > 1 {
		fatalf("nano", "too many operands")
		return 1
	}
	name := ""
	if len(args) == 1 {
		name = args[0]
	}
	editor := newMiniEditor(name)
	if err := editor.run(); err != nil {
		fatalf("nano", "%v", err)
		return 1
	}
	return 0
}
func newMiniEditor(name string) *miniEditor {
	e := &miniEditor{filename: name, message: "^S Save  ^X Exit  ^K Cut line", rows: 24, cols: 80}
	if name != "" {
		if data, err := os.ReadFile(name); err == nil {
			parts := bytes.Split(data, []byte{'\n'})
			if len(parts) > 1 && len(parts[len(parts)-1]) == 0 {
				parts = parts[:len(parts)-1]
			}
			e.lines = parts
		} else if !os.IsNotExist(err) {
			e.message = err.Error()
		}
	}
	if len(e.lines) == 0 {
		e.lines = [][]byte{{}}
	}
	return e
}
func (e *miniEditor) run() error {
	fd := os.Stdin.Fd()
	old, err := terminalRaw(fd)
	if err != nil {
		return fmt.Errorf("stdin is not a terminal: %w", err)
	}
	defer restoreTerminal(fd, old)
	if r, c, ok := terminalDimensions(fd); ok {
		e.rows, e.cols = r, c
	}
	defer fmt.Fprint(os.Stdout, "\x1b[2J\x1b[H")
	for {
		e.refresh()
		key, err := readEditorKey()
		if err != nil {
			return err
		}
		switch key {
		case 24, 17:
			if e.dirty {
				e.message = "Unsaved changes: ^S save, ^X again to discard"
				e.refresh()
				next, _ := readEditorKey()
				if next == 19 {
					if e.save() != nil {
						continue
					}
				} else if next != 24 && next != 17 {
					e.handleKey(next)
					continue
				}
			}
			return nil
		case 19:
			_ = e.save()
		case 11:
			e.cutLine()
		default:
			e.handleKey(key)
		}
	}
}
func (e *miniEditor) handleKey(key int) {
	switch key {
	case 1000:
		if e.row > 0 {
			e.row--
			if e.col > len(e.lines[e.row]) {
				e.col = len(e.lines[e.row])
			}
		}
	case 1001:
		if e.row+1 < len(e.lines) {
			e.row++
			if e.col > len(e.lines[e.row]) {
				e.col = len(e.lines[e.row])
			}
		}
	case 1002:
		if e.col > 0 {
			e.col--
		} else if e.row > 0 {
			e.row--
			e.col = len(e.lines[e.row])
		}
	case 1003:
		if e.col < len(e.lines[e.row]) {
			e.col++
		} else if e.row+1 < len(e.lines) {
			e.row++
			e.col = 0
		}
	case 1004:
		e.col = 0
	case 1005:
		e.col = len(e.lines[e.row])
	case 1006:
		e.row -= e.rows - 2
		if e.row < 0 {
			e.row = 0
		}
	case 1007:
		e.row += e.rows - 2
		if e.row >= len(e.lines) {
			e.row = len(e.lines) - 1
		}
	case 127, 8:
		e.backspace()
	case '\r', '\n':
		e.newline()
	default:
		if key >= 32 && key < 127 {
			line := e.lines[e.row]
			line = append(line, 0)
			copy(line[e.col+1:], line[e.col:])
			line[e.col] = byte(key)
			e.lines[e.row] = line
			e.col++
			e.dirty = true
		}
	}
	e.scroll()
}
func (e *miniEditor) backspace() {
	if e.col > 0 {
		line := e.lines[e.row]
		e.lines[e.row] = append(line[:e.col-1], line[e.col:]...)
		e.col--
		e.dirty = true
	} else if e.row > 0 {
		previous := len(e.lines[e.row-1])
		e.lines[e.row-1] = append(e.lines[e.row-1], e.lines[e.row]...)
		e.lines = append(e.lines[:e.row], e.lines[e.row+1:]...)
		e.row--
		e.col = previous
		e.dirty = true
	}
}
func (e *miniEditor) newline() {
	line := e.lines[e.row]
	left := append([]byte{}, line[:e.col]...)
	right := append([]byte{}, line[e.col:]...)
	e.lines[e.row] = left
	e.lines = append(e.lines, nil)
	copy(e.lines[e.row+2:], e.lines[e.row+1:])
	e.lines[e.row+1] = right
	e.row++
	e.col = 0
	e.dirty = true
}
func (e *miniEditor) cutLine() {
	if len(e.lines) == 1 {
		e.lines[0] = nil
		e.col = 0
	} else {
		e.lines = append(e.lines[:e.row], e.lines[e.row+1:]...)
		if e.row >= len(e.lines) {
			e.row = len(e.lines) - 1
		}
		if e.col > len(e.lines[e.row]) {
			e.col = len(e.lines[e.row])
		}
	}
	e.dirty = true
	e.scroll()
}
func (e *miniEditor) save() error {
	if e.filename == "" {
		e.message = "Cannot save: start nano with a filename"
		return fmt.Errorf("no filename")
	}
	data := bytes.Join(e.lines, []byte{'\n'})
	data = append(data, '\n')
	if err := os.WriteFile(e.filename, data, 0o666); err != nil { //nolint:gosec // G306: editor-created files honor the caller's umask.
		e.message = "Save failed: " + err.Error()
		return err
	}
	e.dirty = false
	e.message = fmt.Sprintf("Wrote %d lines", len(e.lines))
	return nil
}
func (e *miniEditor) scroll() {
	height := e.rows - 2
	if e.row < e.rowOffset {
		e.rowOffset = e.row
	}
	if e.row >= e.rowOffset+height {
		e.rowOffset = e.row - height + 1
	}
	if e.col < e.colOffset {
		e.colOffset = e.col
	}
	if e.col >= e.colOffset+e.cols {
		e.colOffset = e.col - e.cols + 1
	}
}
func (e *miniEditor) refresh() {
	var out strings.Builder
	// Disable autowrap while painting. Filling the last terminal column and then
	// writing CRLF can otherwise wrap twice and scroll the first editor rows off
	// the top of a narrow terminal.
	out.WriteString("\x1b[?25l\x1b[?7l")
	height := e.rows - 2
	for y := 0; y < height; y++ {
		fmt.Fprintf(&out, "\x1b[%d;1H", y+1)
		index := e.rowOffset + y
		if index < len(e.lines) {
			line := e.lines[index]
			start := e.colOffset
			if start > len(line) {
				start = len(line)
			}
			end := start + e.cols
			if end > len(line) {
				end = len(line)
			}
			for _, b := range line[start:end] {
				if b == '\t' {
					out.WriteString("    ")
				} else if b >= 32 {
					out.WriteByte(b)
				}
			}
		} else {
			out.WriteByte('~')
		}
		out.WriteString("\x1b[K")
	}
	title := " ba6 nano "
	if e.filename != "" {
		title += "- " + e.filename
	}
	if e.dirty {
		title += " [modified]"
	}
	if len(title) > e.cols {
		title = title[:e.cols]
	}
	fmt.Fprintf(&out, "\x1b[%d;1H", height+1)
	out.WriteString("\x1b[7m" + title + strings.Repeat(" ", maxInt(0, e.cols-len(title))) + "\x1b[m")
	message := e.message
	if len(message) > e.cols {
		message = message[:e.cols]
	}
	fmt.Fprintf(&out, "\x1b[%d;1H", height+2)
	out.WriteString(message + "\x1b[K")
	cursorRow := e.row - e.rowOffset + 1
	cursorCol := e.col - e.colOffset + 1
	fmt.Fprintf(&out, "\x1b[%d;%dH\x1b[?7h\x1b[?25h", cursorRow, cursorCol)
	fmt.Fprint(os.Stdout, out.String())
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func terminalRaw(fd uintptr) (*syscall.Termios, error) {
	old := new(syscall.Termios)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(old))); errno != 0 { //nolint:gosec // G103: fixed Termios buffer for TCGETS.
		return nil, errno
	}
	raw := *old
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Cflag |= syscall.CS8
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&raw))); errno != 0 { //nolint:gosec // G103: fixed Termios buffer for TCSETS.
		return nil, errno
	}
	return old, nil
}
func restoreTerminal(fd uintptr, old *syscall.Termios) {
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(old))) //nolint:gosec // G103: restoring the previously read Termios value.
}
func terminalDimensions(fd uintptr) (int, int, bool) {
	var ws winsize
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws))); errno != 0 { //nolint:gosec // G103: fixed winsize buffer for TIOCGWINSZ.
		return 0, 0, false
	}
	return int(ws.rows), int(ws.cols), ws.rows > 2 && ws.cols > 0
}
func readEditorKey() (int, error) {
	return readKeyFrom(os.Stdin)
}

// readKeyFrom reads one key press from a terminal in raw mode. Arrow, page and
// home/end keys arrive as escape sequences and are reported as codes above
// 1000. The source is a parameter because a pager reading its data from a pipe
// must take its keys from /dev/tty instead of standard input.
func readKeyFrom(input *os.File) (int, error) {
	one := []byte{0}
	if _, err := input.Read(one); err != nil {
		return 0, err
	}
	if one[0] != 27 {
		return int(one[0]), nil
	}
	seq := make([]byte, 1)
	if _, err := input.Read(seq); err != nil {
		return 27, nil
	}
	if seq[0] != '[' && seq[0] != 'O' {
		return 27, nil
	}
	if _, err := input.Read(seq); err != nil {
		return 27, nil
	}
	switch seq[0] {
	case 'A':
		return 1000, nil
	case 'B':
		return 1001, nil
	case 'D':
		return 1002, nil
	case 'C':
		return 1003, nil
	case 'H':
		return 1004, nil
	case 'F':
		return 1005, nil
	case '5', '6':
		number := seq[0]
		if _, err := input.Read(seq); err != nil {
			return 27, nil
		}
		if number == '5' {
			return 1006, nil
		}
		return 1007, nil
	}
	return 27, nil
}
