package hub

import "sync"

// outbox runs a conversation's output calls one at a time, in order, off the
// caller's goroutine. kon's read loop queues output while holding the hub's
// lock, and a slow chat API must stall neither.
type outbox struct {
	mu      sync.Mutex
	queue   []func()
	running bool
}

func (o *outbox) do(f func()) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.queue = append(o.queue, f)
	if !o.running {
		o.running = true
		go o.drain()
	}
}

func (o *outbox) drain() {
	for {
		o.mu.Lock()
		if len(o.queue) == 0 {
			o.running = false
			o.mu.Unlock()
			return
		}
		f := o.queue[0]
		o.queue = o.queue[1:]
		o.mu.Unlock()
		f()
	}
}
