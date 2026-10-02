package discord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/hizkifw/inari/internal/acp"
	"github.com/hizkifw/inari/internal/hub"
)

// maxChoices is Discord's limit on autocomplete choices.
const maxChoices = 25

// commands are inari's slash commands. Discord validates their options, so a
// handler reads them without checking their types.
func commands(dm bool) []*discordgo.ApplicationCommand {
	// Guild commands cannot be offered in DMs; only global ones can.
	contexts := []discordgo.InteractionContextType{discordgo.InteractionContextGuild}
	if dm {
		contexts = append(contexts, discordgo.InteractionContextBotDM)
	}
	install := []discordgo.ApplicationIntegrationType{discordgo.ApplicationIntegrationGuildInstall}
	cmd := func(name, description string, options ...*discordgo.ApplicationCommandOption) *discordgo.ApplicationCommand {
		return &discordgo.ApplicationCommand{Name: name, Description: description, Options: options, Contexts: &contexts, IntegrationTypes: &install}
	}
	choice := func(name, description string) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: name, Description: description, Autocomplete: true}
	}
	return []*discordgo.ApplicationCommand{
		cmd("cancel", "Stop kon's current turn and drop messages waiting behind it"),
		cmd("new", "Close this channel's session and start fresh with the next message"),
		cmd("compact", "Summarize the conversation so far to free context"),
		cmd("model", "Show or set the model", choice("name", "The model to switch to")),
		cmd("effort", "Show or set the reasoning effort", choice("level", "The effort to switch to")),
		cmd("status", "Show the session's model, context, and cost"),
		cmd("jobs", "List the session's background jobs"),
		cmd("kill", "Stop a background job", &discordgo.ApplicationCommandOption{
			Type: discordgo.ApplicationCommandOptionInteger, Name: "id", Description: "The job's ID, from /jobs", Required: true, MinValue: new(float64(1)),
		}),
	}
}

// registerCommands replaces the bot's commands with inari's: in each
// configured guild, where changes show at once, or globally.
func (c *Connector) registerCommands(appID string) error {
	if len(c.cfg.Guilds) == 0 {
		_, err := c.s.ApplicationCommandBulkOverwrite(appID, "", commands(true))
		return err
	}
	var errs []error
	for _, guild := range c.cfg.Guilds {
		if _, err := c.s.ApplicationCommandBulkOverwrite(appID, guild, commands(false)); err != nil {
			errs = append(errs, fmt.Errorf("guild %s: %w", guild, err))
		}
	}
	return errors.Join(errs...)
}

func (c *Connector) interactionCreate(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type != discordgo.InteractionApplicationCommand && i.Type != discordgo.InteractionApplicationCommandAutocomplete {
		return
	}
	user := i.User
	if i.Member != nil {
		user = i.Member.User
	}
	if user == nil {
		return
	}
	route, ok := c.allowed(user.ID, i.ChannelID)
	if i.Type == discordgo.InteractionApplicationCommandAutocomplete {
		var choices []*discordgo.ApplicationCommandOptionChoice
		if ok {
			choices = c.autocomplete(i, route)
		}
		c.respond(i, &discordgo.InteractionResponse{Type: discordgo.InteractionApplicationCommandAutocompleteResult,
			Data: &discordgo.InteractionResponseData{Choices: choices}})
		return
	}
	if !ok {
		c.respond(i, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "kon is not available to you here.", Flags: discordgo.MessageFlagsEphemeral}})
		return
	}
	// Opening a session can outlast the three seconds Discord allows, so
	// every command is acknowledged first and answered by editing.
	c.respond(i, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredChannelMessageWithSource})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	text, err := c.run(ctx, i, route, user)
	if err != nil {
		text = "⚠️ " + err.Error()
	}
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &text,
		AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}}); err != nil {
		c.log.Error("answer command", "err", err)
	}
}

