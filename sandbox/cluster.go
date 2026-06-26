package sandbox

import (
	"fmt"
	"sort"
	"time"

	"gospr/builder"
	"gospr/node"
)

// Cluster is one running topology of sandbox nodes. It is swappable: Reset
// builds a fresh Cluster and stops the old one, so different DSL can be deployed
// without restarting the process (Node.Initialize is one-way/idempotent).
type Cluster struct {
	ids      []string
	nodes    map[string]*node.Node
	done     chan struct{}
	deployed *builder.BuiltPlan // pinned plan; nil until the first successful deploy
}

// newCluster builds nodeN nodes wired in a full mesh of intercepting peers,
// then starts them. The gossip interval is short so propagation is watchable.
func newCluster(n int, net *Network, hub *Hub) *Cluster {
	c := &Cluster{
		nodes: make(map[string]*node.Node, n),
		done:  make(chan struct{}),
	}
	for i := range n {
		id := fmt.Sprintf("node%d", i+1)
		c.ids = append(c.ids, id)
		// A modest sync timeout keeps a partitioned linearized op's 503 (and the
		// Reset that may wait behind it) responsive in the UI.
		c.nodes[id] = node.New(id,
			node.WithGossipInterval(1500*time.Millisecond),
			node.WithSyncTimeout(2*time.Second))
	}
	for _, from := range c.ids {
		for _, to := range c.ids {
			if from == to {
				continue
			}
			c.nodes[from].AddPeer(to, interceptingPeer{
				from:  from,
				to:    to,
				inbox: c.nodes[to].Inbox(),
				net:   net,
				hub:   hub,
				done:  c.done,
			})
		}
	}
	for _, id := range c.ids {
		c.nodes[id].Start()
	}
	return c
}

// stop tears the cluster down: close done (aborts in-flight delayed deliveries)
// then stop every node goroutine. No goroutine or ticker leaks.
func (c *Cluster) stop() {
	close(c.done)
	for _, id := range c.ids {
		c.nodes[id].Stop()
	}
}

func (c *Cluster) node(id string) (*node.Node, bool) {
	n, ok := c.nodes[id]
	return n, ok
}

// sortedIDs returns node ids in stable display order.
func (c *Cluster) sortedIDs() []string {
	ids := append([]string(nil), c.ids...)
	sort.Strings(ids)
	return ids
}
