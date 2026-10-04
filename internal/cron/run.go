package cron

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/hizkifw/inari/internal/hub"
)

// connector is the hub connector a job's run is a conversation of, as
// "cron:<job>.<n>".
const connector = "cron"

// Target is the conversation a job's final message is delivered to.
type Target struct {
	Conv  string
	Route hub.Route
}

// Runner runs jobs through the hub: each run is a detached conversation of
// its own, and its final message is delivered to the home conversation as a
// notice. It is the hub's Output for those conversations.
type Runner struct {
	hub  *hub.Hub
	home Target
	// route is a conversation's route, or false when inari does not serve
	// it. It may be nil, and then every job reports home.
	route   func(conv string) (hub.Route, bool)
	timeout time.Duration
	log     *slog.Logger

	mu   sync.Mutex
	runs map[string]*run
}

// run is a job's conversation while it runs.
type run struct {
	// final is the latest text kon wrote, which is its last message once
	// the turn ends.
	final string
	done  chan hub.End
}

// NewRunner returns a runner delivering to each job's conversation, or to
// home, that stops a run after timeout. route resolves a job's
// conversation. It registers itself with the hub as the "cron" connector.
func NewRunner(h *hub.Hub, home Target, route func(conv string) (hub.Route, bool), timeout time.Duration, log *slog.Logger) *Runner {
	r := &Runner{hub: h, home: home, route: route, timeout: timeout, log: log.With("component", "cron"), runs: map[string]*run{}}
	h.Register(connector, r)
	return r
}

// Run runs j once, for the time it was due: a task in a session of its own,
// delivering its final message, and a reminder by delivering it.
func (r *Runner) Run(ctx context.Context, j Job, due time.Time) {
	if j.Reminder() {
		r.remind(ctx, j, due)
		return
	}
	key := fmt.Sprintf("%s:%s.%d", connector, j.Name, time.Now().UnixNano())
	ru := &run{done: make(chan hub.End, 1)}
	r.mu.Lock()
	r.runs[key] = ru
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.runs, key)
		r.mu.Unlock()
		// The run is over, so its session goes; kon keeps the transcript.
		if err := r.hub.Reset(context.Background(), key); err != nil {
			r.log.Warn("close job session", "job", j.Name, "err", err)
		}
	}()

	r.log.Info("job started", "job", j.Name, "due", due)
	late := time.Since(due) > time.Minute
	route := hub.Route{CWD: j.CWD, Instructions: jobInstructions(j, due, late), Detached: true}
	report := ""
	if err := r.hub.Handle(ctx, hub.Message{Conv: key, Route: route, Author: "cron:" + j.Name, Text: j.Prompt}); err != nil {
		report = "The job could not start: " + err.Error()
	} else {
		report = r.wait(ctx, key, ru)
	}
	r.log.Info("job finished", "job", j.Name)
	r.deliver(ctx, j, report+"\n\n("+recurrence(j, due)+")")
}

// wait waits for the run's turn to end, stopping it after the timeout, and
// returns what to report.
func (r *Runner) wait(ctx context.Context, key string, ru *run) string {
	timer := time.NewTimer(r.timeout)
	defer timer.Stop()
	var end hub.End
	stopped := false
	select {
	case end = <-ru.done:
	case <-ctx.Done():
		return "The job was stopped because inari is shutting down."
	case <-timer.C:
		stopped = true
		r.hub.Cancel(key)
		select {
		case end = <-ru.done:
		case <-time.After(30 * time.Second):
		}
	}
	r.mu.Lock()
	final := strings.TrimSpace(ru.final)
	r.mu.Unlock()
	var lead string
	switch {
	case stopped:
		lead = fmt.Sprintf("The job was stopped after running for %v.", r.timeout)
	case end.Err != nil:
		lead = "The job failed: " + end.Err.Error()
	case final == "":
		lead = "The job finished without a final message."
	}
	if lead != "" && final != "" {
		return lead + " Its last message:\n\n" + final
	}
	return lead + final
}

// deliver hands a task's report to the home conversation's agent.
func (r *Runner) deliver(ctx context.Context, j Job, report string) {
	r.send(ctx, j, fmt.Sprintf("⏰ cron job %s finished", j.Name), "cron notice: "+j.Name, report)
}

// remind hands a reminder to the home conversation's agent, saying so when
// it comes late.
func (r *Runner) remind(ctx context.Context, j Job, due time.Time) {
	r.log.Info("reminder due", "job", j.Name, "due", due)
	text := j.Prompt
	if time.Since(due) > time.Minute {
		text += fmt.Sprintf("\n\n(This reminder was due at %s and comes late, because inari was not running then.)", due.Format("Mon 2006-01-02 15:04 MST"))
	}
	text += "\n\n(" + recurrence(j, due) + ")"
	r.send(ctx, j, "⏰ reminder "+j.Name, "reminder: "+j.Name, text)
}

