package discord

import (
	"sync"
	"time"

	"github.com/hizkifw/inari/internal/hub"
)

// editEvery spaces out edits to one stretch. Discord allows about five
// message edits per five seconds in a channel, and a turn that runs tools
// quickly would otherwise spend them all.
const editEvery = 1500 * time.Millisecond

// stream is the stretch a channel is showing: the messages that carry it,
// kept up to date as kon works. Only the newest stretch is ever edited.
type stream struct {
	// every is the least time between edits.
	every time.Duration

	mu sync.Mutex
	id int
	// msgs are the messages showing the stretch, and shown what each of
	// them says. A stretch longer than one message spills into several.
	msgs  []string
	shown []string
	// want is the stretch as kon last reported it.
	want  hub.Post
	last  time.Time
	timer *time.Timer
}

// messenger is what a stream needs of Discord, so it can be tested without.
type messenger interface {
	// send posts content as a reply to message replyTo, or not as a reply
	// when replyTo is "".
	send(content, replyTo string) (id string)
	edit(id, content string)
}

// post shows p: a new stretch at once in new messages, and a change to the
// current one by an edit, at most once per s.every. It reports whether it
// sent a new message.
func (s *stream) post(m messenger, p hub.Post) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.msgs) > 0 && p.ID == s.id {
		s.want = p
		if s.timer == nil {
			wait := max(time.Until(s.last.Add(s.every)), 0)
			s.timer = time.AfterFunc(wait, func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				s.timer = nil
				s.show(m)
			})
		}
		return false
	}
	s.settle(m)
	s.id, s.msgs, s.shown, s.want = p.ID, nil, nil, p
	s.show(m)
	return true
}

// flush shows a pending change now, so a finished turn's last state is not
// left waiting.
func (s *stream) flush(m messenger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settle(m)
}

// settle shows a pending change of the current stretch. The caller holds
// s.mu.
func (s *stream) settle(m messenger) {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
		s.show(m)
	}
}

// show brings the messages in line with want, editing only those that
// changed. A stretch that answers steering starts as a reply to it. The
// caller holds s.mu.
func (s *stream) show(m messenger) {
	s.last = time.Now()
	for i, part := range render(s.want) {
		switch {
		case i >= len(s.msgs):
			replyTo := ""
			if i == 0 {
				replyTo = s.want.ReplyTo
			}
			id := m.send(part, replyTo)
			if id == "" {
				// Without the message there is nothing to edit later; the
				// next change tries again.
				return
			}
			s.msgs = append(s.msgs, id)
			s.shown = append(s.shown, part)
		case s.shown[i] != part:
			m.edit(s.msgs[i], part)
			s.shown[i] = part
		}
	}
}
