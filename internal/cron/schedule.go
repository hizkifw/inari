// Package cron runs jobs kon schedules for itself. A job is a prompt and a
// schedule, kept as one file in the job directory. At each due time the job
// runs in a new kon session, and its final message is delivered to the home
// conversation as a notice.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is when a job runs: a cron expression, an interval, or one moment.
type Schedule struct {
	spec string
	// cron is set for a cron expression or a shorthand such as @daily.
	cron *expr
	// every is set for @every.
	every time.Duration
	// at is set for a one-shot job.
	at time.Time
}

// minEvery keeps an interval from turning kon into a busy loop.
const minEvery = time.Minute

// ParseSchedule reads a cron expression ("0 9 * * 1-5"), a shorthand
// (@hourly, @daily, @weekly, @monthly, @yearly), or "@every <duration>".
func ParseSchedule(spec string) (Schedule, error) {
	spec = strings.TrimSpace(spec)
	switch spec {
	case "@hourly":
		return ParseSchedule("0 * * * *")
	case "@daily", "@midnight":
		return ParseSchedule("0 0 * * *")
	case "@weekly":
		return ParseSchedule("0 0 * * 0")
	case "@monthly":
		return ParseSchedule("0 0 1 * *")
	case "@yearly", "@annually":
		return ParseSchedule("0 0 1 1 *")
	}
	if rest, ok := strings.CutPrefix(spec, "@every "); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil {
			return Schedule{}, fmt.Errorf("@every: %w", err)
		}
		if d < minEvery {
			return Schedule{}, fmt.Errorf("@every must be at least %v", minEvery)
		}
		return Schedule{spec: spec, every: d}, nil
	}
	e, err := parseExpr(spec)
	if err != nil {
		return Schedule{}, err
	}
	return Schedule{spec: spec, cron: e}, nil
}

// At is a schedule that runs once, at t.
func At(t time.Time) Schedule {
	return Schedule{spec: "at " + t.Format(time.RFC3339), at: t}
}

// Once reports whether the schedule runs only once.
func (s Schedule) Once() bool { return !s.at.IsZero() }

func (s Schedule) String() string { return s.spec }

// Next is the first time after t the schedule runs, in t's location. It is
// zero when there is none: a one-shot job's moment has passed, or a cron
// expression names a day that never comes, such as 30 February.
func (s Schedule) Next(t time.Time) time.Time {
	switch {
	case s.Once():
		if s.at.After(t) {
			return s.at
		}
		return time.Time{}
	case s.every > 0:
		return t.Add(s.every).Truncate(time.Second)
	default:
		return s.cron.next(t)
	}
}

// expr is a parsed five-field cron expression. Each field is the set of
// values it allows, as bits.
type expr struct {
	minute, hour, dom, month, dow uint64
	// domAny and dowAny record a "*" day field: when both day fields are
	// restricted, a day matching either runs, as in every cron.
	domAny, dowAny bool
}

type field struct {
	name     string
	min, max int
	names    []string // names[i] is value min+i, for months and weekdays
}

var (
	minuteField = field{name: "minute", min: 0, max: 59}
	hourField   = field{name: "hour", min: 0, max: 23}
	domField    = field{name: "day of month", min: 1, max: 31}
	monthField  = field{name: "month", min: 1, max: 12, names: []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}}
	// Day of week takes 0-7, where both 0 and 7 are Sunday.
	dowField = field{name: "day of week", min: 0, max: 7, names: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}}
)

func parseExpr(spec string) (*expr, error) {
	parts := strings.Fields(spec)
	if len(parts) != 5 {
		return nil, fmt.Errorf("a cron expression has 5 fields (minute hour day-of-month month day-of-week), not %d: %q", len(parts), spec)
	}
	var e expr
	var err error
	fields := []struct {
		f   field
		dst *uint64
	}{{minuteField, &e.minute}, {hourField, &e.hour}, {domField, &e.dom}, {monthField, &e.month}, {dowField, &e.dow}}
	for i, f := range fields {
		if *f.dst, err = parseField(parts[i], f.f); err != nil {
			return nil, err
		}
	}
	// Sunday is 0 to time.Weekday; 7 is only a spelling of it.
	if e.dow&(1<<7) != 0 {
		e.dow = e.dow&^(1<<7) | 1
	}
	e.domAny, e.dowAny = parts[2] == "*", parts[4] == "*"
	return &e, nil
}

// parseField reads a comma-separated list of "*", values, ranges, and steps
// such as "*/15" or "1-5/2".
func parseField(s string, f field) (uint64, error) {
	var bits uint64
	for _, item := range strings.Split(s, ",") {
		rng, stepText, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("%s: invalid step %q", f.name, stepText)
			}
			step = n
		}
		lo, hi := f.min, f.max
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var err error
			if lo, err = f.value(a); err != nil {
				return 0, err
			}
			if hi, err = f.value(b); err != nil {
				return 0, err
			}
			if lo > hi {
				return 0, fmt.Errorf("%s: range %q runs backwards", f.name, rng)
			}
		default:
			v, err := f.value(rng)
			if err != nil {
				return 0, err
			}
			lo, hi = v, v
			// "5/10" means from 5 to the end in steps of 10.
			if hasStep {
				hi = f.max
			}
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << v
		}
	}
	return bits, nil
}

func (f field) value(s string) (int, error) {
	for i, name := range f.names {
		if strings.EqualFold(s, name) {
			return f.min + i, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < f.min || v > f.max {
		return 0, fmt.Errorf("%s: %q is not between %d and %d", f.name, s, f.min, f.max)
	}
	return v, nil
}

// next finds the first minute after t that e matches, skipping a whole month,
// day, or hour at a time when it cannot match. Five years is long enough for
// any date that exists, including 29 February.
func (e *expr) next(t time.Time) time.Time {
	loc := t.Location()
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		switch {
		case !has(e.month, int(t.Month())):
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
		case !e.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
		case !has(e.hour, t.Hour()):
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
		case !has(e.minute, t.Minute()):
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

func (e *expr) dayMatches(t time.Time) bool {
	dom, dow := has(e.dom, t.Day()), has(e.dow, int(t.Weekday()))
	switch {
	case e.domAny && e.dowAny:
		return true
	case e.domAny:
		return dow
	case e.dowAny:
		return dom
	default:
		return dom || dow
	}
}

func has(bits uint64, v int) bool { return bits&(1<<v) != 0 }
