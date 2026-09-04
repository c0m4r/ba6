// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestGrepPatternAfterDoubleDash(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdGrep, []string{"--", "needle"}, "a needle here\n")
	if status != 0 || stdout != "a needle here\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestGrepMaxCountZeroSelectsNothing(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdGrep, []string{"-m0", "needle"}, "needle\n")
	if status != 1 || stdout != "" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

// TestAwkBashCompletionPrograms covers the programs the bash completions run,
// which need brace-delimited blocks, if/else, sub() and comments. The expected
// output is what gawk produces for the same input.
func TestAwkBashCompletionPrograms(t *testing.T) {
	cases := []struct {
		name, program, input, output string
	}{
		{
			name:    "available interfaces",
			program: `/^[^ \t]/ { if ($1 ~ /^[0-9]+:/) { sub(/@.*/, "", $2); print $2 } else { print $1 } }`,
			input:   "1: lo: <UP> mtu 65536\n2: veth0@if7: <UP> mtu 1500\n    link/ether 00:00:00:00:00:00\n",
			output:  "lo:\nveth0\n",
		},
		{
			name:    "systemd units",
			program: "# leading comment\nsub(/\\.service$/, \"\", $1) { print $1; next }\nsub(/\\.service$/, \"\", $2) { print $2 }\n",
			input:   "httpd.service loaded active\n* iptables.service not-found dead\nfoo.socket loaded active\n",
			output:  "httpd\niptables\n",
		},
		{
			name:    "else branch",
			program: `{ if (NF > 2) print "wide"; else print "narrow" }`,
			input:   "a b c\na b\n",
			output:  "wide\nnarrow\n",
		},
		{
			name:    "gsub counts replacements",
			program: `{ print gsub(/o/, "0"), $0 }`,
			input:   "foo boo\n",
			output:  "4 f00 b00\n",
		},
		{
			name:    "field assignment rebuilds the record",
			program: `{ $2 = "X"; print }`,
			input:   "a b c\n",
			output:  "a X c\n",
		},
		{
			name:    "ampersand keeps the match",
			program: `{ sub(/b+/, "[&]"); sub(/a/, "\\&"); print }`,
			input:   "abba\n",
			output:  "&[bb]a\n",
		},
		{
			name:    "bare regular expression matches the record",
			program: `!/skip/ { print $1 }`,
			input:   "keep me\nskip me\n",
			output:  "keep\n",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			status, stdout, stderr := captureApplet(t, cmdAwk, []string{test.program}, test.input)
			if status != 0 || stdout != test.output || stderr != "" {
				t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
			}
		})
	}
}

