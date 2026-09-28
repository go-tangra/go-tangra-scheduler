package cron

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Bounds.
const (
	// MaxExprBytes is the longest accepted expression.
	MaxExprBytes = 128
	// MaxLocationBytes is the longest accepted time zone name.
	MaxLocationBytes = 64
	// searchYears is how far Next looks ahead.
	searchYears = 5
)

// ErrNever is returned by Parse for an expression that can never fire.
var ErrNever = errors.New("cron: expression never fires")

// neverRef is the reference instant of the never-fires check; the following
// five years contain two leap days (2024-02-29 and 2028-02-29).
var neverRef = time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC)

// ParseError reports an invalid expression. Field is "minute", "hour",
// "day-of-month", "month", "day-of-week" or "expression".
type ParseError struct {
	Field string
	Msg   string
}

func (e *ParseError) Error() string { return "cron: " + e.Field + ": " + e.Msg }

type fieldSpec struct {
	name     string
	min, max int
	names    map[string]int
}

var specs = [5]fieldSpec{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day-of-month", min: 1, max: 31},
	{name: "month", min: 1, max: 12, names: map[string]int{
		"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
		"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
	}},
	{name: "day-of-week", min: 0, max: 7, names: map[string]int{
		"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6,
	}},
}

// Schedule is a parsed cron expression. Fields are bit sets indexed by value.
type Schedule struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
	text                          [5]string
}

// Parse parses a 5-field cron expression.
func Parse(expr string) (*Schedule, error) {
	if len(expr) > MaxExprBytes {
		return nil, &ParseError{"expression", fmt.Sprintf("longer than %d bytes", MaxExprBytes)}
	}
	for i := 0; i < len(expr); i++ {
		if expr[i] >= 0x80 {
			return nil, &ParseError{"expression", "non-ASCII character"}
		}
	}
	parts := strings.Fields(expr)
	if len(parts) != len(specs) {
		return nil, &ParseError{"expression", fmt.Sprintf("want 5 fields, got %d", len(parts))}
	}
	s := &Schedule{}
	sets := [5]*uint64{&s.minute, &s.hour, &s.dom, &s.month, &s.dow}
	for i, p := range parts {
		bits, text, err := parseField(p, specs[i])
		if err != nil {
			return nil, &ParseError{specs[i].name, err.Error()}
		}
		*sets[i] = bits
		s.text[i] = text
	}
	if s.dow&(1<<7) != 0 { // 7 is Sunday
		s.dow = s.dow&^(1<<7) | 1
	}
	s.domStar = parts[2] == "*"
	s.dowStar = parts[4] == "*"
	if _, ok := s.Next(neverRef, time.UTC); !ok {
		return nil, ErrNever
	}
	return s, nil
}

func parseField(field string, f fieldSpec) (uint64, string, error) {
	items := strings.Split(field, ",")
	out := make([]string, len(items))
	var bits uint64
	for i, item := range items {
		b, text, err := parseItem(item, f)
		if err != nil {
			return 0, "", err
		}
		bits |= b
		out[i] = text
	}
	return bits, strings.Join(out, ","), nil
}

func parseItem(item string, f fieldSpec) (uint64, string, error) {
	if item == "" {
		return 0, "", errors.New("empty item")
	}
	rng, stepText, hasStep := strings.Cut(item, "/")
	step := 1
	if hasStep {
		n, ok := number(stepText)
		if !ok {
			return 0, "", fmt.Errorf("invalid step %q", stepText)
		}
		if width := f.max - f.min + 1; n < 1 || n > width {
			return 0, "", fmt.Errorf("step %d out of range 1-%d", n, width)
		}
		step = n
	}
	var lo, hi int
	var text string
	if rng == "*" {
		lo, hi, text = f.min, f.max, "*"
	} else if a, b, isRange := strings.Cut(rng, "-"); isRange {
		var err error
		if lo, err = value(a, f); err != nil {
			return 0, "", err
		}
		if hi, err = value(b, f); err != nil {
			return 0, "", err
		}
		if lo > hi {
			return 0, "", fmt.Errorf("reversed range %d-%d", lo, hi)
		}
		text = strconv.Itoa(lo) + "-" + strconv.Itoa(hi)
	} else {
		var err error
		if lo, err = value(rng, f); err != nil {
			return 0, "", err
		}
		hi, text = lo, strconv.Itoa(lo)
		if hasStep {
			hi = f.max
		}
	}
	var bits uint64
	for v := lo; v <= hi; v += step {
		bits |= 1 << uint(v)
	}
	if hasStep {
		text += "/" + strconv.Itoa(step)
	}
	return bits, text, nil
}

// value parses a field value (a number or, where allowed, a name).
func value(s string, f fieldSpec) (int, error) {
	if v, ok := f.names[strings.ToUpper(s)]; ok {
		return v, nil
	}
	n, ok := number(s)
	if !ok {
		return 0, fmt.Errorf("invalid value %q", s)
	}
	if n < f.min || n > f.max {
		return 0, fmt.Errorf("value %d out of range %d-%d", n, f.min, f.max)
	}
	return n, nil
}

