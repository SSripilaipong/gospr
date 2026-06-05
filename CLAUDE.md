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

- `POST /api/cluster/deploy` → DSL parsed → builder validates and constructs factories → node initializes and propagates `deployMsg` to peers
- Every ~2s each node gossips a snapshot to one random peer; peer merges via per-key max
- Node lifecycle: `Uninitialized → Initialized` (one-way, idempotent)

## Layer responsibilities

```
parser  →  builder  →  node / crdt
```

| Layer | Owns |
|---|---|
| `parser` | Syntax: text → `Plan` (flat AST slices) |
| `builder` | Semantics: validates types/args, combines TypeDef+QueryDef+UpdateDef, parses string args to typed values, returns `BuiltPlan` with serializable `CollectionSpec` values (`GCounterSpec`, `CompositeSpec`) — no closures stored; validates method param types at deploy time |
| `crdt` | Runtime CRDT logic only — no string parsing, no Plan knowledge; `Query(name string, params []any)`; validates param values at runtime |
| `node` | Lifecycle + message loop; calls factories from `BuiltPlan` |
| `gateway` | HTTP; returns 400 on parse/build errors before touching node state; regenerates Swagger on deploy |

## File map

```
main.go               wires 3 nodes + gateways, connects peer inboxes

builder/
  builder.go          Build(Plan) → BuiltPlan; CollectionSpec interface + GCounterSpec/CompositeSpec (serializable, no closures)
  builder_test.go

crdt/
  crdt.go             CRDT interface (Query takes params []any) + crdt.New factory
  gcounter.go         GCounter — NewGCounter(nodeID, initial float64); stores float64 counts; Add validates >= 0
  composite.go        CompositeCRDT — queries/updates map[string]parser.QuerySpec/UpdateSpec; resolves named params from runtime payload

node/
  node.go             node lifecycle and message loop; Initialize(BuiltPlan), PropagatePlan(BuiltPlan)

swagger/
  swagger.go          Generate(BuiltPlan) → OpenAPI 3.0 JSON; regenerated on each /deploy; param-aware schemas and examples
  swagger_test.go

gateway/
  gateway.go          HTTP: POST /api/cluster/deploy, POST /api/collections/{collection}/{action},
                      GET /api/collections/{collection}/{query}?params=...,
                      GET /api/swagger.json, GET /api/docs (Swagger UI)

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
- **New param type:** add it to `knownParamTypes` in `builder.go`, handle it in `crdt/composite.go:validateParam`, add cases in `swagger/swagger.go:paramToSchema` and `paramExample`.
- **New user-defined type:** use the DSL; no Go code needed.
- **New validation rule:** add it in `builder.Build` or `buildComposite`/`buildPrimitive`.
- **Network propagation of `deployMsg`:** `BuiltPlan` contains only data structs. Use `gob.Register(GCounterSpec{})` / `gob.Register(CompositeSpec{})` for gob, or add a type-discriminator JSON marshaler on `CollectionSpec`.

## DSL syntax

```
collection MyCounter = GCounter(0)

type MyCounterType(x int) = { counter: GCounter(x) }
query MyCounterType.MyValue() = counter.Value()
update MyCounterType.AddOne() = { counter: counter.Add(1) }
update MyCounterType.Up(a real0+) = { counter: counter.Add(a) }   # runtime param
collection MyCounter = MyCounterType(0)
```

### Method param types

| Type | Meaning | Runtime validation |
|---|---|---|
| `int` | integer | none beyond type coercion |
| `real0+` | non-negative real number | value must be >= 0 (float64) |

- `QueryDef` and `UpdateDef` carry `Params []ParamSpec`; `parser.QuerySpec`/`parser.UpdateSpec` bundle params+body.
- `CompositeSpec` stores `Queries map[string]parser.QuerySpec` and `Updates map[string]parser.UpdateSpec` (replaces the old `QueryIndex`/`UpdateIndex` pair).
- Body args that are identifier strings are treated as param references at runtime; digits/floats are literals.
- Build-time: unknown param types and unresolved body args return errors from `buildComposite`.
- `GCounter` is float64 throughout (counts, initial, snapshot). `GCounter.Add` enforces `real0+` semantics (>= 0).

## Parser combinators

`Consumed bool` enables committed-choice: `Or` only retries the right branch if left consumed nothing. `Try` strips `Consumed` on failure to re-enable backtracking.
