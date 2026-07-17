# gospr

A distributed **CRDT engine** driven by a small **functional DSL**,
with convergence **proven at deploy time** by the Z3 SMT solver.

You declare a CRDT type as a *vector* (a distributed-systems vector clock:
`nodeID -> value`, one slot per node) and write its **merge**, **query**, and
**update** behaviors as functional expressions. Before anything deploys, the
builder type-checks your program *and* proves it is a well-formed CvRDT (strong
eventual consistency) — an unprovable merge/update is rejected, not shipped.

```
type T = vector rat0+
merge T = zip max

fn total v::T = reduce + 0 v

query T.Value = total
update T.Add k::rat0+ = local (+ k)

collection Counter = T
```

That's a grow-only counter: each node adds to its own slot, `merge` takes the
elementwise `max`, and `Value` sums the slots.

→ **[Learn by example: docs/examples.md](docs/examples.md)** · **[Full language reference: docs/dsl.md](docs/dsl.md)**

## Features

- **Functional DSL** — merge/query/update written as small applicative expressions with user-defined functions (guarded clauses, recursion).
- **Exact arithmetic** — numbers are `math/big.Rat`; no float rounding at runtime or on the wire.
- **Numeric subtype lattice** — six types (`rat, rat0+, rat0-, int, int0+, int0-`) so a `rat0+` counter rejects a negative `Add` at build time.
- **Structs & LWW registers** — struct-valued slots (product lattice) and a Lamport-clock last-writer-wins register.
- **Deploy-time convergence proof** — merge join-laws + inflationary updates discharged to Z3; faithful to the exact-rational runtime (ℚ ⊆ ℝ).
- **Gossip + optional sync quorum** — eventually-consistent gossip by default; opt into a synchronous quorum (linearizable at ≥ 0.5) per request via a header.
- **Auto-generated Swagger** and an **observable web sandbox** for watching gossip and partitioning links live.

## Prerequisites

- **Go 1.26+**
- **`z3` on your `PATH`** — the builder proves convergence on every deploy, so a
  deploy (and `go test` for the `builder`/`prover`/`e2e` packages) **fails without it**.
  Verify with `z3 --version`. Install from [Z3Prover/z3](https://github.com/Z3Prover/z3)
  or `pip install z3-solver`.
- Node.js is only needed to *rebuild* the web sandbox; the built SPA is committed and
  embedded, so `go build` alone needs no Node.

## Quickstart

```bash
go build ./...

go run . sandbox run                          # web UI on http://localhost:9060 — start here
go run . server local --nodes=3               # 3-node cluster, gateways on :9050/:9051/:9052
go run . check file.gos                        # validate a .gos file (no server)
```

The **sandbox** is the best first experience: deploy a program, invoke updates,
run queries, watch snapshots gossip between nodes, and partition links by clicking
them. The **server** exposes each node's HTTP gateway with Swagger UI at `/api/docs`.

## Using it over HTTP

| Action | Request |
|---|---|
| Deploy a program | `POST /api/cluster/deploy` (body = DSL source) |
| Invoke an update | `POST /api/collections/{collection}/{action}` |
| Run a query | `GET /api/collections/{collection}/{query}?params=...` |
| API docs | `GET /api/docs` · `GET /api/swagger.json` |

Numbers cross the wire as **exact-rational strings** (`"5"`, `"1/2"`) — never JSON
numbers. Opt into a synchronous quorum with the `X-Gospr-Sync-Ratio` header
(a fraction in `[0,1)` or `all`). See [docs/dsl.md](docs/dsl.md#http-surface--wire-format) for details.

## Architecture

```
parser  →  builder  →  prover  →  node / crdt
```

- **parser** — text → AST (syntax only, no scope).
- **builder** — resolves types, type-checks, then gates on the prover.
- **prover** — proves CvRDT convergence per type via Z3.
- **crdt** — runtime: interprets the expression trees over per-node slots.
- **node** — lifecycle, gossip, and the synchronous-quorum layer.

Nodes run as goroutine groups and communicate **only via `chan any`** behind a
`Peer` seam — no shared memory. Every ~2s each node gossips a snapshot to a random
peer, which merges it with that type's user-defined `merge`. For the deep internal
map, see [`CLAUDE.md`](CLAUDE.md).

## Development

```bash
go test -timeout 60s ./...   # always pass -timeout; combinators can loop
go vet ./...
make all                     # rebuild the SPA (dist/) then go build, in order
make rebuild                 # rebuild the SPA only
```

After editing `sandbox/web/src`, run `make rebuild` so the new `dist/` is
re-embedded before running the Go binary.

| Package | Responsibility |
|---|---|
| `parser` | Syntax → `Plan` AST |
| `builder` | Type-check + resolve, gate on the prover |
| `prover` | Z3-backed CvRDT convergence proof |
| `crdt` | Runtime interpreter + wire codec |
| `node` | Lifecycle, gossip, sync quorum |
| `gateway` | HTTP API + Swagger |
| `sandbox` | Observable web playground |
| `numtype` | Numeric subtype lattice |
| `swagger` | OpenAPI generation |
| `e2e` | End-to-end / blackbox tests |

## Status

An MVP with a deliberately scoped surface. Nodes currently talk over in-process
channels behind a `Peer` seam rather than a real network transport — the
sync-quorum DTOs are already wire-ready, so swapping in a transport is the natural
next step. The DSL, type system, and deploy-time convergence proof are the core.
