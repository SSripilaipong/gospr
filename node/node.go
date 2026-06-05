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

type Node struct {
	id          string
	state       State
	inbox       chan any
	peers       []chan any
	collections map[string]crdt.CRDT
	mu          sync.RWMutex
}

func New(id string) *Node {
	return &Node{
		id:          id,
		inbox:       make(chan any, 64),
		collections: make(map[string]crdt.CRDT),
	}
}

func (n *Node) ID() string { return n.id }

func (n *Node) Inbox() chan any { return n.inbox }

func (n *Node) AddPeer(inbox chan any) {
	n.peers = append(n.peers, inbox)
}

func (n *Node) Start() {
	go n.runMessageLoop()
	go n.runGossip()
}

func (n *Node) Initialize(plan builder.BuiltPlan) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state == Initialized {
		return nil
	}
	for _, bc := range plan.Collections {
		n.collections[bc.Name] = bc.New(n.id)
	}
	n.state = Initialized
	log.Printf("[%s] initialized with %d collection(s)", n.id, len(n.collections))
	return nil
}

func (n *Node) PropagatePlan(plan builder.BuiltPlan) {
	msg := deployMsg{built: plan}
	for _, peer := range n.peers {
		peer <- msg
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

func (n *Node) Query(collection, queryName string) (any, error) {
	n.mu.RLock()
	c, ok := n.collections[collection]
	n.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("collection %q not found", collection)
	}
	return c.Query(queryName)
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
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		n.mu.RLock()
		ready := n.state == Initialized
		n.mu.RUnlock()
		if !ready || len(n.peers) == 0 {
			continue
		}
		snap := n.Snapshot()
		peer := n.peers[rand.Intn(len(n.peers))]
		peer <- gossipMsg{senderID: n.id, snapshot: snap}
	}
}

func (n *Node) runMessageLoop() {
	for msg := range n.inbox {
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
