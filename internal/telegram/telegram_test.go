package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hizkifw/inari/internal/acp/acptest"
	"github.com/hizkifw/inari/internal/config"
	"github.com/hizkifw/inari/internal/hub"
	"github.com/hizkifw/inari/internal/store"
)

// TestMain lets the test binary stand in for kon acp; see acptest.
func TestMain(m *testing.M) {
	acptest.Main(m)
}

// fakeTelegram is a Bot API server that hands out queued updates and
// reports every other request as "method {params}".
type fakeTelegram struct {
	updates  chan update
	requests chan string
	nextID   atomic.Int64
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	body, _ := io.ReadAll(r.Body)
	var result any = true
	switch method {
	case "getMe":
		result = user{ID: 1, IsBot: true, Username: "inari_bot"}
	case "getUpdates":
		var batch []update
		select {
		case u := <-f.updates:
			batch = append(batch, u)
		case <-time.After(50 * time.Millisecond):
		case <-r.Context().Done():
		}
		result = batch
	case "sendRichMessage", "sendMessage":
		result = message{MessageID: f.nextID.Add(1)}
	}
	if method != "getUpdates" {
		f.requests <- method + " " + string(body)
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

// await reads requests until one to method contains want, and returns it.
// An empty method matches any.
func (f *fakeTelegram) await(t *testing.T, method, want string) string {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case r := <-f.requests:
			if strings.HasPrefix(r, method) && strings.Contains(r, want) && !strings.HasPrefix(r, "sendChatAction") {
				return r
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s with %q", method, want)
		}
	}
}

func start(t *testing.T) *fakeTelegram {
	return startWith(t, func(*config.Telegram) {})
}

// startWith runs a connector whose config change has made.
func startWith(t *testing.T, change func(*config.Telegram)) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{updates: make(chan update, 8), requests: make(chan string, 256)}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	st, err := store.Open(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.New(hub.Kon{Command: acptest.Command(t)}, "test", st, log)
	t.Cleanup(h.Close)
	cfg := &config.Telegram{Token: "T", APIURL: srv.URL, Access: config.Access{Users: []string{"42"}},
		Routes: config.Routes{DefaultCWD: t.TempDir()}}
	change(cfg)
	c := New(cfg, h, log)
	h.Register(connector, c)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("run: %v", err)
		}
	})
	f.await(t, "setMyCommands", `"command":"cancel"`)
	return f
}

var alice = &user{ID: 42, FirstName: "Alice", Username: "alice"}

func private(text string) update {
	return update{Message: &message{MessageID: 1, From: alice, Chat: chat{ID: 42, Type: "private"}, Text: text}}
}

func TestPrivateChatStreamsAndPosts(t *testing.T) {
	f := start(t)
	f.updates <- private("story")
	f.await(t, "sendRichMessage ", `{"chat_id":42,"rich_message":{"markdown":"sure, let me help\n\n`)
	f.await(t, "sendRichMessage", `"markdown":"hit a snag`)
	f.await(t, "sendRichMessage", `"markdown":"done!"`)
}

func TestDraftsShowInPrivateChats(t *testing.T) {
	f := start(t)
	f.updates <- private("pause")
	// kon stops mid-sentence, so what it has written so far is a draft.
	got := f.await(t, "sendRichMessageDraft", `"draft_id":1,"rich_message":{"markdown":"Writing"}`)
	if strings.Contains(got, "can_stop") || !strings.Contains(got, `"chat_id":42`) {
		t.Fatalf("draft = %s", got)
	}
	f.updates <- private("go on")
	f.await(t, "sendRichMessage ", `"markdown":"Writing more"`)
}

func TestStopButtonCancelsTheTurn(t *testing.T) {
	f := startWith(t, func(c *config.Telegram) { c.StopButton = true })
	f.updates <- private("pause")
	f.await(t, "sendRichMessageDraft", `"can_stop":true`)
	f.updates <- update{Stopped: &generationStopped{Chat: chat{ID: 42, Type: "private"}, DraftID: 1}}
	// What kon wrote before it stopped is kept as a message.
	f.await(t, "sendRichMessage ", `"markdown":"Writing"`)
	f.await(t, "sendRichMessage ", `Cancelled.`)
}

func TestGroupsNeedAccessAndTopicsAreTheirOwn(t *testing.T) {
	f := start(t)
	stranger := &user{ID: 7, FirstName: "Mallory"}
	f.updates <- update{Message: &message{From: stranger, Chat: chat{ID: -100, Type: "supergroup"}, Text: "/status"}}
	f.updates <- update{Message: &message{From: alice, Chat: chat{ID: -100, Type: "supergroup"}, ThreadID: 5, IsTopic: true, Text: "/status@other_bot"}}
	f.updates <- update{Message: &message{From: alice, Chat: chat{ID: -100, Type: "supergroup"}, ThreadID: 5, IsTopic: true, Text: "/status@inari_bot"}}
	got := f.await(t, "sendRichMessage", "No session is open here yet")
	if !strings.Contains(got, `"message_thread_id":5`) {
		t.Fatalf("answer went elsewhere than the topic: %s", got)
	}
	f.updates <- update{Message: &message{From: alice, Chat: chat{ID: -100, Type: "supergroup"}, ThreadID: 5, IsTopic: true, Text: "hi"}}
	// Groups have no drafts, so the first request about the answer is the
	// message itself.
	if got := f.await(t, "", "echo: [Alice (@alice)] hi"); !strings.HasPrefix(got, "sendRichMessage ") {
		t.Fatalf("first answer request: %s", got)
	}
}

func TestStrangersInPrivateChatsAreToldTheirID(t *testing.T) {
	f := start(t)
	mallory := &user{ID: 7, FirstName: "Mallory"}
	f.updates <- update{Message: &message{From: mallory, Chat: chat{ID: -100, Type: "supergroup"}, Text: "hi"}}
	f.updates <- update{Message: &message{From: mallory, Chat: chat{ID: 7, Type: "private"}, Text: "hi"}}
	// The group gets no answer, so the first message sent is the DM's.
	got := f.await(t, "sendRichMessage ", "allowlist")
	if !strings.Contains(got, `"chat_id":7`) || !strings.Contains(got, "user ID `7`") {
		t.Fatalf("refusal = %s", got)
	}
}

func TestModelButtons(t *testing.T) {
	f := start(t)
	f.updates <- private("/model")
	f.await(t, "sendRichMessage", `"inline_keyboard":[[{"text":"• Model A","callback_data":"set:model:0"}],[{"text":"Model B","callback_data":"set:model:1"}]]`)
	f.updates <- update{CallbackQuery: &callbackQuery{ID: "q", From: *alice, Data: "set:model:1",
		Message: &message{MessageID: 9, Chat: chat{ID: 42, Type: "private"}, Date: 1}}}
	f.await(t, "answerCallbackQuery", `"callback_query_id":"q"`)
	f.await(t, "editMessageText", fmt.Sprintf(`"message_id":%d,"rich_message":{"markdown":"Alice (@alice) set model to Model B."}`, 9))
	f.updates <- private("/model a")
	f.await(t, "sendRichMessage", "set model to Model A.")
}
