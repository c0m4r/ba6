// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseTimeSpec parses the human date strings GNU date -d and touch -d accept,
// relative to base. The grammar implemented here is the practical subset: an
// epoch stamp (@SECONDS[.FRACTION]), a calendar date, a clock time, a timezone,
// the day words (now/today/yesterday/tomorrow and weekday names), and relative
// items ("+1 hour", "3 months ago", "next monday"). Items may be combined the
// way the original allows, e.g. "2020-01-02 03:04:05 +0300" or "1 day ago".
//
// Nanoseconds follow the original's rule: any absolute item (a date, a time or
// an epoch stamp) truncates to whole seconds, while a purely relative string
// keeps base's fractional part.
func parseTimeSpec(spec string, base time.Time) (time.Time, error) {
	invalid := func() (time.Time, error) {
		return time.Time{}, fmt.Errorf("invalid date '%s'", spec)
	}
	words := splitTimeSpec(spec)
	if len(words) == 0 {
		// An empty string means today at midnight, as in the original.
		year, month, day := base.Date()
		return time.Date(year, month, day, 0, 0, 0, 0, base.Location()), nil
	}
	if strings.HasPrefix(words[0], "@") {
		if len(words) != 1 {
			return invalid()
		}
		seconds, err := strconv.ParseFloat(words[0][1:], 64)
		if err != nil {
			return invalid()
		}
		whole, frac := int64(seconds), seconds-float64(int64(seconds))
		return time.Unix(whole, int64(frac*1e9)).In(base.Location()), nil
	}

	p := timeSpecParser{base: base, loc: base.Location()}
	for i := 0; i < len(words); i++ {
		if !p.step(words, &i) {
			return invalid()
		}
	}
	return p.result()
}

// timeSpecParser accumulates one date string's items. Absolute fields overwrite
// the corresponding part of base; relative ones are summed and applied last.
type timeSpecParser struct {
	base time.Time
	loc  *time.Location

	haveDate bool
	year     int
	month    time.Month
	day      int

	haveTime bool
	hour     int
	minute   int
	second   int

	relYears  int
	relMonths int
	relDays   int
	relSecs   int64
	// lastRel is the size of the most recent relative item, so a trailing
	// "ago" can reverse just that one - matching the original, where
	// "2 hours 30 minutes ago" is two hours forward and thirty minutes back.
	lastRel relTimeItem

	weekday     time.Weekday
	haveWeekday bool
	weekdayDir  int
}

// relTimeItem is the relative part of one item, kept so "ago" can negate it.
type relTimeItem struct {
	years, months, days int
	secs                int64
	valid               bool
}

var timeSpecMonths = map[string]time.Month{
	"jan": time.January, "january": time.January,
	"feb": time.February, "february": time.February,
	"mar": time.March, "march": time.March,
	"apr": time.April, "april": time.April,
	"may": time.May,
	"jun": time.June, "june": time.June,
	"jul": time.July, "july": time.July,
	"aug": time.August, "august": time.August,
	"sep": time.September, "sept": time.September, "september": time.September,
	"oct": time.October, "october": time.October,
	"nov": time.November, "november": time.November,
	"dec": time.December, "december": time.December,
}

var timeSpecWeekdays = map[string]time.Weekday{
	"sunday": time.Sunday, "sun": time.Sunday,
	"monday": time.Monday, "mon": time.Monday,
	"tuesday": time.Tuesday, "tue": time.Tuesday, "tues": time.Tuesday,
	"wednesday": time.Wednesday, "wed": time.Wednesday,
	"thursday": time.Thursday, "thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday,
	"friday": time.Friday, "fri": time.Friday,
	"saturday": time.Saturday, "sat": time.Saturday,
}

