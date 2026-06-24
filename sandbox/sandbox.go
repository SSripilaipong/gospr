package sandbox

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Server owns the stable Network + Hub and a swappable current Cluster behind an
// RWMutex. Every handler holds the lock for the *entire* node operation (via
// withCluster / withClusterW) — it never grabs the *Cluster pointer and then
// releases — so Reset can safely stop the old cluster once it holds the write
// lock (no handler is still using it).
type Server struct {
	net   *Network
	hub   *Hub
	nodes int

	mu      sync.RWMutex
	current *Cluster
}

// Run starts a sandbox: build the initial cluster, serve the UI + API, and
// block until the context is cancelled or an interrupt arrives.
func Run(ctx context.Context, nodes int, port int) error {
	if nodes < 1 {
		return fmt.Errorf("--nodes must be >= 1")
	}
	s := &Server{
		net:   NewNetwork(),
		hub:   NewHub(),
		nodes: nodes,
	}
	s.current = newCluster(nodes, s.net, s.hub)

	addr := fmt.Sprintf(":%d", port)
	srv := &http.Server{Addr: addr, Handler: s.handler()}
	go func() {
		log.Printf("[sandbox] %d node(s), web UI on http://localhost%s", nodes, addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("sandbox server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-quit:
	case <-ctx.Done():
	}

	shutCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = srv.Shutdown(shutCtx)
	s.mu.Lock()
	s.current.stop()
	s.mu.Unlock()
	return nil
}

// withCluster runs fn under the read lock, holding it for the whole operation.
// Use for read-only and per-node-mutating handlers (the node has its own mutex,
// so the cluster lock only guards the swappable pointer).
func (s *Server) withCluster(fn func(*Cluster) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fn(s.current)
}

// withClusterW runs fn under the write lock — for deploy, which both checks and
// sets the pinned plan, so it must serialize against Reset and concurrent deploys.
func (s *Server) withClusterW(fn func(*Cluster) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(s.current)
}

// reset swaps in a fresh cluster and stops the old one. The write lock waits out
// any in-flight read-locked handler, so old.stop() after unlock is race-free.
//
// Reconnect MUST happen inside the same write-locked transition that publishes
// the new cluster: otherwise a deploy already past parse/build could acquire the
// write lock in the gap, see a fresh (deployed==nil) cluster, and PropagatePlan
// through still-stale partitions — dropping the plan to some nodes and then
// pinning it, leaving them permanently uninitialized. Holding s.mu while taking
// the network lock is deadlock-free: deploy also acquires s.mu before the network
// lock (via PropagatePlan→Send→Connected), so the order is always s.mu → net.
func (s *Server) reset() {
	s.mu.Lock()
	old := s.current
	s.current = newCluster(s.nodes, s.net, s.hub)
	s.net.Reconnect() // links back to connected (delay slider value is kept), atomic with the swap
	s.mu.Unlock()

	old.stop()
	s.hub.Emit(Event{Kind: "reset"})
}
