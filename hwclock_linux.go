// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// rtcTime is the kernel's struct rtc_time, in binary form for the RTC ioctls.
type rtcTime struct {
	sec, min, hour, mday, mon, year, wday, yday, isdst int32
}

const (
	rtcReadTime  = 0x80247009
	rtcSetTime   = 0x4024700a
	rtcReadTime0 = rtcReadTime
)

// readRTC reads the hardware clock: the RTC_RD_TIME ioctl on /dev/rtc (or
// /dev/rtc0), falling back to the sysfs date/time files on systems without a
// usable RTC device node.
func readRTC() (time.Time, error) {
	for _, device := range []string{"/dev/rtc", "/dev/rtc0"} {
		fd, err := syscall.Open(device, syscall.O_RDONLY, 0)
		if err != nil {
			continue
		}
		var tm rtcTime
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), rtcReadTime0, uintptr(unsafe.Pointer(&tm))) //nolint:gosec // G103: fixed rtcTime buffer for RTC_RD_TIME.
		syscall.Close(fd)
		if errno != 0 {
			continue
		}
		return time.Date(int(tm.year)+1900, time.Month(tm.mon+1), int(tm.mday),
			int(tm.hour), int(tm.min), int(tm.sec), 0, time.UTC), nil
	}
	if value, err := readSysfsRTC(); err == nil {
		return value, nil
	}
	return time.Time{}, fmt.Errorf("cannot access the hardware clock")
}

