# CLAUDE.md

<!-- KEEP-SECTION: "About this file" is permanent project guidance. Update its
     wording if it improves, but never delete the section. -->
## About this file (keep this section)

CLAUDE.md is the orientation file a future session reads *instead of* re-scanning
the whole project. It earns its place only if it stays accurate and skimmable.
Maintain it by these rules:

- **Record durable, project-specific context** that aids orientation or implementation
  decisions: purpose & MVP scope, entry points and where logic lives, each package's
  responsibility, build/test/run commands, settled architecture decisions, conventions,
  non-obvious details, gotchas, and current TODOs.
- **Do not record** session history, every file changed, facts obvious from a filename,
  vague summaries, or anything a future session can infer by reading one or two files.
- **Prefer updating an existing note over appending.** When the code changes, edit the
  affected lines — stale guidance is worse than none. Don't grow a changelog.
- **Stay concise and concrete.** Point to the file/function that owns a behavior rather
  than restating its code. Tables and short bullets over prose.
- **The file is descriptive, not a diary.** Anyone should be able to start work from it
  without scrolling history.

## Commands

```bash
go build ./...
go run .                          # start all three nodes (ports 8081/8082/8083)
go test -timeout 10s ./...        # always use -timeout; combinators can loop infinitely
go vet ./...
```

## Purpose & scope

A toy distributed CRDT engine driven by a small functional DSL. A type is a
**vector** (distributed-systems vector clock: `nodeID -> value`). Merge, query,
and update behaviors are written as Haskell-style functional expressions. MVP
supports only the `real` primitive. Users can define their own functions
(`fn add a::real b::real = + a b`) over a small applicative core (variables,
literals, function references, application); `reduce`/`zip`/`local` remain
combinator keywords carrying a function-valued term. Recursion is allowed (guarded
at runtime). Query params and struct vectors (`vector { x real }`) are designed-for
but not implemented.

## Architecture

Three nodes run as goroutine groups and communicate only via `chan any` — no shared memory, no direct cross-node calls.

- `POST /api/cluster/deploy` → DSL parsed → builder validates and builds per-type `Model`s → node initializes and propagates `deployMsg` to peers
- Every ~2s each node gossips a snapshot to one random peer; the peer merges each collection via that type's user-defined `merge` expr (e.g. `zip max` → elementwise max over the union of node slots)
- Node lifecycle: `Uninitialized → Initialized` (one-way, idempotent). Because it is one-way, the gateway rejects a deploy that yields zero collections.

## Layer responsibilities

```
parser  →  builder  →  node / crdt
```

| Layer | Owns |
|---|---|
| `parser` | Syntax: text → `Plan` (flat AST slices, incl. `FnDef`s). Bodies are an applicative `Expr` sum type (`NumLit`/`Name`/`App` + `Reduce`/`Zip`/`Local` combinators). Leaves are emitted as **unresolved** `Name`s — the parser knows no scope. |
| `builder` | Semantics: folds flat `TypeDef`+`FnDef`+`MergeDef`+`QueryDef`+`UpdateDef` into validated `*Model`s + a global `Functions` table, and **resolves** every `Name` → `Var` (bound param) or `Ref` (primitive/user fn, with arity). Rejects duplicate/primitive-shadowing/zero-param fns, duplicate params, unknown identifiers, arity mismatches (over-application, wrong combinator arity, unsaturated fn bodies), non-`real` params, query params, missing merge. Recursion is allowed. `Model` implements `CollectionSpec.New`. |
| `crdt` | Runtime only — no string parsing, no `Plan` knowledge. `VectorCRDT` evaluates resolved `Expr` trees against `map[string]float64` via a small applicative interpreter (`rtVal`, partial application, recursion-depth guard); `Merge` is atomic (build-copy-then-swap). |
| `node` | Lifecycle + message loop; calls `Spec.New` from `BuiltPlan.Collections`. |
| `gateway` | HTTP; returns 400 on parse/build errors and zero-collection deploys before touching node state; regenerates Swagger on deploy. |

## File map

```
main.go               wires 3 nodes + gateways, connects peer inboxes

builder/
  builder.go          Build(Plan) → BuiltPlan{Models, Functions, Collections}; Model + env.resolve (Name→Var/Ref) + arityOf + validators
  builder_test.go     hard-coded-AST integration test + error/duplicate/fn/arity cases

crdt/
  crdt.go             CRDT interface: Apply/Query/Merge/Snapshot
  vector.go           Method, Function, VectorCRDT, NewVector; rtVal interpreter (eval/evalFn/apply), binFn primitives, maxEvalDepth guard; toFloat64
  vector_test.go

node/
  node.go             lifecycle + message loop; Initialize(BuiltPlan), PropagatePlan(BuiltPlan)

swagger/
  swagger.go          Generate(BuiltPlan) → OpenAPI 3.0 JSON; type-switches on *builder.Model; `real`→number schema
  swagger_test.go

gateway/
  gateway.go          HTTP: POST /api/cluster/deploy, POST /api/collections/{collection}/{action},
                      GET /api/collections/{collection}/{query}?params=... (parsed as float64),
                      GET /api/swagger.json, GET /api/docs (Swagger UI)

parser/
  types.go            AST: ElemType, Expr sum type (ExprKind), RefKind, TypeDef/FnDef/MergeDef/QueryDef/UpdateDef/CollectionSpec, Plan
  parser.go           public Parse entry point
  stream.go           value-typed Stream — backtracking is free
  result.go           ParseResult[A], Parser[A], Of2–Of5 tuples
  combinators.go      all combinators
  dsl.go              DSL grammar (line parsers + numberP/symOpP/paramP + exprP/atomP/nameP/applicationP)
  parser_test.go      canonical-snippet integration test + cases

e2e/
  e2e_test.go         string → Parse → Build → Model.New → Add/Value/merge behaviors
```

