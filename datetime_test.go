// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testTimeBase is the "now" every relative case below is measured from: a
// Friday, with a fractional second so the nanosecond rules are visible.
var testTimeBase = time.Date(2026, time.September, 4, 2, 4, 51, 500000000, time.UTC)

func TestParseTimeSpecForms(t *testing.T) {
	const layout = "2006-01-02 15:04:05.000000000 -0700"
	for _, c := range []struct {
		spec string
		want string
	}{
		{"2020-01-02 03:04:05", "2020-01-02 03:04:05.000000000 +0000"},
		{"2020-01-02", "2020-01-02 00:00:00.000000000 +0000"},
		{"20200102", "2020-01-02 00:00:00.000000000 +0000"},
		{"2020/01/02", "2020-01-02 00:00:00.000000000 +0000"},
		{"01/02/2020", "2020-01-02 00:00:00.000000000 +0000"},
		{"@1000000000", "2001-09-09 01:46:40.000000000 +0000"},
		{"@1000000000.25", "2001-09-09 01:46:40.250000000 +0000"},
		{"10:30", "2026-09-04 10:30:00.000000000 +0000"},
		{"3pm", "2026-09-04 15:00:00.000000000 +0000"},
		{"11:59:59 am", "2026-09-04 11:59:59.000000000 +0000"},
		{"", "2026-09-04 00:00:00.000000000 +0000"},
		// A relative string keeps the base's fraction; an absolute one drops it.
		{"+1 hour", "2026-09-04 03:04:51.500000000 +0000"},
		{"-1 day", "2026-09-03 02:04:51.500000000 +0000"},
		{"2 days ago", "2026-09-02 02:04:51.500000000 +0000"},
		{"1 week", "2026-09-11 02:04:51.500000000 +0000"},
		{"2 fortnight", "2026-10-02 02:04:51.500000000 +0000"},
		{"3 months ago", "2026-06-04 02:04:51.500000000 +0000"},
		{"next month", "2026-10-04 02:04:51.500000000 +0000"},
		{"last year", "2025-09-04 02:04:51.500000000 +0000"},
		{"yesterday", "2026-09-03 02:04:51.500000000 +0000"},
		{"tomorrow", "2026-09-05 02:04:51.500000000 +0000"},
		{"now", "2026-09-04 02:04:51.500000000 +0000"},
		{"today", "2026-09-04 02:04:51.500000000 +0000"},
		{"+2 days 3 hours", "2026-09-06 05:04:51.500000000 +0000"},
		// "ago" reverses only the item just before it.
		{"2 hours 30 minutes ago", "2026-09-04 03:34:51.500000000 +0000"},
		{"1 day ago 2 hours ago", "2026-09-03 00:04:51.500000000 +0000"},
		// Weekday words land on midnight of the day they name.
		{"next monday", "2026-09-07 00:00:00.000000000 +0000"},
		{"last friday", "2026-08-28 00:00:00.000000000 +0000"},
		{"sunday", "2026-09-06 00:00:00.000000000 +0000"},
		{"Jan 5 2020", "2020-01-05 00:00:00.000000000 +0000"},
		{"Sep 4", "2026-09-04 00:00:00.000000000 +0000"},
		{"4 sep", "2026-09-04 00:00:00.000000000 +0000"},
		{"5 May 2021 13:00", "2021-05-05 13:00:00.000000000 +0000"},
		// A zone in the string moves the wall clock, not the instant.
		{"2020-01-02T03:04:05Z", "2020-01-02 03:04:05.000000000 +0000"},
		{"2020-01-02T03:04:05+02:00", "2020-01-02 01:04:05.000000000 +0000"},
		{"2020-01-02T03:04:05-05:00", "2020-01-02 08:04:05.000000000 +0000"},
		{"2020-01-02 03:04:05 +0300", "2020-01-02 00:04:05.000000000 +0000"},
		{"1970-01-01 00:00:00 UTC", "1970-01-01 00:00:00.000000000 +0000"},
	} {
		got, err := parseTimeSpec(c.spec, testTimeBase)
		if err != nil {
			t.Errorf("parseTimeSpec(%q) failed: %v", c.spec, err)
			continue
		}
		if text := got.Format(layout); text != c.want {
			t.Errorf("parseTimeSpec(%q) = %s, want %s", c.spec, text, c.want)
		}
	}
}

