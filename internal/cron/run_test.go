package cron

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hizkifw/inari/internal/acp/acptest"
	"github.com/hizkifw/inari/internal/hub"
	"github.com/hizkifw/inari/internal/store"
)

func TestMain(m *testing.M) { acptest.Main(m) }

// home records what the home conversation is shown.
type home struct{ events chan string }

func (h *home) TurnStarted(string) {}

func (h *home) Post(conv string, p hub.Post) {
	kind := "post"
	if p.Notice {
		kind = "notice"
	}
	h.events <- fmt.Sprintf("%s %s %q", kind, conv, p.Text)
}

func (h *home) TurnEnded(string, hub.End) {}

func (h *home) expect(t *testing.T, want ...string) {
	t.Helper()
	for _, w := range want {
		select {
		case got := <-h.events:
			if !strings.HasPrefix(got, w) {
				t.Fatalf("got %s, want %s", got, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", w)
		}
	}
}

func newRunner(t *testing.T, timeout time.Duration) (*Runner, *home, string) {
	t.Helper()
	sessions := filepath.Join(t.TempDir(), "sessions.json")
	st, err := store.Open(sessions)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.New(hub.Kon{Command: acptest.Command(t)}, "test", st, log)
	t.Cleanup(h.Close)
	rec := &home{events: make(chan string, 16)}
	h.Register("test", rec)
	r := NewRunner(h, Target{Conv: "test:home", Route: hub.Route{CWD: t.TempDir()}}, timeout, log)
	return r, rec, sessions
}

func TestRunDeliversTheFinalMessageAsANotice(t *testing.T) {
	r, rec, sessions := newRunner(t, time.Minute)
	j := job(t, "standup", "@daily")
	j.Prompt = "tell a story"
	r.Run(context.Background(), j, time.Now())
	rec.expect(t,
		`notice test:home "⏰ cron job standup finished"`,
		// Only the run's last message reaches home, tagged as a notice.
		`post test:home "echo: [cron notice: standup] done!\n\n(This job repeats on the schedule @daily; the next one is at`)
	// A run's session is not one to resume.
	data, _ := os.ReadFile(sessions)
	if strings.Contains(string(data), "cron:") {
		t.Fatalf("a job's session was remembered:\n%s", data)
	}
}

func TestRunTellsTheSessionItIsAJob(t *testing.T) {
	r, rec, _ := newRunner(t, time.Minute)
	j := job(t, "probe", "@daily")
	j.Prompt = "instructions?"
	r.Run(context.Background(), j, time.Now().Add(-time.Hour))
	rec.expect(t, `notice`)
	got := <-rec.events
	for _, want := range []string{`scheduled job \"probe\"`, "no one can answer", "starts late"} {
		if !strings.Contains(got, want) {
			t.Fatalf("job session was told %s; want it to say %s", got, want)
		}
	}
	if strings.Contains(got, "group chat") {
		t.Fatalf("job session was told it is in a chat: %s", got)
	}
}

func TestRunStopsAJobThatRunsTooLong(t *testing.T) {
	r, rec, _ := newRunner(t, 100*time.Millisecond)
	j := job(t, "stuck", "@daily")
	j.Prompt = "slow"
	r.Run(context.Background(), j, time.Now())
	rec.expect(t, `notice`,
		`post test:home "echo: [cron notice: stuck] The job was stopped after running for 100ms. Its last message:\n\nLooking\n\n(This job repeats`)
}

func TestReminderGoesStraightToHome(t *testing.T) {
	r, rec, _ := newRunner(t, time.Minute)
	j := job(t, "deploy", "")
	j.Kind, j.At, j.Prompt = KindReminder, time.Now(), "Remind Alice about the deploy."
	r.Run(context.Background(), j, time.Now())
	rec.expect(t,
		`notice test:home "⏰ reminder deploy"`,
		`post test:home "echo: [reminder: deploy] Remind Alice about the deploy.\n\n(This was a one-off reminder: it is now removed and will not come again.)"`)
	// Nothing ran: no session was opened for the reminder.
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.runs) != 0 {
		t.Fatalf("runs = %v", r.runs)
	}
}

func TestLateReminderSaysSo(t *testing.T) {
	r, rec, _ := newRunner(t, time.Minute)
	j := job(t, "late", "")
	j.Kind, j.Prompt = KindReminder, "Stretch."
	r.Run(context.Background(), j, time.Now().Add(-time.Hour))
	rec.expect(t, `notice`, `post test:home "echo: [reminder: late] Stretch.\n\n(This reminder was due at`)
}

func TestNoticesSayWhetherTheJobComesAgain(t *testing.T) {
	r, rec, _ := newRunner(t, time.Minute)
	once := job(t, "once", "")
	once.At, once.Prompt = time.Now(), "tell a story"
	r.Run(context.Background(), once, once.At)
	rec.expect(t, `notice`, `post test:home "echo: [cron notice: once] done!\n\n(This was a one-off job: it is now removed and will not come again.)"`)

	daily := job(t, "daily", "@daily")
	daily.Kind, daily.Prompt = KindReminder, "Stretch."
	r.Run(context.Background(), daily, time.Now())
	rec.expect(t, `notice`, `post test:home "echo: [reminder: daily] Stretch.\n\n(This reminder repeats on the schedule @daily; the next one is at`)
}

func TestOneOffJobSessionIsToldNoRunFollows(t *testing.T) {
	r, rec, _ := newRunner(t, time.Minute)
	j := job(t, "probe", "")
	j.At, j.Prompt = time.Now(), "instructions?"
	r.Run(context.Background(), j, j.At)
	rec.expect(t, `notice`)
	if got := <-rec.events; !strings.Contains(got, "do not promise a follow-up") {
		t.Fatalf("one-off job session was told %s", got)
	}
}

func TestUsageNamesTheCommandAndTimezone(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Skip(err)
	}
	got := Usage("/opt/my inari", loc)
	for _, want := range []string{"`'/opt/my inari' cron`", "Times are in Asia/Jakarta.", "\n'/opt/my inari' cron remove standup"} {
		if !strings.Contains(got, want) {
			t.Fatalf("usage does not say %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "{{") || strings.HasSuffix(got, "\n") {
		t.Fatalf("usage is not filled in or trimmed:\n%s", got)
	}
}
