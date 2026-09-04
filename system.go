// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func cmdTrue(_ []string) int  { return 0 }
func cmdFalse(_ []string) int { return 1 }

func cmdSleep(args []string) int {
	if len(args) == 0 {
		fatalf("sleep", "missing operand")
		return 1
	}
	var total float64
	operands := 0
	for _, value := range args {
		if value == "--" {
			continue
		}
		operands++
		seconds, err := parseSleepDuration(value)
		if err != nil {
			fatalf("sleep", "invalid time interval %q", value)
			return 1
		}
		total += seconds
	}
	if operands == 0 {
		fatalf("sleep", "missing operand")
		return 1
	}
	maxSeconds := float64(math.MaxInt64) / float64(time.Second)
	if math.IsInf(total, 0) || math.IsNaN(total) || total < 0 || total > maxSeconds {
		fatalf("sleep", "time interval is out of range")
		return 1
	}
	time.Sleep(time.Duration(total * float64(time.Second)))
	return 0
}

func parseSleepDuration(value string) (float64, error) {
	multiplier := float64(1)
	if len(value) > 0 {
		switch value[len(value)-1] {
		case 's':
			value = value[:len(value)-1]
		case 'm':
			value = value[:len(value)-1]
			multiplier = 60
		case 'h':
			value = value[:len(value)-1]
			multiplier = 60 * 60
		case 'd':
			value = value[:len(value)-1]
			multiplier = 24 * 60 * 60
		}
	}
	amount, err := strconv.ParseFloat(value, 64)
	if err != nil || amount < 0 || math.IsInf(amount, 0) || math.IsNaN(amount) {
		return 0, fmt.Errorf("invalid duration")
	}
	return amount * multiplier, nil
}

// dateOptions is one parsed date(1) command line.
type dateOptions struct {
	utc       bool
	format    string
	formatSet bool
	reference string
	dateSpec  string
	haveDate  bool
	setSpec   string
	haveSet   bool
	fileName  string
	resolve   bool
}

func cmdDate(args []string) int {
	opts := dateOptions{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			if i+1 < len(args) {
				if opts.formatSet || len(args)-i-1 > 1 {
					fatalf("date", "extra operand %q", args[i+1])
					return 1
				}
				opts.format, opts.formatSet = args[i+1], true
			}
			i = len(args)
		case arg == "-u" || arg == "--utc" || arg == "--universal":
			opts.utc = true
		case arg == "--resolution":
			opts.resolve = true
		case arg == "--rfc-email" || arg == "--rfc-822" || arg == "--rfc-2822":
			opts.format, opts.formatSet = "+%a, %d %b %Y %H:%M:%S %z", true
		case arg == "--iso-8601":
			opts.format, opts.formatSet = isoDateFormat("date"), true
		case strings.HasPrefix(arg, "--iso-8601="):
			spec := strings.TrimPrefix(arg, "--iso-8601=")
			format := isoDateFormat(spec)
			if format == "" {
				fatalf("date", "invalid argument %q for %q", spec, "--iso-8601")
				return 1
			}
			opts.format, opts.formatSet = format, true
		case strings.HasPrefix(arg, "--rfc-3339="):
			spec := strings.TrimPrefix(arg, "--rfc-3339=")
			format := rfc3339DateFormat(spec)
			if format == "" {
				fatalf("date", "invalid argument %q for %q", spec, "--rfc-3339")
				return 1
			}
			opts.format, opts.formatSet = format, true
		case strings.HasPrefix(arg, "--date="):
			opts.dateSpec, opts.haveDate = strings.TrimPrefix(arg, "--date="), true
		case strings.HasPrefix(arg, "--set="):
			opts.setSpec, opts.haveSet = strings.TrimPrefix(arg, "--set="), true
		case strings.HasPrefix(arg, "--reference="):
			opts.reference = strings.TrimPrefix(arg, "--reference=")
		case strings.HasPrefix(arg, "--file="):
			opts.fileName = strings.TrimPrefix(arg, "--file=")
		case arg == "--date" || arg == "--set" || arg == "--reference" || arg == "--file":
			if i+1 >= len(args) {
				fatalf("date", "option %q requires an argument", arg)
				return 1
			}
			i++
			opts.setLongValue(arg, args[i])
		case len(arg) > 1 && arg[0] == '-' && arg[1] != '-':
			if !opts.parseShortCluster(arg, args, &i) {
				return 1
			}
		case strings.HasPrefix(arg, "+"):
			if opts.formatSet {
				fatalf("date", "extra operand %q", arg)
				return 1
			}
			opts.format, opts.formatSet = arg, true
		default:
			fatalf("date", "unsupported date operand %q", arg)
			return 1
		}
	}
	return runDate(&opts)
}

func (o *dateOptions) setLongValue(name, value string) {
	switch name {
	case "--date":
		o.dateSpec, o.haveDate = value, true
	case "--set":
		o.setSpec, o.haveSet = value, true
	case "--reference":
		o.reference = value
	case "--file":
		o.fileName = value
	}
}

