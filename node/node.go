package node

import (
	"fmt"
	"gospr/builder"
	"gospr/crdt"
	"log"
	"math/rand"
	"sync"
	"time"
)

type State int

const (
	Uninitialized State = iota
	Initialized
)

type gossipMsg struct {
	senderID string
	snapshot map[string]any
}

type deployMsg struct {
	built builder.BuiltPlan
}

// Peer is the send-side of a node link. The default implementation (ChanPeer)
// delivers straight to a peer's inbox channel; alternative implementations can
// intercept, delay, or drop messages (e.g. the sandbox) without the node code
// knowing the difference. This is the single interception point for both gossip
// and deploy propagation.
type Peer interface{ Send(msg any) }

// ChanPeer is the production Peer: a blocking send to the target inbox.
type ChanPeer struct{ inbox chan any }

func NewChanPeer(inbox chan any) ChanPeer { return ChanPeer{inbox} }

func (p ChanPeer) Send(msg any) { p.inbox <- msg }

// MessageKind classifies an inter-node message without exposing the unexported
// message types, so non-node packages can label traffic.
func MessageKind(msg any) string {
	switch msg.(type) {
	case gossipMsg:
		return "gossip"
	case deployMsg:
		return "deploy"
	default:
		return "unknown"
	}
}

type Node struct {
	id             string
	state          State
	inbox          chan any
	peers          []Peer
	collections    map[string]crdt.CRDT
	gossipInterval time.Duration
	quit           chan struct{}
	stopOnce       sync.Once
	mu             sync.RWMutex
}

// Option configures a Node at construction.
type Option func(*Node)

// WithGossipInterval overrides the default 2s gossip ticker; tests use a short
// interval so propagation converges quickly.
func WithGossipInterval(d time.Duration) Option {
	return func(n *Node) { n.gossipInterval = d }
}

func New(id string, opts ...Option) *Node {
	n := &Node{
		id:             id,
		inbox:          make(chan any, 64),
		collections:    make(map[string]crdt.CRDT),
		gossipInterval: 2 * time.Second,
		quit:           make(chan struct{}),
	}
	for _, opt := range opts {
		opt(n)
	}
	return n
}

func (n *Node) ID() string { return n.id }

func (n *Node) Inbox() chan any { return n.inbox }

func (n *Node) AddPeer(p Peer) {
	n.peers = append(n.peers, p)
}

func (n *Node) Start() {
	go n.runMessageLoop()
	go n.runGossip()
}

// Stop gracefully halts the gossip and message-loop goroutines. It is idempotent
// and prod-neutral: `server local` never calls it (behavior identical to before);
// the sandbox calls it on Reset to tear a cluster down without leaking goroutines.
func (n *Node) Stop() {
	n.stopOnce.Do(func() { close(n.quit) })
}

// Initialized reports whether a plan has been deployed to this node.
func (n *Node) Initialized() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.state == Initialized
}

func (n *Node) Initialize(plan builder.BuiltPlan) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state == Initialized {
		return nil
	}
	for _, bc := range plan.Collections {
		n.collections[bc.Name] = bc.Spec.New(n.id)
	}
	n.state = Initialized
	log.Printf("[%s] initialized with %d collection(s)", n.id, len(n.collections))
	return nil
}

func (n *Node) PropagatePlan(plan builder.BuiltPlan) {
	msg := deployMsg{built: plan}
	for _, peer := range n.peers {
		peer.Send(msg)
	}
}

func (n *Node) Apply(collection, action string, payload []any) error {
	n.mu.RLock()
	c, ok := n.collections[collection]
	n.mu.RUnlock()
	if !ok {
		return fmt.Errorf("collection %q not found", collection)
	}
	return c.Apply(action, payload)
}

func (n *Node) Query(collection, queryName string, params []any) (any, error) {
	n.mu.RLock()
	c, ok := n.collections[collection]
	n.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("collection %q not found", collection)
	}
	return c.Query(queryName, params)
}

func (n *Node) Snapshot() map[string]any {
	n.mu.RLock()
	defer n.mu.RUnlock()
	snap := make(map[string]any, len(n.collections))
	for name, c := range n.collections {
		snap[name] = c.Snapshot()
	}
	return snap
}

func (n *Node) MergeSnapshot(snap map[string]any) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for name, s := range snap {
		c, ok := n.collections[name]
		if !ok {
			continue
		}
		if err := c.Merge(s); err != nil {
			log.Printf("[%s] merge error for %s: %v", n.id, name, err)
		}
	}
}

func (n *Node) runGossip() {
	ticker := time.NewTicker(n.gossipInterval)
	defer ticker.Stop()
	for {
		select {
		case <-n.quit:
			return
		case <-ticker.C:
		}
		n.mu.RLock()
		ready := n.state == Initialized
		n.mu.RUnlock()
		if !ready || len(n.peers) == 0 {
			continue
		}
		snap := n.Snapshot()
		peer := n.peers[rand.Intn(len(n.peers))]
		peer.Send(gossipMsg{senderID: n.id, snapshot: snap})
	}
}

func (n *Node) runMessageLoop() {
	for {
		var msg any
		select {
		case <-n.quit:
			return
		case msg = <-n.inbox:
		}
		switch m := msg.(type) {
		case gossipMsg:
			n.mu.RLock()
			ready := n.state == Initialized
			n.mu.RUnlock()
			if ready {
				n.MergeSnapshot(m.snapshot)
			}
		case deployMsg:
			if err := n.Initialize(m.built); err != nil {
				log.Printf("[%s] deploy error: %v", n.id, err)
			}
		}
	}
}
