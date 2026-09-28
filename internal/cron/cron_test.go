package cron

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func mustParse(t *testing.T, expr string) *Schedule {
	t.Helper()
	s, err := Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q): %v", expr, err)
	}
	return s
}

func utc(y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, time.UTC)
}

func TestParseValid(t *testing.T) {
	tests := []struct {
		expr string
		want string
	}{
		{"* * * * *", "* * * * *"},
		{"  0   3 *\t* *  ", "0 3 * * *"},
		{"*/15 * * * *", "*/15 * * * *"},
		{"0 8 * * 1-5", "0 8 * * 1-5"},
		{"0 0 1 */3 *", "0 0 1 */3 *"},
		{"30 2 29 2 *", "30 2 29 2 *"},
		{"0 0 13 * 5", "0 0 13 * 5"},
		{"0,30 9-17/2 * * *", "0,30 9-17/2 * * *"},
		{"5/20 * * * *", "5/20 * * * *"},
		{"0 0 * jan,Jul *", "0 0 * 1,7 *"},
		{"0 0 * JAN-mar *", "0 0 * 1-3 *"},
		{"0 0 * * mon-FRI", "0 0 * * 1-5"},
		{"0 0 * * sun,sat", "0 0 * * 0,6"},
		{"0 0 * * 7", "0 0 * * 7"},
		{"0 0 * * 5-7", "0 0 * * 5-7"},
		{"0 0 * * 1/2", "0 0 * * 1/2"},
		{"00 03 * * *", "0 3 * * *"},
		{"59 23 31 12 6", "59 23 31 12 6"},
		{"*/60 */24 */31 */12 */8", "*/60 */24 */31 */12 */8"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			s := mustParse(t, tt.expr)
			if got := s.String(); got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
			again := mustParse(t, s.String())
			if !sameSchedule(s, again) {
				t.Fatalf("re-parse of %q differs", s.String())
			}
		})
	}
}

func TestParseSets(t *testing.T) {
	s := mustParse(t, "0 0 * * 7")
	if s.dow != 1 {
		t.Fatalf("7 must fold to Sunday: dow=%b", s.dow)
	}
	s = mustParse(t, "0 0 * * 1/2")
	if s.dow != 1<<1|1<<3|1<<5|1<<0 {
		t.Fatalf("1/2 = %b", s.dow)
	}
	s = mustParse(t, "0 9-17/4 * * *")
	if s.hour != 1<<9|1<<13|1<<17 {
		t.Fatalf("9-17/4 = %b", s.hour)
	}
	s = mustParse(t, "* * * * *")
	if !s.domStar || !s.dowStar {
		t.Fatal("star flags not set")
	}
	s = mustParse(t, "* * 1-31 * 0-6")
	if s.domStar || s.dowStar {
		t.Fatal("explicit full ranges are restricted fields")
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		expr  string
		field string
		msg   string
	}{
		{"", "expression", "want 5 fields, got 0"},
		{"* * * *", "expression", "want 5 fields, got 4"},
		{"* * * * * *", "expression", "want 5 fields, got 6"},
		{strings.Repeat("1", 129), "expression", "longer than 128 bytes"},
		{"* * * * mön", "expression", "non-ASCII"},
		{"60 * * * *", "minute", "out of range 0-59"},
		{"* 24 * * *", "hour", "out of range 0-23"},
		{"* * 0 * *", "day-of-month", "out of range 1-31"},
		{"* * 32 * *", "day-of-month", "out of range 1-31"},
		{"* * * 0 *", "month", "out of range 1-12"},
		{"* * * 13 *", "month", "out of range 1-12"},
		{"* * * * 8", "day-of-week", "out of range 0-7"},
		{"*/0 * * * *", "minute", "step 0 out of range 1-60"},
		{"*/61 * * * *", "minute", "step 61 out of range 1-60"},
		{"* * * * */9", "day-of-week", "step 9 out of range 1-8"},
		{"5-1 * * * *", "minute", "reversed range 5-1"},
		{"* * * DEC-JAN *", "month", "reversed range 12-1"},
		{"1,,2 * * * *", "minute", "empty item"},
		{"1, * * * *", "minute", "empty item"},
		{"a * * * *", "minute", `invalid value "a"`},
		{"* * * JANUARY *", "month", `invalid value "JANUARY"`},
		{"* * * * MON/2", "day-of-week", ""},
		{"* * * * */x", "day-of-week", `invalid step "x"`},
		{"1/2/3 * * * *", "minute", `invalid step "2/3"`},
		{"-1 * * * *", "minute", `invalid value ""`},
		{"1- * * * *", "minute", `invalid value ""`},
		{"1-2-3 * * * *", "minute", `invalid value "2-3"`},
		{"+5 * * * *", "minute", `invalid value "+5"`},
		{"00005 * * * *", "minute", `invalid value "00005"`},
		{"*-5 * * * *", "minute", `invalid value "*"`},
		{"* * * * JAN", "day-of-week", `invalid value "JAN"`},
		{"* * * * SUN-", "day-of-week", `invalid value ""`},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			s, err := Parse(tt.expr)
			if err == nil {
				if tt.msg == "" {
					return // accepted form documented by the table
				}
				t.Fatalf("Parse(%q) = %v, want error", tt.expr, s)
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("error %T %v is not *ParseError", err, err)
			}
			if pe.Field != tt.field {
				t.Fatalf("field = %q, want %q (%v)", pe.Field, tt.field, err)
			}
			if !strings.Contains(pe.Msg, tt.msg) {
				t.Fatalf("msg = %q, want it to contain %q", pe.Msg, tt.msg)
			}
			if want := "cron: " + tt.field + ": "; !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("Error() = %q, want prefix %q", err.Error(), want)
			}
		})
	}
}

