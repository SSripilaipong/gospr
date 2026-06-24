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
go run . server local                       # one node, gateway on :9050
go run . server local --nodes=3 --port=9050 # 3 nodes, gateways on :9050/:9051/:9052
go run . sandbox run --nodes=5 --port=9060  # observable web UI: watch gossip, partition links, deploy/query/invoke
go run . check file.gos                      # validate a .gos file (parse+typecheck+prove), no server
go test -timeout 60s ./...        # always use -timeout; combinators can loop infinitely
go vet ./...

cd sandbox/web && npm install && npm run build  # rebuild the sandbox SPA; dist/ is //go:embed-ed and committed so `go build` needs no Node
```

**Prerequisite:** `z3` must be on `PATH` (`z3 --version`). The builder proves
CRDT convergence via the SMT solver on every deploy, so a build that defines a
type fails without it — including `go test` for `builder`/`e2e`/`prover`. Install
from <https://github.com/Z3Prover/z3> or `pip install z3-solver`.

## Purpose & scope

A toy distributed CRDT engine driven by a small functional DSL. A type is a
**vector** (distributed-systems vector clock: `nodeID -> value`). Merge, query,
and update behaviors are written as Haskell-style functional expressions. MVP
Slots hold **exact rational** numbers (`math/big.Rat`) typed by a small **numeric
subtype lattice** (six types: `rat, rat0+, rat0-, int, int0+, int0-` — domain
{rat,int} × sign {any,≥0,≤0}; see the `numtype` package). Value types are
`numeric` (carrying a `NumType`), `bool` (from comparisons), and `string` (from
string literals). Built-in operators accept *any* numeric operand and each
computes the *tightest sound* result type (`int + int0+ → int`, `rat0+ - rat0+ →
rat`); a `vector rat0+` counter thus rejects `local (- k)` at build time and a
negative `Add` at runtime. Users define functions (`fn add a::rat b::rat = + a b`,
or multi-line **guarded** `fn grade x::rat | (> x 90) = "A" | otherwise = "F"`)
over a small applicative
core (variables, literals, function references, application). `reduce`/`zip`/`local`
remain combinator keywords carrying a function-valued term; `reduce` may also
appear as a value sub-expression inside a **query** body (e.g. `myScore (reduce max 0)`),
so a query can return a `bool`/`string`. Function params are **concrete** numeric
types (numeric-generic params are out of scope); return types are **inferred**.
Recursion is allowed only when a concrete branch anchors its return type
(unanchored recursion is a build error). On top of type-checking, the builder
**proves convergence** (CvRDT / strong eventual consistency) for every type via
the `prover` package: the merge fn must be a join-semilattice (commutative,
associative, idempotent) and every update must be inflationary in merge's
induced order (`merge(x, h(x)) = h(x)`). Both decompose to scalar SMT obligations
discharged by Z3; an unprovable merge/update pair is rejected at build time. This
is **not** a merge-monotonicity-only heuristic — it is the full SEC obligation
pair. The proof is **faithful to the runtime**: the runtime computes in exact
rationals (`big.Rat`), and Z3's `Real` sort over-approximates ℚ (ℚ ⊆ ℝ), so a
universally-quantified obligation proved over ℝ holds for every rational — there
is no float-rounding gap (there is no division primitive, so ℚ stays closed; ints
are exact too). Query params and struct vectors (`vector { x rat }`) are
designed-for but not implemented.

## Architecture

Nodes run as goroutine groups and communicate only via `chan any` — no shared memory, no direct cross-node calls. The send side is the `node.Peer` interface (`Send(msg any)`); production wraps each inbox in `node.ChanPeer` (a blocking channel send). This is the single interception seam: the `sandbox` package supplies its own `Peer` to observe/delay/drop messages **without touching production node code** (the sandbox is designed to be liftable into its own repo). The CLI (`gospr server local`) spins up N nodes (default 1, `--nodes` to scale), wiring each as the others' peer; one node is a degenerate cluster (no peers, gossip no-ops).

- `POST /api/cluster/deploy` → DSL parsed → builder validates, type-checks, **and proves convergence** (via `prover`/Z3) → builds per-type `Model`s → node initializes and propagates `deployMsg` to peers
- Every ~2s each node gossips a snapshot to one random peer; the peer merges each collection via that type's user-defined `merge` expr (e.g. `zip max` → elementwise max over the union of node slots)
- Node lifecycle: `Uninitialized → Initialized` (one-way, idempotent). Because it is one-way, the gateway rejects a deploy that yields zero collections.

## Layer responsibilities

```
parser  →  builder → prover  →  node / crdt
```

| Layer | Owns |
|---|---|
| `parser` | Syntax: text → `Plan` (flat AST slices, incl. `FnDef`s). Bodies are an applicative `Expr` sum type (`NumLit`/`StrLit`/`Name`/`App` + `Guards` + `Reduce`/`Zip`/`Local` combinators). A guarded `fn` body is `ExprGuards{Cases}`; `otherwise` is a `GuardCase.Otherwise` marker, not an expression. Leaves are emitted as **unresolved** `Name`s — the parser knows no scope. |
| `builder` | Semantics: folds flat `TypeDef`+`FnDef`+`MergeDef`+`QueryDef`+`UpdateDef` into validated `*Model`s + a global `Functions` table, **resolves** every `Name` → `Var`/`Ref`, and **type-checks** every term (a small `checker`: `typeOf` over numeric (carrying `numtype.NumType`)/`bool`/`string`, **assignability via `numtype.Sub`** instead of equality, per-operator result rules, combinator boundary checks `Sub(result, elemType)`, memoized DFS return-type inference). Rejects duplicate/primitive-shadowing/zero-param fns, duplicate params, unknown identifiers, arity/type mismatches, results not assignable to the element type, guards without a final `otherwise` (or an `otherwise` not last), `reduce` outside a query, unanchored recursion, unknown param types, query params, missing merge. `Model` implements `CollectionSpec.New`; queries carry an inferred `Result` `ValType` + `ResultNum` `NumType`. |
| `prover` | Proves CvRDT convergence for each `*Model` at the end of `Build` (imports `parser`/`numtype`/`crdt`, never `builder`). Lowers the merge fn and each update fn to a symbolic IR (`sym`, mirroring crdt's eval/apply with user-fn inlining + recursion rejection), builds scalar obligations — merge comm/assoc/idempotence + per-update `merge(x,h(x))=h(x)` — and discharges each by **negation** through Z3 (`z3 -smt2 -in`). Every var is declared `Real` with `is_int`/sign constraints from its own `NumType` (slot from the element type, update params from their declared types), so mixed Int/Rat arithmetic stays well-sorted. `Real` over-approximates the ℚ runtime soundly (unsat over ℝ ⇒ no rational counterexample); rational literals serialize as `(/ p.0 q.0)`. No pure-Go fast path → `z3` is mandatory. |
| `crdt` | Runtime only — no string parsing, no `Plan` knowledge. `VectorCRDT` evaluates resolved `Expr` trees against `map[string]*big.Rat` (exact rationals) via a small applicative interpreter (tagged `rtVal` = num/str/bool/func, `[]rtVal` calling convention, partial application, recursion-depth guard). Arithmetic always allocates fresh `big.Rat`s; the **Snapshot/Merge boundary deep-clones** (`cloneRat`) because `big.Rat` is mutable, so no value is aliased across CRDTs. `Query` evals the body to an `rtVal` (a `reduce` sub-node folds `v.state` under the lock) and converts via `rtToAny` (numbers → exact `RatString`); `Merge` is atomic (build-copy-then-swap). |
| `node` | Lifecycle + message loop; calls `Spec.New` from `BuiltPlan.Collections`. Sends go through the `Peer` interface (`Send`), the one interception seam — prod uses `ChanPeer`; graceful `Stop()` lets a cluster be torn down without leaking goroutines. |
| `gateway` | HTTP; returns 400 on parse/build errors and zero-collection deploys before touching node state; regenerates Swagger on deploy. |
| `sandbox` | Observable web playground (`gospr sandbox run`): a swappable `Cluster` of nodes wired with intercepting `Peer`s that drop (partition) / delay / observe (SSE) messages, an HTTP+SPA server, and atomic Reset. Imports only `parser`/`builder`/`node`/`crdt` — built to lift into its own repo. |

## File map

```
main.go               CLI entry (urfave/cli/v3): `server local` (--nodes/--port, wires N nodes+gateways+peers), `sandbox run` (--nodes/--port → sandbox.Run), and `check <file.gos>` (parse→Build, no server)

