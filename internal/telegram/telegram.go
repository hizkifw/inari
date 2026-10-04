// Package telegram connects Telegram chats to the hub. Each chat, or forum
// topic, kon serves has one session that everyone in it shares; messages are
// prompts, or steering while kon works, and bot commands control the session.
//
// Replies are rich messages, which take the model's Markdown as it is. In
// private chats kon's text streams in as a draft while it is written, and
// the draft can carry a stop button that cancels the turn.
package telegram

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hizkifw/inari/internal/config"
	"github.com/hizkifw/inari/internal/hub"
)

// connector is the name conversations are keyed under, as
// "telegram:<chat>" or "telegram:<chat>/<topic>".
const connector = "telegram"

// maxAttachment is the largest file the Bot API lets a bot download.
const maxAttachment = 20 << 20

const (
	// pollTimeout is how long one getUpdates waits for news.
	pollTimeout = 50 * time.Second
	// callTimeout bounds every other request.
	callTimeout = 30 * time.Second
	// typingEvery renews the typing action, which Telegram shows for five
	// seconds or until the bot sends a message.
	typingEvery = 4 * time.Second
)

// Connector is a running Telegram bot.
type Connector struct {
	cfg *config.Telegram
	hub *hub.Hub
	log *slog.Logger
	api *api
	// username is the bot's, for telling its commands from other bots',
	// and id its user ID, for telling its messages from others'.
	username string
	id       int64

	mu sync.Mutex
	// typing stops each busy conversation's typing action.
	typing map[string]context.CancelFunc
	// streams is each conversation's newest stretch, kept up to date.
	streams map[string]*stream
	// lanes handle each conversation's updates in order.
	lanes map[string]*lane
	// sent is the text of the bot's recent messages, by "<chat>:<message>",
	// and sentOrder their keys oldest first. A reply to a rich message
	// does not carry its text, so this is how a reply to kon is quoted.
	sent      map[string]string
	sentOrder []string
}

// maxSent is how many of its messages the bot remembers the text of.
const maxSent = 1000

// New returns a connector for cfg that sends messages to h. Register it with
// the hub before calling Run.
func New(cfg *config.Telegram, h *hub.Hub, log *slog.Logger) *Connector {
	server := cfg.APIURL
	if server == "" {
		server = DefaultAPIURL
	}
	return &Connector{cfg: cfg, hub: h, log: log.With("connector", connector), api: newAPI(server, cfg.Token),
		typing: map[string]context.CancelFunc{}, streams: map[string]*stream{}, lanes: map[string]*lane{}, sent: map[string]string{}}
}

// Run polls Telegram for updates and serves them until ctx ends.
func (c *Connector) Run(ctx context.Context) error {
	var me user
	if err := c.call(ctx, "getMe", struct{}{}, &me); err != nil {
		return fmt.Errorf("telegram: connect: %w", err)
	}
	c.username, c.id = me.Username, me.ID
	c.log.Info("connected", "user", me.Username)
	if err := c.call(ctx, "setMyCommands", map[string]any{"commands": commands}, nil); err != nil {
		c.log.Error("register commands", "err", err)
	}
	defer c.stopTyping()
	var offset int64
	for {
		var updates []update
		poll, cancel := context.WithTimeout(ctx, pollTimeout+callTimeout)
		err := c.api.call(poll, "getUpdates", map[string]any{
			"offset": offset, "timeout": int(pollTimeout.Seconds()),
			"allowed_updates": []string{"message", "callback_query", "stopped_message_generation"},
		}, &updates)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			// A revoked token never recovers, and another poller with the
			// same token would fight this one for every update.
			if code := errorCode(err); code == http.StatusUnauthorized || code == http.StatusConflict {
				return err
			}
			c.log.Warn("poll", "err", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			c.dispatch(u)
		}
	}
}