func TestParseNeverFires(t *testing.T) {
	for _, expr := range []string{"0 0 30 2 *", "0 0 31 4 *", "0 0 31 2,4,6,9,11 *", "0 0 30,31 feb *"} {
		if _, err := Parse(expr); !errors.Is(err, ErrNever) {
			t.Fatalf("Parse(%q) err = %v, want ErrNever", expr, err)
		}
	}
	// A restricted day-of-week rescues an impossible day-of-month (OR rule).
	mustParse(t, "0 0 30 2 1")
	// Leap day fires.
	mustParse(t, "0 0 29 2 *")
}

func TestNextUTC(t *testing.T) {
	mon := utc(2026, 9, 28, 10, 7) // Monday
	tests := []struct {
		expr  string
		after time.Time
		want  time.Time
	}{
		{"0 3 * * *", mon, utc(2026, 9, 29, 3, 0)},
		{"0 3 * * *", utc(2026, 9, 28, 2, 59), utc(2026, 9, 28, 3, 0)},
		{"*/15 * * * *", mon, utc(2026, 9, 28, 10, 15)},
		{"*/15 * * * *", utc(2026, 9, 28, 10, 15), utc(2026, 9, 28, 10, 30)},
		{"*/15 * * * *", utc(2026, 9, 28, 23, 50), utc(2026, 9, 29, 0, 0)},
		{"*/15 * * * *", time.Date(2026, 9, 28, 10, 14, 59, 999, time.UTC), utc(2026, 9, 28, 10, 15)},
		{"0 8 * * 1-5", utc(2026, 10, 2, 9, 0), utc(2026, 10, 5, 8, 0)},
		{"0 8 * * 1-5", mon, utc(2026, 9, 29, 8, 0)},
		{"0 0 1 */3 *", mon, utc(2026, 10, 1, 0, 0)},
		{"0 0 1 */3 *", utc(2026, 10, 1, 0, 0), utc(2027, 1, 1, 0, 0)},
		{"30 2 29 2 *", utc(2026, 1, 1, 0, 0), utc(2028, 2, 29, 2, 30)},
		{"30 2 29 2 *", utc(2024, 2, 29, 2, 29), utc(2024, 2, 29, 2, 30)},
		{"0 0 13 * 5", mon, utc(2026, 10, 2, 0, 0)},
		{"0 0 13 * 5", utc(2026, 10, 9, 0, 0), utc(2026, 10, 13, 0, 0)},
		{"0 0 13 * 5", utc(2026, 10, 13, 0, 0), utc(2026, 10, 16, 0, 0)},
		{"0 0 * * 7", mon, utc(2026, 10, 4, 0, 0)},
		{"0 0 * jan *", mon, utc(2027, 1, 1, 0, 0)},
		{"59 23 31 12 *", mon, utc(2026, 12, 31, 23, 59)},
	}
	for _, tt := range tests {
		t.Run(tt.expr+"@"+tt.after.Format(time.RFC3339), func(t *testing.T) {
			got, ok := mustParse(t, tt.expr).Next(tt.after, time.UTC)
			if !ok || !got.Equal(tt.want) {
				t.Fatalf("Next = %v %v, want %v", got, ok, tt.want)
			}
			if got.Second() != 0 || got.Nanosecond() != 0 {
				t.Fatalf("seconds not zero: %v", got)
			}
		})
	}
}