// timeSpecUnits maps a relative unit to (years, months, days, seconds).
var timeSpecUnits = map[string]relTimeItem{
	"year": {years: 1}, "years": {years: 1},
	"month": {months: 1}, "months": {months: 1},
	"fortnight": {days: 14}, "fortnights": {days: 14},
	"week": {days: 7}, "weeks": {days: 7},
	"day": {days: 1}, "days": {days: 1},
	"hour": {secs: 3600}, "hours": {secs: 3600},
	"minute": {secs: 60}, "minutes": {secs: 60}, "min": {secs: 60}, "mins": {secs: 60},
	"second": {secs: 1}, "seconds": {secs: 1}, "sec": {secs: 1}, "secs": {secs: 1},
}

// splitTimeSpec breaks a date string into comparable words: whitespace and
// commas separate, and a digit run glued to a word ("+2days") is split apart.
func splitTimeSpec(spec string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(spec); i++ {
		c := spec[i]
		switch {
		case c == ' ' || c == '\t' || c == ',' || c == '\n':
			flush()
		case isASCIILetter(c) && cur.Len() > 0 && isDigitByte(cur.String()[cur.Len()-1]):
			// "2days" -> "2" "days"; a date like 2020-01-02 has no letters.
			flush()
			cur.WriteByte(lowerByte(c))
		case isDigitByte(c) && cur.Len() > 0 && isASCIILetter(cur.String()[cur.Len()-1]):
			flush()
			cur.WriteByte(c)
		default:
			cur.WriteByte(lowerByte(c))
		}
	}
	flush()
	return words
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigitByte(c byte) bool   { return c >= '0' && c <= '9' }
func lowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// step consumes the word at *i (and any words that belong with it), returning
// false when nothing in the grammar matches.
func (p *timeSpecParser) step(words []string, i *int) bool {
	w := words[*i]
	switch w {
	case "now", "today", "this":
		if w == "this" && *i+1 < len(words) {
			// "this monday", "this week": treat as the plain unit.
			return p.stepQualified(words, i, 0)
		}
		return true
	case "yesterday":
		p.addRel(relTimeItem{days: -1})
		return true
	case "tomorrow":
		p.addRel(relTimeItem{days: 1})
		return true
	case "ago":
		if !p.lastRel.valid {
			return false
		}
		p.relYears -= 2 * p.lastRel.years
		p.relMonths -= 2 * p.lastRel.months
		p.relDays -= 2 * p.lastRel.days
		p.relSecs -= 2 * p.lastRel.secs
		p.lastRel.valid = false
		return true
	case "utc", "gmt", "z":
		p.loc = time.UTC
		return true
	case "t":
		// The ISO 8601 date/time designator, split off as its own word.
		return true
	case "next":
		return p.stepQualified(words, i, 1)
	case "last":
		return p.stepQualified(words, i, -1)
	}
	if day, ok := timeSpecWeekdays[w]; ok {
		p.setWeekday(day, 0)
		return true
	}
	if month, ok := timeSpecMonths[w]; ok {
		return p.acceptMonthName(month, words, i)
	}
	if unit, ok := timeSpecUnits[w]; ok {
		p.addRel(scaleRel(unit, 1))
		return true
	}
	return p.acceptNumeric(words, i)
}

// stepQualified handles "next"/"last"/"this" followed by a unit or weekday.
func (p *timeSpecParser) stepQualified(words []string, i *int, sign int) bool {
	if *i+1 >= len(words) {
		return false
	}
	*i++
	w := words[*i]
	if day, ok := timeSpecWeekdays[w]; ok {
		p.setWeekday(day, sign)
		return true
	}
	if unit, ok := timeSpecUnits[w]; ok {
		p.addRel(scaleRel(unit, sign))
		return true
	}
	return false
}

// acceptMonthName handles "Jan 5", "Jan 5 2020" and a bare month name.
func (p *timeSpecParser) acceptMonthName(month time.Month, words []string, i *int) bool {
	year, day := p.base.Year(), 1
	if p.haveDate {
		year, day = p.year, p.day
	}
	if *i+1 < len(words) {
		if n, err := strconv.Atoi(words[*i+1]); err == nil && n >= 1 && n <= 31 && len(words[*i+1]) <= 2 {
			day = n
			*i++
			if *i+1 < len(words) {
				if y, err := strconv.Atoi(words[*i+1]); err == nil && len(words[*i+1]) == 4 {
					year = y
					*i++
				}
			}
		} else if y, err := strconv.Atoi(words[*i+1]); err == nil && len(words[*i+1]) == 4 {
			year = y
			*i++
		}
	}
	p.setDate(year, month, day)
	return true
}

// acceptNumeric handles every word that starts with a digit or a sign: dates,
// clock times, timezone offsets, bare years and signed relative counts.
func (p *timeSpecParser) acceptNumeric(words []string, i *int) bool {
	w := words[*i]
	if len(w) == 0 {
		return false
	}
	if y, m, d, rest, ok := parseSpecDate(w); ok {
		p.setDate(y, m, d)
		if rest == "" {
			return true
		}
		// An ISO stamp carries its zone in the same word: 03:04:05Z, +02:00.
		if zone := strings.IndexAny(rest, "z+"); zone > 0 {
			if !p.acceptZone(rest[zone:]) {
				return false
			}
			rest = rest[:zone]
		} else if minus := strings.LastIndexByte(rest, '-'); minus > 0 {
			if !p.acceptZone(rest[minus:]) {
				return false
			}
			rest = rest[:minus]
		}
		return p.acceptClock(rest)
	}
	if strings.ContainsRune(w, ':') {
		clock := w
		if zone := strings.LastIndexAny(clock, "+-"); zone > 0 {
			if !p.acceptZone(clock[zone:]) {
				return false
			}
			clock = clock[:zone]
		}
		if p.acceptClock(clock) {
			// "03:04:05 pm" and a following timezone are separate words.
			if *i+1 < len(words) {
				switch words[*i+1] {
				case "am", "pm":
					p.applyMeridiem(words[*i+1])
					*i++
				}
			}
			if *i+1 < len(words) && p.acceptZone(words[*i+1]) {
				*i++
			}
			return true
		}
		return false
	}
	if p.haveTime && p.acceptZone(w) {
		return true
	}
	sign := 1
	body := w
	signed := false
	if body[0] == '+' || body[0] == '-' {
		if body[0] == '-' {
			sign = -1
		}
		signed = true
		body = body[1:]
	}
	n, err := strconv.Atoi(body)
	if err != nil {
		return false
	}
	// A bare 8-digit run is a compact date; 4 digits with no sign is a year.
	if !signed && len(body) == 8 {
		y, m, d := n/10000, time.Month(n/100%100), n%100
		if m >= 1 && m <= 12 && d >= 1 && d <= 31 {
			p.setDate(y, m, d)
			return true
		}
	}
	if !signed && len(body) == 4 && p.haveDate {
		p.setDate(n, p.month, p.day)
		return true
	}
	if !signed && n <= 24 && *i+1 < len(words) && (words[*i+1] == "am" || words[*i+1] == "pm") {
		// A bare hour with a meridiem: "3pm".
		p.haveTime = true
		p.hour, p.minute, p.second = n, 0, 0
		p.applyMeridiem(words[*i+1])
		*i++
		return true
	}
	if !signed && n >= 1 && n <= 31 && *i+1 < len(words) {
		if month, ok := timeSpecMonths[words[*i+1]]; ok {
			*i++
			p.setDate(p.base.Year(), month, n)
			if *i+1 < len(words) {
				if y, err := strconv.Atoi(words[*i+1]); err == nil && len(words[*i+1]) == 4 {
					p.setDate(y, month, n)
					*i++
				}
			}
			return true
		}
	}
	// Otherwise the number counts a unit named by the next word.
	if *i+1 < len(words) {
		if unit, ok := timeSpecUnits[words[*i+1]]; ok {
			*i++
			p.addRel(scaleRel(unit, sign*n))
			return true
		}
	}
	return false
}

// acceptClock parses HH:MM[:SS[.frac]] and the compact HHMM/HHMMSS forms.
func (p *timeSpecParser) acceptClock(w string) bool {
	body := w
	meridiem := ""
	for _, suffix := range []string{"am", "pm"} {
		if strings.HasSuffix(body, suffix) {
			meridiem = suffix
			body = strings.TrimSuffix(body, suffix)
		}
	}
	var hour, minute, second int
	switch parts := strings.Split(body, ":"); len(parts) {
	case 1:
		if len(body) != 4 && len(body) != 6 {
			return false
		}
		n, err := strconv.Atoi(body)
		if err != nil {
			return false
		}
		if len(body) == 4 {
			hour, minute = n/100, n%100
		} else {
			hour, minute, second = n/10000, n/100%100, n%100
		}
	case 2, 3:
		var err error
		if hour, err = strconv.Atoi(parts[0]); err != nil {
			return false
		}
		if minute, err = strconv.Atoi(parts[1]); err != nil {
			return false
		}
		if len(parts) == 3 {
			secText := parts[2]
			if dot := strings.IndexByte(secText, '.'); dot >= 0 {
				secText = secText[:dot]
			}
			if second, err = strconv.Atoi(secText); err != nil {
				return false
			}
		}
	default:
		return false
	}
	if hour > 24 || minute > 59 || second > 61 {
		return false
	}
	p.haveTime = true
	p.hour, p.minute, p.second = hour, minute, second
	if meridiem != "" {
		p.applyMeridiem(meridiem)
	}
	return true
}

func (p *timeSpecParser) applyMeridiem(meridiem string) {
	if meridiem == "pm" && p.hour < 12 {
		p.hour += 12
	}
	if meridiem == "am" && p.hour == 12 {
		p.hour = 0
	}
}

// acceptZone parses a numeric UTC offset, ±HH, ±HHMM or ±HH:MM.
func (p *timeSpecParser) acceptZone(w string) bool {
	if w == "utc" || w == "gmt" || w == "z" {
		p.loc = time.UTC
		return true
	}
	if len(w) < 2 || (w[0] != '+' && w[0] != '-') {
		return false
	}
	sign := 1
	if w[0] == '-' {
		sign = -1
	}
	body := strings.Replace(w[1:], ":", "", 1)
	if len(body) != 2 && len(body) != 4 {
		return false
	}
	n, err := strconv.Atoi(body)
	if err != nil {
		return false
	}
	hours, minutes := n, 0
	if len(body) == 4 {
		hours, minutes = n/100, n%100
	}
	if hours > 24 || minutes > 59 {
		return false
	}
	p.loc = time.FixedZone("", sign*(hours*3600+minutes*60))
	return true
}

// parseSpecDate recognises YYYY-MM-DD, YYYY/MM/DD and MM/DD/YYYY, optionally
// with a "T"-joined clock time, which is returned as rest.
func parseSpecDate(w string) (int, time.Month, int, string, bool) {
	rest := ""
	if t := strings.IndexByte(w, 't'); t > 0 && strings.ContainsAny(w[:t], "-/") {
		rest, w = w[t+1:], w[:t]
	}
	sep := byte('-')
	if !strings.ContainsRune(w, '-') {
		sep = '/'
	}
	parts := strings.Split(w, string(sep))
	if len(parts) != 3 {
		return 0, 0, 0, "", false
	}
	nums := make([]int, 3)
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0, 0, 0, "", false
		}
		nums[i] = n
	}
	year, month, day := nums[0], nums[1], nums[2]
	if sep == '/' && len(parts[2]) == 4 {
		// The US ordering: MM/DD/YYYY.
		year, month, day = nums[2], nums[0], nums[1]
	}
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return 0, 0, 0, "", false
	}
	return year, time.Month(month), day, rest, true
}

