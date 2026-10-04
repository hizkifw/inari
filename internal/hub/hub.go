// Package hub connects chat conversations to kon sessions. It owns kon's
// semantics: one session per conversation, the turn queue and steering, and
// gathering each turn's stream into whole posts. Connectors only translate
// between their platform and the hub's Message and Output.
package hub

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hizkifw/inari/internal/acp"
	"github.com/hizkifw/inari/internal/store"
)

// Message is one chat message for kon.
type Message struct {
	// Conv identifies the conversation as "<connector>:<id>", such as
	// "discord:123". Everyone in a conversation shares its session.
	Conv string
	// Route is where the conversation's session works and what it is told.
	Route Route
	// ID is the platform's ID for the message, so that kon's answer to it
	// as steering can be posted as a reply. It may be empty.
	ID string
	// Author is the sender's display name. The session is shared, so the
	// model is told who said what.
	Author      string
	Text        string
	Attachments []Attachment
	// Quote is the message this one replies to, if any, so the model knows
	// what "this" or "that one" means in a busy chat.
	Quote *Quote
}

// Quote is a message replied to: who wrote it, "you" when kon did, and its
// text, which the hub shortens.
type Quote struct {
	Author string
	Text   string
}

// maxQuote bounds how much of a replied-to message the model is shown. The
// start is enough to tell which message it was; kon has the rest in its
// transcript or can ask.
const maxQuote = 200

// Route is how a conversation's session starts: the directory it works in
// and the connector's instructions for it, such as how the platform renders
// text. Instructions apply to new sessions only; kon fixes a session's system
// prompt when it starts, so a changed one takes effect after /new.
type Route struct {
	CWD          string
	Instructions string
	// Detached marks a conversation no person is in, such as a cron job's
	// run: its session is never saved or resumed, it is told only
	// Instructions, and Reset forgets the conversation entirely.
	Detached bool
}

// chatInstructions tells every session how the hub relays it, since the
// [name] prefix is the hub's doing.
//
//go:embed instructions.txt
var chatInstructions string

// instructions are what a new session of conv, with route r, is told. A
// chat session is told its conversation's ID, which `inari cron add --to`
// takes so that what it schedules comes back to the same chat.
func (h *Hub) instructions(conv string, r Route) string {
	if r.Detached {
		return r.Instructions
	}
	id := fmt.Sprintf("This conversation's ID is %s.", conv)
	return strings.TrimSpace(strings.TrimSpace(chatInstructions) + " " + id + "\n\n" + h.extra + "\n\n" + r.Instructions)
}

// SetInstructions adds to what every chat session is told, after the hub's
// own and before its connector's, such as how to schedule jobs. Call it
// before the first message.
func (h *Hub) SetInstructions(text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.extra = strings.TrimSpace(text)
}

// Attachment is a file sent with a message.
type Attachment struct {
	Name string
	MIME string
	Data []byte
}

// Output is how a connector shows a conversation's turns. The hub calls it
// for one conversation at a time and in order, so a method may block.
type Output interface {
	// TurnStarted is called when the conversation goes from idle to busy.
	TurnStarted(conv string)
	// Post shows a stretch of the turn. A post with the same ID as the one
	// before it is that stretch grown or changed, and replaces it; a new ID
	// starts a new message.
	Post(conv string, p Post)
	// TurnEnded is called after every turn's last post.
	TurnEnded(conv string, e End)
}

// Drafter is an Output that shows kon's text while it is written, before
// its stretch is posted, such as Telegram's streamed drafts. A connector
// that implements it gets Draft calls as well as Post calls.
type Drafter interface {
	// Draft shows the stretch p.ID's text so far. It is called again as the
	// text grows, but only with the newest text when the connector falls
	// behind, and never after the stretch's first Post.
	Draft(conv string, p Post)
}