// dispatch hands an update to its conversation's lane: updates for one
// conversation are handled in the order they came, and a slow one, such as
// a session being opened, holds up no other.
func (c *Connector) dispatch(u update) {
	var key string
	var f func()
	switch {
	case u.Message != nil:
		m := u.Message
		key, f = convOf(m), func() { c.message(m) }
	case u.CallbackQuery != nil && u.CallbackQuery.Message != nil:
		q := u.CallbackQuery
		key, f = convOf(q.Message), func() { c.callback(q) }
	case u.Stopped != nil:
		s := u.Stopped
		key, f = conv(s.Chat.ID, s.ThreadID), func() { c.stopped(s) }
	default:
		return
	}
	c.mu.Lock()
	l, ok := c.lanes[key]
	if !ok {
		l = &lane{}
		c.lanes[key] = l
	}
	c.mu.Unlock()
	l.do(f)
}

func (c *Connector) message(m *message) {
	if m.From == nil || m.From.IsBot && m.SenderChat == nil {
		return
	}
	key := convOf(m)
	route, ok := c.allowed(m.From.ID, key)
	if !ok {
		// Telegram's apps show no IDs, so this is how to find them for
		// access.
		c.log.Debug("ignore message", "user", m.From.ID, "chat", key)
		if m.Chat.Type == "private" {
			c.refuse(key, m.From.ID)
		}
		return
	}
	text := m.Text
	if text == "" {
		text = m.Caption
	}
	if name, bot, args, ok := command(text); ok {
		if bot != "" && !strings.EqualFold(bot, c.username) {
			// Another bot's command, such as /help@otherbot.
			return
		}
		if known(name) {
			c.command(key, route, authorName(m), name, args)
			return
		}
		// Anything else that starts with a slash, such as a path, is
		// speech.
	}
	attachments, err := c.download(m)
	if err != nil {
		c.log.Warn("download attachment", "err", err)
		c.notice(key, "Could not read an attachment: "+err.Error())
		return
	}
	if strings.TrimSpace(text) == "" && len(attachments) == 0 {
		return
	}
	msg := hub.Message{Conv: connector + ":" + key, Route: route, ID: strconv.FormatInt(m.MessageID, 10), Author: authorName(m), Text: text, Attachments: attachments,
		Quote: c.quote(m)}
	if err := c.hub.Handle(context.Background(), msg); err != nil {
		c.log.Error("handle message", "chat", key, "err", err)
		c.notice(key, "⚠️ "+err.Error())
	}
}

// refuse tells someone in a private chat why the bot does not answer, with
// the ID that would let them in. Groups are left alone: the bot would answer
// every stranger there.
func (c *Connector) refuse(key string, user int64) {
	var text string
	if !c.trusted(user, key) {
		text = fmt.Sprintf("You are not on this bot's allowlist. To use it, add your user ID %s to `connectors.telegram.access.users` in inari's config.json.", code(strconv.FormatInt(user, 10)))
	} else {
		text = fmt.Sprintf("This chat has no working directory. Set `connectors.telegram.default_cwd`, or a `cwd` for channel %s, in inari's config.json.", code(key))
	}
	chatMessenger{c, targetOf(key)}.send(part{rich: text, plain: text}, "")
}

// stopped cancels the turn whose draft's stop button was pressed. Drafts are
// shown only in private chats, where the chat is the person.
func (c *Connector) stopped(s *generationStopped) {
	key := conv(s.Chat.ID, s.ThreadID)
	if _, ok := c.allowed(s.Chat.ID, key); !ok {
		return
	}
	if err := c.hub.Cancel(connector + ":" + key); err != nil {
		c.log.Warn("stop generation", "chat", key, "err", err)
	}
}

// allowed returns key's route when user may use kon there. A topic is
// trusted when it or its chat is listed.
func (c *Connector) allowed(user int64, key string) (hub.Route, bool) {
	if !c.trusted(user, key) {
		return hub.Route{}, false
	}
	return c.Route(key)
}

// trusted reports whether user may use kon in key, served or not.
func (c *Connector) trusted(user int64, key string) bool {
	id := strconv.FormatInt(user, 10)
	chat, _, _ := strings.Cut(key, "/")
	return c.cfg.Access.Allows(id, key) || c.cfg.Access.Allows(id, chat)
}