func scaleRel(unit relTimeItem, n int) relTimeItem {
	return relTimeItem{
		years:  unit.years * n,
		months: unit.months * n,
		days:   unit.days * n,
		secs:   unit.secs * int64(n),
		valid:  true,
	}
}

func (p *timeSpecParser) addRel(item relTimeItem) {
	p.relYears += item.years
	p.relMonths += item.months
	p.relDays += item.days
	p.relSecs += item.secs
	item.valid = true
	p.lastRel = item
}

func (p *timeSpecParser) setDate(year int, month time.Month, day int) {
	p.haveDate = true
	p.year, p.month, p.day = year, month, day
}

// setWeekday records "monday", "next monday" or "last friday". Direction 0 and
// 1 both move forward to the named day (0 keeps today if it already matches),
// while -1 moves back.
func (p *timeSpecParser) setWeekday(day time.Weekday, direction int) {
	p.haveWeekday = true
	p.weekday = day
	p.weekdayDir = direction
}

func (p *timeSpecParser) result() (time.Time, error) {
	base := p.base.In(p.loc)
	year, month, day := base.Date()
	hour, minute, second, nanos := base.Hour(), base.Minute(), base.Second(), base.Nanosecond()
	if p.haveDate {
		year, month, day = p.year, p.month, p.day
		if !p.haveTime {
			hour, minute, second = 0, 0, 0
		}
		nanos = 0
	}
	if p.haveTime {
		hour, minute, second, nanos = p.hour, p.minute, p.second, 0
		if !p.haveDate {
			year, month, day = base.Date()
		}
	}
	if p.haveWeekday && !p.haveTime {
		hour, minute, second, nanos = 0, 0, 0, 0
	}
	value := time.Date(year, month, day, hour, minute, second, nanos, p.loc)
	if p.haveWeekday {
		value = advanceToWeekday(value, p.weekday, p.weekdayDir)
	}
	value = value.AddDate(p.relYears, p.relMonths, p.relDays)
	value = value.Add(time.Duration(p.relSecs) * time.Second)
	return value.In(p.base.Location()), nil
}

