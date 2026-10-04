package telegram

import (
	"sync"
	"time"

	"github.com/hizkifw/inari/internal/hub"
)

const (
	// editEvery spaces out edits to one stretch. Telegram allows about one
	// message a second in a chat and twenty a minute in a group, and edits
	// count.
	editEvery = 2 * time.Second
	// draftEvery spaces out drafts. Drafts are made for streaming and
	// animate between updates, so they can come much faster than edits.
	draftEvery = 400 * time.Millisecond
)

// stream is the stretch a conversation is showing: first as a draft while
// kon writes it, where Telegram allows drafts, then as the messages that
// carry it, kept up to date as its tool calls run. Only the newest stretch
// is ever drafted or edited.
type stream struct {
	// every is the least time between edits, and draftEvery between
	// drafts.
	every, draftEvery time.Duration

	// mu is held across Telegram calls, so a draft can never land after
	// the message that replaces it.
	mu sync.Mutex
	id int
	// msgs are the messages showing the stretch, and shown what each of
	// them says. A stretch longer than one message spills into several.
	msgs  []int64
	shown []part
	// want is the stretch as kon last reported it.
	want  hub.Post
	last  time.Time
	timer *time.Timer

	// draft is the newest draft not yet shown, if any; drafted is when the
	// last was, and hold is when drafts may resume after a rate limit.
	draft      *hub.Post
	drafted    time.Time
	hold       time.Time
	draftTimer *time.Timer
}

// messenger is what a stream needs of Telegram, so it can be tested without.
type messenger interface {
	send(p part) (id int64)
	edit(id int64, p part)
	// draft shows a draft, and returns how long to wait before the next
	// when Telegram asks to slow down.
	draft(id int, p part) (retry time.Duration)
}

// showDraft queues p as the draft of its stretch, shown at most once per
// s.draftEvery.
func (s *stream) showDraft(m messenger, p hub.Post) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.msgs) > 0 && p.ID == s.id {
		// The stretch is already a message; a draft would only flicker.
		return
	}
	s.draft = &p
	if s.draftTimer != nil {
		return
	}
	wait := max(time.Until(s.drafted.Add(s.draftEvery)), time.Until(s.hold), 0)
	var t *time.Timer
	t = time.AfterFunc(wait, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		// A timer dropped after it fired finds another in its place, or
		// none, and leaves the draft to it.
		if s.draftTimer != t {
			return
		}
		s.draftTimer = nil
		if s.draft == nil {
			return
		}
		p := *s.draft
		s.draft = nil
		s.drafted = time.Now()
		parts := render(hub.Post{ID: p.ID, Text: p.Text})
		if len(parts) == 0 {
			return
		}
		// A stretch too long for one message drafts its last; the rest
		// shows once it is posted. Draft IDs must not be zero.
		if retry := m.draft(p.ID+1, parts[len(parts)-1]); retry > 0 {
			s.hold = time.Now().Add(retry)
		}
	})
	s.draftTimer = t
}

// post shows p: a new stretch at once in new messages, and a change to the
// current one by an edit, at most once per s.every. It reports whether it
// sent a new message.
func (s *stream) post(m messenger, p hub.Post) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The stretch is posted, which takes the place of its draft.
	s.dropDraft()
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
// left waiting, and drops a draft still waiting.
func (s *stream) flush(m messenger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropDraft()
	s.settle(m)
}

// dropDraft forgets a draft not yet shown. The caller holds s.mu.
func (s *stream) dropDraft() {
	s.draft = nil
	if s.draftTimer != nil {
		s.draftTimer.Stop()
		s.draftTimer = nil
	}
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
// changed. The caller holds s.mu.
func (s *stream) show(m messenger) {
	s.last = time.Now()
	for i, p := range render(s.want) {
		switch {
		case i >= len(s.msgs):
			id := m.send(p)
			if id == 0 {
				// Without the message there is nothing to edit later; the
				// next change tries again.
				return
			}
			s.msgs = append(s.msgs, id)
			s.shown = append(s.shown, p)
		case s.shown[i] != p:
			m.edit(s.msgs[i], p)
			s.shown[i] = p
		}
	}
}
