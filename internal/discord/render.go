package discord

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/hizkifw/inari/internal/hub"
)

// maxMessage is Discord's limit on a message's content, in characters.
const maxMessage = 2000

// maxTools is how many tool calls a message lists. A stretch that reads
// forty files shows the latest few, which is where the work is.
const maxTools = 8

// maxTitle bounds a tool call's title, which for a shell call is the whole
// command.
const maxTitle = 120

// render turns a post into the messages that carry it: the text, split to
// fit, then its tool calls as small lines. The calls go in a message of their
// own when they would not fit in the text's last, so the text, which does not
// change, is never cut differently as calls arrive.
func render(p hub.Post) []string {
	text := p.Text
	if p.Notice {
		text = "-# " + strings.ReplaceAll(text, "\n", "\n-# ")
	}
	parts := split(strings.TrimSpace(text), maxMessage)
	tools := renderTools(p.Tools)
	switch {
	case tools == "":
	case len(parts) == 0:
		parts = []string{tools}
	case utf8.RuneCountInString(parts[len(parts)-1])+1+utf8.RuneCountInString(tools) <= maxMessage:
		parts[len(parts)-1] += "\n" + tools
	default:
		parts = append(parts, tools)
	}
	return parts
}

func renderTools(tools []hub.Tool) string {
	var lines []string
	if n := len(tools) - maxTools; n > 0 {
		lines = append(lines, fmt.Sprintf("-# … %d earlier", n))
		tools = tools[n:]
	}
	for _, t := range tools {
		lines = append(lines, fmt.Sprintf("-# %s %s", toolMark(t.Status), code(truncate(t.Title, maxTitle))))
	}
	return strings.Join(lines, "\n")
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
// again in the next message, so each message renders on its own.
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
			// A block's opening line nearly as long as a message still
			// leaves each message a character, so this ends.
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