// number parses 1 to 4 ASCII digits.
func number(s string) (int, bool) {
	if s == "" || len(s) > 4 {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// String returns the normalised expression: single spaces between fields,
// names replaced by numbers, redundant leading zeros removed.
func (s *Schedule) String() string { return strings.Join(s.text[:], " ") }

func (s *Schedule) matchDay(day time.Time) bool {
	domOK := s.dom&(1<<uint(day.Day())) != 0
	dowOK := s.dow&(1<<uint(day.Weekday())) != 0
	if !s.domStar && !s.dowStar {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// Next returns the first occurrence strictly after `after`, evaluated on the
// wall clock of loc (nil means UTC), and false when there is none within
// five years. See the package documentation for the DST rules.
func (s *Schedule) Next(after time.Time, loc *time.Location) (time.Time, bool) {
	if loc == nil {
		loc = time.UTC
	}
	local := after.In(loc)
	y, m, d := local.Date()
	// Calendar days are walked in UTC so day arithmetic ignores DST.
	first := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	end := first.AddDate(searchYears, 0, 0)
	startKey := local.Hour()*60 + local.Minute()
	for day := first; !day.After(end); {
		if s.month&(1<<uint(day.Month())) == 0 {
			day = time.Date(day.Year(), day.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if s.matchDay(day) {
			minKey := 0
			if day.Equal(first) {
				// Wall times earlier than after's wall time on the same date
				// are all before `after`, including across a fall-back.
				minKey = startKey
			}
			if t, ok := s.firstInDay(day, minKey, after, loc); ok {
				return t, true
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return time.Time{}, false
}

func (s *Schedule) firstInDay(day time.Time, minKey int, after time.Time, loc *time.Location) (time.Time, bool) {
	y, m, d := day.Date()
	for h := minKey / 60; h < 24; h++ {
		if s.hour&(1<<uint(h)) == 0 {
			continue
		}
		for mi := 0; mi < 60; mi++ {
			if s.minute&(1<<uint(mi)) == 0 || h*60+mi < minKey {
				continue
			}
			t, ok := wallTime(y, m, d, h, mi, loc)
			if ok && t.After(after) {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// wallTime returns the instant of a local wall-clock time: false when the
// time does not exist (DST gap), the earlier instant when it exists twice.
func wallTime(y int, m time.Month, d, h, mi int, loc *time.Location) (time.Time, bool) {
	t := time.Date(y, m, d, h, mi, 0, 0, loc)
	if t.Hour() != h || t.Minute() != mi || t.Day() != d {
		return time.Time{}, false // normalised: the wall time is skipped by a transition
	}
	// time.Date may return either instant of a repeated wall time; the
	// earlier one uses the offset in effect before the transition.
	_, before := t.Add(-12 * time.Hour).Zone()
	wall := time.Date(y, m, d, h, mi, 0, 0, time.UTC)
	if c := wall.Add(-time.Duration(before) * time.Second).In(loc); c.Before(t) &&
		c.Hour() == h && c.Minute() == mi && c.Day() == d {
		return c, true
	}
	return t, true
}

// Preview returns up to n occurrences after `after`.
func (s *Schedule) Preview(after time.Time, loc *time.Location, n int) []time.Time {
	if n <= 0 {
		return nil
	}
	out := make([]time.Time, 0, min(n, 64))
	for len(out) < n {
		t, ok := s.Next(after, loc)
		if !ok {
			break
		}
		out = append(out, t)
		after = t
	}
	return out
}

// MinGap returns the smallest interval between consecutive occurrences among
// the next n+1 occurrences after `after`, or 0 when there are fewer than two.
func (s *Schedule) MinGap(after time.Time, loc *time.Location, n int) time.Duration {
	ts := s.Preview(after, loc, n+1)
	var gap time.Duration
	for i := 1; i < len(ts); i++ {
		if g := ts[i].Sub(ts[i-1]); i == 1 || g < gap {
			gap = g
		}
	}
	return gap
}

// Count returns the number of occurrences in (from, to], stopping at limit.
func (s *Schedule) Count(from, to time.Time, loc *time.Location, limit int) int {
	n := 0
	for n < limit {
		t, ok := s.Next(from, loc)
		if !ok || t.After(to) {
			break
		}
		n++
		from = t
	}
	return n
}

// LoadLocation resolves an IANA time zone name; "" is UTC. "Local" and names
// longer than MaxLocationBytes are refused.
func LoadLocation(name string) (*time.Location, error) {
	switch {
	case name == "":
		return time.UTC, nil
	case len(name) > MaxLocationBytes:
		return nil, fmt.Errorf("cron: time zone name longer than %d bytes", MaxLocationBytes)
	case strings.EqualFold(name, "Local"):
		return nil, errors.New(`cron: time zone "Local" is not allowed`)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("cron: unknown time zone %q", name)
	}
	return loc, nil
}
