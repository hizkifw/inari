package discord

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hizkifw/inari/internal/hub"
)

// fakeChannel is a messenger that keeps messages in memory and logs each
// request.
type fakeChannel struct {
	mu   sync.Mutex
	msgs map[string]string
	log  []string
}

func (f *fakeChannel) send(content, replyTo string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.msgs == nil {
		f.msgs = map[string]string{}
	}
	id := fmt.Sprint(len(f.msgs) + 1)
	f.msgs[id] = content
	if replyTo != "" {
		f.log = append(f.log, "send "+id+" replying to "+replyTo)
	} else {
		f.log = append(f.log, "send "+id)
	}
	return id
}

func (f *fakeChannel) edit(id, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs[id] = content
	f.log = append(f.log, "edit "+id)
}

func (f *fakeChannel) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

func (f *fakeChannel) message(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.msgs[id]
}

func tools(statuses ...string) []hub.Tool {
	var out []hub.Tool
	for i, s := range statuses {
		out = append(out, hub.Tool{Title: fmt.Sprintf("read %d", i), Status: s})
	}
	return out
}

func TestStreamEditsAStretchAndSendsTheNext(t *testing.T) {
	f := &fakeChannel{}
	s := &stream{every: 0}
	s.post(f, hub.Post{ID: 1, Text: "sure", Tools: tools("in_progress")})
	s.post(f, hub.Post{ID: 1, Text: "sure", Tools: tools(hub.ToolCompleted)})
	waitFor(t, func() bool { return len(f.requests()) == 2 })
	s.post(f, hub.Post{ID: 2, Text: "done!"})
	want := []string{"send 1", "edit 1", "send 2"}
	if got := f.requests(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("requests = %q, want %q", got, want)
	}
	if got := f.message("1"); got != "sure\n-# ✓ `read 0`" {
		t.Fatalf("first message = %q", got)
	}
}

func TestStreamThrottlesEditsToTheLatest(t *testing.T) {
	f := &fakeChannel{}
	s := &stream{every: 50 * time.Millisecond}
	s.post(f, hub.Post{ID: 1, Text: "go", Tools: tools("in_progress")})
	s.post(f, hub.Post{ID: 1, Text: "go", Tools: tools(hub.ToolCompleted, "in_progress")})
	s.post(f, hub.Post{ID: 1, Text: "go", Tools: tools(hub.ToolCompleted, hub.ToolCompleted)})
	waitFor(t, func() bool { return len(f.requests()) == 2 })
	time.Sleep(100 * time.Millisecond)
	if got := f.requests(); len(got) != 2 || got[1] != "edit 1" {
		t.Fatalf("requests = %q, want one send and one edit", got)
	}
	if got := f.message("1"); got != "go\n-# ✓ `read 0`\n-# ✓ `read 1`" {
		t.Fatalf("message = %q", got)
	}
}

// A stretch's last change must show before the next stretch or the turn's
// end, however recently it was edited.
func TestStreamSettlesBeforeMovingOn(t *testing.T) {
	f := &fakeChannel{}
	s := &stream{every: time.Hour}
	s.post(f, hub.Post{ID: 1, Text: "go", Tools: tools("in_progress")})
	s.post(f, hub.Post{ID: 1, Text: "go", Tools: tools(hub.ToolCompleted)})
	s.flush(f)
	s.post(f, hub.Post{ID: 2, Text: "next", Tools: tools("in_progress")})
	s.post(f, hub.Post{ID: 2, Text: "next", Tools: tools(hub.ToolFailed)})
	s.post(f, hub.Post{ID: 3, Text: "done"})
	want := []string{"send 1", "edit 1", "send 2", "edit 2", "send 3"}
	if got := f.requests(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("requests = %q, want %q", got, want)
	}
}

func TestStreamSkipsUnchangedMessages(t *testing.T) {
	f := &fakeChannel{}
	s := &stream{every: time.Hour}
	s.post(f, hub.Post{ID: 1, Text: "same"})
	s.post(f, hub.Post{ID: 1, Text: "same"})
	s.flush(f)
	if got := f.requests(); len(got) != 1 {
		t.Fatalf("requests = %q", got)
	}
}

func TestStreamRepliesToSteeringOnce(t *testing.T) {
	f := &fakeChannel{}
	s := &stream{every: time.Millisecond}
	// Long enough for two messages: only the first is the reply.
	long := strings.Repeat("word ", maxMessage/4)
	s.post(f, hub.Post{ID: 1, Text: long, ReplyTo: "m9"})
	s.post(f, hub.Post{ID: 2, Text: "next"})
	got := strings.Join(f.requests(), ", ")
	if want := "send 1 replying to m9, send 2, send 3"; got != want {
		t.Fatalf("requests = %s, want %s", got, want)
	}
}
