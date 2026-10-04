package telegram

import (
	"fmt"
	"html"
	"strings"
	"unicode/utf8"

	"github.com/hizkifw/inari/internal/hub"
)

const (
	// maxRichMessage is Telegram's limit on a rich message, in characters.
	maxRichMessage = 32768
	// maxRich is how much of a stretch's text one rich message carries,
	// leaving room for its tool calls.
	maxRich = 30000
	// maxPlain is Telegram's limit on a plain text message.
	maxPlain = 4096
	// maxTools is how many tool calls a message lists. A stretch that reads
	// forty files shows the latest few, which is where the work is.
	maxTools = 8
	// maxTitle bounds a tool call's title, which for a shell call is the
	// whole command.
	maxTitle = 120
)

// part is one message's worth of a post, as rich Markdown and as the plain
// text sent when Telegram refuses the Markdown.
type part struct {
	rich, plain string
}

// render turns a post into the messages that carry it: the text, split to
// fit, then its tool calls as footer lines. The calls go in a message of
// their own when they would not fit in the text's last, so the text, which
// does not change, is never cut differently as calls arrive.
func render(p hub.Post) []part {
	var parts []part
	if p.Notice {
		parts = []part{{rich: footers(strings.Split(p.Text, "\n")), plain: p.Text}}
	} else {
		for _, s := range split(strings.TrimSpace(p.Text), maxRich) {
			parts = append(parts, part{rich: s, plain: s})
		}
	}
	if len(p.Tools) > 0 {
		parts = withTools(parts, toolLines(p.Tools))
	}
	// Plain text is the fallback, and must fit the smaller limit even
	// though it then loses the end of a long stretch.
	for i := range parts {
		parts[i].plain = truncate(parts[i].plain, maxPlain)
	}
	return parts
}

// withTools adds tool call lines to a post's last part, or as a part of
// their own when they do not fit.
func withTools(parts []part, tools []string) []part {
	rich, plain := footers(tools), strings.Join(tools, "\n")
	last := len(parts) - 1
	switch {
	case last < 0:
		parts = []part{{rich: rich, plain: plain}}
	case utf8.RuneCountInString(parts[last].rich)+2+utf8.RuneCountInString(rich) <= maxRichMessage:
		parts[last].rich += "\n\n" + rich
		parts[last].plain += "\n" + plain
	default:
		parts = append(parts, part{rich: rich, plain: plain})
	}
	return parts
}

func toolLines(tools []hub.Tool) []string {
	var lines []string
	if n := len(tools) - maxTools; n > 0 {
		lines = append(lines, fmt.Sprintf("… %d earlier", n))
		tools = tools[n:]
	}
	for _, t := range tools {
		lines = append(lines, toolMark(t.Status)+" "+truncate(strings.ReplaceAll(t.Title, "\n", " "), maxTitle))
	}
	return lines
}

// footers shows lines as small print, each a footer block of its own. A
// footer is a block HTML tag, inside which Markdown is not parsed, so the
// lines are escaped as HTML.
func footers(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("<footer>" + html.EscapeString(l) + "</footer>")
	}
	return b.String()
}

func toolMark(status string) string {
	switch status {
	case hub.ToolFailed:
		return "✗"
	case hub.ToolCompleted:
		return "✓"
	}
	return "…"
}

// code wraps s as inline code, with backtick runs longer than any inside it.
func code(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	ticks := "`"
	for strings.Contains(s, ticks) {
		ticks += "`"
	}
	// Padding keeps a backtick at either end from joining the fence.
	if len(ticks) > 1 {
		return ticks + " " + s + " " + ticks
	}
	return ticks + s + ticks
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// split cuts text into messages of at most limit characters, at a line break
// where there is one. A cut inside a code block closes the block and opens it
// again in the next message, so each message renders on its own. It is the
// Discord connector's split; only the limit differs.
func split(text string, limit int) []string {
	const closing = "\n```"
	var out []string
	var cur strings.Builder
	n, base := 0, 0 // characters in cur, and how many of them reopen a block
	fence := ""     // the opening line of the code block cur is inside
	flush := func() {
		if n > base {
			s := strings.TrimRight(cur.String(), "\n")
			if fence != "" {
				s += closing
			}
			out = append(out, s)
		}
		cur.Reset()
		n, base = 0, 0
		if fence != "" {
			cur.WriteString(fence + "\n")
			n = utf8.RuneCountInString(fence) + 1
			base = n
		}
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		t := strings.TrimSpace(line)
		closes := fence != "" && t == "```"
		for line != "" {
			// Room is kept to close a block, unless this line closes it.
			room := limit - n
			if !closes {
				room -= len(closing)
			}
			size := utf8.RuneCountInString(line)
			if size <= room {
				cur.WriteString(line)
				n += size
				break
			}
			if n > base {
				// Start the line in a fresh message rather than cut it.
				flush()
				continue
			}
			// A line longer than a whole message is cut where it must be.
			room = max(room, 1)
			r := []rune(line)
			cur.WriteString(string(r[:room]))
			n += room
			line = string(r[room:])
			flush()
		}
		switch {
		case closes:
			fence = ""
		case fence == "" && strings.HasPrefix(t, "```"):
			// A line that opens and closes a block, such as ```x```, leaves
			// none open.
			if !(len(t) > 3 && strings.HasSuffix(t, "```")) {
				fence = t
			}
		}
	}
	fence = ""
	flush()
	return out
}
