package discord

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestQuote(t *testing.T) {
	s := &discordgo.Session{State: discordgo.NewState()}
	s.State.User = &discordgo.User{ID: "bot", Username: "inari"}
	if q := quote(s, nil); q != nil {
		t.Fatalf("a message replying to nothing quotes %+v", q)
	}
	mine := quote(s, &discordgo.Message{Author: s.State.User, Content: "Shall I deploy?"})
	if mine.Author != "you" || mine.Text != "Shall I deploy?" {
		t.Fatalf("a reply to kon quotes %+v", mine)
	}
	file := quote(s, &discordgo.Message{Author: &discordgo.User{ID: "a", Username: "alice"}, Attachments: []*discordgo.MessageAttachment{{}}})
	if file.Author != "alice" || file.Text != "(an attachment)" {
		t.Fatalf("a reply to a file quotes %+v", file)
	}
}
