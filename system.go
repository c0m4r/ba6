// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
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

func cmdDate(args []string) int {
	utc := false
	formatSet := false
	var reference, format string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			if i+1 < len(args) {
				if format != "" || len(args)-i-1 > 1 {
					fatalf("date", "extra operand %q", args[i+1])
					return 1
				}
				format = args[i+1]
				formatSet = true
			}
			i = len(args)
		case arg == "-u" || arg == "--utc" || arg == "--universal":
			utc = true
		case arg == "-r" || arg == "--reference":
			i++
			if i >= len(args) {
				fatalf("date", "option requires an argument -- 'r'")
				return 1
			}
			reference = args[i]
		case strings.HasPrefix(arg, "--reference="):
			reference = strings.TrimPrefix(arg, "--reference=")
		case strings.HasPrefix(arg, "+"):
			if formatSet {
				fatalf("date", "extra operand %q", arg)
				return 1
			}
			format = arg
			formatSet = true
		default:
			fatalf("date", "unsupported date operand %q", arg)
			return 1
		}
	}
	now := time.Now()
	if reference != "" {
		info, err := os.Stat(reference)
		if err != nil {
			fatalf("date", "%s: %v", reference, err)
			return 1
		}
		now = info.ModTime()
	}
	if utc {
		now = now.UTC()
	}
	if !formatSet {
		format = "+%a %b %e %H:%M:%S %Z %Y"
	}
	if !strings.HasPrefix(format, "+") {
		fatalf("date", "format must begin with '+'")
		return 1
	}
	value, err := formatDate(now, strings.TrimPrefix(format, "+"))
	if err != nil {
		fatalf("date", "%v", err)
		return 1
	}
	if _, err := fmt.Fprintln(os.Stdout, value); err != nil {
		fatalf("date", "write error: %v", err)
		return 1
	}
	return 0
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
		case 'j':
			rendered = fmt.Sprintf("%03d", value.YearDay())
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
		case 'u':
			rendered = strconv.Itoa(int(value.Weekday()+6)%7 + 1)
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
