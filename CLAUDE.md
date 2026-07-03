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
make rebuild                       # = the npm build above; `make all` does SPA then `go build` (correct order — Go embeds dist/ at compile time)
```

A `Makefile` wraps the common commands (`rebuild`/`web`, `build`, `all`, `check`, `test`,
`vet`, `run`, `sandbox`, `clean`). After editing `sandbox/web/src`, run `make rebuild` then
rebuild/run the Go binary so the new `dist/` is re-embedded (`go run` recompiles each time;
a stale pre-built binary serves the old SPA). Vite (`emptyOutDir`) regenerates `index.html`
with the new hashed bundle and removes old hashes, so index.html never drifts from the JS.

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
{rat,int} × sign {any,≥0,≤0}; see the `numtype` package). A slot may instead hold
a **struct** of named numeric fields (see the struct-vector note below). Value
types are `numeric` (carrying a `NumType`), `bool` (from comparisons), `string`
(from string literals), and `struct` (named fields, from struct literals /
struct-typed params). Built-in operators accept *any* numeric operand and each
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
are exact too). A slot may also hold a **struct** of named numeric fields: a
**named struct type** `type X = { Pos rat0+  Neg rat0+ }` plus a vector of it
`type VX = vector X`. Structs are **first-class values** — struct construction
literals `{ Pos: e, Neg: e }`, dot **field access** `a.Pos`, and struct-typed
`fn` params `a::X` (nesting allowed). The whole-struct `merge` (`zip M`, M:X,X→X)
is a **product / joint lattice** proven by flattening to leaf scalar SMT vars and
discharging the struct equality as the conjunction of per-leaf equalities in one
Z3 call. A query may return a scalar OR a whole struct (`reduce` folds struct
slots with a struct-literal init, then may project a field). HTTP method params
stay scalar (update params are scalar-numeric; struct params ride only through
`fn`s and literals). Query params remain designed-for but not implemented.

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
| `parser` | Syntax: text → `Plan` (flat AST slices, incl. `FnDef`s). Bodies are an applicative `Expr` sum type (`NumLit`/`StrLit`/`Name`/`App` + `Guards` + `StructLit`/`Field` + `Reduce`/`Zip`/`Local` combinators). A `TypeDef.Elem` is `KindStruct` (`Fields`) or `KindVector` (element token in `Elem`). Struct type/literal grammars are newline-tolerant inside `{}` (`wsP`/`ws1P`); a type-position token is `typeNameP` (ident + optional `+`/`-`, word-bounded). A guarded `fn` body is `ExprGuards{Cases}`; `otherwise` is a `GuardCase.Otherwise` marker, not an expression. Leaves are emitted as **unresolved** `Name`s — the parser knows no scope (numtype-vs-struct classification is the builder's). |
| `builder` | Semantics: `resolveTypes` first folds the flat `TypeDef`s into a `typeReg` (resolved struct descriptors `crdt.ElemT` with cycle/dup-field detection + vector `*Model`s; a user type name may not shadow a numtype name/`vector`; `resolveToken` classifies a type token as numtype-or-struct). Then it folds `FnDef`+`MergeDef`+`QueryDef`+`UpdateDef`, **resolves** every `Name` → `Var`/`Ref` (+ `StructLit`/`Field`), and **type-checks** every term (a small `checker`: `typeOf` over numeric (carrying `numtype.NumType`)/`bool`/`string`/**`vkStruct`** (ordered fields), **assignability via `numtype.Sub`** + structural struct subtyping, per-operator result rules, combinator boundary checks `Sub(result, vElem(elem))`, memoized DFS return-type inference, reduce fixpoint over `unify`). Rejects duplicate/primitive-shadowing/zero-param fns, duplicate params/fields, unknown identifiers/fields, field access on a non-struct, arity/type mismatches, results not assignable to the element type, struct `collection`s, reserved type names, struct update params (`validateScalarParams`; `fn` params may be struct via `validateFnParams`), `reduce` outside a query, unanchored recursion, query params, missing merge. `Model.Elem` is the resolved `crdt.ElemT`; queries carry `Result`+`ResultNum` (scalar) or `ResultStruct *crdt.ElemT`. `BuiltPlan.Fingerprint` = `sha256` of the canonical-JSON input `Plan`. |
| `prover` | Proves CvRDT convergence for each `*Model` at the end of `Build` (imports `parser`/`numtype`/`crdt`, never `builder`); `Prove` takes the resolved `crdt.ElemT`. Lowers the merge fn and each update fn to a symbolic IR (`sym`, incl. `symStruct` + struct-lit/field lowering, user-fn inlining + recursion rejection). A struct variable **flattens** to leaf scalar SMT vars named by path index (`a_0`, `a_0_1`; DSL identifiers never reach the solver, like update params → `p0`/`p1`); the slot uses `slotVar` prefix. Builds obligations — merge comm/assoc/idempotence + per-update `merge(x,h(x))=h(x)` — and discharges each by **negation** through Z3: a struct equality becomes the **conjunction of per-leaf equalities** in one script (`leafEqs`+`conjunction`), so a product/joint-lattice merge is one Z3 call per obligation. Every leaf var is `Real` with `is_int`/sign from its own `NumType`. `Real` over-approximates ℚ soundly. No pure-Go fast path → `z3` is mandatory. |
| `crdt` | Runtime only — no string parsing, no `Plan` knowledge. `VectorCRDT` evaluates resolved `Expr` trees against `state map[string]rtVal` (a slot is scalar OR struct) via a small applicative interpreter (tagged `rtVal` = num/str/bool/**struct**/func, `[]rtVal` calling convention, partial application, recursion-depth guard; `ExprStructLit` builds a `kStruct`, `ExprField` projects). Combinator fns are applied via the generic `apply` (`evalFuncVal`), so `zip`/`local`/`reduce` work on structs. The **resolved `ElemT`** on the CRDT drives an absent slot's default (`zeroSlot` = 0 / a zero struct) and wire decoding. Arithmetic allocates fresh `big.Rat`s; the Snapshot/Merge/Apply boundary deep-clones (`cloneSlot`, recursing into structs). `Query` → `rtToAny` (num → `RatString`, struct → `map[string]any`); `Merge` is atomic. `ValidateQuery` = the non-evaluating `Query` prefix. Wire: `WireSnapshot{Slots map[string]SlotWire}` where `SlotWire` is recursive (`Num` string OR `Struct` map); `SnapshotWire`/`MergeWire` (de)serialize via `slotToWire`/`wireToSlot` — the latter validates each slot **against the `ElemT`** (exact field set, leaves in-domain) before adoption. Gossip keeps the in-process `map[string]rtVal` `Snapshot`/`Merge`. `Method.ResultStruct` carries a struct query's descriptor for swagger. |
| `node` | Lifecycle + message loop; calls `Spec.New` from `BuiltPlan.Collections`. Sends go through the `Peer` interface (`Send`), the one interception seam — prod uses `ChanPeer` (now `done`-guarded so a `Send` to a stopped peer can't leak a goroutine); peers are also addressable by ID (`AddPeer(id, p)` + `peerByID`) so a sync ack can route back. Graceful `Stop()` tears a cluster down without leaking goroutines. **Linearizable layer:** `ApplyLinearizable` (apply-then-`pushQuorum`) and `QueryLinearizable` (ABD two-phase: non-evaluating `ValidateQuery` preflight → `pullQuorum` gather → `pushQuorum` write-back → local `Query`). Wire-faithful: the exported plain-data DTOs `SyncPush/Pull/AckMsg` (carrying `crdt.WireSnapshot`) flow through `Peer`; a peer acks only if `Initialized` **and** `Fingerprint` matches **and** it holds the collection. The coordinator counts itself a holder — `reached(distinct)` ≙ `(1+distinct)/N >= ratio`, `N=len(peers)+1`; a per-request registry (`pending[ReqID]{ch, seen}`, `nodeID-counter` ReqIDs) demuxes acks, validating known-peer + dedup *before* enqueue. `WithSyncTimeout` bounds each phase; an unmet ratio is `ErrQuorumUnreached` (→ 503). |
| `gateway` | HTTP; returns 400 on parse/build errors and zero-collection deploys before touching node state; regenerates Swagger on deploy. `parseLinearize` reads the `X-Gospr-Linearize`/`X-Gospr-Sync-Ratio` headers (NaN/Inf/out-of-`[0,1]` → fast 400) and dispatches to `*Linearizable` (passing `r.Context()`), mapping `ErrQuorumUnreached` → 503. |
| `node` public accessor | `SnapshotWireAll() map[string]crdt.WireSnapshot` exposes each collection's wire-shaped snapshot (scalar or struct) — the sandbox uses this instead of the opaque in-process `Snapshot()`. |
| `sandbox` | Observable web playground (`gospr sandbox run`): a swappable `Cluster` of nodes wired with intercepting `Peer`s that drop (partition) / delay / observe (SSE) messages, an HTTP+SPA server, and atomic Reset. `nodeState.Collections` is built from `node.SnapshotWireAll()` via `wireToState`/`slotWireToAny` (a struct slot serializes as a nested object). Same linearize-header parsing + 503 mapping as the gateway, run under the cluster RLock for the whole op. Imports only `parser`/`builder`/`node`/`crdt` — built to lift into its own repo. |

## File map

```
main.go               CLI entry (urfave/cli/v3): `server local` (--nodes/--port, wires N nodes+gateways+peers), `sandbox run` (--nodes/--port → sandbox.Run), and `check <file.gos>` (parse→Build, no server)