func TestNextNilLocationIsUTC(t *testing.T) {
	got, ok := mustParse(t, "0 3 * * *").Next(utc(2026, 9, 28, 10, 0), nil)
	if !ok || !got.Equal(utc(2026, 9, 29, 3, 0)) || got.Location() != time.UTC {
		t.Fatalf("Next(nil loc) = %v %v", got, ok)
	}
}

func TestNextNone(t *testing.T) {
	// No 29 February between 2097 and 2103 (2100 is not a leap year).
	s := mustParse(t, "0 0 29 2 *")
	if got, ok := s.Next(utc(2096, 3, 1, 0, 0), time.UTC); ok {
		t.Fatalf("Next = %v, want none within 5 years", got)
	}
}

func TestNextDSTSofia(t *testing.T) {
	sofia := mustLoc(t, "Europe/Sofia")
	local := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, sofia) }
	s := mustParse(t, "30 3 * * *")

	// Spring forward 2026-03-29 03:00 EET -> 04:00 EEST: 03:30 does not exist and is skipped.
	got, ok := s.Next(local(2026, 3, 28, 12, 0), sofia)
	if !ok || !got.Equal(local(2026, 3, 30, 3, 30)) {
		t.Fatalf("spring: Next = %v, want 2026-03-30 03:30", got)
	}
	if got.Location() != sofia {
		t.Fatalf("result not in location: %v", got.Location())
	}
	if n := s.Count(local(2026, 3, 28, 12, 0), local(2026, 3, 30, 12, 0), sofia, 10); n != 1 {
		t.Fatalf("spring count = %d, want 1", n)
	}

	// Fall back 2026-10-25 04:00 EEST -> 03:00 EET: 03:30 occurs twice and runs once.
	first, ok := s.Next(local(2026, 10, 24, 12, 0), sofia)
	if !ok || first.Day() != 25 || first.Hour() != 3 || first.Minute() != 30 {
		t.Fatalf("fall: Next = %v", first)
	}
	// The earlier instant (03:30 EEST = 00:30 UTC) is the one generated.
	if !first.Equal(utc(2026, 10, 25, 0, 30)) {
		t.Fatalf("fall: first = %v, want the first pass 00:30 UTC", first.UTC())
	}
	second, ok := s.Next(first, sofia)
	if !ok || !second.Equal(local(2026, 10, 26, 3, 30)) {
		t.Fatalf("fall: second = %v, want 2026-10-26 03:30", second)
	}
	if n := s.Count(local(2026, 10, 24, 12, 0), local(2026, 10, 26, 0, 0), sofia, 10); n != 1 {
		t.Fatalf("fall count = %d, want 1", n)
	}
	// Starting inside the second pass of the repeated hour does not produce a second run.
	secondPass := time.Date(2026, 10, 25, 1, 10, 0, 0, time.UTC) // 03:10 EET
	got, ok = s.Next(secondPass, sofia)
	if !ok || !got.Equal(local(2026, 10, 26, 3, 30)) {
		t.Fatalf("from second pass: Next = %v", got)
	}

	// Every 30 minutes across the gap: 02:30 EET is followed by 04:00 EEST, 30 real minutes later.
	half := mustParse(t, "*/30 * * * *")
	ts := half.Preview(local(2026, 3, 29, 2, 0), sofia, 3)
	want := []time.Time{local(2026, 3, 29, 2, 30), local(2026, 3, 29, 4, 0), local(2026, 3, 29, 4, 30)}
	for i := range want {
		if !ts[i].Equal(want[i]) {
			t.Fatalf("preview[%d] = %v, want %v", i, ts[i], want[i])
		}
	}
	if gap := half.MinGap(local(2026, 3, 29, 2, 0), sofia, 5); gap != 30*time.Minute {
		t.Fatalf("MinGap across gap = %v", gap)
	}
	// Across the overlap the wall clock 00:30..06:00 has 12 slots, each generated once.
	if n := half.Count(local(2026, 10, 25, 0, 0), local(2026, 10, 25, 6, 0), sofia, 100); n != 12 {
		t.Fatalf("overlap count = %d, want 12", n)
	}
}