// Post is a stretch of a turn worth one chat message: something kon said and
// the tool calls it made after saying it. A stretch is first posted once its
// text is complete, when its first tool call starts or the turn ends, and
// again each time a tool call starts or finishes.
type Post struct {
	// ID tells stretches apart; it only grows within a conversation.
	ID    int
	Text  string
	Tools []Tool
	// ReplyTo is the Message.ID of the steering kon read just before
	// this stretch, which the stretch most likely answers. By then the chat
	// has moved on, so a connector shows the stretch as a reply to it. It
	// is empty for most stretches.
	ReplyTo string
	// Notice marks a message from inari rather than from kon. It is a
	// message of its own and never replaced.
	Notice bool
}

// Tool is a tool call as the transcript titles it.
type Tool struct {
	Title  string
	Status string
}

// Tool call statuses a finished call has; a running one has another.
const (
	ToolCompleted = "completed"
	ToolFailed    = "failed"
)

// End is how a turn ended. Err is set when it failed.
type End struct {
	StopReason string
	Err        error
	// Idle reports that no other turn is running or waiting, so the
	// conversation can stop looking busy.
	Idle bool
}

// Status is a conversation's session as it now stands.
type Status struct {
	SessionID string
	CWD       string
	Busy      bool
	Options   []acp.ConfigOption
	// Used and Size are context tokens; Size is 0 when unknown.
	Used, Size int64
	Cost       *acp.Cost
}

var errKonExited = errors.New("kon exited; the next message starts it again")

// Kon is how the hub starts kon.
type Kon struct {
	Command string
	Args    []string
}

// Hub is the live set of conversations and the kon process serving them.
type Hub struct {
	kon     Kon
	version string
	store   *store.Store
	log     *slog.Logger

	mu sync.Mutex
	// extra is what SetInstructions adds.
	extra string
	// closing is set once Close begins: turns that end from then on were
	// cut off by inari stopping, so they stay marked busy, to be resumed.
	closing bool
	// attachments is the temporary directory attachments are saved under,
	// or "" until the first is.
	attachments string
	outs        map[string]Output
	client      *acp.Client
	convs       map[string]*conv
	session     map[string]*conv
	// starting serializes starting kon, so concurrent first messages share
	// one process.
	starting sync.Mutex
}

// conv is one conversation's session and the turn in progress. Fields below
// mu are guarded by Hub.mu.
type conv struct {
	key string
	out *outbox
	// open serializes opening the session.
	open sync.Mutex

	cwd       string
	sessionID string
	// client is the kon process the session is open in; a different one
	// means kon restarted and the session must be resumed.
	client *acp.Client
	// saved is whether the store remembers the session. kon discards a
	// session that never receives a prompt, so one opened only to list
	// models is remembered once it is prompted.
	saved bool
	// detached is Route.Detached for the open session.
	detached bool
	// turns counts turns running or waiting: prompts sent and turns kon
	// started itself.
	turns int
	// stretch is the ID of the stretch being gathered in text and tools,
	// and dirty whether it changed since it was last posted.
	stretch int
	dirty   bool
	// drafting is whether a Draft of the stretch waits in the outbox.
	drafting bool
	// steers are steering messages sent and not yet reported read, and
	// replyTo the ID of the one the stretch follows.
	steers  []steer
	replyTo string
	text    strings.Builder
	tools   []Tool
	toolAt  map[string]int
	options []acp.ConfigOption
	usage   acp.Update
}

// steer is a steering message kon has yet to read: its text, by which kon
// reports it, and the ID of the chat message it came from.
type steer struct {
	text, id string
}

// New returns a hub that starts kon when it is first needed.
func New(kon Kon, version string, st *store.Store, log *slog.Logger) *Hub {
	return &Hub{kon: kon, version: version, store: st, log: log, outs: map[string]Output{}, convs: map[string]*conv{}, session: map[string]*conv{}}
}

// Register routes conversations named "<connector>:…" to out.
func (h *Hub) Register(connector string, out Output) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.outs[connector] = out
}

// Close stops kon, which cancels every turn and closes every session, and
// removes the saved attachments.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closing = true
	c, dir := h.client, h.attachments
	h.attachments = ""
	h.mu.Unlock()
	if c != nil {
		c.Close()
	}
	if dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			h.log.Warn("remove attachments", "dir", dir, "err", err)
		}
	}
}