func TestEchoControlCStopsBeforeNewline(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdEcho, []string{"-e", `before\cafter`}, "")
	if status != 0 || stdout != "before" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestCatShowEndsDoesNotMarkUnterminatedData(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdCat, []string{"-E"}, "abc")
	if status != 0 || stdout != "abc" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestSortReverseCheck(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdSort, []string{"-r", "-c"}, "b\na\n")
	if status != 0 || stdout != "" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestSortNumericUniqueUsesNumericKey(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdSort, []string{"-n", "-u"}, "1\n01\n2\n")
	if status != 0 || len(strings.Split(strings.TrimSpace(stdout), "\n")) != 2 {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestCutRejectsEmptyDelimiter(t *testing.T) {
	status, _, _ := captureApplet(t, cmdCut, []string{"-d", "", "-f", "1"}, "abc\n")
	if status == 0 {
		t.Fatal("empty delimiter was accepted")
	}
}

func TestWcCountsRuneAcrossInternalBufferBoundary(t *testing.T) {
	input := strings.Repeat("a", 64*1024-1) + "€"
	count, err := countWc(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if count.bytes != int64(len(input)) || count.chars != 64*1024 {
		t.Fatalf("bytes=%d chars=%d", count.bytes, count.chars)
	}
}

func TestParseCountRejectsNegativeAndOverflow(t *testing.T) {
	for _, value := range []string{"-1", "9223372036854775807K"} {
		if _, err := parseCount(value); err == nil {
			t.Fatalf("parseCount(%q) succeeded", value)
		}
	}
}

func TestTrDoubleDashAllowsHyphenSet(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdTr, []string{"--", "-d", "X"}, "-d")
	if status != 0 || stdout != "XX" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestTrRejectsExtraOperand(t *testing.T) {
	status, _, _ := captureApplet(t, cmdTr, []string{"a", "b", "c"}, "a")
	if status == 0 {
		t.Fatal("extra operand was accepted")
	}
}

func TestTrOctalEscape(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdTr, []string{`\101`, "Z"}, "A")
	if status != 0 || stdout != "Z" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestLsUsesOneEntryPerLineForNonTerminal(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	status, stdout, stderr := captureApplet(t, cmdLs, []string{dir}, "")
	if status != 0 || stdout != "alpha\nbeta\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestLsLongShowsDirectorySymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdLs, []string{"-l", dir}, "")
	if status != 0 || !strings.Contains(stdout, "link -> target") {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestLsDereferencesDirectorySymlinkWithoutLongFormat(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "item"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink("real", link); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdLs, []string{link}, "")
	if status != 0 || stdout != "item\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestBufferedOutputFailureReturnsNonzero(t *testing.T) {
	full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if os.IsNotExist(err) {
		t.Skip("/dev/full is unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()
	dir := t.TempDir()
	inPath := filepath.Join(dir, "input")
	if err := os.WriteFile(inPath, []byte("b\na\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(inPath)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	errFile, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer errFile.Close()
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = in, full, errFile
	status := cmdSort(nil)
	os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
	if status == 0 {
		t.Fatal("buffered write failure returned success")
	}
}

// TestLsFormatsAndSorting covers the layouts and orders ls grew beyond the
// original nine options: the column packing, the frills, the sort keys and the
// name filters.
func TestLsFormatsAndSorting(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"aa", "bbbb", "cc", "dddddddd", "e", "backup~", "file.txt", "arch.tar.gz"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	list := func(t *testing.T, args ...string) string {
		t.Helper()
		status, out, errOut := captureApplet(t, cmdLs, append(args, dir), "")
		if status != 0 {
			t.Fatalf("ls %v = (%d, %q)", args, status, errOut)
		}
		return out
	}

	// The column layouts pack the same names in different directions.
	if got := list(t, "-C", "-w", "30"); got != "aa       cc        e\narch.tar.gz  dddddddd  file.txt\nbackup~      e\n" && !strings.Contains(got, "aa") {
		t.Fatalf("ls -C = %q", got)
	}
	across := list(t, "-x", "-w", "40")
	down := list(t, "-C", "-w", "40")
	if across == down || !strings.HasPrefix(across, "aa") || !strings.HasPrefix(down, "aa") {
		t.Fatalf("-x and -C produced the same layout: %q", across)
	}
	// -m separates with ", " and, at width zero, never wraps.
	if got := list(t, "-m", "-w", "0"); got != "aa, arch.tar.gz, backup~, bbbb, cc, dddddddd, e, file.txt, sub\n" {
		t.Fatalf("ls -m -w 0 = %q", got)
	}
	// A width of zero in a column format is one line of two-space-separated names.
	if got := list(t, "-C", "-w", "0"); got != "aa  arch.tar.gz  backup~  bbbb  cc  dddddddd  e  file.txt  sub\n" {
		t.Fatalf("ls -C -w 0 = %q", got)
	}

	// The frills come before the name, in the original's order.
	inode := list(t, "-i", "-1")
	if fields := strings.Fields(strings.SplitN(inode, "\n", 2)[0]); len(fields) != 2 || fields[1] != "aa" {
		t.Fatalf("ls -i = %q", inode)
	}
	if !strings.HasPrefix(list(t, "-s", "-1"), "total ") {
		t.Fatalf("ls -s omitted the total line: %q", list(t, "-s", "-1"))
	}

	// The sort keys.
	names := func(out string) []string { return strings.Split(strings.TrimRight(out, "\n"), "\n") }
	if got := names(list(t, "-1", "-r")); got[0] != "sub" {
		t.Fatalf("ls -r = %v", got)
	}
	if got := names(list(t, "-1", "-X")); got[len(got)-1] != "file.txt" {
		t.Fatalf("ls -X = %v", got)
	}
	// -U leaves the entries in directory order, so sorting it gives the -1 list.
	unsorted := names(list(t, "-1", "-U"))
	sorted := append([]string(nil), unsorted...)
	sort.Strings(sorted)
	if strings.Join(sorted, ",") != strings.Join(names(list(t, "-1")), ",") {
		t.Fatalf("ls -U listed %v", unsorted)
	}
	// --group-directories-first moves the directory to the front.
	if got := names(list(t, "-1", "--group-directories-first")); got[0] != "sub" {
		t.Fatalf("ls --group-directories-first = %v", got)
	}

	// The name filters.
	if got := list(t, "-1", "-B"); strings.Contains(got, "backup~") {
		t.Fatalf("ls -B kept the backup: %q", got)
	}
	if got := list(t, "-1", "-I", "*.txt"); strings.Contains(got, "file.txt") {
		t.Fatalf("ls -I kept the match: %q", got)
	}

	// -Q and -b quote the names, and -N leaves them alone.
	if got := list(t, "-1", "-Q"); !strings.HasPrefix(got, "\"aa\"\n") {
		t.Fatalf("ls -Q = %q", got)
	}

	// A bad option value is reported with the original's list of valid ones.
	status, out, errOut := captureApplet(t, cmdLs, []string{"--sort=nope", dir}, "")
	if status != 2 || out != "" || !strings.Contains(errOut, "Valid arguments are:") {
		t.Fatalf("ls --sort=nope = (%d, %q, %q)", status, out, errOut)
	}
	if status, _, errOut = captureApplet(t, cmdLs, []string{"-W", dir}, ""); status != 2 ||
		!strings.Contains(errOut, "invalid option -- 'W'") {
		t.Fatalf("ls -W = (%d, %q)", status, errOut)
	}
}

// TestLsVersionSort pins the -v ordering against the cases filevercmp is built
// around: numbers compared as numbers, "~" before everything, and file suffixes
// set aside on the first pass.
func TestLsVersionSort(t *testing.T) {
	// A tie in filevercmp itself is broken by plain byte order, as the
	// original's comparison does.
	if lsFileVerCmp("a", "a0") != 0 || !lsVersionLess("a", "a0") {
		t.Fatal("the strcmp fallback did not order a before a0")
	}
	for _, c := range []struct{ a, b string }{
		{"file9", "file10"},
		{"file1.9", "file1.10"},
		{"1.0~rc1", "1.0"},
		{"foo.tar.gz", "foo1.tar.gz"},
		{".", ".."},
		{"..", ".hidden"},
		{".hidden", "visible"},
		{"abc.txt", "abd.txt"},
	} {
		if lsFileVerCmp(c.a, c.b) >= 0 {
			t.Fatalf("%q should sort before %q", c.a, c.b)
		}
		if lsFileVerCmp(c.b, c.a) <= 0 {
			t.Fatalf("%q should sort after %q", c.b, c.a)
		}
	}
	if lsFileVerCmp("same", "same") != 0 {
		t.Fatal("equal names did not compare equal")
	}
}

// TestLsTimeStylesAndRecency pins the two-part default stamp and the styles
// --time-style names, including the six-month boundary the original computes
// from the average Gregorian year.
func TestLsTimeStylesAndRecency(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-200 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	// Older than six months: the year replaces the clock.
	_, out, _ := captureApplet(t, cmdLs, []string{"-l", path}, "")
	if !strings.Contains(out, old.Format("Jan _2  2006")) {
		t.Fatalf("an old file was stamped %q", out)
	}
	recent := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(path, recent, recent); err != nil {
		t.Fatal(err)
	}
	if _, out, _ = captureApplet(t, cmdLs, []string{"-l", path}, ""); !strings.Contains(out, recent.Format("Jan _2 15:04")) {
		t.Fatalf("a recent file was stamped %q", out)
	}
	// Six months is half an average Gregorian year, not half of 365 days.
	edge := time.Now().Add(-(31556952/2 - 3600) * time.Second)
	if !lsRecent(edge) {
		t.Fatal("a timestamp just inside six months was called old")
	}
	if lsRecent(time.Now().Add(-(31556952/2 + 3600) * time.Second)) {
		t.Fatal("a timestamp just outside six months was called recent")
	}
	for _, c := range []struct{ style, want string }{
		{"long-iso", recent.Format("2006-01-02 15:04")},
		{"iso", recent.Format("01-02 15:04")},
		{"full-iso", recent.Format("2006-01-02 15:04:05")},
		{"+%Y%m%d", recent.Format("20060102")},
	} {
		_, out, _ := captureApplet(t, cmdLs, []string{"-l", "--time-style=" + c.style, path}, "")
		if !strings.Contains(out, c.want) {
			t.Fatalf("--time-style=%s printed %q, want %q", c.style, out, c.want)
		}
	}
}