numtype/
  numtype.go          leaf pkg (imports only math/big): NumType{Domain,Sign}, the six names, Parse/String/Sub/Join/Allows(*big.Rat). Zero value = top type `rat`; internal `Zero` sign types the literal 0
  numtype_test.go

builder/
  builder.go          Build(Plan) → BuiltPlan{Models, Functions, Collections}; Model (+ElemNum) + env.resolve (Name→Var/Ref) + arityOf; checker (vtype{kind,num}/sig, primitiveArity + per-op rules addSign/mulSign/negate/max/min + numBin, applyArgs/resultOf, subVtype, typeOf/typeOfGuards/typeOfReduce fixpoint, inferReturn, unify via Join) + validators; final per-model `prover.Prove` convergence gate
  builder_test.go     hard-coded-AST integration test + error/duplicate/fn/arity/type + numeric-subtype + convergence-rejection cases

prover/
  prover.go           Prove(elem, merge, updates, funcs); sym IR + lower (eval/refFn/evalApp/evalGuards, user-fn inlining + recursion guard); merge-law + per-update inflationary obligation builders
  smt.go              sym → SMT-LIB (single Real sort over-approximating ℚ, is_int/sign asserts, fmtRat→(/ p.0 q.0), max/min→ite, ==/ /= → =/distinct); checkGoal/runZ3 via os/exec `z3 -smt2 -in`, unsat=proven; lookPath/z3Binary seams
  prover_test.go      z3-backed accept/reject (max/min join, sum/avg rejected, inflationary, mixed-domain, recursion) + z3-missing seam

