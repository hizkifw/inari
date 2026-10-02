// Package discord connects Discord channels to the hub. Each channel kon
// serves has one session that everyone in it shares; messages are prompts,
// or steering while kon works, and slash commands control the session.
package discord

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/hizkifw/inari/internal/config"
	"github.com/hizkifw/inari/internal/hub"
)

// connector is the name conversations are keyed under, as "discord:<channel>".
const connector = "discord"

// maxAttachment bounds a downloaded attachment. Discord's own upload limit
// for unboosted servers is lower, so this only stops a runaway download.
const maxAttachment = 50 << 20

// Connector is a running Discord bot.
type Connector struct {
	cfg  *config.Discord
	hub  *hub.Hub
	log  *slog.Logger
	s    *discordgo.Session
	http *http.Client

	mu sync.Mutex
	// typing is each busy channel's typing indicator.
	typing map[string]*typer
	// streams is each channel's newest stretch, kept up to date.
	streams map[string]*stream
}

// New returns a connector for cfg that sends messages to h. Register it with
// the hub before calling Run.
func New(cfg *config.Discord, h *hub.Hub, log *slog.Logger) (*Connector, error) {
	s, err := discordgo.New("Bot " + cfg.Token)
	if err != nil {
		return nil, err
	}
	// Reading messages needs the privileged message content intent, which
	// is turned on in the developer portal.
	s.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent
	c := &Connector{cfg: cfg, hub: h, log: log.With("connector", connector), s: s, http: &http.Client{Timeout: time.Minute}, typing: map[string]*typer{}, streams: map[string]*stream{}}
	s.AddHandler(c.ready)
	s.AddHandler(c.messageCreate)
	s.AddHandler(c.interactionCreate)
	return c, nil
}

// Run connects to Discord and serves until ctx ends.
func (c *Connector) Run(ctx context.Context) error {
	if err := c.s.Open(); err != nil {
		return fmt.Errorf("discord: connect: %w", err)
	}
	<-ctx.Done()
	c.mu.Lock()
	for _, t := range c.typing {
		t.stop()
	}
	c.mu.Unlock()
	return c.s.Close()
}

func (c *Connector) ready(s *discordgo.Session, r *discordgo.Ready) {
	c.log.Info("connected", "user", r.User.Username)
	if err := c.registerCommands(r.User.ID); err != nil {
		c.log.Error("register commands", "err", err)
	}
}

func (c *Connector) messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.Bot || m.Author.ID == s.State.User.ID {
		return
	}
	// Only plain messages and replies are speech; joins, pins, and the like
	// are not.
	if m.Type != discordgo.MessageTypeDefault && m.Type != discordgo.MessageTypeReply {
		return
	}
	route, ok := c.allowed(m.Author.ID, m.ChannelID)
	if !ok {
		return
	}
	text := m.ContentWithMentionsReplaced()
	attachments, err := c.download(m.Attachments)
	if err != nil {
		c.log.Warn("download attachment", "err", err)
		c.notice(m.ChannelID, "Could not read an attachment: "+err.Error())
		return
	}
	if strings.TrimSpace(text) == "" && len(attachments) == 0 {
		return
	}
	msg := hub.Message{Conv: conv(m.ChannelID), Route: route, Author: authorName(m.Author, m.Member), Text: text, Attachments: attachments}
	if err := c.hub.Handle(context.Background(), msg); err != nil {
		c.log.Error("handle message", "channel", m.ChannelID, "err", err)
		c.notice(m.ChannelID, "⚠️ "+err.Error())
	}
}

// allowed returns channel's route when user may use kon there.
func (c *Connector) allowed(user, channel string) (hub.Route, bool) {
	if !c.cfg.Access.Allows(user, channel) {
		return hub.Route{}, false
	}
	return c.Route(channel)
}

// Route is how channel's sessions start, whoever is asking. A channel with
// no directory is not served.
func (c *Connector) Route(channel string) (hub.Route, bool) {
	cwd := c.cfg.CWD(channel)
	if cwd == "" {
		return hub.Route{}, false
	}
	instructions := strings.TrimSpace(discordInstructions + "\n\n" + c.cfg.Routes.InstructionsFor(channel))
	return hub.Route{CWD: cwd, Instructions: instructions}, true
}

