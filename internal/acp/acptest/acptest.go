// Package acptest is a scripted stand-in for kon acp, for tests of programs
// that drive it. The test binary serves it when run with a marker variable,
// so a test spawns it as it would spawn kon.
//
// A prompt is echoed back as "echo: <prompt>", except that a prompt ending in
// "slow" starts a turn that waits for steering, one ending in "story" plays a
// turn of text and tool calls, one ending in "instructions?" answers with the
// session's instructions, and one ending in "die" exits mid-turn. A cancel
// ends a waiting turn as cancelled.
package acptest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

const marker = "INARI_FAKE_AGENT"

// Main runs the tests, or serves the fake agent when the binary was started
// as one. Call it from TestMain.
func Main(m *testing.M) {
	if os.Getenv(marker) != "" {
		serve(os.Stdin, os.Stdout)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Command is the executable to spawn as kon: this test binary, marked to
// serve the fake agent.
func Command(t *testing.T) string {
	t.Setenv(marker, "1")
	return os.Args[0]
}

// serve answers just enough ACP for inari's hub.
func serve(r io.Reader, w io.Writer) {
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
	cancels := make(chan struct{}, 1)
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
		case "session/cancel":
			select {
			case cancels <- struct{}{}:
			default:
			}
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
					var steer string
					select {
					case steer = <-steers:
					case <-cancels:
						reply(map[string]any{"stopReason": "cancelled"})
						return
					}
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
