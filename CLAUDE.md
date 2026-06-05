# CLAUDE.md

## Commands

```bash
go build ./...                    # compile all packages
go run .                          # start all three nodes (ports 8081/8082/8083)
go test -timeout 10s ./...        # run all tests (always use -timeout; combinators can loop infinitely)
go test -timeout 10s ./parser/... # run tests for a single package
go vet ./...                      # static analysis
```

## Architecture

gospr simulates a distributed CRDT platform inside a single process. Three nodes run as goroutine groups and communicate only via `chan any` — no shared memory, no direct cross-node calls.

- Client POSTs DSL text to any node's `POST /deploy` → parsed into a `Plan` → node initializes itself and propagates a `deployMsg` to peers via their inbox channels
- Every ~2s each initialized node snapshots its collections and sends a `gossipMsg` to one random peer; the peer merges via per-key max
- Node lifecycle: `Uninitialized → Initialized` (one-way, idempotent — duplicate `deployMsg` is a no-op)

## File map

```
main.go               entry point — wires 3 nodes + gateways, connects peer inboxes

crdt/
  crdt.go             CRDT interface (Apply/Query/Merge/Snapshot) + New() factory
  gcounter.go         GCounter: map[string]int64 nodeID→count, merge = per-key max

node/
  node.go             Node struct, Initialize, PropagatePlan, Apply, Query,
                      runMessageLoop (gossipMsg/deployMsg dispatch), runGossip

gateway/
  gateway.go          HTTP: POST /deploy, POST /{collection}, GET /{collection}/{query}

parser/
  types.go            CollectionSpec, Plan, ParseError
  parser.go           Parse(string) (Plan, error) — public entry
  stream.go           value-typed Stream (Advance returns new struct, backtracking is free)
  result.go           ParseResult[A], Parser[A], Of2/Of3/Of4 tuple types
  combinators.go      all combinators (see below)
  dsl.go              DSL grammar built from combinators
  parser_test.go      7 integration tests via Parse()
```

## Parser combinators (`parser/combinators.go`)

Parsec-style. `Consumed bool` enables committed-choice: `Or` only retries the right branch if left consumed nothing. `Try` strips `Consumed` on failure to re-enable backtracking.

| Combinator | What it does |
|---|---|
| `Satisfy(pred, label)` | consume one rune matching pred |
| `Map(p, f)` | transform result |
| `Discard(p)` | map to `struct{}` |
| `Sequence2/3/4(p...)` | run N parsers in order, return `Of2/Of3/Of4` tuple |
| `Prefix(prefix, p)` | run prefix (discard), keep p's result |
| `Suffix(suffix, p)` | keep p's result, then run suffix (discard) |
| `Or(left, right)` | try left; if not consumed, try right |
| `Try(p)` | on failure, clear Consumed (enables backtracking) |
| `Many / Many1` | zero-or-more / one-or-more |
| `SepBy(p, sep)` | p separated by sep |

**Adding a new CRDT:** implement `crdt.CRDT` and add a case in `crdt.New` — no other files change.

## DSL syntax

```
collection MyCounter = GCounter(0)
collection OtherCounter = GCounter(0)
```

Blank and unrecognized lines are silently skipped. Malformed `collection` lines return a `ParseError`.
