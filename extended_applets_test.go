// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	archivetar "archive/tar"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestSha256sumComputeAndCheck(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "data")
	if err := os.WriteFile(file, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected := fmt.Sprintf("%x", sha256.Sum256([]byte("abc")))
	status, stdout, stderr := captureApplet(t, cmdSha256sum, []string{file}, "")
	if status != 0 || stdout != expected+"  "+file+"\n" {
		t.Fatalf("sha256sum=(%d,%q,%q)", status, stdout, stderr)
	}
	list := filepath.Join(dir, "checksums")
	if err := os.WriteFile(list, []byte(stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr = captureApplet(t, cmdSha256sum, []string{"-c", list}, "")
	if status != 0 || stdout != file+": OK\n" {
		t.Fatalf("sha256sum -c=(%d,%q,%q)", status, stdout, stderr)
	}
}

func TestDfAndDu(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "one")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdDf, []string{dir}, "")
	if status != 0 || !strings.Contains(stdout, "Filesystem") || !strings.Contains(stdout, "Mounted on") {
		t.Fatalf("df=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdDu, []string{"-s", dir}, "")
	if status != 0 || !strings.HasSuffix(strings.TrimSpace(stdout), dir) {
		t.Fatalf("du=(%d,%q,%q)", status, stdout, stderr)
	}
	procLink := filepath.Join(dir, "proc-link")
	if err := os.Symlink("/proc", procLink); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr = captureApplet(t, cmdDf, []string{procLink}, "")
	if status != 0 || !strings.HasSuffix(strings.TrimSpace(stdout), "/proc") {
		t.Fatalf("df symlink target=(%d,%q,%q)", status, stdout, stderr)
	}
}

func TestFindPredicatesDepthAndSize(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "one.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "two.txt"), []byte("yy"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdFind,
		[]string{dir, "-maxdepth", "1", "-type", "f", "-name", "*.txt", "-size", "1c"}, "")
	if status != 0 || stdout != file+"\n" {
		t.Fatalf("find=(%d,%q,%q)", status, stdout, stderr)
	}
}

func TestSedSubstitutionAddressesAndDelete(t *testing.T) {
	// sed defaults to BREs, so grouping uses \( and \) rather than ERE's
	// unescaped parentheses.
	script := `1,2s/\(foo\) \([0-9]\)/\1=x\2/;3d`
	status, stdout, stderr := captureApplet(t, cmdSed, []string{script}, "foo 1\nfoo 2\nbar 3\n")
	if status != 0 || stdout != "foo=x1\nfoo=x2\n" {
		t.Fatalf("sed=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdSed, []string{"-n", `/foo/p`}, "foo\nbar\n")
	if status != 0 || stdout != "foo\n" {
		t.Fatalf("sed -n=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdSed, []string{"-n", "2,1p"}, "one\ntwo\nthree\n")
	if status != 0 || stdout != "two\n" {
		t.Fatalf("sed numeric reverse range=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdSed, []string{"-n", "/x/,/x/p"}, "one\nx\nbetween\nx\nlast\n")
	if status != 0 || stdout != "x\nbetween\nx\n" {
		t.Fatalf("sed regex range=(%d,%q,%q)", status, stdout, stderr)
	}
	// Negated addresses, as used by the iproute2 bash completion to keep only
	// the "TYPE := { ... }" block of ip's help output.
	status, stdout, stderr = captureApplet(t, cmdSed, []string{`/TYPE := /,/}/!d`},
		"Usage: ip link add\nTYPE := { bridge |\n  veth }\ntrailing\n")
	if status != 0 || stdout != "TYPE := { bridge |\n  veth }\n" {
		t.Fatalf("sed negated range=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdSed, []string{"-n", `$!p`}, "one\ntwo\n")
	if status != 0 || stdout != "one\n" {
		t.Fatalf("sed negated last line=(%d,%q,%q)", status, stdout, stderr)
	}
	status, _, stderr = captureApplet(t, cmdSed, []string{`/x/!!d`}, "x\n")
	if status == 0 || !strings.Contains(stderr, "multiple") {
		t.Fatalf("sed repeated negation=(%d,%q)", status, stderr)
	}
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	if err := os.WriteFile(first, []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr = captureApplet(t, cmdSed, []string{"q", first, filepath.Join(dir, "missing")}, "")
	if status != 0 || stdout != "first\n" {
		t.Fatalf("sed q eagerly opened later input=(%d,%q,%q)", status, stdout, stderr)
	}
}

func TestTarGzipRoundTripAndTraversalProtection(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	extracted := filepath.Join(root, "extracted")
	archive := filepath.Join(root, "bundle.tar.gz")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload"), []byte("archive data"), 0o600); err != nil {
		t.Fatal(err)
	}
	insideArchive := filepath.Join(source, "inside.tar")
	status, _, _ := captureApplet(t, cmdTar, []string{"-cf", insideArchive, "-C", source, "."}, "")
	if status == 0 {
		t.Fatal("tar accepted an output archive inside its input directory")
	}
	if _, err := os.Stat(insideArchive); !os.IsNotExist(err) {
		t.Fatalf("rejected archive was created: %v", err)
	}
	if status := cmdTar([]string{"-czf", archive, "-C", source, "."}); status != 0 {
		t.Fatalf("tar create returned %d", status)
	}
	if status := cmdTar([]string{"-xzf", archive, "-C", extracted}); status != 0 {
		t.Fatalf("tar extract returned %d", status)
	}
	contents, err := os.ReadFile(filepath.Join(extracted, "payload"))
	if err != nil || string(contents) != "archive data" {
		t.Fatalf("extracted contents=%q err=%v", contents, err)
	}

	malicious := filepath.Join(root, "malicious.tar")
	file, err := os.Create(malicious)
	if err != nil {
		t.Fatal(err)
	}
	writer := archivetar.NewWriter(file)
	if err := writer.WriteHeader(&archivetar.Header{Name: "../escape", Mode: 0o600, Size: 1, Typeflag: archivetar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	status, _, _ = captureApplet(t, cmdTar, []string{"-xf", malicious, "-C", extracted}, "")
	if status == 0 {
		t.Fatal("tar accepted a path traversal member")
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
		t.Fatalf("path traversal created a file: %v", err)
	}
}

// A symbolic link whose target is lexically inside the archive can still
// escape once it is resolved through a link extracted earlier from the same
// archive: "subdir/parent/.." folds to "subdir" lexically, but reaches the
// parent of the destination when "subdir/parent" is a link to "..".
func TestTarRejectsSymlinkEscapingThroughExtractedLink(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "destination")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	malicious := filepath.Join(root, "malicious.tar")
	file, err := os.Create(malicious)
	if err != nil {
		t.Fatal(err)
	}
	writer := archivetar.NewWriter(file)
	headers := []*archivetar.Header{
		{Name: "subdir", Mode: 0o755, Typeflag: archivetar.TypeDir},
		{Name: "subdir/parent", Mode: 0o777, Typeflag: archivetar.TypeSymlink, Linkname: ".."},
		{Name: "escape", Mode: 0o777, Typeflag: archivetar.TypeSymlink, Linkname: "subdir/parent/.."},
	}
	for _, header := range headers {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, cmdTar, []string{"-xf", malicious, "-C", destination}, "")
	if status == 0 || !strings.Contains(stderr, "escapes destination") {
		t.Fatalf("tar extract = (%d, %q)", status, stderr)
	}
	if _, err := os.Lstat(filepath.Join(destination, "escape")); !os.IsNotExist(err) {
		t.Fatalf("escaping symbolic link was created: %v", err)
	}
}

func TestTarExtractionReplacesExistingHardLink(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	archive := filepath.Join(root, "archive.tar")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "member"), []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := cmdTar([]string{"-cf", archive, "-C", source, "member"}); status != 0 {
		t.Fatalf("tar create returned %d", status)
	}
	victim := filepath.Join(root, "victim")
	member := filepath.Join(destination, "member")
	if err := os.WriteFile(victim, []byte("victim"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victim, member); err != nil {
		t.Fatal(err)
	}
	if status := cmdTar([]string{"-xf", archive, "-C", destination}); status != 0 {
		t.Fatalf("tar extract returned %d", status)
	}
	victimData, err := os.ReadFile(victim)
	if err != nil || string(victimData) != "victim" {
		t.Fatalf("outside hard link changed to %q: %v", victimData, err)
	}
	memberData, err := os.ReadFile(member)
	if err != nil || string(memberData) != "archive" {
		t.Fatalf("extracted member=%q: %v", memberData, err)
	}
	victimInfo, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	memberInfo, err := os.Stat(member)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(victimInfo, memberInfo) {
		t.Fatal("extraction retained the pre-existing hard link")
	}
}

func TestGzipAndGunzipRoundTrip(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input")
	if err := os.WriteFile(input, []byte("compressed data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := cmdGzip([]string{"-k", input}); status != 0 {
		t.Fatalf("gzip returned %d", status)
	}
	if err := os.Remove(input); err != nil {
		t.Fatal(err)
	}
	if status := cmdGunzip([]string{"-k", input + ".gz"}); status != 0 {
		t.Fatalf("gunzip returned %d", status)
	}
	contents, err := os.ReadFile(input)
	if err != nil || string(contents) != "compressed data" {
		t.Fatalf("gunzip contents=%q err=%v", contents, err)
	}
}

func TestSystemAndProcApplets(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdUname, []string{"-s"}, "")
	if status != 0 || strings.TrimSpace(stdout) == "" {
		t.Fatalf("uname=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdId, []string{"-u"}, "")
	if status != 0 || strings.TrimSpace(stdout) != strconv.Itoa(os.Getuid()) {
		t.Fatalf("id=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdHostname, nil, "")
	if status != 0 || strings.TrimSpace(stdout) == "" {
		t.Fatalf("hostname=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdWhoami, nil, "")
	if status != 0 || strings.TrimSpace(stdout) == "" {
		t.Fatalf("whoami=(%d,%q,%q)", status, stdout, stderr)
	}
	pid := strconv.Itoa(os.Getpid())
	status, stdout, stderr = captureApplet(t, cmdPs, []string{"-p", pid, "-o", "pid,comm"}, "")
	if status != 0 || !strings.Contains(stdout, pid) {
		t.Fatalf("ps=(%d,%q,%q)", status, stdout, stderr)
	}
	if status := cmdKill([]string{"-0", pid}); status != 0 {
		t.Fatalf("kill -0 returned %d", status)
	}
	status, stdout, stderr = captureApplet(t, cmdFree, []string{"-k"}, "")
	if status != 0 || !strings.Contains(stdout, "Mem:") || !strings.Contains(stdout, "Swap:") {
		t.Fatalf("free=(%d,%q,%q)", status, stdout, stderr)
	}
}

func TestParseNeighborRuleAndExtendedLinkSet(t *testing.T) {
	neighbor, err := parseNeighborSpec(syscall.AF_UNSPEC,
		[]string{"192.0.2.2", "dev", "eth0", "lladdr", "02:00:00:00:00:02", "nud", "reachable"})
	if err != nil || neighbor.family != syscall.AF_INET || neighbor.state != neighborStates["reachable"] {
		t.Fatalf("neighbor=%+v err=%v", neighbor, err)
	}
	rule, err := parseRuleSpec(syscall.AF_UNSPEC,
		[]string{"from", "192.0.2.0/24", "priority", "100", "table", "200"})
	if err != nil || rule.family != syscall.AF_INET || rule.from.prefix != 24 || rule.priority == nil || *rule.priority != 100 || rule.table != 200 {
		t.Fatalf("rule=%+v err=%v", rule, err)
	}
	link, err := parseLinkSet([]string{"dev", "eth0", "mtu", "1400", "address", "02:00:00:00:00:01", "alias", "uplink", "name", "wan0"})
	if err != nil || link.mtu == nil || *link.mtu != 1400 || link.address.String() != "02:00:00:00:00:01" ||
		link.alias == nil || *link.alias != "uplink" || link.rename != "wan0" {
		t.Fatalf("link=%+v err=%v", link, err)
	}
}

// TestFreeRowsAndUnits covers the rows and unit options free grew past -b/-k/-m/-g:
// the extra rows each add one labelled line in procps' order, -w splits the
// cache column, and the unit families divide by their own power.
func TestFreeRowsAndUnits(t *testing.T) {
	labels := func(t *testing.T, args []string) []string {
		t.Helper()
		status, out, errOut := captureApplet(t, cmdFree, args, "")
		if status != 0 {
			t.Fatalf("free %v = (%d, %q)", args, status, errOut)
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		names := make([]string, 0, len(lines))
		for _, line := range lines {
			// The header line has no label of its own; stand a dot in for it.
			label, _, _ := strings.Cut(line, " ")
			if label == "" {
				label = "."
			}
			names = append(names, label)
		}
		return names
	}
	if got := strings.Join(labels(t, nil), " "); got != ". Mem: Swap:" {
		t.Fatalf("free rows = %q", got)
	}
	// procps draws low/high above the swap row, and total then committed below it.
	if got := strings.Join(labels(t, []string{"-l", "-t", "-v"}), " "); got != ". Mem: Low: High: Swap: Total: Comm:" {
		t.Fatalf("free -l -t -v rows = %q", got)
	}
	status, out, _ := captureApplet(t, cmdFree, []string{"-w"}, "")
	if status != 0 || !strings.Contains(out, "buffers") || !strings.Contains(out, "cache") || strings.Contains(out, "buff/cache") {
		t.Fatalf("free -w header = %q", out)
	}
	// -t's row is the sum of the two above it, column by column.
	_, out, _ = captureApplet(t, cmdFree, []string{"-t", "-b"}, "")
	rows := map[string][]uint64{}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n")[1:] {
		fields := strings.Fields(line)
		for _, field := range fields[1:] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				t.Fatalf("free -t -b printed %q", line)
			}
			rows[fields[0]] = append(rows[fields[0]], value)
		}
	}
	for column := 0; column < 3; column++ {
		if rows["Total:"][column] != rows["Mem:"][column]+rows["Swap:"][column] {
			t.Fatalf("free -t column %d = %v, want the sum of %v and %v", column, rows["Total:"], rows["Mem:"], rows["Swap:"])
		}
	}
	// Each unit family divides by its own power, so the same reading shrinks
	// by 1024 from -b to -k and by 1000 from --bytes to --kilo.
	value := func(args []string) uint64 {
		t.Helper()
		_, out, _ := captureApplet(t, cmdFree, args, "")
		fields := strings.Fields(strings.Split(out, "\n")[1])
		got, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			t.Fatalf("free %v printed %q", args, out)
		}
		return got
	}
	bytes := value([]string{"-b"})
	for _, c := range []struct {
		args    []string
		divisor uint64
	}{
		{[]string{"-k"}, 1 << 10}, {[]string{"-m"}, 1 << 20}, {[]string{"--kibi"}, 1 << 10},
		{[]string{"--kilo"}, 1000}, {[]string{"--mega"}, 1e6}, {[]string{"--si"}, 1000},
	} {
		if got := value(c.args); got != bytes/c.divisor {
			t.Fatalf("free %v = %d, want %d", c.args, got, bytes/c.divisor)
		}
	}
	// -L is one line of four labelled fields, and -h scales rather than divides.
	_, out, _ = captureApplet(t, cmdFree, []string{"-L"}, "")
	if fields := strings.Fields(out); len(fields) != 8 || fields[0] != "SwapUse" || fields[6] != "MemFree" {
		t.Fatalf("free -L = %q", out)
	}
	if _, out, _ = captureApplet(t, cmdFree, []string{"-h"}, ""); !strings.Contains(out, "i") && !strings.Contains(out, "B") {
		t.Fatalf("free -h = %q", out)
	}
	// --si labels its powers of 1000 with the bare letter.
	if _, out, _ = captureApplet(t, cmdFree, []string{"-h", "--si"}, ""); strings.Contains(strings.SplitN(out, "\n", 3)[1], "i") {
		t.Fatalf("free -h --si = %q, want no IEC suffix", out)
	}

	// A bad count or interval is refused with procps' wording, and an unknown
	// option prints the usage text.
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-c", "0"}, "failed to parse count argument"},
		{[]string{"-s", "x"}, "seconds argument failed"},
		{[]string{"-q"}, "invalid option -- 'q'"},
		{[]string{"--nosuch"}, "unrecognized option '--nosuch'"},
	} {
		status, out, errOut := captureApplet(t, cmdFree, c.args, "")
		if status != 1 || out != "" || !strings.Contains(errOut, c.want) {
			t.Fatalf("free %v = (%d, %q, %q), want %q", c.args, status, out, errOut, c.want)
		}
	}
}

// TestDfFieldsAndUnits covers the columns and unit options df grew past -h/-k/-a:
// -i, -T, -P and --output pick the fields, and -B, -h and -H scale them.
func TestDfFieldsAndUnits(t *testing.T) {
	dir := t.TempDir()
	header := func(t *testing.T, args []string) []string {
		t.Helper()
		status, out, errOut := captureApplet(t, cmdDf, args, "")
		if status != 0 {
			t.Fatalf("df %v = (%d, %q)", args, status, errOut)
		}
		return strings.Fields(strings.SplitN(out, "\n", 2)[0])
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{dir}, "Filesystem 1K-blocks Used Available Use% Mounted on"},
		{[]string{"-h", dir}, "Filesystem Size Used Avail Use% Mounted on"},
		{[]string{"-P", dir}, "Filesystem 1024-blocks Used Available Capacity Mounted on"},
		{[]string{"-i", dir}, "Filesystem Inodes IUsed IFree IUse% Mounted on"},
		{[]string{"-T", dir}, "Filesystem Type 1K-blocks Used Available Use% Mounted on"},
		{[]string{"-BM", dir}, "Filesystem 1M-blocks Used Available Use% Mounted on"},
		{[]string{"-B", "1M", dir}, "Filesystem 1M-blocks Used Available Use% Mounted on"},
		// -h wins over -P, which only changes the plain table's wording.
		{[]string{"-h", "-P", dir}, "Filesystem Size Used Avail Use% Mounted on"},
		{[]string{"--output=target,pcent", dir}, "Mounted on Use%"},
		{[]string{"--output", dir}, "Filesystem Type Inodes IUsed IFree IUse% 1K-blocks Used Avail Use% File Mounted on"},
	} {
		if got := strings.Join(header(t, c.args), " "); got != c.want {
			t.Fatalf("df %v header = %q, want %q", c.args, got, c.want)
		}
	}
	// A bare unit is echoed after every value; a size with a count is not.
	_, out, _ := captureApplet(t, cmdDf, []string{"-B", "M", dir}, "")
	size := strings.Fields(strings.Split(out, "\n")[1])[1]
	if !strings.HasSuffix(size, "M") {
		t.Fatalf("df -B M printed %q, want a unit suffix", size)
	}
	if _, out, _ = captureApplet(t, cmdDf, []string{"-B", "1M", dir}, ""); strings.HasSuffix(strings.Fields(strings.Split(out, "\n")[1])[1], "M") {
		t.Fatalf("df -B 1M printed %q, want a bare count", out)
	}
	// The two human forms differ in base, and the SI kilo is a small k.
	blocks := func(args []string) string {
		t.Helper()
		_, out, _ := captureApplet(t, cmdDf, args, "")
		return strings.Fields(strings.Split(out, "\n")[1])[1]
	}
	if iec, si := blocks([]string{"-h", dir}), blocks([]string{"-H", dir}); iec == si && !strings.HasSuffix(iec, "0") {
		t.Logf("human sizes %q and %q coincide on this filesystem", iec, si)
	}
	// --total adds a labelled row whose figures are the sum of the rows above.
	_, out, _ = captureApplet(t, cmdDf, []string{"--total", "-B", "1", dir, dir}, "")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	last := strings.Fields(lines[len(lines)-1])
	if last[0] != "total" || last[len(last)-1] != "-" {
		t.Fatalf("df --total last row = %q", lines[len(lines)-1])
	}
	first := strings.Fields(lines[1])
	one, err := strconv.ParseUint(first[1], 10, 64)
	if err != nil {
		t.Fatalf("df printed %q", lines[1])
	}
	sum, err := strconv.ParseUint(last[1], 10, 64)
	if err != nil || sum != 2*one {
		t.Fatalf("df --total size = %v, want twice %d", last, one)
	}
	// -t selects by type, and a type nothing matches is an error.
	_, out, _ = captureApplet(t, cmdDf, []string{"-T", dir}, "")
	kind := strings.Fields(strings.Split(out, "\n")[1])[1]
	if status, out, _ := captureApplet(t, cmdDf, []string{"-t", kind}, ""); status != 0 || !strings.Contains(out, kind) {
		t.Fatalf("df -t %s = (%d, %q)", kind, status, out)
	}
	status, out, errOut := captureApplet(t, cmdDf, []string{"-t", "nosuchfstype"}, "")
	if status != 1 || out != "" || !strings.Contains(errOut, "no file systems processed") {
		t.Fatalf("df -t nosuchfstype = (%d, %q, %q)", status, out, errOut)
	}

	// Bad option values are reported with GNU's wording, and the ones that are
	// command-line mistakes carry the Try line.
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--output=nope"}, "option --output: field 'nope' unknown"},
		{[]string{"-B", "x"}, "invalid -B argument 'x'"},
		{[]string{"-q"}, "invalid option -- 'q'"},
		{[]string{"--nosuch"}, "unrecognized option '--nosuch'"},
	} {
		status, out, errOut := captureApplet(t, cmdDf, c.args, "")
		if status != 1 || out != "" || !strings.Contains(errOut, c.want) {
			t.Fatalf("df %v = (%d, %q, %q), want %q", c.args, status, out, errOut, c.want)
		}
	}
	// Every operand failing leaves no table at all, only the diagnostics.
	status, out, errOut = captureApplet(t, cmdDf, []string{filepath.Join(dir, "absent")}, "")
	if status != 1 || out != "" || !strings.Contains(errOut, "No such file") {
		t.Fatalf("df on a missing path = (%d, %q, %q)", status, out, errOut)
	}
}

// TestGzipListTestAndLevels covers what gzip grew past -c/-d/-k/-f: the listing,
// the integrity check, the compression levels and the name and suffix options.
func TestGzipListTestAndLevels(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("hello world ", 400)
	source := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(source, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// A level really changes the result, and every level round-trips.
	sizes := map[int]int64{}
	for _, level := range []int{1, 9} {
		copyPath := filepath.Join(dir, fmt.Sprintf("level%d.txt", level))
		if err := os.WriteFile(copyPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if status := cmdGzip([]string{fmt.Sprintf("-%d", level), copyPath}); status != 0 {
			t.Fatalf("gzip -%d = %d", level, status)
		}
		info, err := os.Stat(copyPath + ".gz")
		if err != nil {
			t.Fatal(err)
		}
		sizes[level] = info.Size()
		status, out, _ := captureApplet(t, cmdGzip, []string{"-dc", copyPath + ".gz"}, "")
		if status != 0 || out != body {
			t.Fatalf("level %d did not round-trip: (%d, %d bytes)", level, status, len(out))
		}
	}
	if sizes[9] >= sizes[1] {
		t.Fatalf("-9 produced %d bytes against -1's %d", sizes[9], sizes[1])
	}

	// -l reports the two sizes and a ratio that leaves the member's own
	// header and trailer out of the compressed side.
	archive := filepath.Join(dir, "listed.txt")
	if err := os.WriteFile(archive, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := cmdGzip([]string{"-k", archive}); status != 0 {
		t.Fatalf("gzip -k = %d", status)
	}
	status, out, errOut := captureApplet(t, cmdGzip, []string{"-l", archive + ".gz"}, "")
	if status != 0 {
		t.Fatalf("gzip -l = (%d, %q)", status, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "uncompressed_name") {
		t.Fatalf("gzip -l printed %q", out)
	}
	fields := strings.Fields(lines[1])
	if len(fields) != 4 || fields[1] != strconv.Itoa(len(body)) || fields[3] != archive {
		t.Fatalf("gzip -l row = %v", fields)
	}
	compressed, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	// The ratio counts the deflate stream alone, so it beats the one the two
	// file sizes alone would give.
	naive := gzipRatio(compressed, uint64(len(body)), 0)
	if fields[2] == naive {
		t.Fatalf("gzip -l ratio %s did not discount the container", fields[2])
	}

	// -t is silent on a good member and reports a bad one; -tv says OK.
	if status, out, errOut = captureApplet(t, cmdGzip, []string{"-t", archive + ".gz"}, ""); status != 0 || out != "" || errOut != "" {
		t.Fatalf("gzip -t on a good member = (%d, %q, %q)", status, out, errOut)
	}
	if _, out, _ = captureApplet(t, cmdGzip, []string{"-tv", archive + ".gz"}, ""); out != archive+".gz:\t OK\n" {
		t.Fatalf("gzip -tv = %q", out)
	}
	status, _, errOut = captureApplet(t, cmdGzip, []string{"-t", source}, "")
	if status != 1 || !strings.Contains(errOut, "not in gzip format") {
		t.Fatalf("gzip -t on a plain file = (%d, %q)", status, errOut)
	}
	// A corrupt member is reported as a checksum failure.
	corrupt := filepath.Join(dir, "corrupt.gz")
	good, err := os.ReadFile(archive + ".gz")
	if err != nil {
		t.Fatal(err)
	}
	good[len(good)-6] ^= 0xff
	if err := os.WriteFile(corrupt, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if status, _, errOut = captureApplet(t, cmdGzip, []string{"-t", corrupt}, ""); status != 1 ||
		!strings.Contains(errOut, "invalid compressed data--crc error") {
		t.Fatalf("gzip -t on a corrupt member = (%d, %q)", status, errOut)
	}

	// -S chooses the suffix in both directions.
	suffixed := filepath.Join(dir, "suffixed.txt")
	if err := os.WriteFile(suffixed, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := cmdGzip([]string{"-S", ".z", suffixed}); status != 0 {
		t.Fatalf("gzip -S = %d", status)
	}
	if _, err := os.Stat(suffixed + ".z"); err != nil {
		t.Fatalf("gzip -S did not use the suffix: %v", err)
	}
	if status := cmdGzip([]string{"-d", "-S", ".z", suffixed + ".z"}); status != 0 {
		t.Fatalf("gzip -d -S = %d", status)
	}

	// -n stores neither name nor timestamp, so -lN falls back to the file name.
	anonymous := filepath.Join(dir, "anon.txt")
	if err := os.WriteFile(anonymous, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := cmdGzip([]string{"-n", "-k", anonymous}); status != 0 {
		t.Fatalf("gzip -n = %d", status)
	}
	header, _, _, err := readGzipInfo(anonymous + ".gz")
	if err != nil {
		t.Fatal(err)
	}
	if header.name != "" || !header.modTime.IsZero() {
		t.Fatalf("gzip -n stored %q and %v", header.name, header.modTime)
	}

	// An unknown suffix is the original's warning, and exits 2.
	if status, _, errOut = captureApplet(t, cmdGzip, []string{"-d", source}, ""); status != 2 ||
		!strings.Contains(errOut, "unknown suffix -- ignored") {
		t.Fatalf("gzip -d on a plain name = (%d, %q)", status, errOut)
	}
	// So does an output file that is already there.
	if status, _, errOut = captureApplet(t, cmdGzip, []string{"-k", archive}, ""); status != 2 ||
		!strings.Contains(errOut, "already exists;\tnot overwritten") {
		t.Fatalf("gzip over an existing archive = (%d, %q)", status, errOut)
	}
}

// TestTarSelectionAndOptions covers what tar grew past -c/-x/-t/-f/-z/-C/-v/-p:
// member selection, --strip-components, --exclude, -T, -O and the long listing.
func TestTarSelectionAndOptions(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src")
	if err := os.MkdirAll(filepath.Join(source, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"a.txt": "hello\n", "sub/b.txt": "world\n"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(dir, "t.tar")
	if status := cmdTar([]string{"-cf", archive, "-C", dir, "src"}); status != 0 {
		t.Fatalf("tar -cf = %d", status)
	}

	// -t lists every member; naming a directory selects it and its contents.
	_, out, _ := captureApplet(t, cmdTar, []string{"-tf", archive}, "")
	if lines := strings.Split(strings.TrimRight(out, "\n"), "\n"); len(lines) != 4 {
		t.Fatalf("tar -tf listed %q", out)
	}
	_, out, _ = captureApplet(t, cmdTar, []string{"-tf", archive, "src/sub"}, "")
	if out != "src/sub/\nsrc/sub/b.txt\n" {
		t.Fatalf("tar -tf src/sub = %q", out)
	}
	// A selection that never turns up is the original's failure.
	status, _, errOut := captureApplet(t, cmdTar, []string{"-tf", archive, "nosuch"}, "")
	if status != 2 || !strings.Contains(errOut, "nosuch: Not found in archive") ||
		!strings.Contains(errOut, "Exiting with failure status") {
		t.Fatalf("tar -tf nosuch = (%d, %q)", status, errOut)
	}

	// -tv is the long listing: mode, owner/group, size, stamp and name, with a
	// symbolic link showing what it points at.
	_, out, _ = captureApplet(t, cmdTar, []string{"-tvf", archive, "src/a.txt"}, "")
	fields := strings.Fields(out)
	if len(fields) != 6 || !strings.HasPrefix(fields[0], "-rw") || fields[2] != "6" || fields[5] != "src/a.txt" {
		t.Fatalf("tar -tvf = %q", out)
	}

	// -O writes a member's contents out instead of unpacking it.
	if _, out, _ = captureApplet(t, cmdTar, []string{"-xOf", archive, "src/a.txt"}, ""); out != "hello\n" {
		t.Fatalf("tar -xOf = %q", out)
	}

	// --strip-components drops leading components.
	stripped := filepath.Join(dir, "stripped")
	if err := os.Mkdir(stripped, 0o750); err != nil {
		t.Fatal(err)
	}
	if status := cmdTar([]string{"-xf", archive, "-C", stripped, "--strip-components=1"}); status != 0 {
		t.Fatalf("tar --strip-components = %d", status)
	}
	if _, err := os.Stat(filepath.Join(stripped, "a.txt")); err != nil {
		t.Fatalf("--strip-components did not shorten the path: %v", err)
	}

	// --exclude leaves the matching members out but keeps the directory whose
	// contents were excluded, as the original does.
	partial := filepath.Join(dir, "partial")
	if err := os.Mkdir(partial, 0o750); err != nil {
		t.Fatal(err)
	}
	if status := cmdTar([]string{"-xf", archive, "-C", partial, "--exclude=*/sub/*"}); status != 0 {
		t.Fatalf("tar --exclude = %d", status)
	}
	if _, err := os.Stat(filepath.Join(partial, "src", "sub")); err != nil {
		t.Fatalf("--exclude dropped the directory itself: %v", err)
	}
	if _, err := os.Stat(filepath.Join(partial, "src", "sub", "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("--exclude kept the excluded member: %v", err)
	}

	// -T takes the operand names from a file.
	list := filepath.Join(dir, "list.txt")
	if err := os.WriteFile(list, []byte("src/a.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fromList := filepath.Join(dir, "list.tar")
	if status := cmdTar([]string{"-cf", fromList, "-C", dir, "-T", list}); status != 0 {
		t.Fatalf("tar -T = %d", status)
	}
	if _, out, _ = captureApplet(t, cmdTar, []string{"-tf", fromList}, ""); out != "src/a.txt\n" {
		t.Fatalf("tar -T produced %q", out)
	}

	// Every codec round-trips through its own reader.
	for _, codec := range []string{"-z", "-j", "-J", "--zstd"} {
		packed := filepath.Join(dir, "packed"+strings.TrimLeft(codec, "-"))
		if status := cmdTar([]string{"-cf", packed, codec, "-C", dir, "src"}); status != 0 {
			t.Fatalf("tar -c %s = %d", codec, status)
		}
		unpacked := filepath.Join(dir, "unpacked"+strings.TrimLeft(codec, "-"))
		if err := os.Mkdir(unpacked, 0o750); err != nil {
			t.Fatal(err)
		}
		if status := cmdTar([]string{"-xf", packed, codec, "-C", unpacked}); status != 0 {
			t.Fatalf("tar -x %s = %d", codec, status)
		}
		body, err := os.ReadFile(filepath.Join(unpacked, "src", "sub", "b.txt"))
		if err != nil || string(body) != "world\n" {
			t.Fatalf("%s did not round-trip: %q %v", codec, body, err)
		}
	}

	// An archive that cannot be opened is the original's unrecoverable error.
	status, _, errOut = captureApplet(t, cmdTar, []string{"-tf", filepath.Join(dir, "absent.tar")}, "")
	if status != 2 || !strings.Contains(errOut, "Cannot open:") ||
		!strings.Contains(errOut, "Error is not recoverable") {
		t.Fatalf("tar on a missing archive = (%d, %q)", status, errOut)
	}
}