## DSL syntax

```
type T = vector real            # only `vector real`; `vector { x real }` struct form is deferred (not parsed)
fn lub a::real b::real = max a b # user-defined fn; body is a full applicative expression
merge T = zip lub               # zip: apply a real->real->real fn elementwise per node slot
query T.Value = reduce + 0       # reduce: fold all slots with fn (+) and init (0); empty vector → init
update T.Add k::real = local (+ k)  # local: apply a unary fn (real->real) to ONLY the calling node's slot
collection MyVec = T            # named runtime instance of a type (no args)
```

- **Expressions** are prefix application: `f a b`, `+ a (max b c)`. A bare `(op arg)` like `(+ k)` is a **partial application**, not a special "section" form. Application may under-saturate (partial) but never over-saturate (build error).
  - Partial application binds the **leftmost** argument first, so `(- k)` is `\x -> - k x` = `k - x` (the combinator supplies the slot `x` as the *next* arg). This is the one uniform rule — there is no right-section. For non-commutative ops where you want the slot as the left operand (`x - k`), define a helper with that param order: `fn rsub k::real x::real = - x k` then `local (rsub k)`. (`+ * max min` are commutative, so unaffected.)
- **`fn`**: top-level, global, at least one `real` param (zero-arg fns rejected); body must be saturated (return `real`). May reference other fns / itself — **recursion is allowed**, bounded at runtime by `maxEvalDepth`.
- Primitives: `+ * - max min`, each `real->real->real` (arity 2). `+ * -` are operator tokens; `max`/`min` are ordinary identifiers (so `maxValue` is one name) recognised as primitives by the builder. A `fn` may not shadow a primitive.
- **Combinator slots** (`zip`/`reduce`/`local`) take a single **atom** (a name or a parenthesised term), not a bare application — this keeps `reduce + 0` unambiguous (`+` is the fn, `0` the init). Their function's arity is checked: zip/reduce → 2, local → 1.
- Params: `name::type`, only `type == real`, names unique. Update params work; query params parse but are rejected at build (future feature).
- A collection name = the node's collection key; a `type` defines reusable behavior, instantiated by `collection`.

## Extension points

- **New operator:** add to `primitiveArity` in `builder/builder.go`, add a case in `binFn` in `crdt/vector.go`, and (for a punctuation operator) a `Try(StringP(...))` alternative in `symOpP` (`parser/dsl.go`); word-shaped operators need no parser change (they parse as identifiers).
- **New param type:** relax the `real`-only check in `builder.validateParams`, handle it in `crdt.bindParams`/`toFloat64`, add cases in `swagger.paramToSchema`/`paramExample`.
- **New expression form** (e.g. query params, struct vectors): add/realize an `ExprKind`/`ElemKind` in `parser/types.go`, parse it in `dsl.go`, resolve+validate it in `builder` (`env.resolve`/`resolveCombinator`), evaluate it in `crdt/vector.go` (`eval`).
- **Network propagation of `deployMsg`:** `BuiltPlan` is data-only; gob/JSON-encode `Model` (it holds `parser.Expr` trees, no closures).

## Testing conventions

Tests use `github.com/stretchr/testify` (`assert` + `require`):
- `require.*` — fatal (stops test immediately); use for error checks and preconditions where subsequent code would panic or be meaningless
- `assert.*` — non-fatal (continues test); use for terminal value comparisons
- Typical pattern: `require.NoError(t, err)` for pipeline steps, `assert.Equal(t, want, got)` for value checks

## Gotchas / conventions

- **Committed-choice:** once a line's keyword prefix is consumed, `Or` commits — a recognized-but-malformed line (e.g. `type T = vector foo`) is a **parse error**, not skipped. `Try` strips `Consumed` on failure to re-enable backtracking; every `symOpP` alternative is `Try`-wrapped, as is each trailing-atom attempt in `applicationP`.
- **Name resolution invariant:** `ExprName` appears ONLY in parser output; after `Build`, every leaf is an `ExprVar` (bound param) or `ExprRef` (symbol, with `Arity`/`RefKind`). Proof/optimization passes can rely on this — no unresolved leaves in a `*Model` or `Functions` entry.
- `exprP` is recursive (parenthesised atoms hold expressions); the body is deferred to parse time (`exprP` returns a thunk) to break the parser-construction cycle.
- `Model.Queries`/`Updates` are always non-nil maps (init in `Build`) so swagger/crdt can range/lookup safely; `Model.Funcs`/`BuiltPlan.Functions` is the shared global function env.
- A local update on an absent slot defaults to 0 (Go zero map value). Merge adopts a remote slot if absent locally (zip over the union of node IDs) and is **atomic** — a failing user fn leaves state unchanged.
- Query and update may share a name (separate namespaces, dispatched by HTTP verb). Duplicate names *within* type/fn/merge/query/update/collection are build errors.
- `Expr` is a single struct with a `Kind` tag and union-ish fields; only the fields relevant to `Kind` are set (pointers for nested exprs). Keep it serializable — no closures, so optimization/proof passes stay possible.
