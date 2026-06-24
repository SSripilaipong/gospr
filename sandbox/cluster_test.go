package sandbox

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gospr/builder"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClusterFreshAndUninitialized(t *testing.T) {
	net := NewNetwork()
	hub := NewHub()
	c := newCluster(3, net, hub)
	defer c.stop()

	assert.Len(t, c.nodes, 3)
	assert.Nil(t, c.deployed)
	for _, id := range c.sortedIDs() {
		n, ok := c.node(id)
		require.True(t, ok)
		assert.False(t, n.Initialized(), "%s should start uninitialized", id)
	}
}

func TestResetSwapsToFreshCluster(t *testing.T) {
	s := &Server{net: NewNetwork(), hub: NewHub(), nodes: 3}
	s.current = newCluster(3, s.net, s.hub)

	// Simulate a deploy by marking a node initialized + pinning a plan.
	old := s.current
	n, _ := old.node("node1")
	require.NoError(t, n.Initialize(builder.BuiltPlan{}))
	old.deployed = &builder.BuiltPlan{}
	s.net.SetLink("node1", "node2", false)

	s.reset()

	assert.NotSame(t, old, s.current, "reset installs a new cluster")
	assert.Nil(t, s.current.deployed, "fresh cluster has no pinned plan")
	for _, id := range s.current.sortedIDs() {
		fn, _ := s.current.node(id)
		assert.False(t, fn.Initialized(), "%s reset to uninitialized", id)
	}
	assert.True(t, s.net.Connected("node1", "node2"), "reset reconnects links")

	// Old cluster's done channel is closed (stop ran).
	select {
	case <-old.done:
	case <-time.After(time.Second):
		t.Fatal("old cluster was not stopped")
	}
}

// TestResetReconnectAtomicWithDeploy guards the race where a deploy lands in the
// window between publishing the reset cluster and clearing partitions: a deploy
// that observes a fresh (deployed==nil) post-reset cluster must never still see a
// partition that reset was meant to clear. Concurrent deployers contend on the
// write lock continuously so one reliably acquires it the instant reset releases
// — which, in the buggy ordering (reconnect after unlock), exposes the stale
// partition. Run with -race for full coverage.
func TestResetReconnectAtomicWithDeploy(t *testing.T) {
	s := &Server{net: NewNetwork(), hub: NewHub(), nodes: 3}
	s.current = newCluster(3, s.net, s.hub)
	defer func() { s.current.stop() }()

	var violations atomic.Int64
	for range 100 {
		s.net.SetLink("node1", "node2", false) // partition the (soon to be old) cluster
		before := s.current

		var wg sync.WaitGroup
		stop := make(chan struct{})

		// Several busy deployers hammer the write lock for the whole reset.
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					_ = s.withClusterW(func(c *Cluster) error {
						// A post-reset cluster (c != before) must already have the
						// link reset is meant to clear back to connected.
						if c != before && !s.net.Connected("node1", "node2") {
							violations.Add(1)
						}
						return nil
					})
				}
			}()
		}

		s.reset()
		close(stop)
		wg.Wait()
	}
	assert.Equal(t, int64(0), violations.Load(), "deploy saw a freshly-reset cluster with a stale partition")
}