// run carries out a command and returns its answer, which the channel sees:
// the session is shared, so a change to it is everyone's business.
func (c *Connector) run(ctx context.Context, i *discordgo.InteractionCreate, route hub.Route, user *discordgo.User) (string, error) {
	data := i.ApplicationCommandData()
	key := conv(i.ChannelID)
	who := authorName(user, i.Member)
	switch data.Name {
	case "cancel":
		if err := c.hub.Cancel(key); err != nil {
			return "", err
		}
		return who + " cancelled the turn.", nil
	case "new":
		if err := c.hub.Reset(ctx, key); err != nil {
			return "", err
		}
		return who + " started a new session. The next message begins it.", nil
	case "compact":
		if err := c.hub.Compact(ctx, key, route); err != nil {
			return "", err
		}
		return who + " is compacting the session.", nil
	case "model":
		return c.option(ctx, key, route, who, acp.ConfigModel, data)
	case "effort":
		return c.option(ctx, key, route, who, acp.ConfigEffort, data)
	case "status":
		return c.status(key), nil
	case "jobs":
		return c.jobs(ctx, key)
	case "kill":
		id := int(data.GetOption("id").IntValue())
		if err := c.hub.KillJob(ctx, key, id); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s stopped job %d.", who, id), nil
	}
	return "", fmt.Errorf("unknown command /%s", data.Name)
}

// option shows a config option, or sets it when a value is given.
func (c *Connector) option(ctx context.Context, key string, route hub.Route, who, id string, data discordgo.ApplicationCommandInteractionData) (string, error) {
	var value string
	if len(data.Options) > 0 {
		value = data.Options[0].StringValue()
	}
	options, err := c.hub.Options(ctx, key, route)
	if err != nil {
		return "", err
	}
	if value == "" {
		o, ok := find(options, id)
		if !ok {
			return "", fmt.Errorf("kon offers no %s setting for this model", id)
		}
		return fmt.Sprintf("%s: %s", o.Name, choiceName(o, o.CurrentValue)), nil
	}
	options, err = c.hub.SetOption(ctx, key, route, id, value)
	if err != nil {
		return "", err
	}
	o, _ := find(options, id)
	return fmt.Sprintf("%s set %s to %s.", who, strings.ToLower(o.Name), choiceName(o, value)), nil
}

func (c *Connector) status(key string) string {
	st, ok := c.hub.Status(key)
	if !ok {
		return "No session is open here yet; send a message to start one."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Session `%s` in `%s`\n", st.SessionID, st.CWD)
	for _, o := range st.Options {
		fmt.Fprintf(&b, "%s: %s\n", o.Name, choiceName(o, o.CurrentValue))
	}
	if st.Size > 0 {
		fmt.Fprintf(&b, "Context: %s of %s tokens (%d%%)\n", count(st.Used), count(st.Size), st.Used*100/st.Size)
	}
	if st.Cost != nil {
		fmt.Fprintf(&b, "Cost: %.4f %s\n", st.Cost.Amount, st.Cost.Currency)
	}
	if st.Busy {
		b.WriteString("kon is working.")
	} else {
		b.WriteString("kon is idle.")
	}
	return b.String()
}

func (c *Connector) jobs(ctx context.Context, key string) (string, error) {
	jobs, err := c.hub.Jobs(ctx, key)
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
		fmt.Fprintf(&b, "**%d** %s, %s\n", j.ID, code(truncate(j.Command, maxTitle)), state)
	}
	return truncate(b.String(), maxMessage), nil
}

// autocomplete offers the values of the option a command names, matching
// what the user has typed so far.
func (c *Connector) autocomplete(i *discordgo.InteractionCreate, route hub.Route) []*discordgo.ApplicationCommandOptionChoice {
	data := i.ApplicationCommandData()
	id := map[string]string{"model": acp.ConfigModel, "effort": acp.ConfigEffort}[data.Name]
	if id == "" {
		return nil
	}
	var typed string
	for _, o := range data.Options {
		if o.Focused {
			typed = strings.ToLower(o.StringValue())
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	options, err := c.hub.Options(ctx, conv(i.ChannelID), route)
	if err != nil {
		return nil
	}
	o, _ := find(options, id)
	var choices []*discordgo.ApplicationCommandOptionChoice
	for _, ch := range o.Options {
		if typed != "" && !strings.Contains(strings.ToLower(ch.Name), typed) && !strings.Contains(strings.ToLower(ch.Value), typed) {
			continue
		}
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{Name: truncate(ch.Name, 100), Value: ch.Value})
		if len(choices) == maxChoices {
			break
		}
	}
	return choices
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

func (c *Connector) respond(i *discordgo.InteractionCreate, r *discordgo.InteractionResponse) {
	if err := c.s.InteractionRespond(i.Interaction, r); err != nil {
		c.log.Error("respond to interaction", "err", err)
	}
}

var _ hub.Output = (*Connector)(nil)
