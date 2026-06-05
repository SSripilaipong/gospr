# CLAUDE.md

## Commands

```bash
go build ./...
go run .                          # start all three nodes (ports 8081/8082/8083)
go test -timeout 10s ./...        # always use -timeout; combinators can loop infinitely
go vet ./...
```

## Architecture

Three nodes run as goroutine groups and communicate only via `chan any` — no shared memory, no direct cross-node calls.

- `POST /deploy` → DSL parsed into a `Plan` → node initializes and propagates `deployMsg` to peers
- Every ~2s each node gossips a snapshot to one random peer; peer merges via per-key max
- Node lifecycle: `Uninitialized → Initialized` (one-way, idempotent)

## File map

```
main.go               wires 3 nodes + gateways, connects peer inboxes

crdt/
  crdt.go             CRDT interface + built-in factory (crdt.New)
  gcounter.go         GCounter — counts map + initial offset; Value() = initial + sum(counts)
  composite.go        CompositeCRDT — per-field delegation for user-defined types

node/
  node.go             node lifecycle and message loop

gateway/
  gateway.go          HTTP: POST /deploy, POST /{collection}, GET /{collection}/{query}

parser/
  types.go            AST types and Plan
  parser.go           public Parse entry point
  stream.go           value-typed Stream — backtracking is free
  result.go           ParseResult[A], Parser[A], Of2–Of5 tuples
  combinators.go      all combinators
  dsl.go              DSL grammar
  parser_test.go
```

## Extension points

- **New built-in CRDT:** implement `crdt.CRDT` and add a case in `crdt.New` — no other files change.
- **New user-defined type:** use the DSL; no Go code needed.

## DSL syntax

```
collection MyCounter = GCounter(0)

type MyCounterType(x int) = { counter: GCounter(x) }
query MyCounterType.MyValue() = counter.Value()
update MyCounterType.AddOne() = { counter: counter.Add(1) }
collection MyCounter = MyCounterType(0)
```

## Parser combinators

`Consumed bool` enables committed-choice: `Or` only retries the right branch if left consumed nothing. `Try` strips `Consumed` on failure to re-enable backtracking.
