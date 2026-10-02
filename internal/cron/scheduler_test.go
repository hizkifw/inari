package cron

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// clockedScheduler is a scheduler on a clock the test moves, recording the
// runs it starts. A run lasts until release is closed.
type clockedScheduler struct {
	*Scheduler
	mu      sync.Mutex
	now     time.Time
	runs    []string
	release chan struct{}
}

func newClocked(t *testing.T, d Dir, start time.Time) *clockedScheduler {
	c := &clockedScheduler{now: start, release: make(chan struct{})}
	c.Scheduler = NewScheduler(d, time.UTC, func(ctx context.Context, j Job, due time.Time) {
		c.mu.Lock()
		c.runs = append(c.runs, j.Name+"@"+due.Format("15:04"))
		release := c.release
		c.mu.Unlock()
		<-release
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.Scheduler.now = func() time.Time {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.now
	}
	t.Cleanup(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		close(c.release)
	})
	return c
}

func (c *clockedScheduler) at(s string) {
	c.mu.Lock()
	c.now = at(s)
	c.mu.Unlock()
	c.tick(context.Background())
}

func (c *clockedScheduler) started(t *testing.T, want ...string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		got := append([]string(nil), c.runs...)
		c.mu.Unlock()
		if len(got) >= len(want) || time.Now().After(deadline) {
			if len(got) != len(want) {
				t.Fatalf("runs = %q, want %q", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("runs = %q, want %q", got, want)
				}
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// finish ends the runs in progress and waits for the scheduler to see them
// end.
func (c *clockedScheduler) finish(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	close(c.release)
	c.release = make(chan struct{})
	c.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.Scheduler.mu.Lock()
		running := false
		for _, p := range c.Scheduler.plans {
			running = running || p.running
		}
		c.Scheduler.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("runs did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSchedulerRunsWhenDue(t *testing.T) {
	d := Dir(t.TempDir())
	d.Add(job(t, "nine", "0 9 * * *"), false)
	c := newClocked(t, d, at("2026-10-02 08:58"))
	c.at("2026-10-02 08:58")
	c.at("2026-10-02 08:59")
	c.started(t)
	c.at("2026-10-02 09:00")
	c.started(t, "nine@09:00")
	c.finish(t)
	c.at("2026-10-02 09:01")
	c.at("2026-10-03 09:00")
	c.started(t, "nine@09:00", "nine@09:00")
}

func TestSchedulerSkipsRunsMissedBeforeItStarted(t *testing.T) {
	d := Dir(t.TempDir())
	d.Add(job(t, "nine", "0 9 * * *"), false)
	c := newClocked(t, d, at("2026-10-02 12:00"))
	c.at("2026-10-02 12:00")
	c.started(t)
}

func TestSchedulerRunsALateOneShotOnceAndRemovesIt(t *testing.T) {
	d := Dir(t.TempDir())
	once := job(t, "ping", "")
	once.At = at("2026-10-02 09:00")
	d.Add(once, false)
	c := newClocked(t, d, at("2026-10-02 12:00"))
	c.at("2026-10-02 12:00")
	c.started(t, "ping@09:00")
	if jobs, _ := d.List(); len(jobs) != 0 {
		t.Fatalf("one-shot job still listed: %+v", jobs)
	}
	c.finish(t)
	c.at("2026-10-02 12:01")
	c.started(t, "ping@09:00")
}

func TestSchedulerSkipsARunWhileTheLastRuns(t *testing.T) {
	d := Dir(t.TempDir())
	d.Add(job(t, "tick", "* * * * *"), false)
	c := newClocked(t, d, at("2026-10-02 08:59"))
	c.at("2026-10-02 08:59")
	c.at("2026-10-02 09:00")
	c.at("2026-10-02 09:01")
	c.started(t, "tick@09:00")
	c.finish(t)
	c.at("2026-10-02 09:02")
	c.started(t, "tick@09:00", "tick@09:02")
}

func TestSchedulerFollowsTheDirectory(t *testing.T) {
	d := Dir(t.TempDir())
	c := newClocked(t, d, at("2026-10-02 08:00"))
	c.at("2026-10-02 08:00")
	d.Add(job(t, "late-add", "30 8 * * *"), false)
	c.at("2026-10-02 08:10")
	d.Remove("late-add")
	c.at("2026-10-02 08:30")
	c.started(t)
	// Changing a job's schedule takes effect at the next poll.
	d.Add(job(t, "moved", "0 9 * * *"), false)
	c.at("2026-10-02 08:40")
	d.Add(job(t, "moved", "45 8 * * *"), true)
	c.at("2026-10-02 08:44")
	c.at("2026-10-02 08:45")
	c.started(t, "moved@08:45")
}

func TestTickWaitsNoLongerThanTheNextRun(t *testing.T) {
	d := Dir(t.TempDir())
	once := job(t, "soon", "")
	once.At = at("2026-10-02 08:00").Add(3 * time.Second)
	d.Add(once, false)
	c := newClocked(t, d, at("2026-10-02 08:00"))
	if wait := c.tick(context.Background()); wait != 3*time.Second {
		t.Fatalf("tick waits %v, want 3s", wait)
	}
}