// send shows notice in the job's conversation and hands text to its agent
// from author, which relays it to the people there: as steering when the
// agent is working, so it arrives before its next step, and as a turn of its
// own otherwise.
func (r *Runner) send(ctx context.Context, j Job, notice, author, text string) {
	to := r.target(j)
	if to.Conv != j.To && j.To != "" {
		text += fmt.Sprintf("\n\n(This was meant for the conversation %s, which inari no longer serves, so it came here instead.)", j.To)
	}
	r.hub.Notice(to.Conv, notice)
	msg := hub.Message{Conv: to.Conv, Route: to.Route, Author: author, Text: text}
	if err := r.hub.Handle(ctx, msg); err != nil {
		r.log.Error("deliver", "job", j.Name, "conv", to.Conv, "err", err)
	}
}

// target is where j reports: its own conversation while inari serves it,
// and home otherwise, so nothing it reports is lost.
func (r *Runner) target(j Job) Target {
	if j.To == "" || j.To == r.home.Conv || r.route == nil {
		return r.home
	}
	route, ok := r.route(j.To)
	if !ok {
		r.log.Warn("job's conversation is not served; delivering home", "job", j.Name, "conv", j.To)
		return r.home
	}
	return Target{Conv: j.To, Route: route}
}

// TurnStarted is part of hub.Output.
func (r *Runner) TurnStarted(string) {}

// Post keeps the run's latest text, which becomes its final message.
func (r *Runner) Post(conv string, p hub.Post) {
	if p.Notice || strings.TrimSpace(p.Text) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if ru := r.runs[conv]; ru != nil {
		ru.final = p.Text
	}
}

// TurnEnded ends the run once its conversation is idle.
func (r *Runner) TurnEnded(conv string, e hub.End) {
	if !e.Idle {
		return
	}
	r.mu.Lock()
	ru := r.runs[conv]
	r.mu.Unlock()
	if ru == nil {
		return
	}
	select {
	case ru.done <- e:
	default:
	}
}

// jobInstructions tell a job's session what it is, since it starts empty
// and nobody watches it.
func jobInstructions(j Job, due time.Time, late bool) string {
	when, _ := j.When()
	var b strings.Builder
	fmt.Fprintf(&b, "You are running the scheduled job %q (schedule: %s) for inari, due at %s. ", j.Name, when, due.Format("Mon 2006-01-02 15:04 MST"))
	b.WriteString("No one reads this session while it runs, and no one can answer questions. ")
	b.WriteString("When you finish, your final message is delivered as a notice to the agent of the chat that scheduled the job, which relays it to the people there. ")
	b.WriteString("Make that final message a short, self-contained report of what you found or did.")
	if late {
		b.WriteString(" This run starts late, because inari was not running when it was due.")
	}
	if next := nextRun(j, due); next.IsZero() {
		b.WriteString(" This job runs once and is removed, so no later run will pick up where this one stops: do not promise a follow-up, and say plainly what is left undone.")
	} else {
		fmt.Fprintf(&b, " The job runs again at %s, but that run starts in a new, empty session and will not see this one.", next.Format("Mon 2006-01-02 15:04 MST"))
	}
	return b.String()
}

// recurrence says whether j comes again. Without it, the home agent tends
// to read a one-off job's "not found yet" as something a later run will
// follow up on.
func recurrence(j Job, due time.Time) string {
	what := "job"
	if j.Reminder() {
		what = "reminder"
	}
	next := nextRun(j, due)
	if next.IsZero() {
		return fmt.Sprintf("This was a one-off %s: it is now removed and will not come again.", what)
	}
	return fmt.Sprintf("This %s repeats on the schedule %s; the next one is at %s.", what, j.Schedule, next.Format("Mon 2006-01-02 15:04 MST"))
}

// nextRun is when j runs after the run due at due, or zero when it does
// not. A late run counts from now, as the scheduler does.
func nextRun(j Job, due time.Time) time.Time {
	when, err := j.When()
	if err != nil || when.Once() {
		return time.Time{}
	}
	now := time.Now().In(due.Location())
	if now.Before(due) {
		now = due
	}
	return when.Next(now)
}

// usage is the template of what Usage says, kept as text so it reads as
// the session sees it.
//
//go:embed usage.txt
var usageText string

var usage = template.Must(template.New("usage").Parse(usageText))

// Usage tells chat sessions how to schedule jobs. exe is the inari to run,
// and loc where schedules are read.
func Usage(exe string, loc *time.Location) string {
	var b strings.Builder
	data := struct {
		Cmd      string
		Location *time.Location
	}{shellQuote(exe) + " cron", loc}
	// The template is fixed and its data is plain text, so it cannot fail.
	usage.Execute(&b, data)
	return strings.TrimSpace(b.String())
}

// shellQuote quotes s for a POSIX shell when it needs it.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
