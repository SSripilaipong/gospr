package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"gospr/builder"
	"gospr/gateway"
	"gospr/node"
	"gospr/parser"

	"github.com/urfave/cli/v3"
)

func main() {
	cmd := &cli.Command{
		Name:  "gospr",
		Usage: "a toy distributed CRDT engine driven by a small functional DSL",
		Commands: []*cli.Command{
			serverCmd(),
			checkCmd(),
		},
	}
	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func serverCmd() *cli.Command {
	return &cli.Command{
		Name:  "server",
		Usage: "run gospr nodes",
		Commands: []*cli.Command{
			{
				Name:  "local",
				Usage: "run a local cluster (single node by default)",
				Flags: []cli.Flag{
					&cli.IntFlag{
						Name:  "nodes",
						Value: 1,
						Usage: "number of nodes to run",
					},
					&cli.IntFlag{
						Name:  "port",
						Value: 9050,
						Usage: "base HTTP port; node i listens on port+i",
					},
				},
				Action: runLocal,
			},
		},
	}
}

func checkCmd() *cli.Command {
	return &cli.Command{
		Name:      "check",
		Usage:     "validate a .gos file (parse, type-check, prove convergence) without running a server",
		ArgsUsage: "<file.gos>",
		Action:    runCheck,
	}
}

func runLocal(ctx context.Context, cmd *cli.Command) error {
	nodes := cmd.Int("nodes")
	basePort := cmd.Int("port")

	if nodes < 1 {
		return cli.Exit("--nodes must be >= 1", 1)
	}
	if basePort < 1 || basePort+nodes-1 > 65535 {
		return cli.Exit("--port out of range: need 1 <= port and port+nodes-1 <= 65535", 1)
	}

	ns := make([]*node.Node, nodes)
	for i := range ns {
		ns[i] = node.New(fmt.Sprintf("node%d", i+1))
	}

	// Wire peer inboxes: each node gets every other node. With a single node
	// this adds no peers, and gossip no-ops without peers — no special-casing.
	for i, n := range ns {
		for j, peer := range ns {
			if i != j {
				n.AddPeer(peer.Inbox())
			}
		}
	}

	for i, n := range ns {
		n.Start()
		gw := gateway.New(n, fmt.Sprintf(":%d", basePort+i))
		go gw.Start()
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-quit:
	case <-ctx.Done():
	}
	return nil
}

func runCheck(ctx context.Context, cmd *cli.Command) error {
	path := cmd.Args().First()
	if path == "" {
		return cli.Exit("usage: gospr check <file.gos>", 1)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cli.Exit(err.Error(), 1)
	}
	plan, err := parser.Parse(string(data))
	if err != nil {
		return cli.Exit(fmt.Sprintf("%s: parse error: %v", path, err), 1)
	}
	built, err := builder.Build(plan)
	if err != nil {
		return cli.Exit(fmt.Sprintf("%s: build error: %v", path, err), 1)
	}
	fmt.Printf("ok: %s — %d collection(s)\n", path, len(built.Collections))
	if len(built.Collections) == 0 {
		fmt.Println("note: no collections declared — this file deploys to nothing")
	}
	return nil
}