// Route is how key's sessions start, whoever is asking. A chat with no
// directory is not served.
func (c *Connector) Route(key string) (hub.Route, bool) {
	at := c.cfg.RouteKey(key)
	cwd := c.cfg.CWD(at)
	if cwd == "" {
		return hub.Route{}, false
	}
	instructions := strings.TrimSpace(strings.TrimSpace(telegramInstructions) + "\n\n" + c.cfg.Routes.InstructionsFor(at))
	return hub.Route{CWD: cwd, Instructions: instructions}, true
}

// telegramInstructions tell a session how its replies reach people, so it
// writes for Telegram rather than for a terminal.
//
//go:embed instructions.txt
var telegramInstructions string

// download fetches a message's files. A photo comes in several sizes, of
// which the largest is kept.
func (c *Connector) download(m *message) ([]hub.Attachment, error) {
	type item struct {
		f          *file
		name, mime string
	}
	var items []item
	if n := len(m.Photo); n > 0 {
		items = append(items, item{&m.Photo[n-1], "photo.jpg", "image/jpeg"})
	}
	for _, it := range []item{{m.Document, "document", ""}, {m.Audio, "audio", ""}, {m.Video, "video.mp4", "video/mp4"},
		{m.Voice, "voice.ogg", "audio/ogg"}, {m.VideoNote, "video-note.mp4", "video/mp4"}, {m.Animation, "animation.mp4", "video/mp4"}} {
		if it.f != nil {
			items = append(items, it)
		}
	}
	var out []hub.Attachment
	for _, it := range items {
		name, mime := it.f.FileName, it.f.MIME
		if name == "" {
			name = it.name
		}
		if mime == "" {
			mime = it.mime
		}
		if it.f.Size > maxAttachment {
			return nil, fmt.Errorf("%s is larger than the %d MB Telegram lets bots download", name, maxAttachment>>20)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		var f file
		err := c.api.call(ctx, "getFile", map[string]any{"file_id": it.f.FileID}, &f)
		var data []byte
		if err == nil {
			data, err = c.api.download(ctx, f.Path, maxAttachment)
		}
		cancel()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, hub.Attachment{Name: name, MIME: mime, Data: data})
	}
	return out, nil
}

// quote is the message m replies to, as the model is shown it, or nil when
// it replies to none. In a forum topic every message replies to the topic's
// first, which is no reply at all.
func (c *Connector) quote(m *message) *hub.Quote {
	r := m.ReplyTo
	if r == nil || m.IsTopic && r.MessageID == m.ThreadID {
		return nil
	}
	q := &hub.Quote{Text: r.Text}
	if q.Text == "" {
		q.Text = r.Caption
	}
	if m.Quote != nil && m.Quote.Text != "" {
		q.Text = m.Quote.Text
	}
	switch {
	case r.From != nil && r.From.ID == c.id:
		q.Author = "you"
		if q.Text == "" {
			q.Text = c.recall(r.Chat.ID, r.MessageID)
		}
	case r.From != nil || r.SenderChat != nil:
		q.Author = authorName(r)
	default:
		return nil
	}
	if q.Text == "" {
		q.Text = "(a message without text)"
	}
	return q
}

// remember keeps the text of a message the bot sent, for quoting it.
func (c *Connector) remember(chat, id int64, text string) {
	key := fmt.Sprintf("%d:%d", chat, id)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.sent[key]; !ok {
		c.sentOrder = append(c.sentOrder, key)
	}
	c.sent[key] = text
	if len(c.sentOrder) > maxSent {
		delete(c.sent, c.sentOrder[0])
		c.sentOrder = c.sentOrder[1:]
	}
}

func (c *Connector) recall(chat, id int64) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sent[fmt.Sprintf("%d:%d", chat, id)]
}

// authorName is how the model is told who spoke. A message sent as a chat,
// such as by an anonymous admin, is from the chat.
func authorName(m *message) string {
	if m.SenderChat != nil {
		return m.SenderChat.Title
	}
	return userName(*m.From)
}

// userName is a person's name, with their username when they have one, since
// names are not unique.
func userName(u user) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	switch {
	case u.Username == "":
		return name
	case name == "":
		return "@" + u.Username
	}
	return name + " (@" + u.Username + ")"
}