// advanceToWeekday implements the original's weekday arithmetic: "monday" and
// "next monday" both land on the coming Monday (today counts for the bare form,
// but "next" always moves at least one week when the day already matches),
// while "last monday" walks backwards.
func advanceToWeekday(value time.Time, want time.Weekday, direction int) time.Time {
	delta := (int(want) - int(value.Weekday()) + 7) % 7
	switch {
	case direction < 0:
		back := (int(value.Weekday()) - int(want) + 7) % 7
		if back == 0 {
			back = 7
		}
		return value.AddDate(0, 0, -back)
	case direction > 0 && delta == 0:
		return value.AddDate(0, 0, 7)
	default:
		return value.AddDate(0, 0, delta)
	}
}

// parseTouchStamp parses touch -t's [[CC]YY]MMDDhhmm[.ss].
func parseTouchStamp(stamp string, base time.Time) (time.Time, error) {
	invalid := func() (time.Time, error) {
		return time.Time{}, fmt.Errorf("invalid date format '%s'", stamp)
	}
	body, secText := stamp, ""
	if dot := strings.IndexByte(body, '.'); dot >= 0 {
		body, secText = body[:dot], body[dot+1:]
	}
	for i := 0; i < len(body); i++ {
		if !isDigitByte(body[i]) {
			return invalid()
		}
	}
	second := 0
	if secText != "" {
		if len(secText) != 2 {
			return invalid()
		}
		n, err := strconv.Atoi(secText)
		if err != nil || n > 61 {
			return invalid()
		}
		second = n
	}
	year := base.Year()
	switch len(body) {
	case 8: // MMDDhhmm
	case 10: // YYMMDDhhmm
		yy, err := strconv.Atoi(body[:2])
		if err != nil {
			return invalid()
		}
		// POSIX: 69-99 mean 1969-1999, 00-68 mean 2000-2068.
		year = 2000 + yy
		if yy >= 69 {
			year = 1900 + yy
		}
		body = body[2:]
	case 12: // CCYYMMDDhhmm
		y, err := strconv.Atoi(body[:4])
		if err != nil {
			return invalid()
		}
		year = y
		body = body[4:]
	default:
		return invalid()
	}
	fields := make([]int, 4)
	for i := range fields {
		n, err := strconv.Atoi(body[i*2 : i*2+2])
		if err != nil {
			return invalid()
		}
		fields[i] = n
	}
	month, day, hour, minute := fields[0], fields[1], fields[2], fields[3]
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 {
		return invalid()
	}
	return time.Date(year, time.Month(month), day, hour, minute, second, 0, base.Location()), nil
}
