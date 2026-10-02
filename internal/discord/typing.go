package discord

import (
	"context"
	"time"
)

const (
	// typingEvery renews the indicator, which Discord shows for about ten
	// seconds after each request.
	typingEvery = 8 * time.Second
	// typingGrace is how long the indicator waits to come back after a
	// message clears it. Discord cannot take an indicator back, so one
	// restarted after a turn's last message would linger for ten seconds;
	// the turn's end arrives well within the grace and stops it first.
	typingGrace = time.Second
)

// typer keeps a channel's typing indicator up while kon works.
type typer struct {
	cancel context.CancelFunc
	post   chan struct{}
}

// startTyping shows the indicator by calling send now and renewing it every
// period, until stop.
func startTyping(send func(), every, grace time.Duration) *typer {
	ctx, cancel := context.WithCancel(context.Background())
	t := &typer{cancel: cancel, post: make(chan struct{}, 1)}
	go func() {
		send()
		timer := time.NewTimer(every)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.post:
				timer.Reset(grace)
			case <-timer.C:
				send()
				timer.Reset(every)
			}
		}
	}()
	return t
}

// posted reports that a message was sent, which clears the indicator.
func (t *typer) posted() {
	select {
	case t.post <- struct{}{}:
	default:
	}
}

func (t *typer) stop() { t.cancel() }
