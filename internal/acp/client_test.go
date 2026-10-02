package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"
)

// recorder is a Handler that keeps what it is sent.
type recorder struct {
	mu      sync.Mutex
	updates []Update
	events  []string
}

func (r *recorder) Update(id string, u Update) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates = append(r.updates, u)
}

func (r *recorder) TurnStart(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "start "+id)
}

func (r *recorder) TurnEnd(id, stop, errText string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "end "+id+" "+stop+errText)
}

// fakeAgent answers each request from the client with serve, which returns
// the lines to write: notifications, then the response.
func fakeAgent(t *testing.T, serve func(method string, id json.RawMessage, params json.RawMessage) []string) (*Client, *recorder) {
	c, rec, _ := fakeAgentHangup(t, serve)
	return c, rec
}

// fakeAgentHangup is fakeAgent with a function that makes the agent go away.
func fakeAgentHangup(t *testing.T, serve func(method string, id json.RawMessage, params json.RawMessage) []string) (*Client, *recorder, func()) {
	t.Helper()
	toAgent, fromClient := io.Pipe()
	toClient, fromAgent := io.Pipe()
	rec := &recorder{}
	c := New(toClient, fromClient, rec)
	go func() {
		sc := bufio.NewScanner(toAgent)
		for sc.Scan() {
			var m struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("client sent invalid JSON: %v", err)
				return
			}
			for _, line := range serve(m.Method, m.ID, m.Params) {
				io.WriteString(fromAgent, line+"\n")
			}
		}
		fromAgent.Close()
	}()
	t.Cleanup(func() { fromClient.Close() })
	return c, rec, func() { fromAgent.Close() }
}

func result(id json.RawMessage, v string) string {
	return `{"jsonrpc":"2.0","id":` + string(id) + `,"result":` + v + `}`
}

func TestInitializeOptsIntoAgentTurns(t *testing.T) {
	var sent json.RawMessage
	c, _ := fakeAgent(t, func(method string, id, params json.RawMessage) []string {
		sent = params
		return []string{result(id, `{"protocolVersion":1,"agentInfo":{"name":"kon","version":"v0.4.0"}}`)}
	})
	a, err := c.Initialize(context.Background(), "inari", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if a.AgentInfo.Version != "v0.4.0" {
		t.Fatalf("agent = %+v", a)
	}
	var req struct {
		ClientCapabilities struct {
			Meta map[string]map[string]bool `json:"_meta"`
		} `json:"clientCapabilities"`
	}
	json.Unmarshal(sent, &req)
	if !req.ClientCapabilities.Meta["kon.kitsu.red"]["agentTurns"] {
		t.Fatalf("initialize did not opt into agent turns: %s", sent)
	}
}

func TestPromptStreamsUpdatesBeforeItsResult(t *testing.T) {
	c, rec := fakeAgent(t, func(method string, id, params json.RawMessage) []string {
		return []string{
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}`,
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_1","update":{"sessionUpdate":"tool_call","toolCallId":"c1","title":"read a.go","status":"in_progress","content":[{"type":"content","content":{"type":"text","text":"x"}}]}}}`,
			result(id, `{"stopReason":"end_turn"}`),
		}
	})
	stop, err := c.Prompt(context.Background(), "ses_1", []ContentBlock{TextBlock("hello")})
	if err != nil || stop != StopEndTurn {
		t.Fatalf("Prompt = %q, %v", stop, err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.updates) != 2 {
		t.Fatalf("updates = %+v", rec.updates)
	}
	if rec.updates[0].Content.Text != "hi" {
		t.Fatalf("chunk = %+v", rec.updates[0])
	}
	if u := rec.updates[1]; u.Kind != "tool_call" || u.Title != "read a.go" || u.Content.Text != "" {
		t.Fatalf("tool call = %+v", u)
	}
}

func TestAgentErrorKeepsItsCode(t *testing.T) {
	c, _ := fakeAgent(t, func(method string, id, params json.RawMessage) []string {
		return []string{`{"jsonrpc":"2.0","id":` + string(id) + `,"error":{"code":-32600,"message":"no turn is running"}}`}
	})
	err := c.Steer(context.Background(), "ses_1", "hi")
	if !IsCode(err, CodeInvalidRequest) {
		t.Fatalf("err = %v", err)
	}
}

func TestExtensionTurnNotifications(t *testing.T) {
	c, rec := fakeAgent(t, func(method string, id, params json.RawMessage) []string {
		return []string{
			`{"jsonrpc":"2.0","method":"_kon.kitsu.red/turn_start","params":{"sessionId":"ses_1"}}`,
			`{"jsonrpc":"2.0","method":"_kon.kitsu.red/turn_end","params":{"sessionId":"ses_1","stopReason":"end_turn"}}`,
			result(id, `{}`),
		}
	})
	if err := c.CloseSession(context.Background(), "ses_1"); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.events) != 2 || rec.events[0] != "start ses_1" || rec.events[1] != "end ses_1 end_turn" {
		t.Fatalf("events = %q", rec.events)
	}
}

func TestCallsFailWhenTheAgentGoes(t *testing.T) {
	sent := make(chan struct{})
	c, _, hangup := fakeAgentHangup(t, func(method string, id, params json.RawMessage) []string {
		close(sent)
		return nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := c.Prompt(context.Background(), "ses_1", nil)
		done <- err
	}()
	<-sent
	hangup()
	select {
	case err := <-done:
		if err != ErrClosed {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call did not fail when the agent went away")
	}
	<-c.Done()
	if _, err := c.Prompt(context.Background(), "ses_1", nil); err != ErrClosed {
		t.Fatalf("call after close: %v", err)
	}
}
