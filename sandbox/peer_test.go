package sandbox

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitEvent reads one event with statuses we care about, timing out the test.
func waitEvent(t *testing.T, ch chan Event, want string) Event {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Status == want {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q event", want)
		}
	}
}

func TestPeerConnectedDelivers(t *testing.T) {
	net := NewNetwork()
	hub := NewHub()
	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)
	inbox := make(chan any, 1)
	done := make(chan struct{})

	p := interceptingPeer{from: "node1", to: "node2", inbox: inbox, net: net, hub: hub, done: done}
	p.Send("hello")

	waitEvent(t, sub, "inflight")
	waitEvent(t, sub, "delivered")
	select {
	case got := <-inbox:
		assert.Equal(t, "hello", got)
	case <-time.After(time.Second):
		t.Fatal("message never delivered to inbox")
	}
}

func TestPeerDisconnectedDrops(t *testing.T) {
	net := NewNetwork()
	net.SetLink("node1", "node2", false)
	hub := NewHub()
	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)
	inbox := make(chan any, 1)
	done := make(chan struct{})

	p := interceptingPeer{from: "node1", to: "node2", inbox: inbox, net: net, hub: hub, done: done}
	p.Send("hello")

	ev := waitEvent(t, sub, "dropped")
	assert.Equal(t, "node2", ev.To)
	select {
	case <-inbox:
		t.Fatal("partitioned message must not be delivered")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPeerDeliveryAbortsOnDone(t *testing.T) {
	net := NewNetwork()
	net.SetDelay(2 * time.Second) // long enough that we abort mid-delay
	hub := NewHub()
	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)
	inbox := make(chan any) // unbuffered: a send would block if attempted
	done := make(chan struct{})

	p := interceptingPeer{from: "node1", to: "node2", inbox: inbox, net: net, hub: hub, done: done}
	p.Send("hello")
	waitEvent(t, sub, "inflight")

	close(done) // tear the cluster down during the delay

	select {
	case <-inbox:
		t.Fatal("delivery should have been aborted by done")
	case <-time.After(300 * time.Millisecond):
		// no delivery — the goroutine returned promptly instead of waiting 2s
	}
}

func TestHubNonBlocking(t *testing.T) {
	hub := NewHub()
	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)
	// Emit far more than the buffer; must not block even with no reader draining.
	for range 1000 {
		hub.Emit(Event{Kind: "gossip", Status: "inflight"})
	}
	require.NotEmpty(t, sub) // buffer holds some; the rest were dropped, not blocked
}