// Handle sends a message to its conversation's session: as steering when a
// turn is running, so kon reads it before its next step, and otherwise as a
// prompt. Attachments are saved as files and listed by path, so a message
// with any steers as well.
func (h *Hub) Handle(ctx context.Context, m Message) error {
	c, cl, err := h.open(ctx, m.Conv, m.Route)
	if err != nil {
		return err
	}
	// A name with brackets in it could pass for another tag, such as a
	// cron notice's.
	unbracket := strings.NewReplacer("[", "", "]", "")
	author := unbracket.Replace(m.Author)
	tag := author
	if q := m.Quote; q != nil {
		quoted := strings.Join(strings.Fields(q.Text), " ")
		if r := []rune(quoted); len(r) > maxQuote {
			quoted = string(r[:maxQuote-1]) + "…"
		}
		tag = fmt.Sprintf("%s, replying to %s: %q", author, unbracket.Replace(q.Author), unbracket.Replace(quoted))
	}
	text := fmt.Sprintf("[%s] %s", tag, m.Text)
	if strings.TrimSpace(m.Text) == "" {
		text = fmt.Sprintf("[%s] sent %d attachment(s).", tag, len(m.Attachments))
	}
	h.mu.Lock()
	busy, id := c.turns > 0, c.sessionID
	h.mu.Unlock()
	files, err := h.attach(id, m.Attachments)
	if err != nil {
		return err
	}
	text += files
	if busy {
		// It is noted before it is sent, since kon may report it read
		// before Steer returns.
		h.mu.Lock()
		c.steers = append(c.steers, steer{text: text, id: m.ID})
		h.mu.Unlock()
		err := cl.Steer(ctx, id, text)
		if err != nil {
			h.mu.Lock()
			c.steers = slices.DeleteFunc(c.steers, func(s steer) bool { return s.text == text && s.id == m.ID })
			h.mu.Unlock()
		}
		// The turn may have ended since; then the message starts the next.
		if !acp.IsCode(err, acp.CodeInvalidRequest) {
			return err
		}
	}
	h.prompt(c, cl, []acp.ContentBlock{acp.TextBlock(text)})
	return nil
}

// maxResumes is how many times in a row a cut-off turn is resumed. A turn
// that keeps restarting inari, such as one whose upgrade keeps failing,
// stops there.
const maxResumes = 2

// resumeText tells kon why it is prompted after a restart. The turn may
// have been what restarted inari, such as by upgrading it.
const resumeText = "Your last turn was cut off because inari restarted, which the turn itself may have caused, such as by upgrading inari. " +
	"Continue the work from where it stopped, checking what was already done first. If it was finished, say so in a sentence."

// Resume continues the turns that inari stopping cut off: each such
// conversation's session is resumed and told so, and kon picks its work up.
// route gives a conversation's route, or false when it is no longer
// served. Call it once the connectors are registered.
func (h *Hub) Resume(ctx context.Context, route func(conv string) (Route, bool)) {
	for conv, b := range h.store.Interrupted() {
		r, ok := route(conv)
		if !ok || r.CWD != b.CWD {
			// The conversation is gone, or moved and starts a new
			// session, so there is nothing to continue.
			h.store.SetBusy(conv, false)
			continue
		}
		if b.Resumes >= maxResumes {
			h.store.SetBusy(conv, false)
			h.Notice(conv, "inari restarted while kon was working, again, so it was not resumed this time. Send a message to continue.")
			continue
		}
		b.Resumes++
		if err := h.store.Set(conv, b); err != nil {
			h.log.Error("save session binding", "conv", conv, "err", err)
		}
		h.log.Info("resume interrupted turn", "conv", conv, "session", b.SessionID)
		h.Notice(conv, "inari restarted while kon was working; resuming.")
		if err := h.Handle(ctx, Message{Conv: conv, Route: r, Author: "inari", Text: resumeText}); err != nil {
			h.log.Error("resume interrupted turn", "conv", conv, "err", err)
			h.Notice(conv, "⚠️ Could not resume: "+err.Error())
		}
	}
}

