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
a **string** (a storable leaf, e.g. an LWW register's `value`) or a **struct** of
named fields (numeric or string; see the struct-vector note below). Value
types are `numeric` (carrying a `NumType`), `bool` (from comparisons), `string`
(literals, string slot leaves, and string comparison tiebreaks), and `struct` (named fields, from struct literals /
struct-typed params). Built-in operators accept *any* numeric operand and each
computes the *tightest sound* result type (`int + int0+ → int`, `rat0+ - rat0+ →
rat`); a `vector rat0+` counter thus rejects `local (- k)` at build time and a
negative `Add` at runtime. Users define functions (`fn add a::rat b::rat = + a b`,
or multi-line **guarded** `fn grade x::rat | (> x 90) = "A" | otherwise = "F"`)
over a small applicative
core (variables, literals, function references, application). `zip`/`local`/`write`
are the combinator keywords carrying a function-valued term; `reduce` is a **pure
fold** `reduce fn init vec` that takes the vector to fold **explicitly** (no implicit
state read), so it may appear anywhere a vector value is in scope — inside a `fn`
body, a `write`, or a query. A **query is a function of the whole vector**
(`query X.Q = qf`, `qf :: X -> result`; the runtime supplies the vector), so a query
can return a `bool`/`string`/struct. Function params are **concrete** numeric
types (numeric-generic params are out of scope) but may also be a whole-**vector**
type (folded by `reduce`); return types are **inferred** but
may carry an optional `-> type` annotation (`fn add a::rat b::rat -> rat = …`).
Recursion whose base case anchors the return type infers with no annotation;
otherwise-unanchored recursion needs a `-> type` annotation (which seeds inference
so the recursive call and body are checked against the declared type) — an
un-annotated unanchored recursion is a build error. On top of type-checking, the builder
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
stay scalar (query params are scalar-numeric; an **update** param may also be a
**string**; struct/vector params ride only through `fn`s and literals). Query
params are bound into the body at query time. Beyond `local` (mutate the calling
node's own slot), an update may be a **`write <fn>`** whose fn reads the **whole
vector** (`X -> Elem`) — needed for a **Lamport clock** (`version = 1 + max(version
over all slots)`), the basis of an **LWW register** (`{ version, value }`, merge =
per-slot argmax-by-version with a string-value tiebreak). Comparisons (`> < >= <=
== /=`) work on numeric **or** string operands (string order is lexicographic, and
UTF-8 is order-preserving so the runtime's bytewise order matches the prover's
`str.<`); mixed numeric/string comparison is a build error. The prover discharges a
`write`'s inflationary obligation using a **reduce-domination** rule: the fold's
result `M` is bounded by `M ≥ x.<field>` (for a proven `max`-fold over that field),
so the new `version = 1+M` dominates the slot and merge selects the write.

## Architecture

Nodes run as goroutine groups and communicate only via `chan any` — no shared memory, no direct cross-node calls. The send side is the `node.Peer` interface (`Send(msg any)`); production wraps each inbox in `node.ChanPeer` (a blocking channel send). This is the single interception seam: the `sandbox` package supplies its own `Peer` to observe/delay/drop messages **without touching production node code** (the sandbox is designed to be liftable into its own repo). The CLI (`gospr server local`) spins up N nodes (default 1, `--nodes` to scale), wiring each as the others' peer; one node is a degenerate cluster (no peers, gossip no-ops).

- `POST /api/cluster/deploy` → DSL parsed → builder validates, type-checks, **and proves convergence** (via `prover`/Z3) → builds per-type `Model`s → node initializes and propagates `deployMsg` to peers
- Every ~2s each node gossips a snapshot to one random peer; the peer merges each collection via that type's user-defined `merge` expr (e.g. `zip max` → elementwise max over the union of node slots)
- Node lifecycle: `Uninitialized → Initialized` (one-way, idempotent). Because it is one-way, the gateway rejects a deploy that yields zero collections.

## Layer responsibilities

```
parser  →  builder → prover  →  node / crdt
```

One-line responsibility + the constraints not obvious from one file. Per-file
symbol maps live in the File map below; deeper rationale in Architecture / Gotchas
/ Extension points.

| Layer | Owns |
|---|---|
| `parser` | Syntax only: text → `Plan` (flat AST slices; bodies are an applicative `Expr` sum type). Knows **no scope** — leaves are unresolved `Name`s (numtype-vs-struct classification is the builder's job). |
| `builder` | Semantics: resolve types + every `Name`→`Var`/`Ref`, type-check every term (the `checker`: assignability via `numtype.Sub`, structural struct subtyping, return-type inference), then gate on `prover.Prove`. Emits `BuiltPlan{Models,Functions,Collections}` + a `Fingerprint` (sha256 of the canonical-JSON `Plan`). |
| `prover` | Proves CvRDT convergence per `*Model` via Z3 (merge join-laws + per-update inflationary `merge(x,h(x))=h(x)`, for `local` and `write`). Structs flatten to leaf SMT vars, struct-eq = leaf conjunction in one call. A string leaf gets the SMT `String` sort (comparisons → `str.<`). A `write` body's `reduce` lowers to a fresh `M` bounded by proven fold-domination lemmas. `Real` over-approximates ℚ soundly. Imports `parser`/`numtype`/`crdt`, **never `builder`**; `z3` is mandatory (no pure-Go path). |
| `crdt` | Runtime only — no parsing, no `Plan` knowledge. Interprets resolved `Expr` trees over per-slot state (numeric, string, OR struct). A whole-vector value (`kVector`) is built at a `write`/query boundary and folded by the pure `reduce`. Owns the wire codec (`WireSnapshot`), which **validates each slot against the resolved `ElemT`** (3-way num/str/struct tag, exactly-one-tag, UTF-8 guard) before adoption. Deep-clones at the Snapshot/Merge/Apply boundary; `Merge`/`write` are atomic. |
| `node` | Lifecycle + message loop. All sends cross the `Peer` seam (prod `ChanPeer`) — the single interception point. **Sync-quorum layer:** `ApplySync` (apply→`pushQuorum`) / `QuerySync` (ABD two-phase); quorum rule `reached` = `holders/N > ratio` (strict, so `0.5` is a true majority; `ratio ≥ AllRatio` = all N); unmet → `ErrQuorumUnreached` (503). |
| `gateway` | HTTP: deploy / apply / query + Swagger regen. `parseSync` reads the `X-Gospr-Sync-Ratio` header; maps `ErrQuorumUnreached`→503, other errors→400. |
| `sandbox` | Observable playground (`gospr sandbox run`): swappable `Cluster` of nodes wired with intercepting `Peer`s (partition/delay/observe) + SPA. Imports only `parser`/`builder`/`node`/`crdt` — built to lift into its own repo. |

## File map

```
main.go               CLI entry (urfave/cli/v3): `server local` (--nodes/--port, wires N nodes+gateways+peers), `sandbox run` (--nodes/--port → sandbox.Run), and `check <file.gos>` (parse→Build, no server)

numtype/
  numtype.go          leaf pkg (imports only math/big): NumType{Domain,Sign}, the six names, Parse/String/Sub/Join/Allows(*big.Rat). Zero value = top type `rat`; internal `Zero` sign types the literal 0
  numtype_test.go

builder/
  builder.go          Build(Plan) → BuiltPlan{Models, Functions, Collections}; resolveTypes→typeReg (struct/alias registries + vector Models; resolveToken accepts `string` leaf, rejects vector names; reg.isVector; on-demand resolveStruct/resolveVectorElem/resolveAlias sharing one cycle set), Model{Elem crdt.ElemT} + env.resolve (Name→Var/Ref, StructLit/Field, `reduce fn init vec`, `write` combinator) + arityOf; checker (vtype{kind,num,fields,fn,elem}+vElem/elemTOf/vVector, vkFunc/vkVector via isFunc, resolveTokenVtype (vector param/return, distinct from leaf-only resolveToken), per-op rules + numBin, applyArgs (cmpOps accept num/num OR str/str via checkCmpOperands, reject mixed)/resultOf, subVtype/unify/vtypeEqual (struct/function/vector structural), typeOf→typeOfGuards/typeOfReduce(folds the vec arg's element)/checkQueryFn(query = fn-of-vector, containsVector gate)/checkCombinatorFn(write applies fn to the vector), annotation-seeded+strict inferReturn) + validators (validateFnParams allows vector params + resolveScalarParams(allowString split): update params may be strings, query params may not, rejects structs); final per-model `prover.Prove` convergence gate
  builder_test.go     hard-coded-AST integration test + error/duplicate/fn/arity/type + numeric-subtype + convergence-rejection cases

prover/
  prover.go           Prove(elem crdt.ElemT, merge, updates, funcs); sym IR (symStruct + symVector marker; each sym carries a `sort` num/str/bool; strCst/strVr) + lower (eval/refFn/evalApp/evalGuards + StructLit/Field/StrLit, user-fn inlining + recursion guard); symVarOf flattens a struct var to path-index leaf vars (str leaf → String-sorted); guarded ite distributes over struct fields via iteSym; per-update obligation dispatches `local` (fn of slot x) vs `write` (fn of a vector marker whose self=x): evalReduce lowers `reduce` to a fresh `M`, foldProjection extracts the max/min field path, checkFoldLemmas discharges `f≥acc`+`f≥e.path` in Z3, then assumes `M≥init`,`M≥x.path`; bindUpdateParams (numeric or String-sorted); goal{claim,assume} for lemma/hypothesis goals
  smt.go              sym → SMT-LIB (Real for numeric leaves over-approximating ℚ + `String` for string leaves; is_int/sign asserts, fmtRat→(/ p.0 q.0), max/min→ite, ==/ /= → =/distinct, string `< <= > >=`→`str.<`/`str.<=`(±swap), smtStrLit); leafEqs+conjunction flatten a struct equality to a leaf-conjunction negated in one goal; buildScript asserts hypotheses then the negated claim/equality; checkGoal/runZ3 via os/exec `z3 -smt2 -in`, unsat=proven; lookPath/z3Binary seams
  prover_test.go      z3-backed accept/reject (max/min join, sum/avg rejected, inflationary, mixed-domain, recursion) + z3-missing seam

crdt/
  crdt.go             CRDT interface: Apply/Query/Merge/Snapshot + ValidateQuery (non-eval preflight) + SnapshotWire/MergeWire; WireSnapshot{Slots map[string]SlotWire} + recursive SlotWire{Num|Str *string|Struct} (exactly one populated — a 3-way tag); ElemT/FieldT resolved element descriptors (numeric Num, Str string leaf, or struct Fields)
  vector.go           Method (with Result ValType + ResultNum), Function, VectorCRDT (state map[string]rtVal), NewVector, cloneRat/cloneSlot/zeroSlot (deep-clone + defaults at Snapshot/Merge; kStr/kStruct branches); tagged rtVal interpreter (eval/evalFn/apply; kNum/kStr/kBool/kStruct/kFunc/**kVector**), primOp/arith/cmpOp (compareVals: string lexicographic) primitives, rtToAny (num→RatString), maxEvalDepth guard; Apply dispatches local (self slot) vs **write** (whole-vector snapshot); eval `reduce fn init vec` folds the passed kVector; Query applies the fn-of-vector body to a kVector; bindParams (numtype.Allows for numeric, utf8.ValidString for string params); toRat. slotToWire/wireToSlot (3-way num/str/struct tag, exactly-one-tag + utf8 guard); mergeRemote core shared by Merge/MergeWire
  vector_test.go

node/
  node.go             lifecycle + message loop; Initialize (stores Fingerprint), PropagatePlan; Peer/ChanPeer (done-guarded), AddPeer(id,p)+peerByID, Done(), MessageKind, Initialized(), Stop(). Synchronous-quorum symbols (design in the `node` row above): SyncPush/Pull/AckMsg, WithSyncTimeout, ErrQuorumUnreached, pendingReq registry (register/nextReqID), handleSync*/routeAck demux, reached/collectAcks/pushQuorum/pullQuorum/broadcast, ApplySync/QuerySync, AllRatio, mergeCollection/snapshotCollection/ValidateQuery. Invariant: collectAcks counts a pull ack only after its merge succeeds
  node_test.go        Stop() halts gossip + is idempotent; MessageKind
  linearize_test.go   sync write/read, quorum failure (+ctx-cancel), ack validity (undeployed + fingerprint mismatch), zero-ack short-circuit, read preflight, ChanPeer done-bound, DTO gob/json codec, distinct-peer ack counting (no inflation / no crowding-out / failed-merge pull ack doesn't count)

sandbox/             observe/interact playground; imports only parser/builder/node/crdt — designed to lift into its own repo. `gospr sandbox run`
  sandbox.go          Server (stable Network+Hub, swappable *Cluster behind RWMutex), Run; locking contract: withCluster=RLock for reads/Apply, withClusterW=Lock for deploy (pin check+set), Parse/Build (z3) run OUTSIDE the lock; reset() = Lock→swap+net.Reconnect()→Unlock→old.stop(). GOTCHA: Reconnect MUST be inside the same write lock as the swap, else a deploy can land in the gap and propagate through stale partitions then pin (lock order is always s.mu→net, so no deadlock)
  cluster.go          Cluster (swappable topology): newCluster wires a full mesh of interceptingPeer + short gossip interval; stop() closes done + Stops every node; pinned deployed *BuiltPlan
  network.go          Network: per-pair partition (order-independent "a|b" key) + global delay, both RWMutex-guarded; survives Reset (delay kept, links reconnected)
  hub.go              SSE Event fan-out; Emit is non-blocking (buffered per-sub chan + select/default drop) so a stuck client never stalls the gossip/deploy send path
  peer.go             interceptingPeer (node.Peer): drops on partition, else emits inflight + delivers after delay in a goroutine guarded by the cluster's done chan (timer+select so Reset aborts in-flight sends promptly)
  server.go           HTTP+SPA: GET state/events(SSE), POST deploy(pin-the-plan, 409 on 2nd, zero-collection guard)/reset/links/speed, POST/GET nodes/{id}/collections/{collection}/{action|query} (string-only params); parseSync+writeOpError → Apply/QuerySync under the cluster RLock, ErrQuorumUnreached→503; //go:embed web/dist
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
                      GET /api/swagger.json, GET /api/docs (Swagger UI). parseSync (presence-based X-Gospr-Sync-Ratio header: absent→async, present→sync at fraction in [0,1) or `all`; empty/≥1/NaN/Inf/range→400) + writeOpError (ErrQuorumUnreached→503) dispatch update/query to node.Apply/QuerySync
  parselinearize_test.go  parseSync table test (absent→off, accepted fractions + `all`, present-empty/≥1/NaN/Inf/range/non-numeric rejected)

parser/
  types.go            AST: ElemType{Kind,Elem,Inner,Fields} (ElemKind = KindStruct|KindVector(Elem token or Inner inline struct)|KindElemRef(`V.Elem` alias)), Expr sum type (ExprKind incl. StrLit/Guards/Reduce(+Vec)/Zip/Local/**Write**), GuardCase, ValType, RefKind, TypeDef/FnDef(+RetType: optional `-> type` token)/MergeDef/QueryDef/UpdateDef/CollectionSpec, Plan
  parser.go           public Parse entry point
  stream.go           value-typed Stream — backtracking is free
  result.go           ParseResult[A], Parser[A], Of2–Of5 tuples
  combinators.go      all combinators
  dsl.go              DSL grammar (line parsers incl. guarded fnLineP(optional `-> type` return annotation on the header via typeNameP)/guardLineP + numberP(signed decimal)/stringLitP/symOpP/typeNameP(ident + `.Elem` member OR sign suffix)/paramP/structTypeP/elemTypeP(struct | Try(vector <inline struct|token>) | `Base.Elem` alias) + reduceFormP(`reduce fn init vec` — 3 atoms)/structLitP + updateLineP(`local`|`write` atom) + exprP/atomP(base+.field)/nameP/applicationP; commentP + wsP/ws1P newline/comment-tolerant braces; blankOrCommentLineP skips blank/comment lines, unknownLineP makes any other unrecognized line a parse error)
  parser_test.go      canonical-snippet integration test + cases

e2e/
  e2e_test.go         model-level: string → Parse → Build → Model.New → Add/Value/merge behaviors
  cluster_http_test.go blackbox: real 3-node cluster (nodes+gossip+httptest gateways), driven ONLY via HTTP; deploy→Add on one node→poll until value gossips to another (node.WithGossipInterval for speed, require.Eventually for convergence); also linearized write(node1)→linearized read(node2) with gossip OFF (1h) proving the sync quorum carried it
```

## DSL syntax

```
type T = vector rat0+           # scalar numeric: rat|rat0+|rat0-|int|int0+|int0-
fn lub a::rat b::rat = max a b  # user-defined fn; body is a full applicative expression
fn add a::rat b::rat -> rat = + a b  # optional `-> type` return annotation (numtype/struct/V.Elem/bool/string)
fn grade x::rat -> string        # guarded fn: multi-line, value-typed branches; annotation on the header
| (> x 90) = "A"                #   each cond is a bool; results share one type (numeric/bool/string)
| otherwise = "F"               #   `otherwise` is mandatory and must be the last case (build-checked)
merge T = zip lub               # zip: apply a numeric,numeric->numeric fn elementwise per node slot
fn top v::T = grade (reduce max 0 v)  # reduce fn init vec: a PURE fold; v is the whole-vector param
query T.Grade = top             # query body is a fn of the vector (T -> result); runtime supplies v
update T.Add k::rat0+ = local (+ k)   # local: apply a unary fn to ONLY the calling node's slot
collection MyVec = T            # named runtime instance of a type (no args)

# --- struct vectors: a slot holds a struct of named numeric fields ---
type X = {                       # named STRUCT type; fields are `Name Type`, newline-separated
  Pos rat0+
  Neg rat0+
}
type VX = vector X               # a vector whose element is the struct X
# ...or inline the struct in the vector and name it later via VX.Elem (no separate `type X`):
#   type VX = vector { Pos rat0+  Neg rat0+ }
#   fn J a::VX.Elem b::VX.Elem = ...   ;   type X = VX.Elem   (element-ref alias, any element type)
fn J a::X b::X = { Pos: max a.Pos b.Pos, Neg: max a.Neg b.Neg }  # struct params, struct literal, dot access
fn S a::X b::X = { Pos: + a.Pos b.Pos, Neg: + a.Neg b.Neg }      # (query-only fold; a per-field SUM is not idempotent)
fn incPos k::rat0+ s::X = { Pos: + s.Pos k, Neg: s.Neg }
merge VX = zip J                 # whole-struct merge (product lattice), proven per-field
update VX.AddPos k::rat0+ = local (incPos k)                     # struct-typed local update
fn net v::VX = - (reduce S { Pos: 0, Neg: 0 } v).Pos (reduce S { Pos: 0, Neg: 0 } v).Neg
query VX.Net = net                                              # project a field off the fold
fn totals v::VX = reduce S { Pos: 0, Neg: 0 } v
query VX.Totals = totals                                        # a query may return a whole struct (JSON object)
collection C = VX

# --- LWW register: a { version, value } slot, latest write wins ---
type X = vector { version int0+  value string }   # string is a storable slot leaf
merge X = zip Merge                                # per-slot argmax-by-version, string tiebreak
fn Merge a::X.Elem b::X.Elem -> X.Elem
| (> a.version b.version) = a
| (< a.version b.version) = b
| (> a.value b.value)     = a                      # deterministic tiebreak on the string value
| otherwise               = b
fn maxVer acc::int0+ e::X.Elem -> int0+ = max acc e.version
fn NextX s::string v::X -> X.Elem = { version: + 1 (reduce maxVer 0 v), value: s }  # Lamport clock
update X.Set s::string = write (NextX s)           # write: fn reads the WHOLE vector (X -> Elem)
fn LatestValue v::X -> string = (reduce Merge { version: 0, value: "" } v).value
query X.Value = LatestValue
collection Reg = X
```

- **Expressions** are prefix application: `f a b`, `+ a (max b c)`. A bare `(op arg)` like `(+ k)` is a **partial application**, not a special "section" form. Application may under-saturate (partial) but never over-saturate (build error).
  - Partial application binds the **leftmost** argument first, so `(- k)` is `\x -> - k x` = `k - x` (the combinator supplies the slot `x` as the *next* arg). This is the one uniform rule — there is no right-section. For non-commutative ops where you want the slot as the left operand (`x - k`), define a helper with that param order: `fn rsub k::rat x::rat = - x k` then `local (rsub k)`. (`+ * max min` are commutative, so unaffected.)
- **`fn`**: top-level, global, at least one numeric param (zero-arg fns rejected); body must be saturated. Return type (numeric/`bool`/`string`/struct) is **inferred**, or declared with an optional `-> type` annotation after the params (a single type token, reusing `typeNameP`; on the header line for both single-line and guarded forms). An annotation **seeds** inference (`checker.inferReturn`) before the body is visited, so the body — and any recursive call — is checked against the declared type (`subVtype`), and the body's type must be **concrete** (a body left `vUnknown` is rejected). May reference other fns / itself — anchored recursion infers with no annotation; otherwise-unanchored recursion needs the annotation (an un-annotated unanchored recursion is a build error); runtime is bounded by `maxEvalDepth`.
- **Guarded `fn`**: `| cond = result` lines, `cond` a `bool`, all `result`s the same type (numeric branches join to a common numeric supertype). The final case must be `otherwise` (enforced at build time, so matches are total). A guarded body is `ExprGuards`. A `-> type` return annotation, if present, goes on the header line (before the first `|`). A `fn` may take a whole-**vector** param and fold it with `reduce` (pure); `reduce` in a body with no vector in scope is a type error.
- **Numeric types & operators**: six types `rat, rat0+, rat0-, int, int0+, int0-` (domain {rat,int} × sign {any,≥0,≤0}); see `numtype`. `rat` is exact rational (`big.Rat`) at runtime. Operators take **any** numeric operand; each computes the tightest sound result (`+`/`-` via `addSign`, `*` via `mulSign`, `max`/`min` by bound analysis — so `rat0+ - rat0+ → rat`). **Assignability** (`numtype.Sub`, not equality) governs args & the combinator boundary, so `int0+` flows where `rat0+` is wanted. The literal `0` has an internal `Zero` sign, assignable to any numeric type; a positive literal is non-negative and a **negative literal** (`-5`, `-2.5`) is non-positive, so it flows into a `rat0-`/`int0-` target but is rejected where a non-negative type is required. Source literals are **exact finite decimals** (`-5`, `-2.5`) — exact, not approximate (`0.1` is precisely `1/10`), but decimal-only: there is no `/` rational literal syntax in source (a deliberate choice — the `p/q` form is wire-only; see Wire form). `-5` (no space) is a negative literal while `- 5` (space) is the `-` operator applied. Comparisons `> < >= <= == /=` take numeric **or** string operands (both operands the same kind — mixed is a build error) and yield `bool`; string order is lexicographic (bytewise UTF-8 == code-point order, matching the prover's `str.<`). Strings are `"..."` (`\" \\ \n \t` escapes), and a string is a storable slot leaf. The builder rejects type mismatches (`+ (> x 1) 2`) and results not assignable to the element type.
- **Combinator slots** (`zip`/`local`/`write`) take a single **atom** (a name or a parenthesised term), not a bare application. The fn is applied to element-typed args (E = a numeric type, a string, OR a struct), except `write` which is applied to the **whole vector**: zip → (E,E)->Sub E; local → (E)->Sub E; write → (X)->Sub E (X = the vector). `reduce fn init vec` is a **pure fold** (a value expression, not a combinator): its **init** is a literal (numeric or struct), its **vec** is any vector-typed value, and its result type is the lattice **fixpoint** of folding the fn over (acc, E) from the init's type. A **query** is a fn of the vector (`X -> result`); `reduce` folds that vector inside a helper `fn`. A query result may not be a vector (not serializable).
- **Structs**: `type X = { Name Type ... }` is a named struct type (fields space-separated, newline/whitespace-separated, ≥1, `{}` may span lines); `Type` is a numtype name or another struct type (nesting allowed; recursion rejected). `vector`'s element is a numtype name, a struct type name, an **inline struct body** (`type V = vector { ... }`), or a `W.Elem` reference. A **struct literal** `{ Name: expr, ... }` (comma-separated, trailing comma & newlines ok) constructs a struct; **field access** `a.Pos` (chainable) projects one. Struct assignability is **structural** (exact field set, each field `Sub`). Reserved: a user type name may not be a numtype name or `vector`. The brace-aware parsing is inline (the `{}` grammars are newline-tolerant), not a text pre-pass; struct-lit/field-access are `ExprStructLit`/`ExprField`.
- **Vector element access (`V.Elem`)**: `V.Elem` is a *type* token (parsed as one dotted token; only the `.Elem` member exists) usable anywhere a type token is — `fn`/update/query params and struct/vector element positions — resolving to vector `V`'s element type. `type X = V.Elem` (`KindElemRef`) **aliases** that element as a new named type (struct → a named struct type; numeric → a numeric alias); combined with an inline `vector { ... }` this defines a vector-of-struct with no separate `type X`. Aliases + inline/element resolution are on-demand + memoized with one shared cycle-detection set (`resolveStruct`/`resolveVectorElem`/`resolveAlias` in `builder`), so `type V = vector V.Elem` and alias↔vector loops are rejected. Update/query params are still wire-scalar: a numeric `V.Elem`/alias param is **normalized** to its concrete numtype name in the built `crdt.Method` (`resolveScalarParams`), a struct one is rejected.
- Primitives: `+ * -` and comparisons are operator tokens; `max`/`min` are ordinary identifiers (so `maxValue` is one name) recognised as primitives by the builder. A `fn` may not shadow a primitive.
- Params: `name::type`, names unique. A **`fn`** param may be numeric, a `string`, a struct type, OR a whole-**vector** type (fns receive struct/vector slots from combinators/queries). An **update** param may be numeric OR `string` (it crosses the POST JSON wire via `bindParams`: numeric validated by `numtype.Allows`, string by `utf8.ValidString`); a struct/vector update param is a build error. A **query** param must be numeric (the GET `?params=` wire is comma-split, so it can't carry arbitrary strings — `resolveScalarParams`'s `allowString=false` rejects them); bound into the body during eval.
- **Wire form**: numbers cross the HTTP/JSON boundary as **exact-rational strings** (`"5"`, `"1/2"`, input `"0.1"` → `1/10`) — never JSON numbers — so nothing is lost to float at I/O. The `/` rational form exists only at this wire boundary, not in DSL source (source literals are **exact finite decimals**) — an intentional asymmetry that keeps `/` out of source (ℚ stays closed, no division operator) without any loss of exactness. DSL numeric literals are likewise parsed exact (`parser.Expr.Num` is `*big.Rat`). Swagger types numeric fields as `string`.
- **Comments**: `#` runs to end of line and is insignificant whitespace **everywhere** — whole-line, trailing a statement, inside `{ … }` bodies, and between guarded-`fn` cases. Any other unrecognized non-blank line is a **parse error** (not silently skipped), so a typo like `udpate T.Add …` fails loudly. There is no block-comment form.
- **Consistency header** (opt-in, on both the update `POST` and query `GET`): a single **presence-based** header `X-Gospr-Sync-Ratio` (parsed by `parseSync` in gateway + sandbox). Absent → async local-only; **present → synchronous quorum**. Value = a fraction `f ∈ [0,1)` or the literal `all`. Quorum is **strict** `holders/N > f`, so `f=0.5` is a true majority (`floor(N/2)+1`) — the linearizable knee — `all` (= `node.AllRatio`, 1.0) requires every node, and a numeric `≥ 1` is a 400 (use `all`). Present-but-empty / NaN / ±Inf / out-of-range / non-numeric all 400. `f ≥ 0.5` (or `all`) ⇒ read/write quorums overlap ⇒ **linearizable**; a weaker `f` only guarantees synchronous replication to that fraction. Unmet quorum → **503**. A 503 on a *write* is indeterminate: the local slot is already mutated (retrying a non-idempotent `Add` can double-count). Documented in Swagger (header parameter + `503`) on every update/query op.
- A collection name = the node's collection key; a `type` defines reusable behavior, instantiated by `collection`.

## Extension points

- **New operator:** add its arity to `primitiveArity` in `builder/builder.go` and a result rule (a `numBin` case for arithmetic, or `cmpOps` membership for a bool result), add a case in `primOp` in `crdt/vector.go` (`arith` / `cmp`), add a `serialize` case in `prover/smt.go` so the convergence proof can reason about it (and `cmpOps` membership in `prover/prover.go` for a bool op), and (for a punctuation operator) a `Try(StringP(...))` alternative in `symOpP` (`parser/dsl.go`, multi-char before single-char); word-shaped operators need no parser change (they parse as identifiers).
- **New numeric type:** add the name + `NumType` to `numtype.Parse`/`String` and adjust `Sub`/`Join`/`Allows`; add it to `numTypeNameP` in `parser/dsl.go` (longest-match order). The builder/crdt/swagger consume `numtype` generically, so usually need no change.
- **New value type** (non-numeric): add a `parser.ValType` + builder `vkind`, handle it in `checker.typeOf`/`subVtype`/`unify`, add an `rtKind` + constructor in `crdt/vector.go` and a `rtToAny`/`valTypeSchema` case.
- **New expression form**: add/realize an `ExprKind`/`ElemKind` in `parser/types.go`, parse it in `dsl.go`, resolve it in `builder.env.resolve` and type-check it in `checker.typeOf`, evaluate it in `crdt/vector.go` (`eval`). Struct vectors (`ExprStructLit`/`ExprField`) are the worked example — grep those kinds across the layers to see the full seam list.
- **New expression form & the prover:** any new `ExprKind` reachable from a merge/update fn body also needs an `eval` case in `prover/prover.go` (lower it to a `sym`), else the convergence proof errors out. Structs (product/joint lattice), string leaves (`String` sort), and the `write`/`reduce`-domination path are done. The reduce-domination rule is a **concrete first cut**: it recognizes only a `max`/`min`-fold over a single field path (`foldProjection`) and backs the extracted bound with two Z3 lemmas — a fully general fold extractor is the natural next step. A further direction is struct-valued update/query wire params (currently `bindParams` handles numeric + string scalars).
- **Network propagation of messages:** the synchronous-quorum `SyncPush/Pull/AckMsg` are **already** exported plain-data DTOs carrying a concrete `crdt.WireSnapshot` (exact-rational strings) — gob/JSON-ready, pinned by a node codec round-trip test, so flipping to a real transport is a no-op for them. Still in-process today (channels). The older `gossipMsg`/`deployMsg` remain unexported with unexported fields; over the wire `deployMsg` needs `BuiltPlan` exported-encodable (`Model` holds `parser.Expr` trees, no closures; `Expr.Num` is `*big.Rat` with Gob/Text marshalers).

## Testing conventions

Tests use `github.com/stretchr/testify` (`assert` + `require`):
- `require.*` — fatal (stops test immediately); use for error checks and preconditions where subsequent code would panic or be meaningless
- `assert.*` — non-fatal (continues test); use for terminal value comparisons
- Typical pattern: `require.NoError(t, err)` for pipeline steps, `assert.Equal(t, want, got)` for value checks

## Gotchas / conventions

- **Committed-choice:** once a line's keyword prefix is consumed, `Or` commits — a recognized-but-malformed line (e.g. `type T = vector foo`) is a **parse error**, not skipped. `Try` strips `Consumed` on failure to re-enable backtracking; every `symOpP` alternative is `Try`-wrapped, as is each trailing-atom attempt in `applicationP`.
- **Name resolution invariant:** `ExprName` appears ONLY in parser output; after `Build`, every leaf is an `ExprVar` (bound param), `ExprRef` (symbol, with `Arity`/`RefKind`), or a literal (`ExprNumLit`/`ExprStrLit`). Proof/optimization passes can rely on this — no unresolved leaves in a `*Model` or `Functions` entry.
- **Totality of guards:** the builder requires a guarded `fn` to end with `otherwise` (a `GuardCase.Otherwise` marker — an always-true `Cond` does NOT count), so the runtime's "non-exhaustive guards" error is unreachable. `otherwise` may appear only as the last case.
- **`reduce` is a pure fold over a passed vector (no implicit state):** `reduce fn init vec` folds the `vec` **value** handed to it (built from `v.state` at a `write`/query boundary under `v.mu`), not `v.state` directly. It is valid in any body where a vector value is in scope; a merge/`local` fn has only element params (no vector), so `reduce` there is a type error, keeping those bodies pure. The prover reaches `reduce` only via a `write` body, where it lowers to a fresh `M` bounded by the reduce-domination rule (`prover.evalReduce`/`foldProjection`/`checkFoldLemmas`).
- **`write` reads the whole vector; `local` reads only the self slot.** `Apply` builds a working `kVector` snapshot (a copy of `v.state` plus the self default) for a `write` fn, then assigns the fn's result to the self slot only on success — same compute-next-then-mutate atomicity as `local`. The self slot must be in the snapshot both for the Lamport max and for the prover's `x = v[self]` assumption.
- **Prover soundness with structs:** a `sym` node must never carry a whole struct through scalar position, or the SMT flattening skips its fields and can spuriously prove a non-lattice merge. When adding a sym form reachable from a struct-valued fn, distribute it per-field (see `iteSym` in `prover/prover.go` and `TestProve_structGuardedNonCommutativeRejected` for why).
- **Return-type inference & `-> type` annotations:** `checker.inferReturn` is memoized DFS. **Un-annotated:** a recursive call caught mid-inference yields `vUnknown`, which unifies with any concrete type — anchored recursion resolves; a type left `vUnknown` (no concrete branch) is rejected. **Annotated** (`checker.annotations`, resolved by `resolveResultType` — numtype/struct/`V.Elem` plus `bool`/`string`; errors on an unknown token): the declared type is **seeded into the memo before the body is visited**, so recursion resolves to it instead of `vUnknown`. The body is then **strictly** validated — it must be concrete (`containsUnknown` rejects `vUnknown` **including one buried in a struct field**, so `subVtype`'s unknown-defers rule can't launder an unresolved recursive body) and `subVtype` the declared type. `containsUnknown` also gates the un-annotated inferred return before it is memoized and every query result (`checkValue`), so no nested `vUnknown` reaches `c.result` or a built artifact. This is what lets otherwise-unanchored recursion build. `bool`/`string` are **reserved** type names (they name builtin return types), so `type bool`/`type string` are rejected in `resolveTypes`.
- **Functions are first-class in the type model, not runtime values.** `typeOf` returns one `vtype` — a value kind or `vkFunc` (nesting `fnType`); function-ness is `isFunc`, not a `len(params)` count, so a `vkFunc` in an argument/struct-field/element position is a type error (no higher-order values/closures; runtime/prover untouched). `arityOf` stays the resolve-phase structural pre-check (runs before any `vtype`); the `vkFunc` signature is the checking-phase authority.
- `exprP` is recursive (parenthesised atoms hold expressions); the body is deferred to parse time (`exprP` returns a thunk) to break the parser-construction cycle.
- `Model.Queries`/`Updates` are always non-nil maps (init in `Build`) so swagger/crdt can range/lookup safely; `Model.Funcs`/`BuiltPlan.Functions` is the shared global function env.
- A local update on an absent slot defaults to 0 (Go zero map value). Merge adopts a remote slot if absent locally (zip over the union of node IDs) and is **atomic** — a failing user fn leaves state unchanged.
- Query and update may share a name (separate namespaces, dispatched by HTTP verb). Duplicate names *within* type/fn/merge/query/update/collection are build errors.
- `Expr` is a single struct with a `Kind` tag and union-ish fields; only the fields relevant to `Kind` are set (pointers for nested exprs). Keep it serializable — no closures, so optimization/proof passes stay possible.
- **Sync ack routing:** a peer replies to a `SyncPush/PullMsg` by looking up `peerByID[m.FromID]`, so every broadcast literal MUST set `FromID:n.id` (a zero `FromID` ⇒ acks can't route back ⇒ silent false `ErrQuorumUnreached`). `AddPeer` is `(id, p)` for this reason — all wiring sites pass the peer's ID. `pendingReq.seen` (distinct-ack dedup) is touched ONLY in the single message-loop goroutine (`handleSyncAck`), so it is lock-free by construction — don't read/write it elsewhere.