// parseShortCluster consumes one bundled short-option word ("-uR", "-d@100",
// "-Iseconds"). Options that take a value swallow the rest of the word, or the
// next argument when the word ends there.
func (o *dateOptions) parseShortCluster(arg string, args []string, i *int) bool {
	for j := 1; j < len(arg); j++ {
		c := arg[j]
		switch c {
		case 'u':
			o.utc = true
		case 'R':
			o.format, o.formatSet = "+%a, %d %b %Y %H:%M:%S %z", true
		case 'I':
			format := isoDateFormat(arg[j+1:])
			if format == "" {
				fatalf("date", "invalid argument %q for %q", arg[j+1:], "--iso-8601")
				return false
			}
			o.format, o.formatSet = format, true
			return true
		case 'd', 's', 'r', 'f':
			value := arg[j+1:]
			if value == "" {
				*i++
				if *i >= len(args) {
					fatalf("date", "option requires an argument -- '%c'", c)
					return false
				}
				value = args[*i]
			}
			switch c {
			case 'd':
				o.dateSpec, o.haveDate = value, true
			case 's':
				o.setSpec, o.haveSet = value, true
			case 'r':
				o.reference = value
			case 'f':
				o.fileName = value
			}
			return true
		default:
			fatalf("date", "invalid option -- '%c'", c)
			return false
		}
	}
	return true
}

// isoDateFormat maps -I's optional argument to a format string; the empty
// string means the argument was not one the original accepts.
func isoDateFormat(spec string) string {
	switch spec {
	case "", "date":
		return "+%Y-%m-%d"
	case "hours":
		return "+%Y-%m-%dT%H%:z"
	case "minutes":
		return "+%Y-%m-%dT%H:%M%:z"
	case "seconds":
		return "+%Y-%m-%dT%H:%M:%S%:z"
	case "ns":
		// ISO 8601 writes the fraction with a comma; the original does too.
		return "+%Y-%m-%dT%H:%M:%S,%N%:z"
	}
	return ""
}

func rfc3339DateFormat(spec string) string {
	switch spec {
	case "date":
		return "+%Y-%m-%d"
	case "seconds":
		return "+%Y-%m-%d %H:%M:%S%:z"
	case "ns":
		return "+%Y-%m-%d %H:%M:%S.%N%:z"
	}
	return ""
}

func runDate(opts *dateOptions) int {
	if opts.resolve {
		if _, err := fmt.Fprintln(os.Stdout, "0.000000001"); err != nil {
			fatalf("date", "write error: %v", err)
			return 1
		}
		return 0
	}
	if opts.haveDate && opts.reference != "" {
		fatalf("date", "the options to print and set the time may not be used together")
		return 1
	}
	now := time.Now()
	if opts.reference != "" {
		info, err := os.Stat(opts.reference)
		if err != nil {
			fatalf("date", "%s: %s", opts.reference, errText(err))
			return 1
		}
		now = info.ModTime()
	}
	status := 0
	if opts.haveSet {
		when, err := parseTimeSpec(opts.setSpec, now)
		if err != nil {
			fatalf("date", "%v", err)
			return 1
		}
		if err := setSystemClock(when); err != nil {
			// The original still prints the requested time on stdout.
			fatalf("date", "cannot set date: %s", errText(err))
			status = 1
		}
		now = when
	}
	if opts.haveDate {
		when, err := parseTimeSpec(opts.dateSpec, now)
		if err != nil {
			fatalf("date", "%v", err)
			return 1
		}
		now = when
	}
	format := opts.format
	if !opts.formatSet {
		format = "+%a %b %e %H:%M:%S %Z %Y"
	}
	if !strings.HasPrefix(format, "+") {
		fatalf("date", "format must begin with '+'")
		return 1
	}
	format = strings.TrimPrefix(format, "+")
	if opts.fileName != "" {
		if fileStatus := dateFromFile(opts, format, now); fileStatus != 0 {
			return fileStatus
		}
		return status
	}
	if opts.utc {
		now = now.UTC()
	}
	if printDate(now, format) != 0 {
		return 1
	}
	return status
}

// dateFromFile implements -f: one date string per line, each printed with the
// same format.
func dateFromFile(opts *dateOptions, format string, base time.Time) int {
	r, err := openInput(opts.fileName)
	if err != nil {
		fatalf("date", "%s: %s", opts.fileName, errText(err))
		return 1
	}
	defer func() { _ = r.Close() }()
	status := 0
	sc := newLineScanner(r)
	for sc.Scan() {
		when, err := parseTimeSpec(sc.Text(), base)
		if err != nil {
			fatalf("date", "%v", err)
			status = 1
			continue
		}
		if opts.utc {
			when = when.UTC()
		}
		if printDate(when, format) != 0 {
			return 1
		}
	}
	if scanErr("date", opts.fileName, sc) {
		status = 1
	}
	return status
}