// Compact compacts the session, as /compact does in kon.
func (h *Hub) Compact(ctx context.Context, conv string, route Route) error {
	c, cl, err := h.open(ctx, conv, route)
	if err != nil {
		return err
	}
	if h.busy(c) {
		return errors.New("kon is busy; cancel or wait for the turn to finish")
	}
	h.prompt(c, cl, []acp.ContentBlock{acp.TextBlock("/compact")})
	return nil
}

// prompt runs a turn in the background, posting its output as it goes.
func (h *Hub) prompt(c *conv, cl *acp.Client, blocks []acp.ContentBlock) {
	h.mu.Lock()
	id, cwd, save := c.sessionID, c.cwd, !c.saved
	c.saved = true
	h.beginTurn(c)
	h.mu.Unlock()
	if save {
		// The turn has begun, so the binding starts busy.
		if err := h.store.Set(c.key, store.Binding{SessionID: id, CWD: cwd, Busy: true}); err != nil {
			h.log.Error("save session binding", "conv", c.key, "err", err)
		}
	}
	go func() {
		stop, err := cl.Prompt(context.Background(), id, blocks)
		h.mu.Lock()
		defer h.mu.Unlock()
		// A kon that exited may have ended this conversation's turns
		// already; if not, this turn learned of it first.
		if c.client != cl {
			return
		}
		if errors.Is(err, acp.ErrClosed) {
			err = errKonExited
		}
		h.endTurn(c, stop, err)
	}()
}

// beginTurn counts a turn in. The caller holds h.mu.
func (h *Hub) beginTurn(c *conv) {
	c.turns++
	if c.turns == 1 {
		h.setBusy(c, true)
		h.emit(c, func(out Output) { out.TurnStarted(c.key) })
	}
}

// setBusy records whether c has a turn running, so a restart knows what it
// cut off. Turns that end once inari is stopping were cut off, so they stay
// busy. The caller holds h.mu.
func (h *Hub) setBusy(c *conv, busy bool) {
	if !c.saved || c.detached || !busy && h.closing {
		return
	}
	if err := h.store.SetBusy(c.key, busy); err != nil {
		h.log.Error("save session binding", "conv", c.key, "err", err)
	}
}

// endTurn posts what is left of a turn and counts it out. The caller holds
// h.mu.
func (h *Hub) endTurn(c *conv, stop string, err error) {
	h.show(c)
	h.next(c)
	c.turns = max(c.turns-1, 0)
	if c.turns == 0 {
		// Steering kon never read is gone with the turn.
		c.steers = nil
		h.setBusy(c, false)
	}
	end := End{StopReason: stop, Err: err, Idle: c.turns == 0}
	h.emit(c, func(out Output) { out.TurnEnded(c.key, end) })
}

// show posts the stretch as it now stands, if that is news. The caller holds
// h.mu.
func (h *Hub) show(c *conv) {
	text := strings.TrimSpace(c.text.String())
	if !c.dirty || text == "" && len(c.tools) == 0 {
		return
	}
	c.dirty = false
	// The stretch keeps changing after this is queued, so the post gets a
	// copy of its calls.
	p := Post{ID: c.stretch, Text: text, Tools: slices.Clone(c.tools), ReplyTo: c.replyTo}
	h.emit(c, func(out Output) { out.Post(c.key, p) })
}

// next starts a new stretch, so what follows is a new message. The caller
// holds h.mu.
func (h *Hub) next(c *conv) {
	c.stretch++
	c.dirty = false
	// A draft still queued belongs to the old stretch and will skip itself,
	// so the new stretch queues its own.
	c.drafting = false
	c.text.Reset()
	c.tools, c.toolAt = nil, map[string]int{}
	c.replyTo = ""
}