numtype/
  numtype.go          leaf pkg (imports only math/big): NumType{Domain,Sign}, the six names, Parse/String/Sub/Join/Allows(*big.Rat). Zero value = top type `rat`; internal `Zero` sign types the literal 0
  numtype_test.go

builder/
  builder.go          Build(Plan) → BuiltPlan{Models, Functions, Collections}; resolveTypes→typeReg (struct registry + resolveToken + vector Models), Model{Elem crdt.ElemT} + env.resolve (Name→Var/Ref, StructLit/Field) + arityOf; checker (vtype{kind,num,fields}+vElem/elemTOf, sig, per-op rules + numBin, applyArgs/resultOf, subVtype (struct structural), typeOf/typeOfGuards/typeOfReduce fixpoint + StructLit/Field, inferReturn, unify/vtypeEqual) + validators (validateFnParams/validateScalarParams); final per-model `prover.Prove` convergence gate
  builder_test.go     hard-coded-AST integration test + error/duplicate/fn/arity/type + numeric-subtype + convergence-rejection cases

prover/
  prover.go           Prove(elem crdt.ElemT, merge, updates, funcs); sym IR (+symStruct) + lower (eval/refFn/evalApp/evalGuards + StructLit/Field, user-fn inlining + recursion guard); symVarOf flattens a struct var to path-index leaf vars; guarded ite distributes over struct fields via iteSym; merge-law + per-update inflationary obligation builders
  smt.go              sym → SMT-LIB (single Real sort over-approximating ℚ, is_int/sign asserts, fmtRat→(/ p.0 q.0), max/min→ite, ==/ /= → =/distinct); leafEqs+conjunction flatten a struct equality to a leaf-conjunction negated in one goal; checkGoal/runZ3 via os/exec `z3 -smt2 -in`, unsat=proven; lookPath/z3Binary seams
  prover_test.go      z3-backed accept/reject (max/min join, sum/avg rejected, inflationary, mixed-domain, recursion) + z3-missing seam

