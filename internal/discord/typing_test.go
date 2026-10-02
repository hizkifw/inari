package discord

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestTypingRenews(t *testing.T) {
	var sent atomic.Int32
	ty := startTyping(func() { sent.Add(1) }, 10*time.Millisecond, time.Hour)
	time.Sleep(55 * time.Millisecond)
	ty.stop()
	if n := sent.Load(); n < 3 {
		t.Fatalf("sent %d times, want it renewed", n)
	}
}

func TestTypingComesBackAfterAPost(t *testing.T) {
	var sent atomic.Int32
	ty := startTyping(func() { sent.Add(1) }, time.Hour, 10*time.Millisecond)
	defer ty.stop()
	waitFor(t, func() bool { return sent.Load() == 1 })
	ty.posted()
	waitFor(t, func() bool { return sent.Load() == 2 })
}

// The bug this guards against: a turn's last post followed at once by its
// end must not leave an indicator Discord keeps showing.
func TestTypingStopsBeforeTheGraceAfterTheLastPost(t *testing.T) {
	var sent atomic.Int32
	ty := startTyping(func() { sent.Add(1) }, time.Hour, 50*time.Millisecond)
	waitFor(t, func() bool { return sent.Load() == 1 })
	ty.posted()
	ty.stop()
	time.Sleep(100 * time.Millisecond)
	if n := sent.Load(); n != 1 {
		t.Fatalf("sent %d times; the indicator came back after the turn ended", n)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
