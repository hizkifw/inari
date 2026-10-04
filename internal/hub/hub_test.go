package hub

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hizkifw/inari/internal/acp/acptest"
	"github.com/hizkifw/inari/internal/store"
)

// TestMain lets the test binary stand in for kon acp; see acptest.
func TestMain(m *testing.M) {
	acptest.Main(m)
}

// recorder is an Output that sends what it is shown to a channel.
type recorder struct{ events chan string }

func (r *recorder) TurnStarted(conv string) { r.events <- "start" }

func (r *recorder) Post(conv string, p Post) {
	var tools []string
	for _, t := range p.Tools {
		tools = append(tools, t.Title+"="+t.Status)
	}
	r.events <- fmt.Sprintf("post %d %v %q", p.ID, tools, p.Text)
}

func (r *recorder) TurnEnded(conv string, e End) {
	r.events <- fmt.Sprintf("end %s err=%v idle=%v", e.StopReason, e.Err, e.Idle)
}

func (r *recorder) expect(t *testing.T, want ...string) {
	t.Helper()
	for _, w := range want {
		select {
		case got := <-r.events:
			if !strings.HasPrefix(got, w) {
				t.Fatalf("got %q, want %q", got, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %q", w)
		}
	}
}

func newHub(t *testing.T) (*Hub, *recorder, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := New(Kon{Command: acptest.Command(t)}, "test", st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := &recorder{events: make(chan string, 64)}
	h.Register("test", rec)
	t.Cleanup(h.Close)
	return h, rec, st
}

func TestPromptIsAttributedAndPosted(t *testing.T) {
	h, rec, st := newHub(t)
	ctx := context.Background()
	cwd := t.TempDir()
	if err := h.Handle(ctx, Message{Conv: "test:1", Route: Route{CWD: cwd}, Author: "alice", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	rec.expect(t, "start", `post 0 [] "echo: [alice] hi"`, "end end_turn err=<nil> idle=true")
	if b, ok := st.Get("test:1"); !ok || b.CWD != cwd || b.SessionID == "" {
		t.Fatalf("binding = %+v, %v", b, ok)
	}
}

func TestMessageWhileBusySteers(t *testing.T) {
	h, rec, _ := newHub(t)
	ctx := context.Background()
	cwd := t.TempDir()
	if err := h.Handle(ctx, Message{Conv: "test:1", Route: Route{CWD: cwd}, Author: "alice", Text: "slow"}); err != nil {
		t.Fatal(err)
	}
	// The text goes out with the call that follows it, and the call's
	// end edits the same stretch.
	rec.expect(t, "start", `post 0 [read a.go=in_progress] "Looking"`)
	if err := h.Handle(ctx, Message{Conv: "test:1", Route: Route{CWD: cwd}, Author: "bob", Text: "use the helper"}); err != nil {
		t.Fatal(err)
	}
	rec.expect(t,
		`post 0 [read a.go=completed] "Looking"`,
		`post 1 [] "Done after [bob] use the helper"`,
		"end end_turn err=<nil> idle=true")
}

func TestKonExitEndsTurnAndResumes(t *testing.T) {
	h, rec, st := newHub(t)
	ctx := context.Background()
	cwd := t.TempDir()
	if err := h.Handle(ctx, Message{Conv: "test:1", Route: Route{CWD: cwd}, Author: "alice", Text: "die"}); err != nil {
		t.Fatal(err)
	}
	rec.expect(t, "start", "end  err=kon exited; the next message starts it again idle=true")
	first, _ := st.Get("test:1")
	if err := h.Handle(ctx, Message{Conv: "test:1", Route: Route{CWD: cwd}, Author: "alice", Text: "again"}); err != nil {
		t.Fatal(err)
	}
	rec.expect(t, "start", `post 1 [] "echo: [alice] again"`, "end end_turn")
	if again, _ := st.Get("test:1"); again.SessionID != first.SessionID {
		t.Fatalf("session changed from %s to %s instead of resuming", first.SessionID, again.SessionID)
	}
}

func TestChangedCWDStartsANewSession(t *testing.T) {
	h, rec, st := newHub(t)
	ctx := context.Background()
	h.Handle(ctx, Message{Conv: "test:1", Route: Route{CWD: t.TempDir()}, Author: "a", Text: "one"})
	rec.expect(t, "start", "post", "end")
	first, _ := st.Get("test:1")
	cwd := t.TempDir()
	h.Handle(ctx, Message{Conv: "test:1", Route: Route{CWD: cwd}, Author: "a", Text: "two"})
	rec.expect(t, "start", "post", "end")
	if second, _ := st.Get("test:1"); second.SessionID == first.SessionID || second.CWD != cwd {
		t.Fatalf("binding = %+v after moving from %+v", second, first)
	}
}

func TestSetOptionAndReset(t *testing.T) {
	h, _, st := newHub(t)
	ctx := context.Background()
	cwd := t.TempDir()
	options, err := h.SetOption(ctx, "test:1", Route{CWD: cwd}, "model", "b")
	if err != nil {
		t.Fatal(err)
	}
	if options[0].CurrentValue != "b" {
		t.Fatalf("options = %+v", options)
	}
	if st, ok := h.Status("test:1"); !ok || st.Options[0].CurrentValue != "b" {
		t.Fatalf("status = %+v, %v", st, ok)
	}
	if _, ok := st.Get("test:1"); ok {
		t.Fatal("a session never prompted was remembered; kon will have discarded it")
	}
	if err := h.Reset(ctx, "test:1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.Status("test:1"); ok {
		t.Fatal("reset kept the session open")
	}
}

func TestResetEndsARunningTurn(t *testing.T) {
	h, rec, _ := newHub(t)
	ctx := context.Background()
	if err := h.Handle(ctx, Message{Conv: "test:1", Route: Route{CWD: t.TempDir()}, Author: "alice", Text: "slow"}); err != nil {
		t.Fatal(err)
	}
	rec.expect(t, "start", `post 0 [read a.go=in_progress] "Looking"`)
	if err := h.Reset(ctx, "test:1"); err != nil {
		t.Fatal(err)
	}
	rec.expect(t, "end cancelled err=<nil> idle=true")
}

// TestStretchesFollowWhatKonSays is the shape a chat shows: each thing kon
// says is a new message, and the tool calls after it are edited into it as
// they start and finish.
func TestStretchesFollowWhatKonSays(t *testing.T) {
	h, rec, _ := newHub(t)
	if err := h.Handle(context.Background(), Message{Conv: "test:1", Route: Route{CWD: t.TempDir()}, Author: "alice", Text: "story"}); err != nil {
		t.Fatal(err)
	}
	rec.expect(t, "start",
		`post 0 [read a.go=in_progress] "sure, let me help"`,
		`post 0 [read a.go=completed] "sure, let me help"`,
		`post 0 [read a.go=completed read b.go=in_progress] "sure, let me help"`,
		`post 0 [read a.go=completed read b.go=completed] "sure, let me help"`,
		`post 1 [shell go test=in_progress] "hit a snag"`,
		`post 1 [shell go test=completed] "hit a snag"`,
		`post 2 [] "done!"`,
		"end end_turn")
}

func TestNewSessionsAreToldTheyAreInAChat(t *testing.T) {
	h, rec, _ := newHub(t)
	route := Route{CWD: t.TempDir(), Instructions: "Discord renders Markdown."}
	if err := h.Handle(context.Background(), Message{Conv: "test:1", Route: route, Author: "alice", Text: "instructions?"}); err != nil {
		t.Fatal(err)
	}
	rec.expect(t, "start")
	got := <-rec.events
	if !strings.Contains(got, "[alice]") || !strings.HasSuffix(got, `Discord renders Markdown."`) {
		t.Fatalf("session was told %s", got)
	}
}

func TestAttachmentsAreSavedAndListed(t *testing.T) {
	h, rec, _ := newHub(t)
	files := []Attachment{
		{Name: "../notes.zip", MIME: "application/zip", Data: []byte("PK")},
		{Name: "notes.zip", Data: []byte("again")},
	}
	if err := h.Handle(context.Background(), Message{Conv: "test:1", Route: Route{CWD: t.TempDir()}, Author: "alice", Attachments: files}); err != nil {
		t.Fatal(err)
	}
	rec.expect(t, "start")
	got := <-rec.events
	paths := regexp.MustCompile(`- (\S+) \(([^)]*)\)`).FindAllStringSubmatch(got, -1)
	if !strings.Contains(got, "[alice] sent 2 attachment(s).") || len(paths) != 2 {
		t.Fatalf("kon was sent %s", got)
	}
	want := []struct{ base, about, data string }{
		{"notes.zip", "application/zip, 2 bytes", "PK"},
		{"2-notes.zip", "5 bytes", "again"},
	}
	for i, w := range want {
		p := paths[i][1]
		if filepath.Base(p) != w.base || paths[i][2] != w.about {
			t.Fatalf("attachment %d listed as %s (%s)", i, p, paths[i][2])
		}
		if data, err := os.ReadFile(p); err != nil || string(data) != w.data {
			t.Fatalf("%s holds %q, %v", p, data, err)
		}
	}
	rec.expect(t, "end end_turn")
	status, _ := h.Status("test:1")
	session := filepath.Dir(filepath.Dir(paths[0][1]))
	if filepath.Base(session) != status.SessionID {
		t.Fatalf("attachments saved in %s, not under session %s", session, status.SessionID)
	}
	if err := h.Reset(context.Background(), "test:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(session); !os.IsNotExist(err) {
		t.Fatalf("attachments outlived /new: %v", err)
	}
	h.Close()
	if _, err := os.Stat(filepath.Dir(session)); !os.IsNotExist(err) {
		t.Fatalf("attachment directory outlived Close: %v", err)
	}
}

// drafter is a recorder that is shown drafts too.
type drafter struct{ recorder }

func (d *drafter) Draft(conv string, p Post) {
	d.events <- fmt.Sprintf("draft %d %q", p.ID, p.Text)
}

func TestDraftsComeBeforeTheirStretchIsPosted(t *testing.T) {
	h, _, _ := newHub(t)
	d := &drafter{recorder{events: make(chan string, 64)}}
	h.Register("draft", d)
	if err := h.Handle(context.Background(), Message{Conv: "draft:1", Route: Route{CWD: t.TempDir()}, Author: "alice", Text: "story"}); err != nil {
		t.Fatal(err)
	}
	type draft struct {
		id   int
		text string
	}
	var drafts []draft
	posted := map[int]string{}
	for {
		var ev string
		select {
		case ev = <-d.events:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the turn to end")
		}
		var id int
		var text string
		switch {
		case strings.HasPrefix(ev, "end"):
			// Drafts coalesce, so which ones arrive depends on timing; what
			// holds is that each is the start of its stretch, shown before it.
			for _, dr := range drafts {
				if !strings.HasPrefix(posted[dr.id], dr.text) {
					t.Fatalf("draft %q of stretch %d is not the start of %q", dr.text, dr.id, posted[dr.id])
				}
			}
			return
		case strings.HasPrefix(ev, "draft"):
			fmt.Sscanf(ev, "draft %d %q", &id, &text)
			if _, ok := posted[id]; ok {
				t.Fatalf("%s came after stretch %d was posted", ev, id)
			}
			drafts = append(drafts, draft{id, text})
		case strings.HasPrefix(ev, "post"):
			fmt.Sscanf(ev, "post %d", &id)
			_, quoted, _ := strings.Cut(ev, "] ")
			fmt.Sscanf(quoted, "%q", &text)
			posted[id] = text
		}
	}
}
