package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/hizkifw/inari/internal/acp"
	"github.com/hizkifw/inari/internal/hub"
)

// maxButtons bounds the choices a /model or /effort keyboard offers.
const maxButtons = 50

// commands are inari's bot commands, as the command menu lists them.
var commands = []botCommand{
	{"cancel", "Stop kon's current turn and drop messages waiting behind it"},
	{"new", "Close this chat's session and start fresh with the next message"},
	{"compact", "Summarize the conversation so far to free context"},
	{"model", "Show or set the model"},
	{"effort", "Show or set the reasoning effort"},
	{"status", "Show the session's model, context, and cost"},
	{"jobs", "List the session's background jobs"},
	{"kill", "Stop a background job: /kill <id>"},
}

// known reports whether name is one of inari's commands. /start is what
// Telegram sends when someone first opens a chat with the bot.
func known(name string) bool {
	return name == "start" || name == "help" || slices.ContainsFunc(commands, func(b botCommand) bool { return b.Command == name })
}

// command splits a message such as "/model@inari_bot opus" into its
// command, the bot it names, and its arguments.
func command(text string) (name, bot, args string, ok bool) {
	if !strings.HasPrefix(text, "/") {
		return "", "", "", false
	}
	word := text[1:]
	if i := strings.IndexFunc(word, unicode.IsSpace); i >= 0 {
		word, args = word[:i], word[i:]
	}
	name, bot, _ = strings.Cut(word, "@")
	return strings.ToLower(name), bot, strings.TrimSpace(args), name != ""
}

// command carries out a command and posts its answer, which the chat sees:
// the session is shared, so a change to it is everyone's business.
func (c *Connector) command(key string, route hub.Route, who, name, args string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	m := chatMessenger{c, targetOf(key)}
	text, markup, err := c.run(ctx, connector+":"+key, route, who, name, args)
	if err != nil {
		text = "⚠️ " + err.Error()
	}
	m.sendMarkup(part{rich: text, plain: text}, "", markup)
}

func (c *Connector) run(ctx context.Context, conv string, route hub.Route, who, name, args string) (string, *inlineKeyboard, error) {
	switch name {
	case "start", "help":
		return help(), nil, nil
	case "cancel":
		if err := c.hub.Cancel(conv); err != nil {
			return "", nil, err
		}
		return who + " cancelled the turn.", nil, nil
	case "new":
		if err := c.hub.Reset(ctx, conv); err != nil {
			return "", nil, err
		}
		return who + " started a new session. The next message begins it.", nil, nil
	case "compact":
		if err := c.hub.Compact(ctx, conv, route); err != nil {
			return "", nil, err
		}
		return who + " is compacting the session.", nil, nil
	case "model":
		return c.option(ctx, conv, route, who, acp.ConfigModel, args)
	case "effort":
		return c.option(ctx, conv, route, who, acp.ConfigEffort, args)
	case "status":
		return c.status(conv), nil, nil
	case "jobs":
		text, err := c.jobs(ctx, conv)
		return text, nil, err
	case "kill":
		id, err := strconv.Atoi(args)
		if err != nil || id < 1 {
			return "", nil, errors.New("give the job's ID, from /jobs: /kill <id>")
		}
		if err := c.hub.KillJob(ctx, conv, id); err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("%s stopped job %d.", who, id), nil, nil
	}
	return "", nil, fmt.Errorf("unknown command /%s", name)
}

func help() string {
	var b strings.Builder
	b.WriteString("Talk to kon here; everyone in the chat shares one session.\n\n")
	for _, cmd := range commands {
		fmt.Fprintf(&b, "- /%s: %s\n", cmd.Command, cmd.Description)
	}
	return b.String()
}

// option shows a config option with a button per choice, or sets it when a
// value is given. Telegram has no autocomplete, so a value matches a
// choice's value or name, or a part of one that only it has.
func (c *Connector) option(ctx context.Context, conv string, route hub.Route, who, id, value string) (string, *inlineKeyboard, error) {
	options, err := c.hub.Options(ctx, conv, route)
	if err != nil {
		return "", nil, err
	}
	o, ok := find(options, id)
	if !ok {
		return "", nil, fmt.Errorf("kon offers no %s setting for this model", id)
	}
	if value == "" {
		return fmt.Sprintf("%s: %s", o.Name, choiceName(o, o.CurrentValue)), keyboard(o), nil
	}
	choice, err := match(o, value)
	if err != nil {
		return "", nil, err
	}
	return c.set(ctx, conv, route, who, id, choice)
}

// set sets a config option and says who set it to what.
func (c *Connector) set(ctx context.Context, conv string, route hub.Route, who, id, value string) (string, *inlineKeyboard, error) {
	options, err := c.hub.SetOption(ctx, conv, route, id, value)
	if err != nil {
		return "", nil, err
	}
	o, _ := find(options, id)
	return fmt.Sprintf("%s set %s to %s.", who, strings.ToLower(o.Name), choiceName(o, value)), nil, nil
}