crdt/
  crdt.go             CRDT interface: Apply/Query/Merge/Snapshot
  vector.go           Method (with Result ValType + ResultNum), Function, VectorCRDT (state map[string]*big.Rat), NewVector, cloneRat (deep-clone at Snapshot/Merge); tagged rtVal interpreter (eval/evalFn/apply over *big.Rat), primOp/arith/cmp primitives, rtToAny (num→RatString), maxEvalDepth guard; bindParams validates via numtype.Allows; toRat
  vector_test.go

node/
  node.go             lifecycle + message loop; Initialize(BuiltPlan), PropagatePlan(BuiltPlan); Peer interface + ChanPeer (interception seam), MessageKind, Initialized(), graceful Stop() (quit chan + select in both loops; prod calls it never, sandbox calls it on Reset)
  node_test.go        Stop() halts gossip + is idempotent; MessageKind

sandbox/             observe/interact playground; imports only parser/builder/node/crdt — designed to lift into its own repo. `gospr sandbox run`
  sandbox.go          Server (stable Network+Hub, swappable *Cluster behind RWMutex), Run; locking contract: withCluster=RLock for reads/Apply, withClusterW=Lock for deploy (pin check+set), Parse/Build (z3) run OUTSIDE the lock; reset() = Lock→swap+net.Reconnect()→Unlock→old.stop(). GOTCHA: Reconnect MUST be inside the same write lock as the swap, else a deploy can land in the gap and propagate through stale partitions then pin (lock order is always s.mu→net, so no deadlock)
  cluster.go          Cluster (swappable topology): newCluster wires a full mesh of interceptingPeer + short gossip interval; stop() closes done + Stops every node; pinned deployed *BuiltPlan
  network.go          Network: per-pair partition (order-independent "a|b" key) + global delay, both RWMutex-guarded; survives Reset (delay kept, links reconnected)
  hub.go              SSE Event fan-out; Emit is non-blocking (buffered per-sub chan + select/default drop) so a stuck client never stalls the gossip/deploy send path
  peer.go             interceptingPeer (node.Peer): drops on partition, else emits inflight + delivers after delay in a goroutine guarded by the cluster's done chan (timer+select so Reset aborts in-flight sends promptly)
  server.go           HTTP+SPA: GET state/events(SSE), POST deploy(pin-the-plan, 409 on 2nd, zero-collection guard)/reset/links/speed, POST/GET nodes/{id}/collections/{collection}/{action|query} (string-only params); //go:embed web/dist
  *_test.go           network/peer(done-abort)/cluster(reset + reset-reconnect-atomic-with-deploy race) unit tests
  web/                Vite+TS+Web Components SPA (no framework). src/components/{sandbox-app(poll /state ~750ms; /events SSE used only for Reset),node-graph(SVG circle; partition by clicking links; node blinks on state change),node-panel(deploy / one input per declared param),network-controls(typed delay + Reset)}; dist/ committed + embedded
                      Conventions: (1) each component SIGNATURE-GUARDS render — setData hashes ONLY the fields it displays and skips re-render when unchanged, so the 750ms poll/gossip churn never wipes focus or in-progress textbox text; when a component starts displaying new data, ADD it to that signature. node-panel also keeps a `draft` of the deploy code. (2) NO message-flight animation (removed — it didn't reflect the real per-message rate). Instead node-graph blinks a node (.node-circle.blink, a brightness() pulse) when that node's slot signature changes between polls — so visible feedback is poll-driven (~750ms granularity), not SSE per-message. The SSE /events stream now only carries Reset. (3) Links are partitioned by clicking the graph (no separate buttons): a wide transparent `.link-hit` line per pair is keyboard-operable (tabindex/role=button/aria-label/Enter-Space) inside an svg role=group (NOT role=img, which would hide the controls); the visible `.link` has pointer-events:none. Node slot values render as `[v1 v2 …]` (bare values, no nodeID keys — user preference). (4) .graph-wrap is capped max-width:600px+centered so it doesn't balloon on wide screens. All animation honors prefers-reduced-motion.

swagger/
  swagger.go          Generate(BuiltPlan) → OpenAPI 3.0 JSON; type-switches on *builder.Model; numSchema → string (numbers are exact-rational strings on the wire) with the NumType named in the description
  swagger_test.go

gateway/
  gateway.go          HTTP: POST /api/cluster/deploy, POST /api/collections/{collection}/{action},
                      GET /api/collections/{collection}/{query}?params=... (passed as exact-rational strings),
                      GET /api/swagger.json, GET /api/docs (Swagger UI)

parser/
  types.go            AST: ElemType (Scalar = numeric type name), Expr sum type (ExprKind incl. StrLit/Guards), GuardCase, ValType, RefKind, TypeDef/FnDef/MergeDef/QueryDef/UpdateDef/CollectionSpec, Plan
  parser.go           public Parse entry point
  stream.go           value-typed Stream — backtracking is free
  result.go           ParseResult[A], Parser[A], Of2–Of5 tuples
  combinators.go      all combinators
  dsl.go              DSL grammar (line parsers incl. guarded fnLineP/guardLineP + numberP/stringLitP/symOpP/numTypeNameP/paramP/reduceFormP + exprP/atomP/nameP/applicationP)
  parser_test.go      canonical-snippet integration test + cases

e2e/
  e2e_test.go         model-level: string → Parse → Build → Model.New → Add/Value/merge behaviors
  cluster_http_test.go blackbox: real 3-node cluster (nodes+gossip+httptest gateways), driven ONLY via HTTP; deploy→Add on one node→poll until value gossips to another (node.WithGossipInterval for speed, require.Eventually for convergence)
```

## DSL syntax

```
type T = vector rat0+           # scalar numeric: rat|rat0+|rat0-|int|int0+|int0- ; `vector { x rat }` struct form is deferred
fn lub a::rat b::rat = max a b  # user-defined fn; body is a full applicative expression
fn grade x::rat                  # guarded fn: multi-line, value-typed branches
| (> x 90) = "A"                #   each cond is a bool; results share one type (numeric/bool/string)
| otherwise = "F"               #   `otherwise` is mandatory and must be the last case (build-checked)
merge T = zip lub               # zip: apply a numeric,numeric->numeric fn elementwise per node slot
query T.Grade = grade (reduce max 0)  # query body is a general expr; reduce folds the slots to a numeric
update T.Add k::rat0+ = local (+ k)   # local: apply a unary fn to ONLY the calling node's slot
collection MyVec = T            # named runtime instance of a type (no args)
```

- **Expressions** are prefix application: `f a b`, `+ a (max b c)`. A bare `(op arg)` like `(+ k)` is a **partial application**, not a special "section" form. Application may under-saturate (partial) but never over-saturate (build error).
  - Partial application binds the **leftmost** argument first, so `(- k)` is `\x -> - k x` = `k - x` (the combinator supplies the slot `x` as the *next* arg). This is the one uniform rule — there is no right-section. For non-commutative ops where you want the slot as the left operand (`x - k`), define a helper with that param order: `fn rsub k::rat x::rat = - x k` then `local (rsub k)`. (`+ * max min` are commutative, so unaffected.)
- **`fn`**: top-level, global, at least one numeric param (zero-arg fns rejected); body must be saturated. Return type (numeric/`bool`/`string`) is **inferred**. May reference other fns / itself — recursion is allowed only when a concrete branch anchors the return type (unanchored recursion is a build error); runtime is bounded by `maxEvalDepth`.
- **Guarded `fn`**: `| cond = result` lines, `cond` a `bool`, all `result`s the same type (numeric branches join to a common numeric supertype). The final case must be `otherwise` (enforced at build time, so matches are total). A guarded body is `ExprGuards`; `reduce` is not allowed inside any `fn` (functions stay pure).
- **Numeric types & operators**: six types `rat, rat0+, rat0-, int, int0+, int0-` (domain {rat,int} × sign {any,≥0,≤0}); see `numtype`. `rat` is exact rational (`big.Rat`) at runtime. Operators take **any** numeric operand; each computes the tightest sound result (`+`/`-` via `addSign`, `*` via `mulSign`, `max`/`min` by bound analysis — so `rat0+ - rat0+ → rat`). **Assignability** (`numtype.Sub`, not equality) governs args & the combinator boundary, so `int0+` flows where `rat0+` is wanted. The literal `0` has an internal `Zero` sign, assignable to any numeric type. Comparisons `> < >= <= == /=` are numeric,numeric->`bool`. Strings are `"..."` (`\" \\ \n \t` escapes). The builder rejects type mismatches (`+ (> x 1) 2`) and results not assignable to the element type.
- **Combinator slots** (`zip`/`reduce`/`local`) take a single **atom** (a name or a parenthesised term), not a bare application — this keeps `reduce + 0` unambiguous (`+` is the fn, `0` the init). The fn is applied to element-typed args and its result must be `Sub` the element type: zip → (E,E)->Sub E; local → (E)->Sub E. `reduce` is also a value atom inside a **query** body (`f (reduce max 0)`); its result type is the lattice **fixpoint** of folding the fn over (acc, E) from the init's type.
- Primitives: `+ * -` and comparisons are operator tokens; `max`/`min` are ordinary identifiers (so `maxValue` is one name) recognised as primitives by the builder. A `fn` may not shadow a primitive.
- Params: `name::type` where `type` is one of the six numeric names, names unique. Values are validated at runtime against the param's type (`numtype.Allows`). Update params work; query params parse but are rejected at build (future feature).
- **Wire form**: numbers cross the HTTP/JSON boundary as **exact-rational strings** (`"5"`, `"1/2"`, input `"0.1"` → `1/10`) — never JSON numbers — so nothing is lost to float at I/O. DSL numeric literals are likewise parsed exact (`parser.Expr.Num` is `*big.Rat`). Swagger types numeric fields as `string`.
- A collection name = the node's collection key; a `type` defines reusable behavior, instantiated by `collection`.

## Extension points

- **New operator:** add its arity to `primitiveArity` in `builder/builder.go` and a result rule (a `numBin` case for arithmetic, or `cmpOps` membership for a bool result), add a case in `primOp` in `crdt/vector.go` (`arith` / `cmp`), add a `serialize` case in `prover/smt.go` so the convergence proof can reason about it (and `cmpOps` membership in `prover/prover.go` for a bool op), and (for a punctuation operator) a `Try(StringP(...))` alternative in `symOpP` (`parser/dsl.go`, multi-char before single-char); word-shaped operators need no parser change (they parse as identifiers).
- **New numeric type:** add the name + `NumType` to `numtype.Parse`/`String` and adjust `Sub`/`Join`/`Allows`; add it to `numTypeNameP` in `parser/dsl.go` (longest-match order). The builder/crdt/swagger consume `numtype` generically, so usually need no change.
- **New value type** (non-numeric): add a `parser.ValType` + builder `vkind`, handle it in `checker.typeOf`/`subVtype`/`unify`, add an `rtKind` + constructor in `crdt/vector.go` and a `rtToAny`/`valTypeSchema` case.
- **New expression form** (e.g. query params, struct vectors): add/realize an `ExprKind`/`ElemKind` in `parser/types.go`, parse it in `dsl.go`, resolve it in `builder.env.resolve` and type-check it in `checker.typeOf`, evaluate it in `crdt/vector.go` (`eval`).
- **New expression form & the prover:** any new `ExprKind` reachable from a merge/update fn body also needs an `eval` case in `prover/prover.go` (lower it to a `sym`), else the convergence proof errors out. Struct vectors are the planned next step: per-field independent merge is a product lattice (prove each field's scalar fn separately); cross-field/LWW-style merges need a joint obligation — both still reduce to QF SMT.
- **Network propagation of `deployMsg`:** `BuiltPlan` is data-only; gob/JSON-encode `Model` (it holds `parser.Expr` trees, no closures). `Expr.Num` is `*big.Rat`, which has Gob/Text marshalers — propagation is in-process today (channels), so this only matters once it goes over the wire.

## Testing conventions

Tests use `github.com/stretchr/testify` (`assert` + `require`):
- `require.*` — fatal (stops test immediately); use for error checks and preconditions where subsequent code would panic or be meaningless
- `assert.*` — non-fatal (continues test); use for terminal value comparisons
- Typical pattern: `require.NoError(t, err)` for pipeline steps, `assert.Equal(t, want, got)` for value checks

## Gotchas / conventions

- **Committed-choice:** once a line's keyword prefix is consumed, `Or` commits — a recognized-but-malformed line (e.g. `type T = vector foo`) is a **parse error**, not skipped. `Try` strips `Consumed` on failure to re-enable backtracking; every `symOpP` alternative is `Try`-wrapped, as is each trailing-atom attempt in `applicationP`.
- **Name resolution invariant:** `ExprName` appears ONLY in parser output; after `Build`, every leaf is an `ExprVar` (bound param), `ExprRef` (symbol, with `Arity`/`RefKind`), or a literal (`ExprNumLit`/`ExprStrLit`). Proof/optimization passes can rely on this — no unresolved leaves in a `*Model` or `Functions` entry.
- **Totality of guards:** the builder requires a guarded `fn` to end with `otherwise` (a `GuardCase.Otherwise` marker — an always-true `Cond` does NOT count), so the runtime's "non-exhaustive guards" error is unreachable. `otherwise` may appear only as the last case.
- **`reduce` reads state, so it's query-scoped:** `reduce` evaluates by folding `v.state`, hence its eval requires `v.mu` held (Query holds it). The builder forbids `reduce` in `fn`/merge/update bodies so those stay pure value-functions.
- **Return-type inference (Option A):** `checker.inferReturn` is memoized DFS; a recursive call caught mid-inference yields `vUnknown`, which unifies with any concrete type. If a function's type stays `vUnknown` (no concrete branch), Build rejects it.
- `exprP` is recursive (parenthesised atoms hold expressions); the body is deferred to parse time (`exprP` returns a thunk) to break the parser-construction cycle.
- `Model.Queries`/`Updates` are always non-nil maps (init in `Build`) so swagger/crdt can range/lookup safely; `Model.Funcs`/`BuiltPlan.Functions` is the shared global function env.
- A local update on an absent slot defaults to 0 (Go zero map value). Merge adopts a remote slot if absent locally (zip over the union of node IDs) and is **atomic** — a failing user fn leaves state unchanged.
- Query and update may share a name (separate namespaces, dispatched by HTTP verb). Duplicate names *within* type/fn/merge/query/update/collection are build errors.
- `Expr` is a single struct with a `Kind` tag and union-ish fields; only the fields relevant to `Kind` are set (pointers for nested exprs). Keep it serializable — no closures, so optimization/proof passes stay possible.