// read takes the steering kon reports it read in text and returns the ID of
// the newest of it. Several steers can arrive in one report, so each whose
// text the report holds is read; a report that holds none, written some
// other way, reads the oldest. The caller holds h.mu.
func (h *Hub) read(c *conv, text string) string {
	id, found := "", false
	c.steers = slices.DeleteFunc(c.steers, func(s steer) bool {
		if strings.Contains(text, s.text) {
			id, found = s.id, true
			return true
		}
		return false
	})
	if !found && len(c.steers) > 0 {
		id = c.steers[0].id
		c.steers = c.steers[1:]
	}
	return id
}

// draft queues the stretch's text for a connector that shows it as it is
// written. Chunks arrive far faster than a chat API takes them, so at most
// one draft waits in the outbox, and it reads the newest text when it runs.
// The caller holds h.mu.
func (h *Hub) draft(c *conv) {
	name, _, _ := strings.Cut(c.key, ":")
	d, ok := h.outs[name].(Drafter)
	if !ok || c.drafting {
		return
	}
	c.drafting = true
	id := c.stretch
	c.out.do(func() {
		h.mu.Lock()
		if c.stretch != id {
			// The stretch was posted, which queued its Post behind this.
			h.mu.Unlock()
			return
		}
		c.drafting = false
		text := strings.TrimSpace(c.text.String())
		h.mu.Unlock()
		if text != "" {
			d.Draft(c.key, Post{ID: id, Text: text})
		}
	})
}

func (h *Hub) notice(c *conv, text string) {
	h.emit(c, func(out Output) { out.Post(c.key, Post{Text: text, Notice: true}) })
}

// emit queues f for the connector serving c, which is looked up now, under
// h.mu, and called later, off it. The caller holds h.mu.
func (h *Hub) emit(c *conv, f func(Output)) {
	if h.closing {
		// The connectors have stopped, and a turn cut off now is resumed
		// when inari starts again, so there is nothing to tell.
		return
	}
	name, _, _ := strings.Cut(c.key, ":")
	out, ok := h.outs[name]
	if !ok {
		return
	}
	c.out.do(func() { f(out) })
}

func (h *Hub) busy(c *conv) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return c.turns > 0
}

// Cancel stops the running turn and every message waiting behind it.
func (h *Hub) Cancel(conv string) error {
	c, cl, id := h.live(conv)
	if c == nil {
		return errors.New("no session is open here")
	}
	return cl.Cancel(id)
}

// Reset closes the conversation's session and forgets it, so the next
// message starts a fresh one, and removes the attachments sent to it.
func (h *Hub) Reset(ctx context.Context, conv string) error {
	c, cl, id := h.live(conv)
	if c == nil {
		// kon is not running, but the session it would resume may still
		// have attachments.
		b, _ := h.store.Get(conv)
		id = b.SessionID
	}
	defer h.dropAttachments(id)
	if c != nil {
		c.open.Lock()
		defer c.open.Unlock()
		if err := cl.CloseSession(ctx, id); err != nil {
			h.log.Warn("close session", "conv", conv, "session", id, "err", err)
		}
		h.mu.Lock()
		h.detach(c, End{StopReason: acp.StopCancelled, Idle: true})
		detached := c.detached
		if detached {
			delete(h.convs, conv)
		}
		h.mu.Unlock()
		if detached {
			return nil
		}
	}
	return h.store.Delete(conv)
}

// Notice posts a line from inari, not from kon, to the conversation.
func (h *Hub) Notice(key, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.convs[key]
	if c == nil {
		c = &conv{key: key, out: &outbox{}, toolAt: map[string]int{}}
		h.convs[key] = c
	}
	h.notice(c, text)
}

// Options returns the session's config options, opening it if need be.
func (h *Hub) Options(ctx context.Context, conv string, route Route) ([]acp.ConfigOption, error) {
	c, _, err := h.open(ctx, conv, route)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return c.options, nil
}

