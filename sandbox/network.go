package sandbox

import (
	"sync"
	"time"
)

// Network is the shared, concurrency-safe control state for the simulated
// links between nodes: which directed/undirected pairs are partitioned and the
// artificial delay applied to every message before delivery. It is stable
// across cluster Resets (the delay slider survives; links snap back to
// connected on Reset).
type Network struct {
	mu           sync.RWMutex
	disconnected map[string]bool // key = unordered "a|b" pair
	delay        time.Duration
}

// defaultDelay is the artificial per-message delay a fresh Network starts with.
const defaultDelay = 1000 * time.Millisecond

func NewNetwork() *Network {
	return &Network{disconnected: map[string]bool{}, delay: defaultDelay}
}

// linkKey builds the order-independent key for a node pair.
func linkKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

// Connected reports whether messages may flow between a and b.
func (n *Network) Connected(a, b string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return !n.disconnected[linkKey(a, b)]
}

// SetLink connects or disconnects the a<->b pair.
func (n *Network) SetLink(a, b string, connected bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if connected {
		delete(n.disconnected, linkKey(a, b))
	} else {
		n.disconnected[linkKey(a, b)] = true
	}
}

// Reconnect clears all partitions (used on cluster Reset).
func (n *Network) Reconnect() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.disconnected = map[string]bool{}
}

// Disconnected returns a snapshot of the partitioned pairs (key -> true).
func (n *Network) Disconnected() map[string]bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make(map[string]bool, len(n.disconnected))
	for k := range n.disconnected {
		out[k] = true
	}
	return out
}

func (n *Network) Delay() time.Duration {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.delay
}

func (n *Network) SetDelay(d time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.delay = d
}