func TestParseTimeSpecRejectsGarbage(t *testing.T) {
	for _, spec := range []string{"not a date", "hello world", "2020-13-01", "2020-01-32", "99:99"} {
		if _, err := parseTimeSpec(spec, testTimeBase); err == nil {
			t.Errorf("parseTimeSpec(%q) was accepted", spec)
		} else if !strings.Contains(err.Error(), "invalid date '"+spec+"'") {
			t.Errorf("parseTimeSpec(%q) error = %v", spec, err)
		}
	}
}

func TestParseTouchStamp(t *testing.T) {
	const layout = "2006-01-02 15:04:05"
	for _, c := range []struct {
		stamp string
		want  string
	}{
		{"01020304", "2026-01-02 03:04:00"},
		{"202001020304.05", "2020-01-02 03:04:05"},
		{"0001020304", "2000-01-02 03:04:00"},
		// POSIX splits the two-digit year at 69.
		{"6901020304", "1969-01-02 03:04:00"},
		{"6801020304", "2068-01-02 03:04:00"},
	} {
		got, err := parseTouchStamp(c.stamp, testTimeBase)
		if err != nil {
			t.Errorf("parseTouchStamp(%q) failed: %v", c.stamp, err)
			continue
		}
		if text := got.Format(layout); text != c.want {
			t.Errorf("parseTouchStamp(%q) = %s, want %s", c.stamp, text, c.want)
		}
	}
	for _, stamp := range []string{"bad", "202013020304", "010203045", "01020304.5"} {
		if _, err := parseTouchStamp(stamp, testTimeBase); err == nil {
			t.Errorf("parseTouchStamp(%q) was accepted", stamp)
		}
	}
}

func TestDateFormatDirectives(t *testing.T) {
	value := time.Date(2001, time.September, 9, 3, 46, 40, 0, time.FixedZone("CEST", 2*3600))
	for _, c := range []struct{ format, want string }{
		{"%U|%W|%V|%G|%g|%C", "36|36|36|2001|01|20"},
		{"%k|%l|%q|%j|%u|%w", " 3| 3|3|252|7|0"},
		{"%:z|%::z|%z", "+02:00|+02:00:00|+0200"},
		{"%F %T", "2001-09-09 03:46:40"},
	} {
		got, err := formatDate(value, c.format)
		if err != nil {
			t.Errorf("formatDate(%q) failed: %v", c.format, err)
			continue
		}
		if got != c.want {
			t.Errorf("formatDate(%q) = %q, want %q", c.format, got, c.want)
		}
	}
	// A week that belongs to the previous ISO year, and one before the first
	// Sunday of the year.
	first := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	if got, err := formatDate(first, "%U %W %V %G"); err != nil || got != "00 00 01 2026" {
		t.Errorf("formatDate on 2026-01-01 = %q (%v)", got, err)
	}
}

func TestDateStampOptions(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-u", "-d", "@1000000000", "-R"}, "Sun, 09 Sep 2001 01:46:40 +0000"},
		{[]string{"-u", "-d", "@1000000000", "-I"}, "2001-09-09"},
		{[]string{"-u", "-d", "@1000000000", "-Ihours"}, "2001-09-09T01+00:00"},
		{[]string{"-u", "-d", "@1000000000", "-Iminutes"}, "2001-09-09T01:46+00:00"},
		{[]string{"-u", "-d", "@1000000000", "-Iseconds"}, "2001-09-09T01:46:40+00:00"},
		{[]string{"-u", "-d", "@1000000000", "-Ins"}, "2001-09-09T01:46:40,000000000+00:00"},
		{[]string{"-u", "-d", "@1000000000", "--rfc-3339=seconds"}, "2001-09-09 01:46:40+00:00"},
		{[]string{"-u", "-d", "@1000000000", "--rfc-3339=ns"}, "2001-09-09 01:46:40.000000000+00:00"},
		{[]string{"-u", "-d@1000000000", "+%s"}, "1000000000"},
		{[]string{"-u", "--date=@1000000000", "+%F"}, "2001-09-09"},
		{[]string{"--resolution"}, "0.000000001"},
	} {
		status, stdout, stderr := captureApplet(t, cmdDate, c.args, "")
		if status != 0 || stdout != c.want+"\n" {
			t.Errorf("date %v = (%d, %q, %q), want %q", c.args, status, stdout, stderr, c.want)
		}
	}
	if status, _, stderr := captureApplet(t, cmdDate, []string{"-d", "not a date"}, ""); status == 0 ||
		!strings.Contains(stderr, "invalid date 'not a date'") {
		t.Errorf("date -d with a bad string = (%d, %q)", status, stderr)
	}
}