func TestNextDSTNewYork(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	local := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, ny) }

	// 2026-03-08 02:00 EST -> 03:00 EDT: 02:30 skipped.
	s := mustParse(t, "30 2 * * *")
	got, ok := s.Next(local(2026, 3, 7, 12, 0), ny)
	if !ok || !got.Equal(local(2026, 3, 9, 2, 30)) {
		t.Fatalf("spring: Next = %v", got)
	}
	// 2026-11-01 02:00 EDT -> 01:00 EST: 01:30 runs once.
	s = mustParse(t, "30 1 * * *")
	first, ok := s.Next(local(2026, 10, 31, 12, 0), ny)
	if !ok || first.Day() != 1 || first.Hour() != 1 || first.Minute() != 30 {
		t.Fatalf("fall: Next = %v", first)
	}
	if !first.Equal(utc(2026, 11, 1, 5, 30)) { // 01:30 EDT, the first pass
		t.Fatalf("fall: first = %v, want 05:30 UTC", first.UTC())
	}
	second, _ := s.Next(first, ny)
	if !second.Equal(local(2026, 11, 2, 1, 30)) {
		t.Fatalf("fall: second = %v", second)
	}
	// Non-DST times keep their wall clock.
	s = mustParse(t, "0 9 * * 1-5")
	got, _ = s.Next(local(2026, 11, 1, 0, 0), ny)
	if !got.Equal(local(2026, 11, 2, 9, 0)) || got.Hour() != 9 {
		t.Fatalf("weekday 09:00 = %v", got)
	}
}

