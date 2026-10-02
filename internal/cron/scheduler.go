package cron

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// pollEvery is how often the scheduler reads the job directory, which is how
// it learns of jobs `inari cron` adds or removes. A job is due at most this
// late.
const pollEvery = 10 * time.Second

// Scheduler runs each job in a directory at its due times.
type Scheduler struct {
	dir Dir
	loc *time.Location
	run func(ctx context.Context, j Job, due time.Time)
	log *slog.Logger
	now func() time.Time

	mu    sync.Mutex
	plans map[string]*plan
	// failed are the job files that last failed to read, so each problem is
	// logged once rather than every poll.
	failed map[string]bool
}

// plan is when a job next runs.
type plan struct {
	// key is the job's fingerprint, to notice when it changes.
	key     string
	next    time.Time
	running bool
}

// NewScheduler returns a scheduler of the jobs in dir, read in loc, that calls
// run for each due run.
func NewScheduler(dir Dir, loc *time.Location, run func(context.Context, Job, time.Time), log *slog.Logger) *Scheduler {
	return &Scheduler{dir: dir, loc: loc, run: run, log: log.With("component", "cron"), now: time.Now, plans: map[string]*plan{}, failed: map[string]bool{}}
}

// Run schedules until ctx ends. A recurring run that came due while inari
// was not running is skipped; a one-shot job that came due then runs at
// once, as late.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		t.Reset(s.tick(ctx))
	}
}

// tick starts what is due and returns how long to wait for the next tick.
func (s *Scheduler) tick(ctx context.Context) time.Duration {
	now := s.now().In(s.loc)
	jobs, errs := s.dir.List()
	failed := map[string]bool{}
	for _, err := range errs {
		failed[err.Error()] = true
		if !s.failed[err.Error()] {
			s.log.Warn("skip job file", "err", err)
		}
	}
	s.failed = failed

	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	wait := pollEvery
	for _, j := range jobs {
		seen[j.Name] = true
		p := s.plans[j.Name]
		if p == nil || p.key != j.key() {
			when, _ := j.When()
			next := j.At
			if !when.Once() {
				next = when.Next(now)
			}
			// A replaced job keeps running if it was.
			running := p != nil && p.running
			p = &plan{key: j.key(), next: next, running: running}
			s.plans[j.Name] = p
		}
		if p.next.IsZero() {
			continue
		}
		if now.Before(p.next) {
			wait = min(wait, p.next.Sub(now))
			continue
		}
		due := p.next
		when, _ := j.When()
		if when.Once() {
			// Removed before it runs, so a crash mid-run does not run it
			// again.
			if err := s.dir.Remove(j.Name); err != nil {
				s.log.Warn("remove one-shot job", "job", j.Name, "err", err)
			}
			p.next = time.Time{}
		} else {
			p.next = when.Next(now)
		}
		if p.running {
			s.log.Warn("skip job run; the last one is still running", "job", j.Name, "due", due)
			continue
		}
		p.running = true
		go func() {
			s.run(ctx, j, due)
			s.mu.Lock()
			if q := s.plans[j.Name]; q != nil {
				q.running = false
			}
			s.mu.Unlock()
		}()
	}
	for name, p := range s.plans {
		if !seen[name] && !p.running {
			delete(s.plans, name)
		}
	}
	return max(wait, time.Second)
}
