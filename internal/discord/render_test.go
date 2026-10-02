package discord

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hizkifw/inari/internal/hub"
)

func TestSplitShortTextIsOneMessage(t *testing.T) {
	got := split("hello\nworld", 2000)
	if len(got) != 1 || got[0] != "hello\nworld" {
		t.Fatalf("split = %q", got)
	}
}

func TestSplitEmptyIsNothing(t *testing.T) {
	if got := split("", 2000); len(got) != 0 {
		t.Fatalf("split = %q", got)
	}
}

func TestSplitAtLineBreaks(t *testing.T) {
	text := strings.Repeat("abcdefghi\n", 10) // 100 characters
	got := split(text, 35)
	for _, m := range got {
		if utf8.RuneCountInString(m) > 35 {
			t.Fatalf("message over limit: %q", m)
		}
		for _, line := range strings.Split(m, "\n") {
			if line != "abcdefghi" {
				t.Fatalf("line was cut: %q in %q", line, got)
			}
		}
	}
	if joined := strings.Join(got, "\n"); joined != strings.TrimSuffix(text, "\n") {
		t.Fatalf("lost text: %q", joined)
	}
}

func TestSplitCutsLongLine(t *testing.T) {
	text := strings.Repeat("x", 100)
	got := split(text, 30)
	if strings.Join(got, "") != text {
		t.Fatalf("lost text: %q", got)
	}
	for _, m := range got {
		if utf8.RuneCountInString(m) > 30 {
			t.Fatalf("message over limit: %q", m)
		}
	}
}

func TestSplitReopensCodeBlock(t *testing.T) {
	text := "intro\n```go\n" + strings.Repeat("fmt.Println()\n", 10) + "```\noutro"
	got := split(text, 60)
	if len(got) < 2 {
		t.Fatalf("expected several messages, got %q", got)
	}
	for _, m := range got {
		if utf8.RuneCountInString(m) > 60 {
			t.Fatalf("message over limit: %q", m)
		}
		if strings.Count(m, "```")%2 != 0 {
			t.Fatalf("unbalanced fence in %q", m)
		}
	}
	if !strings.HasPrefix(got[1], "```go\n") {
		t.Fatalf("block not reopened with its language: %q", got[1])
	}
	if !strings.HasSuffix(got[len(got)-1], "outro") {
		t.Fatalf("lost the end: %q", got)
	}
}

func TestSplitCountsCharactersNotBytes(t *testing.T) {
	text := strings.Repeat("é", 40)
	if got := split(text, 50); len(got) != 1 {
		t.Fatalf("split = %q", got)
	}
}

func TestRenderTextThenTools(t *testing.T) {
	p := hub.Post{Text: "Let me look.", Tools: []hub.Tool{
		{Title: "read main.go", Status: hub.ToolCompleted},
		{Title: "shell go test", Status: hub.ToolFailed},
		{Title: "read go.mod", Status: "in_progress"},
	}}
	got := render(p)
	want := "Let me look.\n-# ✓ `read main.go`\n-# ✗ `shell go test`\n-# … `read go.mod`"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("render = %q, want %q", got, want)
	}
}

func TestRenderToolsAlone(t *testing.T) {
	got := render(hub.Post{Tools: []hub.Tool{{Title: "read a", Status: hub.ToolCompleted}}})
	if len(got) != 1 || got[0] != "-# ✓ `read a`" {
		t.Fatalf("render = %q", got)
	}
}

func TestRenderShowsLatestTools(t *testing.T) {
	var p hub.Post
	for i := range maxTools + 3 {
		p.Tools = append(p.Tools, hub.Tool{Title: fmt.Sprintf("read %d", i), Status: hub.ToolCompleted})
	}
	got := render(p)[0]
	if !strings.HasPrefix(got, "-# … 3 earlier\n") || strings.Contains(got, "read 2`") || !strings.Contains(got, fmt.Sprintf("read %d`", maxTools+2)) {
		t.Fatalf("render = %q", got)
	}
}

// Text that fills its message keeps the tool calls out of it, so the text
// is never cut differently as calls come and go.
func TestRenderToolsSpillIntoTheirOwnMessage(t *testing.T) {
	text := strings.Repeat("x", maxMessage-5)
	got := render(hub.Post{Text: text, Tools: []hub.Tool{{Title: "read a", Status: hub.ToolCompleted}}})
	if len(got) != 2 || got[0] != text || got[1] != "-# ✓ `read a`" {
		t.Fatalf("render = %q", got)
	}
}

func TestRenderNotice(t *testing.T) {
	got := render(hub.Post{Text: "a\nb", Notice: true})
	if got[0] != "-# a\n-# b" {
		t.Fatalf("render = %q", got)
	}
}

func TestCodeEscapesBackticks(t *testing.T) {
	if got := code("echo `date`"); got != "`` echo `date` ``" {
		t.Fatalf("code = %q", got)
	}
}
