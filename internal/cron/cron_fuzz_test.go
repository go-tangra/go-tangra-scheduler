package cron

import (
	"testing"
	"time"
)

func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"* * * * *", "0 3 * * *", "*/15 * * * *", "0 8 * * 1-5", "0 0 1 */3 *",
		"30 2 29 2 *", "0 0 13 * 5", "0,30 9-17/2 * * *", "5/20 * * * *",
		"0 0 * jan-MAR sun,sat", "0 0 * * 7", "0 0 30 2 *", "60 * * * *",
		"1,,2 * * * *", "*/0 * * * *", "5-1 * * * *", "", "* * * * * *",
	} {
		f.Add(seed, int64(0))
	}
	sofia, err := time.LoadLocation("Europe/Sofia")
	if err != nil {
		f.Fatal(err)
	}
	base := time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, expr string, offset int64) {
		s, err := Parse(expr)
		if err != nil {
			return
		}
		again, err := Parse(s.String())
		if err != nil {
			t.Fatalf("String() %q of %q does not re-parse: %v", s.String(), expr, err)
		}
		if !sameSchedule(s, again) || again.String() != s.String() {
			t.Fatalf("re-parse of %q differs", s.String())
		}
		// Keep the reference time within a few years of base.
		after := base.Add(time.Duration(offset%(3*365*24*3600)) * time.Second)
		for _, loc := range []*time.Location{time.UTC, sofia} {
			next, ok := s.Next(after, loc)
			if !ok {
				continue
			}
			if !next.After(after) {
				t.Fatalf("Next(%v) = %v not after input (%q, %v)", after, next, expr, loc)
			}
			if next.Second() != 0 || next.Nanosecond() != 0 {
				t.Fatalf("Next has seconds: %v", next)
			}
		}
	})
}
