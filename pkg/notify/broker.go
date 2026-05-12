package notify

import "sync"

// Broker is a fan-out SSE publisher. Each subscriber gets its own independent
// buffered channel so a slow client cannot block others.
type Broker struct {
	mu   sync.RWMutex
	subs map[uint64]chan Event
	next uint64
}

// Event is a typed SSE payload.
type Event struct {
	Type    string `json:"type"`
	Payload []byte `json:"payload"` // raw JSON
}

func NewBroker() *Broker {
	return &Broker{subs: make(map[uint64]chan Event)}
}

// Subscribe registers a new subscriber and returns its channel plus an
// unsubscribe function the caller must invoke when done.
func (b *Broker) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	id := b.next
	b.next++
	ch := make(chan Event, 64)
	b.subs[id] = ch
	b.mu.Unlock()

	return ch, func() {
		b.mu.Lock()
		delete(b.subs, id)
		close(ch)
		b.mu.Unlock()
	}
}

// Publish sends ev to every current subscriber. Slow subscribers are dropped
// (non-blocking send).
func (b *Broker) Publish(ev Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// PublishJSON is a convenience wrapper that sets Type and passes raw JSON bytes.
func (b *Broker) PublishJSON(evType string, payload []byte) {
	b.Publish(Event{Type: evType, Payload: payload})
}