func TestPreview(t *testing.T) {
	s := mustParse(t, "0 */6 * * *")
	got := s.Preview(utc(2026, 9, 28, 10, 0), time.UTC, 4)
	want := []time.Time{utc(2026, 9, 28, 12, 0), utc(2026, 9, 28, 18, 0), utc(2026, 9, 29, 0, 0), utc(2026, 9, 29, 6, 0)}
	if len(got) != len(want) {
		t.Fatalf("len = %d", len(got))
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	if got := s.Preview(utc(2026, 9, 28, 10, 0), time.UTC, 0); len(got) != 0 {
		t.Fatalf("n=0 → %v", got)
	}
	if got := s.Preview(utc(2026, 9, 28, 10, 0), time.UTC, -3); len(got) != 0 {
		t.Fatalf("n<0 → %v", got)
	}
	// Stops when no further occurrence exists within the search window.
	leap := mustParse(t, "0 0 29 2 *")
	if got := leap.Preview(utc(2092, 3, 1, 0, 0), time.UTC, 5); len(got) != 1 || !got[0].Equal(utc(2096, 2, 29, 0, 0)) {
		t.Fatalf("leap preview = %v", got)
	}
}

func TestMinGap(t *testing.T) {
	after := utc(2026, 9, 28, 10, 7)
	tests := []struct {
		expr string
		n    int
		want time.Duration
	}{
		{"*/15 * * * *", 10, 15 * time.Minute},
		{"* * * * *", 10, time.Minute},
		{"0 3 * * *", 10, 24 * time.Hour},
		{"0,5 * * * *", 10, 5 * time.Minute},
		{"0 3 * * *", 1, 24 * time.Hour},
		{"0 3 * * *", 0, 0},
		{"0 3 * * *", -1, 0},
	}
	for _, tt := range tests {
		if got := mustParse(t, tt.expr).MinGap(after, time.UTC, tt.n); got != tt.want {
			t.Fatalf("MinGap(%q, %d) = %v, want %v", tt.expr, tt.n, got, tt.want)
		}
	}
	// Fewer than two occurrences → 0.
	if got := mustParse(t, "0 0 29 2 *").MinGap(utc(2092, 3, 1, 0, 0), time.UTC, 10); got != 0 {
		t.Fatalf("single occurrence gap = %v", got)
	}
}

func TestCount(t *testing.T) {
	s := mustParse(t, "0 * * * *")
	from, to := utc(2026, 9, 28, 10, 0), utc(2026, 9, 28, 15, 0)
	if n := s.Count(from, to, time.UTC, 100); n != 5 {
		t.Fatalf("hourly over 5h = %d, want 5", n)
	}
	if n := s.Count(from, to, time.UTC, 3); n != 3 {
		t.Fatalf("capped = %d, want 3", n)
	}
	if n := s.Count(from, to, time.UTC, 0); n != 0 {
		t.Fatalf("limit 0 = %d", n)
	}
	if n := s.Count(to, from, time.UTC, 10); n != 0 {
		t.Fatalf("empty interval = %d", n)
	}
	if n := s.Count(from, from.Add(59*time.Minute), time.UTC, 10); n != 0 {
		t.Fatalf("(10:00,10:59] = %d", n)
	}
	leap := mustParse(t, "0 0 29 2 *")
	if n := leap.Count(utc(2092, 3, 1, 0, 0), utc(2110, 1, 1, 0, 0), time.UTC, 10); n != 1 {
		t.Fatalf("count stops at search horizon: %d", n)
	}
}

func TestLoadLocation(t *testing.T) {
	loc, err := LoadLocation("")
	if err != nil || loc != time.UTC {
		t.Fatalf("empty → %v %v", loc, err)
	}
	for _, name := range []string{"UTC", "Europe/Sofia", "America/New_York"} {
		loc, err := LoadLocation(name)
		if err != nil || loc.String() != name {
			t.Fatalf("%q → %v %v", name, loc, err)
		}
	}
	for _, name := range []string{"Local", "Mars/Olympus_Mons", "../etc/passwd", "/etc/localtime", strings.Repeat("A", 65)} {
		if _, err := LoadLocation(name); err == nil || !strings.HasPrefix(err.Error(), "cron: ") {
			t.Fatalf("%q accepted or bad error: %v", name, err)
		}
	}
}

func TestParseErrorMessage(t *testing.T) {
	e := &ParseError{Field: "hour", Msg: "value 24 out of range 0-23"}
	if e.Error() != "cron: hour: value 24 out of range 0-23" {
		t.Fatal(e.Error())
	}
}

func sameSchedule(a, b *Schedule) bool {
	return a.minute == b.minute && a.hour == b.hour && a.dom == b.dom && a.month == b.month &&
		a.dow == b.dow && a.domStar == b.domStar && a.dowStar == b.dowStar
}
