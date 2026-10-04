package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hizkifw/inari/internal/buildinfo"
)

// DefaultAPIURL is Telegram's own Bot API server.
const DefaultAPIURL = "https://api.telegram.org"

// maxRetryWait bounds how long a rate-limited call waits to try again. A
// longer wait is reported as the error instead, so one flood limit cannot
// stall a conversation for minutes.
const maxRetryWait = 10 * time.Second

// api is a Bot API client. Every method is a POST of JSON parameters, and
// every answer an envelope with ok, the result, or why it failed.
type api struct {
	// base and files hold the bot token, so neither may reach a log.
	base  string
	files string
	http  *http.Client
}

func newAPI(server, token string) *api {
	server = strings.TrimRight(server, "/")
	return &api{base: server + "/bot" + token, files: server + "/file/bot" + token, http: &http.Client{}}
}

// apiError is a request the Bot API refused.
type apiError struct {
	Method      string
	Code        int
	Description string
	// RetryAfter is how long to wait before trying again, when Code is 429.
	RetryAfter time.Duration
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram: %s: %s", e.Method, e.Description)
}

// errorCode returns the Bot API's error code for err, or 0 when the request
// never got an answer.
func errorCode(err error) int {
	var e *apiError
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

// call sends method and decodes its result into result, which may be nil.
// A rate-limited call waits as told and tries again.
func (a *api) call(ctx context.Context, method string, params, result any) error {
	for tries := 0; ; tries++ {
		err := a.once(ctx, method, params, result)
		var e *apiError
		if tries == 2 || !errors.As(err, &e) || e.Code != http.StatusTooManyRequests || e.RetryAfter > maxRetryWait {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(e.RetryAfter):
		}
	}
}

// once sends method a single time.
func (a *api) once(ctx context.Context, method string, params, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: %s: %w", method, redact(err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", buildinfo.UserAgent())
	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: %s: %w", method, redact(err))
	}
	defer resp.Body.Close()
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("telegram: %s: %s", method, resp.Status)
	}
	if !env.OK {
		return &apiError{Method: method, Code: env.ErrorCode, Description: env.Description, RetryAfter: time.Duration(env.Parameters.RetryAfter) * time.Second}
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(env.Result, result)
}

// download fetches a file getFile named, reading at most limit bytes.
func (a *api) download(ctx context.Context, path string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.files+"/"+path, nil)
	if err != nil {
		return nil, redact(err)
	}
	req.Header.Set("User-Agent", buildinfo.UserAgent())
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, redact(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, redact(err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("larger than %d MB", limit>>20)
	}
	return data, nil
}

// redact drops the URL from an HTTP client's error, since the URL carries
// the bot token.
func redact(err error) error {
	var u *url.Error
	if errors.As(err, &u) {
		return u.Err
	}
	return err
}

// The types below hold only the fields inari reads.

type update struct {
	UpdateID      int64              `json:"update_id"`
	Message       *message           `json:"message"`
	CallbackQuery *callbackQuery     `json:"callback_query"`
	Stopped       *generationStopped `json:"stopped_message_generation"`
}

type message struct {
	MessageID int64 `json:"message_id"`
	ThreadID  int64 `json:"message_thread_id"`
	// IsTopic tells a forum topic's thread from a reply thread, which also
	// carries a ThreadID.
	IsTopic    bool   `json:"is_topic_message"`
	From       *user  `json:"from"`
	SenderChat *chat  `json:"sender_chat"`
	Chat       chat   `json:"chat"`
	Date       int64  `json:"date"`
	Text       string `json:"text"`
	Caption    string `json:"caption"`
	Photo      []file `json:"photo"`
	Document   *file  `json:"document"`
	Audio      *file  `json:"audio"`
	Video      *file  `json:"video"`
	Voice      *file  `json:"voice"`
	VideoNote  *file  `json:"video_note"`
	Animation  *file  `json:"animation"`
}

type user struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type chat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

// file is any file a message carries, a photo size included, and what
// getFile returns for it.
type file struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MIME     string `json:"mime_type"`
	Size     int64  `json:"file_size"`
	Path     string `json:"file_path"`
}

type callbackQuery struct {
	ID      string   `json:"id"`
	From    user     `json:"from"`
	Message *message `json:"message"`
	Data    string   `json:"data"`
}

// generationStopped is a user pressing a draft's stop button.
type generationStopped struct {
	Chat     chat  `json:"chat"`
	ThreadID int64 `json:"message_thread_id"`
	DraftID  int64 `json:"draft_id"`
}

type richMessage struct {
	Markdown string `json:"markdown"`
}

type inlineKeyboard struct {
	Rows [][]inlineButton `json:"inline_keyboard"`
}

type inlineButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

type botCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}
