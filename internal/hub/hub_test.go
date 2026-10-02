package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hizkifw/inari/internal/store"
)

// TestMain lets the test binary stand in for kon acp: run with
// INARI_FAKE_AGENT set, it serves a scripted agent on stdio.
func TestMain(m *testing.M) {
	if os.Getenv("INARI_FAKE_AGENT") != "" {
		fakeAgent(os.Stdin, os.Stdout)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeAgent answers just enough ACP for the hub. A prompt is echoed back,
// except that "slow" starts a turn that waits for steering and "die" exits
// mid-turn.
func fakeAgent(r io.Reader, w io.Writer) {
	var mu sync.Mutex
	enc := json.NewEncoder(w)
	send := func(v any) {
		mu.Lock()
		defer mu.Unlock()
		enc.Encode(v)
	}
	update := func(id string, u map[string]any) {
		send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": id, "update": u}})
	}
	text := func(id, kind, s string) {
		update(id, map[string]any{"sessionUpdate": kind, "content": map[string]any{"type": "text", "text": s}})
	}
	options := func(model string) []map[string]any {
		return []map[string]any{{"id": "model", "name": "Model", "category": "model", "type": "select", "currentValue": model,
			"options": []map[string]string{{"value": "a", "name": "Model A"}, {"value": "b", "name": "Model B"}}}}
	}
	steers := make(chan string, 1)
	var running sync.Mutex
	busy := false
	sessions := 0
	instructions := map[string]string{}

	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
				Text      string `json:"text"`
				Value     string `json:"value"`
				Prompt    []struct {
					Text string `json:"text"`
				} `json:"prompt"`
				Meta struct {
					Kon struct {
						Instructions string `json:"instructions"`
					} `json:"kon.kitsu.red"`
				} `json:"_meta"`
			} `json:"params"`
		}
		json.Unmarshal(sc.Bytes(), &m)
		reply := func(result any) { send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result}) }
		fail := func(code int, msg string) {
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": code, "message": msg}})
		}
		id := m.Params.SessionID
		switch m.Method {
		case "initialize":
			reply(map[string]any{"protocolVersion": 1, "agentInfo": map[string]string{"name": "fake", "version": "v0"}})
		case "session/new":
			sessions++
			sid := fmt.Sprintf("ses_%d_%d", os.Getpid(), sessions)
			mu.Lock()
			instructions[sid] = m.Params.Meta.Kon.Instructions
			mu.Unlock()
			reply(map[string]any{"sessionId": sid, "configOptions": options("a")})
		case "session/resume":
			reply(map[string]any{"configOptions": options("a")})
		case "session/close":
			reply(map[string]any{})
		case "session/set_config_option":
			reply(map[string]any{"configOptions": options(m.Params.Value)})
		case "_kon.kitsu.red/steer":
			running.Lock()
			if !busy {
				running.Unlock()
				fail(-32600, "no turn is running; send session/prompt instead")
				continue
			}
			running.Unlock()
			steers <- m.Params.Text
			reply(map[string]any{})
		case "session/prompt":
			var prompt string
			for _, b := range m.Params.Prompt {
				prompt += b.Text
			}
			running.Lock()
			busy = true
			running.Unlock()
			go func() {
				defer func() {
					running.Lock()
					busy = false
					running.Unlock()
				}()
				switch {
				case strings.HasSuffix(prompt, "die"):
					os.Exit(1)
				case strings.HasSuffix(prompt, "instructions?"):
					mu.Lock()
					said := instructions[id]
					mu.Unlock()
					text(id, "agent_message_chunk", said)
				case strings.HasSuffix(prompt, "story"):
					call := func(id, call, title string) {
						update(id, map[string]any{"sessionUpdate": "tool_call", "toolCallId": call, "title": title, "status": "in_progress"})
						update(id, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": call, "content": []any{}})
						update(id, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": call, "status": "completed"})
					}
					text(id, "agent_message_chunk", "sure, ")
					text(id, "agent_message_chunk", "let me help")
					call(id, "c1", "read a.go")
					call(id, "c2", "read b.go")
					text(id, "agent_message_chunk", "hit a snag")
					call(id, "c3", "shell go test")
					text(id, "agent_message_chunk", "done!")
				case strings.HasSuffix(prompt, "slow"):
					text(id, "agent_message_chunk", "Looking")
					update(id, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "c1", "title": "read a.go", "status": "in_progress"})
					steer := <-steers
					update(id, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "c1", "status": "completed"})
					text(id, "user_message_chunk", steer)
					text(id, "agent_message_chunk", "Done after "+steer)
				default:
					text(id, "agent_message_chunk", "echo: "+prompt)
				}
				reply(map[string]any{"stopReason": "end_turn"})
			}()
		default:
			fail(-32601, "method not found")
		}
	}
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
	t.Setenv("INARI_FAKE_AGENT", "1")
	st, err := store.Open(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := New(Kon{Command: os.Args[0]}, "test", st, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