func printDate(value time.Time, format string) int {
	text, err := formatDate(value, format)
	if err != nil {
		fatalf("date", "%v", err)
		return 1
	}
	if _, err := fmt.Fprintln(os.Stdout, text); err != nil {
		fatalf("date", "write error: %v", err)
		return 1
	}
	return 0
}

// setSystemClock implements date -s. The kernel call needs CAP_SYS_TIME, so an
// unprivileged run reports the same "Operation not permitted" the original does.
func setSystemClock(when time.Time) error {
	tv := syscall.NsecToTimeval(when.UnixNano())
	return syscall.Settimeofday(&tv)
}

func formatDate(value time.Time, format string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			out.WriteByte(format[i])
			continue
		}
		if i+1 >= len(format) {
			return "", fmt.Errorf("trailing %% in format")
		}
		i++
		directive := format[i]
		// %:z and %::z are the only multi-character directives.
		if directive == ':' {
			colons := 1
			for i+colons < len(format) && format[i+colons] == ':' {
				colons++
			}
			if colons > 2 || i+colons >= len(format) || format[i+colons] != 'z' {
				return "", fmt.Errorf("unsupported format directive %%:%c", format[i+1])
			}
			offset := value.Format("-0700")
			out.WriteString(offset[:3] + ":" + offset[3:])
			if colons == 2 {
				out.WriteString(":00")
			}
			i += colons
			continue
		}
		var rendered string
		switch directive {
		case '%':
			rendered = "%"
		case 'a':
			rendered = value.Format("Mon")
		case 'A':
			rendered = value.Format("Monday")
		case 'b', 'h':
			rendered = value.Format("Jan")
		case 'B':
			rendered = value.Format("January")
		case 'c':
			rendered = value.Format("Mon Jan 02 15:04:05 2006")
		case 'C':
			rendered = fmt.Sprintf("%02d", value.Year()/100)
		case 'd':
			rendered = value.Format("02")
		case 'D':
			rendered = value.Format("01/02/06")
		case 'e':
			rendered = fmt.Sprintf("%2d", value.Day())
		case 'F':
			rendered = value.Format("2006-01-02")
		case 'H':
			rendered = value.Format("15")
		case 'I':
			rendered = value.Format("03")
		case 'G':
			isoYear, _ := value.ISOWeek()
			rendered = fmt.Sprintf("%04d", isoYear)
		case 'g':
			isoYear, _ := value.ISOWeek()
			rendered = fmt.Sprintf("%02d", isoYear%100)
		case 'j':
			rendered = fmt.Sprintf("%03d", value.YearDay())
		case 'k':
			rendered = fmt.Sprintf("%2d", value.Hour())
		case 'l':
			hour := value.Hour() % 12
			if hour == 0 {
				hour = 12
			}
			rendered = fmt.Sprintf("%2d", hour)
		case 'm':
			rendered = value.Format("01")
		case 'M':
			rendered = value.Format("04")
		case 'n':
			rendered = "\n"
		case 'N':
			rendered = fmt.Sprintf("%09d", value.Nanosecond())
		case 'p':
			rendered = value.Format("PM")
		case 'P':
			rendered = strings.ToLower(value.Format("PM"))
		case 'r':
			rendered = value.Format("03:04:05 PM")
		case 'R':
			rendered = value.Format("15:04")
		case 's':
			rendered = strconv.FormatInt(value.Unix(), 10)
		case 'S':
			rendered = value.Format("05")
		case 't':
			rendered = "\t"
		case 'T':
			rendered = value.Format("15:04:05")
		case 'q':
			rendered = strconv.Itoa((int(value.Month())-1)/3 + 1)
		case 'u':
			rendered = strconv.Itoa(int(value.Weekday()+6)%7 + 1)
		case 'U':
			rendered = fmt.Sprintf("%02d", weekOfYear(value, time.Sunday))
		case 'V':
			_, isoWeek := value.ISOWeek()
			rendered = fmt.Sprintf("%02d", isoWeek)
		case 'W':
			rendered = fmt.Sprintf("%02d", weekOfYear(value, time.Monday))
		case 'w':
			rendered = strconv.Itoa(int(value.Weekday()))
		case 'x':
			rendered = value.Format("01/02/06")
		case 'X':
			rendered = value.Format("15:04:05")
		case 'y':
			rendered = value.Format("06")
		case 'Y':
			rendered = value.Format("2006")
		case 'z':
			rendered = value.Format("-0700")
		case 'Z':
			rendered = value.Format("MST")
		default:
			return "", fmt.Errorf("unsupported format directive %%%c", directive)
		}
		out.WriteString(rendered)
	}
	return out.String(), nil
}

// weekOfYear renders date's %U (first is Sunday) and %W (first is Monday): the
// week number counting from the year's first such weekday, with the days before
// it in week zero.
func weekOfYear(value time.Time, first time.Weekday) int {
	offset := (int(value.Weekday()) - int(first) + 7) % 7
	return (value.YearDay() - offset + 6) / 7
}