func TestDateReadsSpecsFromFile(t *testing.T) {
	dir := t.TempDir()
	list := filepath.Join(dir, "dates")
	if err := os.WriteFile(list, []byte("@0\n@86400\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdDate, []string{"-u", "-f", list, "+%F"}, "")
	if status != 0 || stdout != "1970-01-01\n1970-01-02\n" {
		t.Fatalf("date -f = (%d, %q, %q)", status, stdout, stderr)
	}
}

func TestTouchTimestampOptions(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")

	if status, _, stderr := captureApplet(t, cmdTouch, []string{"-d", "2020-01-02 03:04:05", file}, ""); status != 0 {
		t.Fatalf("touch -d = (%d, %q)", status, stderr)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2020, time.January, 2, 3, 4, 5, 0, time.Local)
	if !info.ModTime().Equal(want) || !atimeOf(info).Equal(want) {
		t.Fatalf("touch -d set mtime=%v atime=%v, want %v", info.ModTime(), atimeOf(info), want)
	}

	// -t uses the POSIX stamp, and -a leaves the modification time alone.
	if status, _, stderr := captureApplet(t, cmdTouch, []string{"-a", "-t", "202105060708.09", file}, ""); status != 0 {
		t.Fatalf("touch -a -t = (%d, %q)", status, stderr)
	}
	info, err = os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	wantAccess := time.Date(2021, time.May, 6, 7, 8, 9, 0, time.Local)
	if !atimeOf(info).Equal(wantAccess) {
		t.Fatalf("touch -a -t set atime=%v, want %v", atimeOf(info), wantAccess)
	}
	if !info.ModTime().Equal(want) {
		t.Fatalf("touch -a changed mtime to %v, want %v", info.ModTime(), want)
	}

	// -r copies both stamps from another file.
	copyTo := filepath.Join(dir, "copy")
	if status, _, stderr := captureApplet(t, cmdTouch, []string{"-r", file, copyTo}, ""); status != 0 {
		t.Fatalf("touch -r = (%d, %q)", status, stderr)
	}
	copied, err := os.Stat(copyTo)
	if err != nil {
		t.Fatal(err)
	}
	if !copied.ModTime().Equal(want) || !atimeOf(copied).Equal(wantAccess) {
		t.Fatalf("touch -r gave mtime=%v atime=%v", copied.ModTime(), atimeOf(copied))
	}

	// -c does not create, and a bad -d is refused the way the original words it.
	missing := filepath.Join(dir, "missing")
	if status, _, _ := captureApplet(t, cmdTouch, []string{"-c", missing}, ""); status != 0 {
		t.Fatalf("touch -c on a missing file = %d", status)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatal("touch -c created the file")
	}
	if status, _, stderr := captureApplet(t, cmdTouch, []string{"-d", "nope", file}, ""); status == 0 ||
		!strings.Contains(stderr, "invalid date format 'nope'") {
		t.Fatalf("touch -d with a bad string = (%d, %q)", status, stderr)
	}
}

func TestTouchNoDereferenceActsOnTheSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if status, _, stderr := captureApplet(t, cmdTouch, []string{"-d", "2000-01-01", target}, ""); status != 0 {
		t.Fatalf("touch -d = (%d, %q)", status, stderr)
	}
	if err := os.Symlink("target", link); err != nil {
		t.Fatal(err)
	}
	if status, _, stderr := captureApplet(t, cmdTouch, []string{"-h", "-d", "2020-05-05 06:07:08", link}, ""); status != 0 {
		t.Fatalf("touch -h = (%d, %q)", status, stderr)
	}
	linkInfo, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	wantLink := time.Date(2020, time.May, 5, 6, 7, 8, 0, time.Local)
	if !linkInfo.ModTime().Equal(wantLink) {
		t.Fatalf("touch -h set the link's mtime to %v, want %v", linkInfo.ModTime(), wantLink)
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !targetInfo.ModTime().Equal(time.Date(2000, time.January, 1, 0, 0, 0, 0, time.Local)) {
		t.Fatalf("touch -h changed the target's mtime to %v", targetInfo.ModTime())
	}
}