// SetOption sets one of the session's config options. kon saves it as the
// default for new sessions too.
func (h *Hub) SetOption(ctx context.Context, conv string, route Route, id, value string) ([]acp.ConfigOption, error) {
	c, cl, err := h.open(ctx, conv, route)
	if err != nil {
		return nil, err
	}
	options, err := cl.SetConfig(ctx, c.sessionID, id, value)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	c.options = options
	h.mu.Unlock()
	return options, nil
}

// Status describes the conversation's session, or reports false when none
// is open.
func (h *Hub) Status(conv string) (Status, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.convs[conv]
	if c == nil || c.sessionID == "" {
		return Status{}, false
	}
	return Status{SessionID: c.sessionID, CWD: c.cwd, Busy: c.turns > 0, Options: c.options, Used: c.usage.Used, Size: c.usage.Size, Cost: c.usage.Cost}, true
}

// Jobs lists the session's background jobs.
func (h *Hub) Jobs(ctx context.Context, conv string) ([]acp.Job, error) {
	c, cl, id := h.live(conv)
	if c == nil {
		return nil, nil
	}
	return cl.Jobs(ctx, id)
}

// KillJob stops one of the session's background jobs.
func (h *Hub) KillJob(ctx context.Context, conv string, job int) error {
	c, cl, id := h.live(conv)
	if c == nil {
		return errors.New("no session is open here")
	}
	return cl.KillJob(ctx, id, job)
}

// live returns conv's session when it is open in the running kon.
func (h *Hub) live(conv string) (*conv, *acp.Client, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.convs[conv]
	if c == nil || c.sessionID == "" || c.client == nil || c.client != h.client {
		return nil, nil, ""
	}
	return c, c.client, c.sessionID
}

// open returns conv's session, resuming the one the store remembers or
// starting a new one. A conversation whose directory changed in the config
// starts over, since a kon session belongs to its directory.
func (h *Hub) open(ctx context.Context, key string, route Route) (*conv, *acp.Client, error) {
	cwd := route.CWD
	h.mu.Lock()
	c := h.convs[key]
	if c == nil {
		c = &conv{key: key, out: &outbox{}, toolAt: map[string]int{}}
		h.convs[key] = c
	}
	h.mu.Unlock()

	c.open.Lock()
	defer c.open.Unlock()
	cl, err := h.agent(ctx)
	if err != nil {
		return nil, nil, err
	}
	h.mu.Lock()
	ready := c.client == cl && c.sessionID != "" && c.cwd == cwd
	h.mu.Unlock()
	if ready {
		return c, cl, nil
	}

	var s acp.Session
	resumed := false
	b, known := h.store.Get(key)
	if known && b.CWD == cwd && !route.Detached {
		s, err = cl.ResumeSession(ctx, b.SessionID, cwd)
		resumed = err == nil
		if err != nil {
			h.log.Warn("resume session", "conv", key, "session", b.SessionID, "err", err)
			h.mu.Lock()
			h.notice(c, fmt.Sprintf("Could not resume the previous session (%v), so this is a new one.", err))
			h.mu.Unlock()
		}
	}
	if s.SessionID == "" {
		s, err = cl.NewSession(ctx, cwd, h.instructions(key, route))
		if err != nil {
			return nil, nil, fmt.Errorf("start a kon session: %w", err)
		}
	}
	h.mu.Lock()
	old := c.sessionID
	delete(h.session, c.sessionID)
	// A detached session counts as saved so that it never is.
	c.sessionID, c.cwd, c.client, c.options, c.saved, c.detached = s.SessionID, cwd, cl, s.ConfigOptions, resumed || route.Detached, route.Detached
	h.session[s.SessionID] = c
	h.mu.Unlock()
	h.log.Info("session open", "conv", key, "session", s.SessionID, "cwd", cwd)
	// A session this one replaces is never resumed, so nothing reads its
	// attachments again.
	for _, id := range []string{old, b.SessionID} {
		if id != s.SessionID {
			h.dropAttachments(id)
		}
	}
	return c, cl, nil
}

