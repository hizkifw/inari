package cron

import (
	"testing"
	"time"
)

var utc = time.UTC

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, utc)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNext(t *testing.T) {
	// 2026-10-02 is a Friday.
	from := at("2026-10-02 10:30")
	for _, c := range []struct{ spec, want string }{
		{"* * * * *", "2026-10-02 10:31"},
		{"*/15 * * * *", "2026-10-02 10:45"},
		{"0 9 * * *", "2026-10-03 09:00"},
		{"0 9 * * 1-5", "2026-10-05 09:00"},
		{"0 9 * * mon-fri", "2026-10-05 09:00"},
		{"30 10 * * *", "2026-10-03 10:30"},
		{"0 0 1 * *", "2026-11-01 00:00"},
		{"0 12 * jan *", "2027-01-01 12:00"},
		{"0 0 29 2 *", "2028-02-29 00:00"},
		{"0 0 * * 7", "2026-10-04 00:00"},
		{"0 0 * * 0", "2026-10-04 00:00"},
		{"5/20 * * * *", "2026-10-02 10:45"},
		{"0 8,17 * * *", "2026-10-02 17:00"},
		// Both day fields restricted: either one matching is enough.
		{"0 0 13 * 5", "2026-10-09 00:00"},
		{"@hourly", "2026-10-02 11:00"},
		{"@daily", "2026-10-03 00:00"},
		{"@weekly", "2026-10-04 00:00"},
		{"@monthly", "2026-11-01 00:00"},
	} {
		s, err := ParseSchedule(c.spec)
		if err != nil {
			t.Errorf("ParseSchedule(%q): %v", c.spec, err)
			continue
		}
		if got := s.Next(from); !got.Equal(at(c.want)) {
			t.Errorf("%q: Next = %v, want %s", c.spec, got, c.want)
		}
	}
}

func TestNextNeverForADayThatNeverComes(t *testing.T) {
	s, err := ParseSchedule("0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Next(at("2026-10-02 10:30")); !got.IsZero() {
		t.Fatalf("Next = %v, want never", got)
	}
}

func TestNextKeepsTheLocation(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	s, _ := ParseSchedule("0 9 * * *")
	got := s.Next(time.Date(2026, 10, 2, 10, 0, 0, 0, loc))
	if want := time.Date(2026, 10, 3, 9, 0, 0, 0, loc); !got.Equal(want) || got.Location() != loc {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}

func TestEvery(t *testing.T) {
	s, err := ParseSchedule("@every 90m")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Next(at("2026-10-02 10:30")); !got.Equal(at("2026-10-02 12:00")) {
		t.Fatalf("Next = %v", got)
	}
	if _, err := ParseSchedule("@every 10s"); err == nil {
		t.Fatal("an interval under a minute was accepted")
	}
}

func TestAtRunsOnce(t *testing.T) {
	when := at("2026-10-02 12:00")
	s := At(when)
	if !s.Once() || !s.Next(at("2026-10-02 10:00")).Equal(when) || !s.Next(when).IsZero() {
		t.Fatalf("At(%v) = %+v", when, s)
	}
}

func TestParseScheduleRejects(t *testing.T) {
	for _, spec := range []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "5-1 * * * *", "*/0 * * * *", "x * * * *", "@sometimes", "@every soon"} {
		if _, err := ParseSchedule(spec); err == nil {
			t.Errorf("ParseSchedule(%q) succeeded", spec)
		}
	}
}
