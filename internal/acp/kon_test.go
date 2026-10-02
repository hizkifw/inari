package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRealKon speaks to the kon INARI_KON names, which must be one with
// session instructions (v0.1.15 or later). kon gets empty config and data
// directories, so it touches none of the user's state, and a model served by
// the test, so the system prompt kon sends can be read back.
func TestRealKon(t *testing.T) {
	kon := os.Getenv("INARI_KON")
	if kon == "" || testing.Short() {
		t.Skip("set INARI_KON to a kon executable to run")
	}
	model := &fakeModel{reply: "Hello from the model."}
	server := httptest.NewServer(model)
	defer server.Close()

	config := t.TempDir()
	writeKonConfig(t, config, server.URL)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rec := &recorder{}
	c, err := Spawn(kon, []string{"acp"}, rec)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	agent, err := c.Initialize(ctx, "inari", "test")
	if err != nil {
		t.Fatal(err)
	}
	if agent.ProtocolVersion != ProtocolVersion || agent.AgentInfo.Name != "kon" {
		t.Fatalf("agent = %+v", agent)
	}
	if !agent.Supports(ExtensionInstructions) {
		t.Fatal("kon does not take session instructions; it needs v0.1.15 or later")
	}
	s, err := c.NewSession(ctx, t.TempDir(), "Answer in British English.")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Steer(ctx, s.SessionID, "hi"); !IsCode(err, CodeInvalidRequest) {
		t.Fatalf("steering an idle session: %v", err)
	}
	stop, err := c.Prompt(ctx, s.SessionID, []ContentBlock{TextBlock("[alice] hi")})
	if err != nil {
		t.Fatal(err)
	}
	if stop != StopEndTurn {
		t.Fatalf("stop reason = %q", stop)
	}

	system, user := model.request()
	if !strings.Contains(system, "Answer in British English.") {
		t.Fatalf("the system prompt kon sent lacks the session's instructions:\n%s", system)
	}
	if !strings.Contains(user, "[alice] hi") {
		t.Fatalf("the prompt kon sent = %q", user)
	}
	rec.mu.Lock()
	var said strings.Builder
	for _, u := range rec.updates {
		if u.Kind == "agent_message_chunk" {
			said.WriteString(u.Content.Text)
		}
	}
	rec.mu.Unlock()
	if said.String() != model.reply {
		t.Fatalf("kon relayed %q, want %q", said.String(), model.reply)
	}
	if err := c.CloseSession(ctx, s.SessionID); err != nil {
		t.Fatal(err)
	}
}

func writeKonConfig(t *testing.T, dir, url string) {
	t.Helper()
	config := map[string]any{
		"default_model": "fake",
		"models": []map[string]any{{
			"name": "fake", "type": "openai-compatible", "base_url": url, "model": "fake",
			"context_window_tokens": 100000,
		}},
	}
	data, _ := json.Marshal(config)
	if err := os.MkdirAll(filepath.Join(dir, "kon"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kon", "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeModel is an OpenAI-compatible chat completions endpoint that streams
// one reply and keeps the last request's system and user messages.
type fakeModel struct {
	reply string

	mu           sync.Mutex
	system, user string
}

func (m *fakeModel) request() (system, user string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.system, m.user
}

func (m *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(body, &req)
	m.mu.Lock()
	for _, msg := range req.Messages {
		switch msg.Role {
		case "system", "developer":
			m.system += text(msg.Content)
		case "user":
			m.user = text(msg.Content)
		}
	}
	m.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	chunk := func(v any) {
		data, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	chunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": m.reply}}}})
	chunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// text reads a message's content, which is a string or a list of parts.
func text(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}
