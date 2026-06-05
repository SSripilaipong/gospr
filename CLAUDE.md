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

- `POST /deploy` → DSL parsed → builder validates and constructs factories → node initializes and propagates `deployMsg` to peers
- Every ~2s each node gossips a snapshot to one random peer; peer merges via per-key max
- Node lifecycle: `Uninitialized → Initialized` (one-way, idempotent)

## Layer responsibilities

```
parser  →  builder  →  node / crdt
```

| Layer | Owns |
|---|---|
| `parser` | Syntax: text → `Plan` (flat AST slices) |
| `builder` | Semantics: validates types/args, combines TypeDef+QueryDef+UpdateDef, parses string args to typed values, returns `BuiltPlan` with serializable `CollectionSpec` values (`GCounterSpec`, `CompositeSpec`) — no closures stored |
| `crdt` | Runtime CRDT logic only — no string parsing, no Plan knowledge |
| `node` | Lifecycle + message loop; calls factories from `BuiltPlan` |
| `gateway` | HTTP; returns 400 on parse/build errors before touching node state |

## File map

```
main.go               wires 3 nodes + gateways, connects peer inboxes

builder/
  builder.go          Build(Plan) → BuiltPlan; CollectionSpec interface + GCounterSpec/CompositeSpec (serializable, no closures)
  builder_test.go

crdt/
  crdt.go             CRDT interface only
  gcounter.go         GCounter — NewGCounter(nodeID, initial int64); Value() = initial + sum(counts)
  composite.go        CompositeCRDT — pre-indexed queryIndex/updateIndex maps; NewComposite takes field factories

node/
  node.go             node lifecycle and message loop; Initialize(BuiltPlan), PropagatePlan(BuiltPlan)

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

- **New built-in CRDT:** implement `crdt.CRDT`, export a typed constructor (e.g. `NewFoo(...)`), add a case in `builder.buildPrimitive` — no other files change.
- **New user-defined type:** use the DSL; no Go code needed.
- **New validation rule:** add it in `builder.Build` or `buildComposite`/`buildPrimitive`.
- **Network propagation of `deployMsg`:** `BuiltPlan` contains only data structs. Use `gob.Register(GCounterSpec{})` / `gob.Register(CompositeSpec{})` for gob, or add a type-discriminator JSON marshaler on `CollectionSpec`.

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
