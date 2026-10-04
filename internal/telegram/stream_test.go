package telegram

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hizkifw/inari/internal/hub"
)

// fakeChat is a messenger that logs each request.
type fakeChat struct {
	mu   sync.Mutex
	next int64
	log  []string
}

func (f *fakeChat) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, s)
}

func (f *fakeChat) send(p part) int64 {
	f.mu.Lock()
	f.next++
	id := f.next
	f.mu.Unlock()
	f.record(fmt.Sprintf("send %d %s", id, p.rich))
	return id
}

func (f *fakeChat) edit(id int64, p part) { f.record(fmt.Sprintf("edit %d %s", id, p.rich)) }

func (f *fakeChat) draft(id int, p part) time.Duration {
	f.record(fmt.Sprintf("draft %d %s", id, p.rich))
	return 0
}

func (f *fakeChat) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.log)
}

func TestDraftsCoalesceAndStopAtThePost(t *testing.T) {
	f := &fakeChat{}
	s := &stream{every: time.Hour, draftEvery: 50 * time.Millisecond}
	s.showDraft(f, hub.Post{ID: 0, Text: "Hel"})
	time.Sleep(20 * time.Millisecond)
	// These come within draftEvery of the first, so only the last shows.
	s.showDraft(f, hub.Post{ID: 0, Text: "Hello"})
	s.showDraft(f, hub.Post{ID: 0, Text: "Hello there"})
	time.Sleep(100 * time.Millisecond)
	s.showDraft(f, hub.Post{ID: 0, Text: "Hello there, friend"})
	// The post comes before that draft is due, and takes its place.
	s.post(f, hub.Post{ID: 0, Text: "Hello there, friend!"})
	s.showDraft(f, hub.Post{ID: 0, Text: "late"})
	time.Sleep(100 * time.Millisecond)
	want := []string{"draft 1 Hel", "draft 1 Hello there", "send 1 Hello there, friend!"}
	if got := f.requests(); !slices.Equal(got, want) {
		t.Fatalf("requests = %q, want %q", got, want)
	}
}

func TestStreamEditsToolCallsIntoThePost(t *testing.T) {
	f := &fakeChat{}
	s := &stream{every: 30 * time.Millisecond, draftEvery: time.Millisecond}
	s.post(f, hub.Post{ID: 0, Text: "Looking", Tools: []hub.Tool{{Title: "read a.go"}}})
	s.post(f, hub.Post{ID: 0, Text: "Looking", Tools: []hub.Tool{{Title: "read a.go", Status: hub.ToolCompleted}}})
	time.Sleep(60 * time.Millisecond)
	s.showDraft(f, hub.Post{ID: 1, Text: "Found"})
	time.Sleep(20 * time.Millisecond)
	s.post(f, hub.Post{ID: 1, Text: "Found it"})
	want := []string{
		"send 1 Looking\n\n<footer>… read a.go</footer>",
		"edit 1 Looking\n\n<footer>✓ read a.go</footer>",
		"draft 2 Found",
		"send 2 Found it",
	}
	if got := f.requests(); !slices.Equal(got, want) {
		t.Fatalf("requests = %q, want %q", got, want)
	}
}

func TestRender(t *testing.T) {
	tools := make([]hub.Tool, maxTools+2)
	for i := range tools {
		tools[i] = hub.Tool{Title: fmt.Sprintf("shell echo <%d> & more", i), Status: hub.ToolFailed}
	}
	parts := render(hub.Post{Text: "**done**", Tools: tools})
	if len(parts) != 1 {
		t.Fatalf("got %d parts", len(parts))
	}
	rich, plain := parts[0].rich, parts[0].plain
	if !strings.HasPrefix(rich, "**done**\n\n<footer>… 2 earlier</footer>\n<footer>✗ shell echo &lt;2&gt; &amp; more</footer>") {
		t.Errorf("rich = %q", rich)
	}
	if !strings.HasPrefix(plain, "**done**\n… 2 earlier\n✗ shell echo <2> & more") {
		t.Errorf("plain = %q", plain)
	}
	notice := render(hub.Post{Text: "⏰ job <x> finished\n\nsee above", Notice: true})
	if want := "<footer>⏰ job &lt;x&gt; finished</footer>\n<footer>see above</footer>"; notice[0].rich != want {
		t.Errorf("notice = %q, want %q", notice[0].rich, want)
	}
	long := render(hub.Post{Text: strings.Repeat("a\n", maxRich)})
	if len(long) != 3 || len([]rune(long[0].plain)) > maxPlain {
		t.Errorf("long text gave %d parts, the first plain %d long", len(long), len([]rune(long[0].plain)))
	}
}

func TestCommand(t *testing.T) {
	for text, want := range map[string]string{
		"/status":                "status  ",
		"/Model@inari_bot  opus": "model inari_bot opus",
		"/kill\n3":               "kill  3",
		"/etc/hosts is broken":   "etc/hosts  is broken",
	} {
		name, bot, args, ok := command(text)
		if got := name + " " + bot + " " + args; !ok || got != want {
			t.Errorf("command(%q) = %q, %v; want %q", text, got, ok, want)
		}
	}
	if _, _, _, ok := command("hi /status"); ok {
		t.Error("a message that does not start with a slash is a command")
	}
}
