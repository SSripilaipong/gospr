package node

import (
	"fmt"
	"gospr/crdt"
	"gospr/parser"
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
	plan parser.Plan
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

func (n *Node) Initialize(plan parser.Plan) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state == Initialized {
		return nil
	}
	typeMap := make(map[string]parser.TypeDef, len(plan.Types))
	for _, td := range plan.Types {
		typeMap[td.Name] = td
	}
	queryMap := make(map[string][]parser.QueryDef)
	for _, qd := range plan.Queries {
		queryMap[qd.TypeName] = append(queryMap[qd.TypeName], qd)
	}
	updateMap := make(map[string][]parser.UpdateDef)
	for _, ud := range plan.Updates {
		updateMap[ud.TypeName] = append(updateMap[ud.TypeName], ud)
	}
	for _, spec := range plan.Collections {
		var c crdt.CRDT
		var err error
		if td, ok := typeMap[spec.Type]; ok {
			c, err = crdt.NewComposite(td, queryMap[spec.Type], updateMap[spec.Type], spec.Args, n.id)
		} else {
			c, err = crdt.New(spec.Type, spec.Args, n.id)
		}
		if err != nil {
			return fmt.Errorf("node %s: %w", n.id, err)
		}
		n.collections[spec.Name] = c
	}
	n.state = Initialized
	log.Printf("[%s] initialized with %d collection(s)", n.id, len(n.collections))
	return nil
}

func (n *Node) PropagatePlan(plan parser.Plan) {
	msg := deployMsg{plan: plan}
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
			if err := n.Initialize(m.plan); err != nil {
				log.Printf("[%s] deploy error: %v", n.id, err)
			}
		}
	}
}