// setRTC writes the hardware clock with RTC_SET_TIME, falling back to the
// sysfs interface.
func setRTC(t time.Time) error {
	for _, device := range []string{"/dev/rtc", "/dev/rtc0"} {
		fd, err := syscall.Open(device, syscall.O_RDWR, 0)
		if err != nil {
			continue
		}
		tm := rtcTime{
			sec:   int32(t.Second()),      //nolint:gosec // G115: time fields are small bounded values.
			min:   int32(t.Minute()),      //nolint:gosec // G115: see above.
			hour:  int32(t.Hour()),        //nolint:gosec // G115: see above.
			mday:  int32(t.Day()),         //nolint:gosec // G115: see above.
			mon:   int32(t.Month()) - 1,   //nolint:gosec // G115: see above.
			year:  int32(t.Year()) - 1900, //nolint:gosec // G115: see above.
			wday:  int32(t.Weekday()),     //nolint:gosec // G115: see above.
			isdst: -1,
		}
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), rtcSetTime, uintptr(unsafe.Pointer(&tm))) //nolint:gosec // G103: fixed rtcTime buffer for RTC_SET_TIME.
		syscall.Close(fd)
		if errno == 0 {
			return nil
		}
	}
	for _, base := range []string{"/sys/class/rtc/rtc0", "/sys/class/rtc/rtc"} {
		date := base + "/date"
		if _, err := os.Stat(date); err == nil {
			if err := os.WriteFile(date, []byte(t.Format("2006-01-02")), 0o600); err != nil { //nolint:gosec // G306: sysfs ignores the mode; the store is write-only.
				return err
			}
			if err := os.WriteFile(base+"/time", []byte(t.Format("15:04:05")), 0o600); err != nil { //nolint:gosec // G306: sysfs ignores the mode; the store is write-only.
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("cannot access the hardware clock")
}

// readSysfsRTC reads the RTC through the sysfs date/time files.
func readSysfsRTC() (time.Time, error) {
	for _, base := range []string{"/sys/class/rtc/rtc0", "/sys/class/rtc/rtc"} {
		date, err1 := os.ReadFile(base + "/date")
		tod, err2 := os.ReadFile(base + "/time")
		if err1 != nil || err2 != nil {
			continue
		}
		parts := strings.Split(strings.TrimSpace(string(tod)), ":")
		if len(parts) != 3 {
			continue
		}
		sec, err1 := strconv.Atoi(parts[2])
		min, err2 := strconv.Atoi(parts[1])
		hour, err3 := strconv.Atoi(parts[0])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		value, err := time.Parse("2006-01-02", strings.TrimSpace(string(date)))
		if err != nil {
			continue
		}
		return value.Add(time.Duration(hour)*time.Hour + time.Duration(min)*time.Minute + time.Duration(sec)*time.Second), nil
	}
	return time.Time{}, io.EOF
}

// cmdHwclock implements hwclock(1): read and set the hardware clock. The
// default (and -r/--show) prints the RTC time in UTC; -s/--hctosys sets the
// system clock from it and -w/--systohc or --set --date write it back.
func cmdHwclock(args []string) int {
	mode := "show"
	utc := true
	var setDate string
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-r" || a == "--show":
			mode = "show"
		case a == "--get":
			mode = "get"
		case a == "-s" || a == "--hctosys":
			mode = "hctosys"
		case a == "-w" || a == "--systohc":
			mode = "systohc"
		case a == "--set":
			mode = "set"
		case a == "--date":
			i++
			if i >= len(args) {
				fatalf("hwclock", "option requires an argument -- 'date'")
				return 1
			}
			setDate = args[i] //nolint:gosec // G602: i is checked against len(args) immediately above.
		case strings.HasPrefix(a, "--date="):
			setDate = strings.TrimPrefix(a, "--date=")
		case a == "--utc" || a == "-u":
			utc = true
		case a == "--localtime" || a == "-l":
			utc = false
		case a == "--":
			i++
			goto rest
		case len(a) > 1 && a[0] == '-':
			fatalf("hwclock", "invalid option %q", a)
			return 1
		}
	}
rest:
	if i < len(args) {
		fatalf("hwclock", "extra operand %q", args[i])
		return 1
	}
	zone := time.UTC
	if !utc {
		zone = time.Local
	}
	rtcToSystem := func(t time.Time) time.Time {
		if utc {
			return t
		}
		// The RTC holds the local wall time; reinterpret its fields.
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.Local)
	}
	systemToRTC := func(t time.Time) time.Time {
		if utc {
			return t.UTC()
		}
		// The RTC stores the local wall time.
		local := t.Local()
		return time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), local.Second(), 0, time.UTC)
	}
	switch mode {
	case "show", "get":
		rtc, err := readRTC()
		if err != nil {
			fatalf("hwclock", "%v", err)
			return 1
		}
		now := time.Now()
		display := time.Date(rtc.Year(), rtc.Month(), rtc.Day(), rtc.Hour(), rtc.Minute(), rtc.Second(), now.Nanosecond(), zone)
		fmt.Printf("%04d-%02d-%02d %02d:%02d:%02d.%06d%s\n",
			display.Year(), display.Month(), display.Day(), display.Hour(), display.Minute(), display.Second(),
			display.Nanosecond()/1000, zoneOffset(display))
		return 0
	case "hctosys":
		rtc, err := readRTC()
		if err != nil {
			fatalf("hwclock", "%v", err)
			return 1
		}
		tv := syscall.NsecToTimeval(rtcToSystem(rtc).UnixNano())
		if err := syscall.Settimeofday(&tv); err != nil {
			fatalf("hwclock", "cannot set the system clock: %s", errText(err))
			return 1
		}
		return 0
	case "systohc":
		if err := setRTC(systemToRTC(time.Now())); err != nil {
			fatalf("hwclock", "%v", err)
			return 1
		}
		return 0
	case "set":
		if setDate == "" {
			fatalf("hwclock", "--set requires --date")
			return 1
		}
		parsed, ok := parseHwclockDate(setDate)
		if !ok {
			fatalf("hwclock", "invalid date %q", setDate)
			return 1
		}
		if err := setRTC(systemToRTC(parsed)); err != nil {
			fatalf("hwclock", "%v", err)
			return 1
		}
		return 0
	}
	return 0
}

// zoneOffset renders the zone's offset suffix in the +HH:MM form hwclock
// prints.
func zoneOffset(t time.Time) string {
	_, offset := t.Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("%s%02d:%02d", sign, offset/3600, offset%3600/60)
}

// parseHwclockDate accepts the forms --set --date uses most: an RFC3339
// timestamp, "YYYY-MM-DD HH:MM:SS", and "HH:MM:SS".
func parseHwclockDate(spec string) (time.Time, bool) {
	if value, err := time.Parse(time.RFC3339, spec); err == nil {
		return value, true
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "15:04:05"} {
		if value, err := time.ParseInLocation(layout, spec, time.Local); err == nil {
			return value, true
		}
	}
	return time.Time{}, false
}
