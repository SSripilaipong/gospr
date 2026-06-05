# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
go build ./...        # compile all packages
go run .              # start all three nodes (ports 8081/8082/8083)
go test ./...         # run all tests
go test ./crdt/...    # run tests for a single package
go vet ./...          # static analysis
```

## Architecture

gospr simulates a distributed CRDT platform inside a single process. Three independent nodes run as goroutine groups and communicate exclusively through `chan any` — no shared memory, no direct method calls across node boundaries.

**Data flow for deployment:**
1. Client POSTs DSL text to any node's `/deploy` HTTP endpoint
2. `parser.Parse` turns the text into a `parser.Plan` (list of `CollectionSpec`)
3. The receiving node calls `node.Initialize(plan)` on itself, then `node.PropagatePlan(plan)` which drops a `deployMsg` into each peer's inbox channel
4. Peer nodes pick up the `deployMsg` in their `runMessageLoop` and call `Initialize` on themselves

**Data flow for gossip (anti-entropy):**
- Every ~2s each initialized node calls `Snapshot()` on all its collections and sends a `gossipMsg` to one randomly chosen peer inbox
- The receiving node merges the snapshot via `MergeSnapshot`, which calls `CRDT.Merge` per collection
- GCounter merge is per-key max on `map[string]int64` (nodeID → count)

**Adding a new CRDT type:** implement the `crdt.CRDT` interface (`Apply`, `Query`, `Merge`, `Snapshot`) and add a case in `crdt.New`. No other files need to change.

**Inter-node message types** (`gossipMsg`, `deployMsg`) are unexported structs in `node/node.go`. The channel carries `any`; the message loop type-switches to dispatch.

**Node lifecycle:** `Uninitialized → Initialized` (one-way). `Initialize` is idempotent — a second call is a no-op, which is how duplicate `deployMsg` arrivals are handled safely.

## DSL syntax

```
collection MyCounter = GCounter(0)
collection OtherCounter = GCounter(0)
```

Blank lines and unrecognized lines are silently skipped. Malformed `collection` lines return a parse error.
