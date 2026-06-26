package node

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gospr/builder"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingPeer counts how many messages it received.
type recordingPeer struct {
	count atomic.Int64
}

func (p *recordingPeer) Send(any) { p.count.Add(1) }

func TestMessageKind(t *testing.T) {
	assert.Equal(t, "gossip", MessageKind(gossipMsg{}))
	assert.Equal(t, "deploy", MessageKind(deployMsg{}))
	assert.Equal(t, "unknown", MessageKind(42))
}

func TestStopHaltsGossip(t *testing.T) {
	n := New("node1", WithGossipInterval(10*time.Millisecond))
	peer := &recordingPeer{}
	n.AddPeer("node2", peer)
	require.NoError(t, n.Initialize(builder.BuiltPlan{}))
	n.Start()

	// Let it gossip a few times.
	require.Eventually(t, func() bool { return peer.count.Load() > 0 }, time.Second, 5*time.Millisecond)

	n.Stop()
	time.Sleep(30 * time.Millisecond) // let the loop observe quit
	after := peer.count.Load()
	time.Sleep(60 * time.Millisecond) // several intervals would have elapsed
	assert.Equal(t, after, peer.count.Load(), "no gossip after Stop")
}

func TestStopIsIdempotent(t *testing.T) {
	n := New("node1")
	n.Start()
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() { defer wg.Done(); n.Stop() }()
	}
	wg.Wait() // must not panic on a double close
}