// agent returns the running kon, starting it when it is not.
func (h *Hub) agent(ctx context.Context) (*acp.Client, error) {
	h.starting.Lock()
	defer h.starting.Unlock()
	h.mu.Lock()
	cl := h.client
	h.mu.Unlock()
	if cl != nil {
		select {
		case <-cl.Done():
			// It exited and watch has yet to notice; start another.
		default:
			return cl, nil
		}
	}
	cl, err := acp.Spawn(h.kon.Command, h.kon.Args, handler{h})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	agent, err := cl.Initialize(ctx, "inari", h.version)
	if err != nil {
		cl.Close()
		return nil, fmt.Errorf("initialize kon: %w", err)
	}
	h.log.Info("kon started", "version", agent.AgentInfo.Version)
	if !agent.Supports(acp.ExtensionInstructions) {
		// kon before v0.1.15 ignores them; sessions still work, but the
		// model is not told it is in a chat.
		h.log.Warn("kon does not take session instructions; update it to v0.1.15 or later")
	}
	h.mu.Lock()
	h.client = cl
	h.mu.Unlock()
	go h.watch(cl)
	return cl, nil
}

// watch ends the turns of a kon that exits, so no conversation stays busy,
// and leaves the next message to start kon again and resume its session.
func (h *Hub) watch(cl *acp.Client) {
	<-cl.Done()
	h.log.Warn("kon exited", "err", cl.Err())
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client == cl {
		h.client = nil
	}
	for _, c := range h.convs {
		if c.client != cl {
			continue
		}
		h.detach(c, End{Err: errKonExited, Idle: true})
	}
}

// detach forgets c's session. Its turns' prompts will be answered, if at
// all, to a conversation that no longer counts them, so they are ended here
// with end. The caller holds h.mu.
func (h *Hub) detach(c *conv, end End) {
	delete(h.session, c.sessionID)
	c.client, c.sessionID = nil, ""
	c.steers = nil
	if c.turns > 0 {
		h.show(c)
		h.next(c)
		c.turns = 0
		h.setBusy(c, false)
		h.emit(c, func(out Output) { out.TurnEnded(c.key, end) })
	}
}

// handler takes what kon sends on its own. It runs on the client's read
// loop, so it only updates state and queues output.
type handler struct{ h *Hub }

func (hd handler) Update(sessionID string, u acp.Update) {
	h := hd.h
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.session[sessionID]
	if c == nil {
		return
	}
	switch u.Kind {
	case "agent_message_chunk":
		// Text after tool calls is kon speaking again: a new message.
		if len(c.tools) > 0 {
			h.next(c)
		}
		c.text.WriteString(u.Content.Text)
		c.dirty = true
		h.draft(c)
	case "tool_call":
		c.toolAt[u.ToolCallID] = len(c.tools)
		c.tools = append(c.tools, Tool{Title: u.Title, Status: u.Status})
		c.dirty = true
		h.show(c)
	case "tool_call_update":
		// Shell output streams in as updates with no status; only a
		// finished call changes the post.
		if i, ok := c.toolAt[u.ToolCallID]; ok && u.Status != "" && u.Status != c.tools[i].Status {
			c.tools[i].Status = u.Status
			c.dirty = true
			h.show(c)
		}
	case "user_message_chunk":
		// Steering was delivered: what kon says next answers it, so it
		// starts a new message, a reply to the steering.
		h.show(c)
		h.next(c)
		c.replyTo = h.read(c, u.Content.Text)
	case "usage_update":
		c.usage = u
	case "config_option_update":
		c.options = u.ConfigOptions
	}
}

func (hd handler) TurnStart(sessionID string) {
	h := hd.h
	h.mu.Lock()
	defer h.mu.Unlock()
	if c := h.session[sessionID]; c != nil {
		h.beginTurn(c)
	}
}

func (hd handler) TurnEnd(sessionID, stopReason, errText string) {
	h := hd.h
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.session[sessionID]
	if c == nil {
		return
	}
	var err error
	if errText != "" {
		err = errors.New(errText)
	}
	h.endTurn(c, stopReason, err)
}