// keyboard offers o's choices as buttons. A button carries the choice's
// index, since a value can be longer than the 64 bytes a button holds.
func keyboard(o acp.ConfigOption) *inlineKeyboard {
	var k inlineKeyboard
	for i, ch := range o.Options {
		if i == maxButtons {
			break
		}
		label := ch.Name
		if ch.Value == o.CurrentValue {
			label = "• " + label
		}
		k.Rows = append(k.Rows, []inlineButton{{Text: truncate(label, 60), Data: fmt.Sprintf("set:%s:%d", o.ID, i)}})
	}
	if len(k.Rows) == 0 {
		return nil
	}
	return &k
}

// match finds the choice value names.
func match(o acp.ConfigOption, value string) (string, error) {
	want := strings.ToLower(value)
	var partial []string
	for _, ch := range o.Options {
		v, n := strings.ToLower(ch.Value), strings.ToLower(ch.Name)
		if v == want || n == want {
			return ch.Value, nil
		}
		if strings.Contains(v, want) || strings.Contains(n, want) {
			partial = append(partial, ch.Value)
		}
	}
	switch len(partial) {
	case 0:
		return "", fmt.Errorf("no %s matches %q; send /%s to see the choices", strings.ToLower(o.Name), value, o.ID)
	case 1:
		return partial[0], nil
	}
	return "", fmt.Errorf("%q matches %d choices; be more specific, or send /%s to pick one", value, len(partial), o.ID)
}

// callback takes a press of a /model or /effort button, and answers it by
// replacing the buttons with who chose what.
func (c *Connector) callback(q *callbackQuery) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	answer := func(text string) {
		if err := c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": q.ID, "text": text}, nil); err != nil {
			c.log.Warn("answer button", "err", err)
		}
	}
	key := convOf(q.Message)
	route, ok := c.allowed(q.From.ID, key)
	if !ok {
		answer("kon is not available to you here.")
		return
	}
	var id string
	var index int
	if _, err := fmt.Sscanf(strings.ReplaceAll(q.Data, ":", " "), "set %s %d", &id, &index); err != nil {
		answer("")
		return
	}
	conv := connector + ":" + key
	options, err := c.hub.Options(ctx, conv, route)
	if err != nil {
		answer(err.Error())
		return
	}
	o, ok := find(options, id)
	if !ok || index < 0 || index >= len(o.Options) {
		answer("That choice is gone; send the command again.")
		return
	}
	who := userName(q.From)
	text, _, err := c.set(ctx, conv, route, who, id, o.Options[index].Value)
	if err != nil {
		answer(err.Error())
		return
	}
	answer("")
	chatMessenger{c, targetOf(key)}.edit(q.Message.MessageID, part{rich: text, plain: text})
}

func (c *Connector) status(conv string) string {
	st, ok := c.hub.Status(conv)
	if !ok {
		return "No session is open here yet; send a message to start one."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Session %s in %s\n\n", code(st.SessionID), code(st.CWD))
	for _, o := range st.Options {
		fmt.Fprintf(&b, "- %s: %s\n", o.Name, choiceName(o, o.CurrentValue))
	}
	if st.Size > 0 {
		fmt.Fprintf(&b, "- Context: %s of %s tokens (%d%%)\n", count(st.Used), count(st.Size), st.Used*100/st.Size)
	}
	if st.Cost != nil {
		fmt.Fprintf(&b, "- Cost: %.4f %s\n", st.Cost.Amount, st.Cost.Currency)
	}
	if st.Busy {
		b.WriteString("\nkon is working.")
	} else {
		b.WriteString("\nkon is idle.")
	}
	return b.String()
}

func (c *Connector) jobs(ctx context.Context, conv string) (string, error) {
	jobs, err := c.hub.Jobs(ctx, conv)
	if err != nil {
		return "", err
	}
	if len(jobs) == 0 {
		return "No background jobs.", nil
	}
	var b strings.Builder
	for _, j := range jobs {
		state := "running"
		if !j.Running {
			state = "exited " + j.Exit
		}
		fmt.Fprintf(&b, "- **%d** %s, %s\n", j.ID, code(truncate(j.Command, maxTitle)), state)
	}
	return truncate(b.String(), maxRich), nil
}

func find(options []acp.ConfigOption, id string) (acp.ConfigOption, bool) {
	for _, o := range options {
		if o.ID == id {
			return o, true
		}
	}
	return acp.ConfigOption{}, false
}

func choiceName(o acp.ConfigOption, value string) string {
	for _, ch := range o.Options {
		if ch.Value == value {
			return ch.Name
		}
	}
	return value
}

// count shows a token count compactly, as 12.3k.
func count(n int64) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}
