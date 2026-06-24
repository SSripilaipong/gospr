package sandbox

import (
	"time"

	"gospr/node"
)

// interceptingPeer is a node.Peer that sits on one directed link (from -> to)
// and observes/delays/drops every message before it reaches the target's inbox.
// It is the sandbox's whole reason for the node.Peer seam: production node code
// stays unaware that anything is watching.
type interceptingPeer struct {
	from, to string
	inbox    chan any
	net      *Network
	hub      *Hub
	done     <-chan struct{} // owning cluster's lifetime; closed on Reset/teardown
}

func (p interceptingPeer) Send(msg any) {
	kind := node.MessageKind(msg)

	// Partitioned link: the message is lost. This is how a network partition is
	// simulated — gossip/deploy simply never arrives.
	if !p.net.Connected(p.from, p.to) {
		p.hub.Emit(Event{Kind: kind, From: p.from, To: p.to, Status: "dropped"})
		return
	}

	p.hub.Emit(Event{Kind: kind, From: p.from, To: p.to, Status: "inflight"})

	// Deliver asynchronously after the artificial delay so one slow link never
	// blocks another node's gossip loop or the deploy HTTP call. Both the wait
	// and the final send are guarded by done so a Reset aborts in-flight
	// deliveries promptly instead of waiting out a long delay — and no goroutine
	// lingers blocking on a stopped node's inbox.
	go func() {
		t := time.NewTimer(p.net.Delay())
		select {
		case <-t.C:
			select {
			case p.inbox <- msg:
				p.hub.Emit(Event{Kind: kind, From: p.from, To: p.to, Status: "delivered"})
			case <-p.done:
			}
		case <-p.done:
			t.Stop()
		}
	}()
}