crdt/
  crdt.go             CRDT interface: Apply/Query/Merge/Snapshot + ValidateQuery (non-eval preflight) + SnapshotWire/MergeWire; WireSnapshot{Slots map[string]SlotWire} + recursive SlotWire{Num|Struct}; ElemT/FieldT resolved element descriptors (scalar Num or struct Fields)
  vector.go           Method (with Result ValType + ResultNum), Function, VectorCRDT (state map[string]*big.Rat), NewVector, cloneRat (deep-clone at Snapshot/Merge); tagged rtVal interpreter (eval/evalFn/apply over *big.Rat), primOp/arith/cmp primitives, rtToAny (num→RatString), maxEvalDepth guard; bindParams validates via numtype.Allows; toRat. Query=ValidateQuery+eval; mergeRemote core shared by Merge/MergeWire; SnapshotWire emits RatString
  vector_test.go

node/
  node.go             lifecycle + message loop; Initialize (stores Fingerprint), PropagatePlan; Peer/ChanPeer (done-guarded), AddPeer(id,p)+peerByID, Done(), MessageKind, Initialized(), Stop(). Linearizable symbols (design in the `node` row above): SyncPush/Pull/AckMsg, WithSyncTimeout, ErrQuorumUnreached, pendingReq registry (register/nextReqID), handleSync*/routeAck demux, reached/collectAcks/pushQuorum/pullQuorum/broadcast, ApplyLinearizable/QueryLinearizable, mergeCollection/snapshotCollection/ValidateQuery. Invariant: collectAcks counts a pull ack only after its merge succeeds
  node_test.go        Stop() halts gossip + is idempotent; MessageKind
  linearize_test.go   sync write/read, quorum failure (+ctx-cancel), ack validity (undeployed + fingerprint mismatch), zero-ack short-circuit, read preflight, ChanPeer done-bound, DTO gob/json codec, distinct-peer ack counting (no inflation / no crowding-out / failed-merge pull ack doesn't count)

sandbox/             observe/interact playground; imports only parser/builder/node/crdt — designed to lift into its own repo. `gospr sandbox run`
  sandbox.go          Server (stable Network+Hub, swappable *Cluster behind RWMutex), Run; locking contract: withCluster=RLock for reads/Apply, withClusterW=Lock for deploy (pin check+set), Parse/Build (z3) run OUTSIDE the lock; reset() = Lock→swap+net.Reconnect()→Unlock→old.stop(). GOTCHA: Reconnect MUST be inside the same write lock as the swap, else a deploy can land in the gap and propagate through stale partitions then pin (lock order is always s.mu→net, so no deadlock)
  cluster.go          Cluster (swappable topology): newCluster wires a full mesh of interceptingPeer + short gossip interval; stop() closes done + Stops every node; pinned deployed *BuiltPlan
  network.go          Network: per-pair partition (order-independent "a|b" key) + global delay, both RWMutex-guarded; survives Reset (delay kept, links reconnected)
  hub.go              SSE Event fan-out; Emit is non-blocking (buffered per-sub chan + select/default drop) so a stuck client never stalls the gossip/deploy send path
  peer.go             interceptingPeer (node.Peer): drops on partition, else emits inflight + delivers after delay in a goroutine guarded by the cluster's done chan (timer+select so Reset aborts in-flight sends promptly)
  server.go           HTTP+SPA: GET state/events(SSE), POST deploy(pin-the-plan, 409 on 2nd, zero-collection guard)/reset/links/speed, POST/GET nodes/{id}/collections/{collection}/{action|query} (string-only params); parseLinearize+writeOpError → *Linearizable under the cluster RLock, ErrQuorumUnreached→503; //go:embed web/dist
  *_test.go           network/peer(done-abort)/cluster(reset + reset-reconnect-atomic-with-deploy race) + linearize_test(partitioned linearized update→503) unit tests
  web/                Vite+TS+Web Components SPA (no framework). App-shell layout (topbar + focal graph + right inspector). src/components/{sandbox-app(orchestrator: app-shell markup; polls /state ~750ms; owns selected node + selectedCollection + liveQuery + lastDeployedSource; /events SSE used only for Reset),node-graph(SVG circle; shows ONLY the selected collection; per-node value chip + optional live-query chip; partition by clicking links; node blinks on state change),node-panel(the right inspector: cluster-wide live-label query picker + selected-node invoke blocks + network delay),deploy-modal(the only DSL editor — opened from the topbar)}; dist/ committed + embedded
                      Concepts: ONE collection shown at a time (topbar selector drives the graph + inspector). A LIVE LABEL polls a (param-less) query across every node each tick and renders the result as a chip under each node (queryAll uses Promise.allSettled + the app skips nodes lacking the collection, so a not-yet-deployed/partitioned node never breaks the poll). Deploy is one-shot per cluster (server pins the plan); the modal redeploys by VALIDITY-PREFLIGHT: POST /deploy first — 400 = bad code (cluster untouched), 409 = valid but pinned → only then reset() + redeploy. Server stores no DSL source, so the modal prefills the last same-session source (else SAMPLE).
                      Conventions: (1) each component SIGNATURE-GUARDS render — setData hashes ONLY the fields it displays and skips re-render when unchanged, so the 750ms poll/gossip churn never wipes focus or in-progress textbox text; when a component starts displaying new data, ADD it to that signature. node-graph's signature + blink are scoped to the SELECTED collection's slots (+ live results), so gossip on a hidden collection causes no churn. (2) NO message-flight animation. node-graph blinks a node (.node-circle.blink, brightness() pulse) when its slot signature changes between polls — poll-driven (~750ms), not SSE. The SSE /events stream only carries Reset. (3) Links partitioned by clicking the graph: a wide transparent `.link-hit` line per pair is keyboard-operable (tabindex/role=button/aria-label/Enter-Space) inside an svg role=group (NOT role=img); the visible `.link` has pointer-events:none. Node slot values render as `[v1 v2 …]` (bare values, no nodeID keys — user preference); a struct slot renders as `{Pos:5 Neg:2}` (see `fmtSlot`, `SlotValue = string | object` in api.ts). (4) .graph-wrap capped max-width:760px+centered. All animation honors prefers-reduced-motion.
                      GOTCHAS: (a) A custom-element module used ONLY as a type gets tree-shaken under isolatedModules, dropping its `customElements.define` side effect — construct such elements with `new Foo()` (a value use) so registration runs (this bit deploy-modal). (b) SVG `getBBox()` returns a ZERO box for a detached subtree — attach the node `<g>` to the live tree BEFORE measuring text, or chip backgrounds collapse to a tiny pill. (c) The deploy modal is appended OUTSIDE `.shell` because it sets `inert` on `.shell` while open.

swagger/
  swagger.go          Generate(BuiltPlan) → OpenAPI 3.0 JSON; type-switches on *builder.Model; numSchema → string (numbers are exact-rational strings on the wire) with the NumType named in the description; structSchema → object schema for a struct query result (Method.ResultStruct)
  swagger_test.go

gateway/
  gateway.go          HTTP: POST /api/cluster/deploy, POST /api/collections/{collection}/{action},
                      GET /api/collections/{collection}/{query}?params=... (passed as exact-rational strings),
                      GET /api/swagger.json, GET /api/docs (Swagger UI). parseLinearize (X-Gospr-Linearize/-Sync-Ratio headers, NaN/Inf/out-of-[0,1]→400) + writeOpError (ErrQuorumUnreached→503) dispatch update/query to node.*Linearizable
  parselinearize_test.go  parseLinearize table test (defaults, accepted ratios, NaN/Inf/range/non-numeric rejected)

parser/
  types.go            AST: ElemType (Scalar = numeric type name), Expr sum type (ExprKind incl. StrLit/Guards), GuardCase, ValType, RefKind, TypeDef/FnDef/MergeDef/QueryDef/UpdateDef/CollectionSpec, Plan
  parser.go           public Parse entry point
  stream.go           value-typed Stream — backtracking is free
  result.go           ParseResult[A], Parser[A], Of2–Of5 tuples
  combinators.go      all combinators
  dsl.go              DSL grammar (line parsers incl. guarded fnLineP/guardLineP + numberP/stringLitP/symOpP/typeNameP/paramP/structTypeP/elemTypeP + reduceFormP/structLitP + exprP/atomP(base+.field)/nameP/applicationP; wsP/ws1P newline-tolerant braces)
  parser_test.go      canonical-snippet integration test + cases

e2e/
  e2e_test.go         model-level: string → Parse → Build → Model.New → Add/Value/merge behaviors
  cluster_http_test.go blackbox: real 3-node cluster (nodes+gossip+httptest gateways), driven ONLY via HTTP; deploy→Add on one node→poll until value gossips to another (node.WithGossipInterval for speed, require.Eventually for convergence); also linearized write(node1)→linearized read(node2) with gossip OFF (1h) proving the sync quorum carried it
```

## DSL syntax

```
type T = vector rat0+           # scalar numeric: rat|rat0+|rat0-|int|int0+|int0-
fn lub a::rat b::rat = max a b  # user-defined fn; body is a full applicative expression
fn grade x::rat                  # guarded fn: multi-line, value-typed branches
| (> x 90) = "A"                #   each cond is a bool; results share one type (numeric/bool/string)
| otherwise = "F"               #   `otherwise` is mandatory and must be the last case (build-checked)
merge T = zip lub               # zip: apply a numeric,numeric->numeric fn elementwise per node slot
query T.Grade = grade (reduce max 0)  # query body is a general expr; reduce folds the slots to a numeric
update T.Add k::rat0+ = local (+ k)   # local: apply a unary fn to ONLY the calling node's slot
collection MyVec = T            # named runtime instance of a type (no args)

# --- struct vectors: a slot holds a struct of named numeric fields ---
type X = {                       # named STRUCT type; fields are `Name Type`, newline-separated
  Pos rat0+
  Neg rat0+
}
type VX = vector X               # a vector whose element is the struct X
fn J a::X b::X = { Pos: max a.Pos b.Pos, Neg: max a.Neg b.Neg }  # struct params, struct literal, dot access
fn S a::X b::X = { Pos: + a.Pos b.Pos, Neg: + a.Neg b.Neg }      # (query-only fold; a per-field SUM is not idempotent)
fn incPos k::rat0+ s::X = { Pos: + s.Pos k, Neg: s.Neg }
merge VX = zip J                 # whole-struct merge (product lattice), proven per-field
update VX.AddPos k::rat0+ = local (incPos k)                     # struct-typed local update
query VX.Net = - (reduce S { Pos: 0, Neg: 0 }).Pos (reduce S { Pos: 0, Neg: 0 }).Neg  # project a field
query VX.Totals = reduce S { Pos: 0, Neg: 0 }                    # a query may return a whole struct (JSON object)
collection C = VX
```

- **Expressions** are prefix application: `f a b`, `+ a (max b c)`. A bare `(op arg)` like `(+ k)` is a **partial application**, not a special "section" form. Application may under-saturate (partial) but never over-saturate (build error).
  - Partial application binds the **leftmost** argument first, so `(- k)` is `\x -> - k x` = `k - x` (the combinator supplies the slot `x` as the *next* arg). This is the one uniform rule — there is no right-section. For non-commutative ops where you want the slot as the left operand (`x - k`), define a helper with that param order: `fn rsub k::rat x::rat = - x k` then `local (rsub k)`. (`+ * max min` are commutative, so unaffected.)
- **`fn`**: top-level, global, at least one numeric param (zero-arg fns rejected); body must be saturated. Return type (numeric/`bool`/`string`) is **inferred**. May reference other fns / itself — recursion is allowed only when a concrete branch anchors the return type (unanchored recursion is a build error); runtime is bounded by `maxEvalDepth`.
- **Guarded `fn`**: `| cond = result` lines, `cond` a `bool`, all `result`s the same type (numeric branches join to a common numeric supertype). The final case must be `otherwise` (enforced at build time, so matches are total). A guarded body is `ExprGuards`; `reduce` is not allowed inside any `fn` (functions stay pure).
- **Numeric types & operators**: six types `rat, rat0+, rat0-, int, int0+, int0-` (domain {rat,int} × sign {any,≥0,≤0}); see `numtype`. `rat` is exact rational (`big.Rat`) at runtime. Operators take **any** numeric operand; each computes the tightest sound result (`+`/`-` via `addSign`, `*` via `mulSign`, `max`/`min` by bound analysis — so `rat0+ - rat0+ → rat`). **Assignability** (`numtype.Sub`, not equality) governs args & the combinator boundary, so `int0+` flows where `rat0+` is wanted. The literal `0` has an internal `Zero` sign, assignable to any numeric type. Comparisons `> < >= <= == /=` are numeric,numeric->`bool`. Strings are `"..."` (`\" \\ \n \t` escapes). The builder rejects type mismatches (`+ (> x 1) 2`) and results not assignable to the element type.
- **Combinator slots** (`zip`/`reduce`/`local`) take a single **atom** (a name or a parenthesised term), not a bare application — this keeps `reduce + 0` unambiguous (`+` is the fn, `0` the init). The fn is applied to element-typed args (E = a numeric type OR a struct) and its result must be `Sub` the element type: zip → (E,E)->Sub E; local → (E)->Sub E. `reduce` is also a value atom inside a **query** body (`f (reduce max 0)`); its **init** is a literal (numeric or struct) and its result type is the lattice **fixpoint** of folding the fn over (acc, E) from the init's type.
- **Structs**: `type X = { Name Type ... }` is a named struct type (fields space-separated, newline/whitespace-separated, ≥1, `{}` may span lines); `Type` is a numtype name or another struct type (nesting allowed; recursion rejected). `vector`'s element is a numtype name OR a struct type name. A **struct literal** `{ Name: expr, ... }` (comma-separated, trailing comma & newlines ok) constructs a struct; **field access** `a.Pos` (chainable) projects one. Struct assignability is **structural** (exact field set, each field `Sub`). Reserved: a user type name may not be a numtype name or `vector`. The brace-aware parsing is inline (the `{}` grammars are newline-tolerant), not a text pre-pass; struct-lit/field-access are `ExprStructLit`/`ExprField`.
- Primitives: `+ * -` and comparisons are operator tokens; `max`/`min` are ordinary identifiers (so `maxValue` is one name) recognised as primitives by the builder. A `fn` may not shadow a primitive.
- Params: `name::type`, names unique. A **`fn`** param may be a numeric name OR a struct type (fns receive struct slots from combinators). An **update** param must be a numeric name (it crosses the wire via `bindParams`, scalar-only; a struct update param is a build error) and is validated at runtime against its type (`numtype.Allows`). **Query** params parse but are rejected at build (future feature).
- **Wire form**: numbers cross the HTTP/JSON boundary as **exact-rational strings** (`"5"`, `"1/2"`, input `"0.1"` → `1/10`) — never JSON numbers — so nothing is lost to float at I/O. DSL numeric literals are likewise parsed exact (`parser.Expr.Num` is `*big.Rat`). Swagger types numeric fields as `string`.
- **Consistency headers** (opt-in, on both the update `POST` and query `GET`): `X-Gospr-Linearize: true` switches an op to synchronous quorum mode (absent → today's local-only behavior); `X-Gospr-Sync-Ratio: <f>` is the fraction of the cluster to reach, `f ∈ [0,1]`, default `0.5`. A write applies locally then pushes to the ratio; a read is two-phase (gather then write-back) then queries. `ratio > 0.5` makes read/write quorums overlap ⇒ linearizable. Unmet quorum (e.g. under a partition) → **503**. A 503 on a *write* is indeterminate (the local slot is already mutated and may still converge via gossip — retrying a non-idempotent `Add` can double-count). Swagger does not document the headers yet.
- A collection name = the node's collection key; a `type` defines reusable behavior, instantiated by `collection`.

## Extension points

- **New operator:** add its arity to `primitiveArity` in `builder/builder.go` and a result rule (a `numBin` case for arithmetic, or `cmpOps` membership for a bool result), add a case in `primOp` in `crdt/vector.go` (`arith` / `cmp`), add a `serialize` case in `prover/smt.go` so the convergence proof can reason about it (and `cmpOps` membership in `prover/prover.go` for a bool op), and (for a punctuation operator) a `Try(StringP(...))` alternative in `symOpP` (`parser/dsl.go`, multi-char before single-char); word-shaped operators need no parser change (they parse as identifiers).
- **New numeric type:** add the name + `NumType` to `numtype.Parse`/`String` and adjust `Sub`/`Join`/`Allows`; add it to `numTypeNameP` in `parser/dsl.go` (longest-match order). The builder/crdt/swagger consume `numtype` generically, so usually need no change.
- **New value type** (non-numeric): add a `parser.ValType` + builder `vkind`, handle it in `checker.typeOf`/`subVtype`/`unify`, add an `rtKind` + constructor in `crdt/vector.go` and a `rtToAny`/`valTypeSchema` case.
- **New expression form** (e.g. query params): add/realize an `ExprKind`/`ElemKind` in `parser/types.go`, parse it in `dsl.go`, resolve it in `builder.env.resolve` and type-check it in `checker.typeOf`, evaluate it in `crdt/vector.go` (`eval`). Struct vectors (`ExprStructLit`/`ExprField`) are the worked example — grep those kinds across the layers to see the full seam list.
- **New expression form & the prover:** any new `ExprKind` reachable from a merge/update fn body also needs an `eval` case in `prover/prover.go` (lower it to a `sym`), else the convergence proof errors out. Structs are done (product/joint lattice: leaf-flattened, struct equality = leaf conjunction in one Z3 call); a cross-field/LWW merge is expressible today (all leaf vars share one goal scope). A future direction is struct-valued update/query wire params (currently scalar-only via `bindParams`).
- **Network propagation of messages:** the linearizable `SyncPush/Pull/AckMsg` are **already** exported plain-data DTOs carrying a concrete `crdt.WireSnapshot` (exact-rational strings) — gob/JSON-ready, pinned by a node codec round-trip test, so flipping to a real transport is a no-op for them. Still in-process today (channels). The older `gossipMsg`/`deployMsg` remain unexported with unexported fields; over the wire `deployMsg` needs `BuiltPlan` exported-encodable (`Model` holds `parser.Expr` trees, no closures; `Expr.Num` is `*big.Rat` with Gob/Text marshalers).

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
- **Prover soundness with structs:** a `sym` node must never carry a whole struct through scalar position, or the SMT flattening skips its fields and can spuriously prove a non-lattice merge. When adding a sym form reachable from a struct-valued fn, distribute it per-field (see `iteSym` in `prover/prover.go` and `TestProve_structGuardedNonCommutativeRejected` for why).
- **Return-type inference (Option A):** `checker.inferReturn` is memoized DFS; a recursive call caught mid-inference yields `vUnknown`, which unifies with any concrete type. If a function's type stays `vUnknown` (no concrete branch), Build rejects it.
- `exprP` is recursive (parenthesised atoms hold expressions); the body is deferred to parse time (`exprP` returns a thunk) to break the parser-construction cycle.
- `Model.Queries`/`Updates` are always non-nil maps (init in `Build`) so swagger/crdt can range/lookup safely; `Model.Funcs`/`BuiltPlan.Functions` is the shared global function env.
- A local update on an absent slot defaults to 0 (Go zero map value). Merge adopts a remote slot if absent locally (zip over the union of node IDs) and is **atomic** — a failing user fn leaves state unchanged.
- Query and update may share a name (separate namespaces, dispatched by HTTP verb). Duplicate names *within* type/fn/merge/query/update/collection are build errors.
- `Expr` is a single struct with a `Kind` tag and union-ish fields; only the fields relevant to `Kind` are set (pointers for nested exprs). Keep it serializable — no closures, so optimization/proof passes stay possible.
- **Sync ack routing:** a peer replies to a `SyncPush/PullMsg` by looking up `peerByID[m.FromID]`, so every broadcast literal MUST set `FromID:n.id` (a zero `FromID` ⇒ acks can't route back ⇒ silent false `ErrQuorumUnreached`). `AddPeer` is `(id, p)` for this reason — all wiring sites pass the peer's ID. `pendingReq.seen` (distinct-ack dedup) is touched ONLY in the single message-loop goroutine (`handleSyncAck`), so it is lock-free by construction — don't read/write it elsewhere.
