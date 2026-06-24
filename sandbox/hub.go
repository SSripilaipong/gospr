package sandbox

import "sync"

// Event is a single observable moment in the cluster's life, streamed to the UI
// over SSE to drive the link animations. It is best-effort telemetry; the
// authoritative state always comes from the /state poll.
type Event struct {
	Kind   string `json:"kind"`             // "gossip" | "deploy" | "reset"
	From   string `json:"from,omitempty"`   // sender node id
	To     string `json:"to,omitempty"`     // target node id
	Status string `json:"status,omitempty"` // "inflight" | "delivered" | "dropped"
}

// Hub fans out Events to all SSE subscribers without ever blocking the emitter.
// Each subscriber owns a buffered channel; a slow/stuck client simply drops
// events (animation is best-effort) rather than stalling the gossip loop or the
// deploy HTTP handler that emit from the intercepting peer.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: map[chan Event]struct{}{}}
}

func (h *Hub) Subscribe() chan Event {
	ch := make(chan Event, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) Unsubscribe(ch chan Event) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// Emit delivers ev to every subscriber, skipping any whose buffer is full.
func (h *Hub) Emit(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default: // drop on full — never block the emitter
		}
	}
}