// discordInstructions tell a session how its replies reach people, so it
// writes for Discord rather than for a terminal.
const discordInstructions = `The chat is a Discord channel. Your replies are posted as Discord messages, and each stretch of text you write between tool calls becomes its own message. Discord renders a subset of Markdown: bold, italics, strikethrough, inline code, fenced code blocks with a language, lists, block quotes, links, and headings with #, ## and ###. It does not render tables, horizontal rules, images, or HTML, so use lists instead of tables. A message over 2000 characters is split into several, so keep replies short and to the point. People see a one-line summary of each tool call you make, but not its output; tell them what you found.`

func (c *Connector) download(list []*discordgo.MessageAttachment) ([]hub.Attachment, error) {
	var out []hub.Attachment
	for _, a := range list {
		if a.Size > maxAttachment {
			return nil, fmt.Errorf("%s is larger than %d MB", a.Filename, maxAttachment>>20)
		}
		resp, err := c.http.Get(a.URL)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachment+1))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %s", a.Filename, resp.Status)
		}
		out = append(out, hub.Attachment{Name: a.Filename, MIME: a.ContentType, Data: data})
	}
	return out, nil
}

// authorName is how the model is told who spoke: the name shown in the
// channel, with the username when they differ, since display names are not
// unique.
func authorName(u *discordgo.User, m *discordgo.Member) string {
	name := u.GlobalName
	if m != nil && m.Nick != "" {
		name = m.Nick
	}
	if name == "" || name == u.Username {
		return u.Username
	}
	return name + " (@" + u.Username + ")"
}

func conv(channel string) string { return connector + ":" + channel }

func channelOf(conv string) string { return strings.TrimPrefix(conv, connector+":") }

// TurnStarted shows the typing indicator until the channel is idle again.
func (c *Connector) TurnStarted(conv string) {
	channel := channelOf(conv)
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.typing[channel]; ok {
		t.stop()
	}
	c.typing[channel] = startTyping(func() { c.s.ChannelTyping(channel) }, typingEvery, typingGrace)
}

// Post shows a stretch of a turn: a new message for a new stretch, and edits
// as its tool calls start and finish.
func (c *Connector) Post(conv string, p hub.Post) {
	channel := channelOf(conv)
	if p.Notice {
		c.notice(channel, p.Text)
		return
	}
	if !c.stream(channel).post(channelMessenger{c, channel}, p) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.typing[channel]; ok {
		t.posted()
	}
}

func (c *Connector) stream(channel string) *stream {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.streams[channel]
	if !ok {
		s = &stream{every: editEvery}
		c.streams[channel] = s
	}
	return s
}

// channelMessenger sends and edits messages in one channel for a stream.
type channelMessenger struct {
	c       *Connector
	channel string
}

func (m channelMessenger) send(content string) string { return m.c.send(m.channel, content) }

func (m channelMessenger) edit(id, content string) {
	_, err := m.c.s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		ID: id, Channel: m.channel, Content: &content,
		AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}},
		Flags:           discordgo.MessageFlagsSuppressEmbeds,
	})
	if err != nil {
		m.c.log.Error("edit message", "channel", m.channel, "err", err)
	}
}

// TurnEnded reports a turn that did not finish on its own.
func (c *Connector) TurnEnded(conv string, e hub.End) {
	channel := channelOf(conv)
	c.stream(channel).flush(channelMessenger{c, channel})
	switch {
	case e.Err != nil:
		c.send(channel, "⚠️ "+e.Err.Error())
	case e.StopReason == "cancelled":
		c.send(channel, "-# Cancelled.")
	}
	if e.Idle {
		c.mu.Lock()
		if t, ok := c.typing[channel]; ok {
			t.stop()
			delete(c.typing, channel)
		}
		c.mu.Unlock()
	}
}

func (c *Connector) notice(channel, text string) {
	for _, part := range render(hub.Post{Text: text, Notice: true}) {
		c.send(channel, part)
	}
}

// send posts content without pinging anyone, whatever the model wrote, and
// without link previews, which crowd a transcript. It returns the message's
// ID, or "" when it could not be sent.
func (c *Connector) send(channel, content string) string {
	msg, err := c.s.ChannelMessageSendComplex(channel, &discordgo.MessageSend{
		Content:         content,
		AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}},
		Flags:           discordgo.MessageFlagsSuppressEmbeds,
	})
	if err != nil {
		c.log.Error("send message", "channel", channel, "err", err)
		return ""
	}
	return msg.ID
}