// convOf is the conversation m belongs to: its chat, or its forum topic.
// Replies in a group carry a thread ID too, but only a topic's counts.
func convOf(m *message) string {
	if m.IsTopic {
		return conv(m.Chat.ID, m.ThreadID)
	}
	return conv(m.Chat.ID, 0)
}

func conv(chat, thread int64) string {
	key := strconv.FormatInt(chat, 10)
	if thread != 0 {
		key += "/" + strconv.FormatInt(thread, 10)
	}
	return key
}

// target is where a conversation's messages go.
type target struct {
	chat, thread int64
}

func targetOf(key string) target {
	key = strings.TrimPrefix(key, connector+":")
	chat, thread, _ := strings.Cut(key, "/")
	var t target
	t.chat, _ = strconv.ParseInt(chat, 10, 64)
	t.thread, _ = strconv.ParseInt(thread, 10, 64)
	return t
}

// params are the parameters every message to t starts with.
func (t target) params() map[string]any {
	p := map[string]any{"chat_id": t.chat}
	if t.thread != 0 {
		p["message_thread_id"] = t.thread
	}
	return p
}

// private reports whether t is a private chat, the only kind Telegram
// streams drafts to. A private chat's ID is its person's, which is
// positive; groups' are negative.
func (t target) private() bool { return t.chat > 0 }

