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
  crdt.go             CRDT interface and built-in factory
  gcounter.go         GCounter: map[string]int64 nodeID→count, merge = per-key max
  composite.go        CompositeCRDT for user-defined types: per-field delegation

node/
  node.go             Node lifecycle and message loop

gateway/
  gateway.go          HTTP: POST /deploy, POST /{collection}, GET /{collection}/{query}

parser/
  types.go            AST types and Plan
  parser.go           public Parse entry point
  stream.go           value-typed Stream — Advance returns new struct, backtracking is free
  result.go           ParseResult[A], Parser[A], Of2–Of5 tuple types
  combinators.go      all combinators (see below)
  dsl.go              DSL grammar built from combinators
  parser_test.go
```

## Parser combinators (`parser/combinators.go`)

Parsec-style. `Consumed bool` enables committed-choice: `Or` only retries the right branch if left consumed nothing. `Try` strips `Consumed` on failure to re-enable backtracking.

| Combinator | What it does |
|---|---|
| `Satisfy(pred, label)` | consume one rune matching pred |
| `Map(p, f)` | transform result |
| `Discard(p)` | map to `struct{}` |
| `Sequence2/3/4/5(p...)` | run N parsers in order, return `Of2/Of3/Of4/Of5` tuple |
| `Prefix(prefix, p)` | run prefix (discard), keep p's result |
| `Suffix(suffix, p)` | keep p's result, then run suffix (discard) |
| `Or(left, right)` | try left; if not consumed, try right |
| `Try(p)` | on failure, clear Consumed (enables backtracking) |
| `Many / Many1` | zero-or-more / one-or-more |
| `SepBy(p, sep)` | p separated by sep |

**Adding a new built-in CRDT:** implement `crdt.CRDT` and add a case in `crdt.New` — no other files change.

**Adding a user-defined composite type:** use the DSL syntax; no Go code needed.

## DSL syntax

```
# built-in collection
collection MyCounter = GCounter(0)

# user-defined composite type
type MyCounterType(x int) = { counter: GCounter(x) }
query MyCounterType.MyValue() = counter.Value()
update MyCounterType.AddOne() = { counter: counter.Add(1) }
collection MyCounter = MyCounterType(0)
```

- `type` — defines a named composite type with typed parameters and named CRDT fields. Parameters are substituted positionally when instantiating.
- `query TypeName.Method()` — maps a method name to a field query (`field.QueryName()`).
- `update TypeName.Method()` — maps a method name to one or more field updates (`{ field: field.Action(args) }`).
- `collection` — instantiates either a built-in CRDT or a user-defined type.
- Merging for composite types is automatic: each field merges independently via its own CRDT's `Merge`.
- Blank and unrecognized lines are silently skipped. Malformed `collection`/`type`/`query`/`update` lines (keyword matched but syntax invalid) return a `ParseError`.
