package main

import (
	"gospr/gateway"
	"gospr/node"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	nodes := []*node.Node{
		node.New("node1"),
		node.New("node2"),
		node.New("node3"),
	}

	// Wire peer inboxes: each node gets the other two
	for i, n := range nodes {
		for j, peer := range nodes {
			if i != j {
				n.AddPeer(peer.Inbox())
			}
		}
	}

	ports := []string{":8081", ":8082", ":8083"}
	for i, n := range nodes {
		n.Start()
		gw := gateway.New(n, ports[i])
		go gw.Start()
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
}