// TurnStarted shows the typing action until the conversation is idle again.
func (c *Connector) TurnStarted(key string) {
	t := targetOf(key)
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	if stop, ok := c.typing[key]; ok {
		stop()
	}
	c.typing[key] = cancel
	c.mu.Unlock()
	go func() {
		ticker := time.NewTicker(typingEvery)
		defer ticker.Stop()
		for {
			p := t.params()
			p["action"] = "typing"
			if err := c.call(ctx, "sendChatAction", p, nil); err != nil && ctx.Err() == nil {
				c.log.Debug("typing", "chat", key, "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (c *Connector) stopTyping() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, stop := range c.typing {
		stop()
		delete(c.typing, key)
	}
}

// Draft streams kon's text as a draft, in private chats; elsewhere the
// stretch shows once it is posted.
func (c *Connector) Draft(key string, p hub.Post) {
	t := targetOf(key)
	if !t.private() {
		return
	}
	c.stream(key).showDraft(chatMessenger{c, t}, p)
}

// Post shows a stretch of a turn: a new message for a new stretch, and edits
// as its tool calls start and finish.
func (c *Connector) Post(key string, p hub.Post) {
	if p.Notice {
		c.notice(key, p.Text)
		return
	}
	c.stream(key).post(chatMessenger{c, targetOf(key)}, p)
}

// TurnEnded reports a turn that did not finish on its own.
func (c *Connector) TurnEnded(key string, e hub.End) {
	c.stream(key).flush(chatMessenger{c, targetOf(key)})
	switch {
	case e.Err != nil:
		c.notice(key, "⚠️ "+e.Err.Error())
	case e.StopReason == "cancelled":
		c.notice(key, "Cancelled.")
	}
	if e.Idle {
		c.mu.Lock()
		if stop, ok := c.typing[key]; ok {
			stop()
			delete(c.typing, key)
		}
		c.mu.Unlock()
	}
}

func (c *Connector) stream(key string) *stream {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.streams[key]
	if !ok {
		s = &stream{every: editEvery, draftEvery: draftEvery}
		c.streams[key] = s
	}
	return s
}

func (c *Connector) notice(key string, text string) {
	m := chatMessenger{c, targetOf(key)}
	for _, p := range render(hub.Post{Text: text, Notice: true}) {
		m.send(p, "")
	}
}

// call sends method with a bounded wait.
func (c *Connector) call(ctx context.Context, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return c.api.call(ctx, method, params, result)
}

// chatMessenger sends, edits, and drafts messages in one conversation for a
// stream.
type chatMessenger struct {
	c *Connector
	t target
}

// send posts p as a rich message, or as plain text when Telegram refuses
// the Markdown, as a reply to message replyTo unless it is "". It returns
// the message's ID, or 0 when it could not be sent.
func (m chatMessenger) send(p part, replyTo string) int64 {
	return m.sendMarkup(p, replyTo, nil)
}

func (m chatMessenger) sendMarkup(p part, replyTo string, markup *inlineKeyboard) int64 {
	params := m.t.params()
	params["rich_message"] = richMessage{Markdown: p.rich}
	if id, err := strconv.ParseInt(replyTo, 10, 64); err == nil {
		// A reply to a deleted message is sent as a plain one.
		params["reply_parameters"] = map[string]any{"message_id": id, "allow_sending_without_reply": true}
	}
	if markup != nil {
		params["reply_markup"] = markup
	}
	var sent message
	err := m.c.call(context.Background(), "sendRichMessage", params, &sent)
	if errorCode(err) == http.StatusBadRequest {
		m.c.log.Warn("send rich message; sending plain text", "chat", m.t.chat, "err", err)
		delete(params, "rich_message")
		params["text"] = p.plain
		params["link_preview_options"] = map[string]bool{"is_disabled": true}
		err = m.c.call(context.Background(), "sendMessage", params, &sent)
	}
	if err != nil {
		m.c.log.Error("send message", "chat", m.t.chat, "err", err)
		return 0
	}
	m.c.remember(m.t.chat, sent.MessageID, p.plain)
	return sent.MessageID
}

func (m chatMessenger) edit(id int64, p part) {
	m.editMarkup(id, p, nil)
}

// editMarkup replaces message id with p, and its buttons with markup, or
// with none.
func (m chatMessenger) editMarkup(id int64, p part, markup *inlineKeyboard) {
	params := map[string]any{"chat_id": m.t.chat, "message_id": id, "rich_message": richMessage{Markdown: p.rich}}
	if markup != nil {
		params["reply_markup"] = markup
	}
	err := m.c.call(context.Background(), "editMessageText", params, nil)
	if notModified(err) {
		return
	}
	if errorCode(err) == http.StatusBadRequest {
		m.c.log.Warn("edit rich message; editing plain text", "chat", m.t.chat, "err", err)
		delete(params, "rich_message")
		params["text"] = p.plain
		params["link_preview_options"] = map[string]bool{"is_disabled": true}
		err = m.c.call(context.Background(), "editMessageText", params, nil)
	}
	if err != nil && !notModified(err) {
		m.c.log.Error("edit message", "chat", m.t.chat, "err", err)
		return
	}
	m.c.remember(m.t.chat, id, p.plain)
}

// draft streams p as draft id, with a button that stops the turn when the
// config asks for one. A draft is best effort: one that fails is replaced by the next, or by the message.
func (m chatMessenger) draft(id int, p part) time.Duration {
	params := m.t.params()
	params["draft_id"] = id
	params["rich_message"] = richMessage{Markdown: p.rich}
	if m.c.cfg.StopButton {
		params["can_stop"] = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	err := m.c.api.once(ctx, "sendRichMessageDraft", params, nil)
	var e *apiError
	if errors.As(err, &e) && e.Code == http.StatusTooManyRequests {
		return max(e.RetryAfter, time.Second)
	}
	if err != nil {
		m.c.log.Debug("draft", "chat", m.t.chat, "err", err)
	}
	return 0
}

// notModified reports an edit that changed nothing, which Telegram refuses
// but is not a failure.
func notModified(err error) bool {
	var e *apiError
	return errors.As(err, &e) && strings.Contains(e.Description, "message is not modified")
}

// lane runs one conversation's updates one at a time, in order, off the
// polling goroutine.
type lane struct {
	mu      sync.Mutex
	queue   []func()
	running bool
}

func (l *lane) do(f func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.queue = append(l.queue, f)
	if !l.running {
		l.running = true
		go l.drain()
	}
}

func (l *lane) drain() {
	for {
		l.mu.Lock()
		if len(l.queue) == 0 {
			l.running = false
			l.mu.Unlock()
			return
		}
		f := l.queue[0]
		l.queue = l.queue[1:]
		l.mu.Unlock()
		f()
	}
}

var (
	_ hub.Output  = (*Connector)(nil)
	_ hub.Drafter = (*Connector)(nil)
)
