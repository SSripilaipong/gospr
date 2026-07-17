# The gospr DSL

A reference for writing `.gos` programs. For the project overview and how to run a
cluster, see the [README](../README.md).

- [Mental model](#mental-model)
- [A first program](#a-first-program)
- [Types](#types)
- [Expressions](#expressions)
- [Functions](#functions)
- [Vectors & combinators](#vectors--combinators)
- [Queries](#queries)
- [Structs](#structs)
- [Element references (`V.Elem`)](#element-references-velem)
- [Worked example: an LWW register](#worked-example-an-lww-register)
- [Convergence: what the builder proves](#convergence-what-the-builder-proves)
- [Comments](#comments)
- [HTTP surface & wire format](#http-surface--wire-format)
- [Grammar summary](#grammar-summary)

## Mental model

A **type** is a *vector*: a map `nodeID -> value`, one **slot** per node. Each node
owns and mutates only its own slot. You define three behaviors as functional
expressions:

- **merge** — how two nodes' vectors combine (must be a join-semilattice).
- **update** — how a node changes its own slot (must be inflationary).
- **query** — a read over the whole vector.

A `collection` is a named runtime instance of a type. Before deploying, gospr
type-checks your program *and proves* the merge/update pair converges (CvRDT).

## A first program

```
type T = vector rat0+                  # a vector of non-negative rationals
merge T = zip max                      # combine slots elementwise with max

fn total v::T = reduce + 0 v           # sum every slot, starting from 0

query T.Value = total                  # expose that sum as a query
update T.Add k::rat0+ = local (+ k)    # add k to the calling node's own slot

collection Counter = T                 # instantiate it
```

This is a grow-only counter. Node A does `Add 3`, node B does `Add 5`; after they
gossip, both hold `{A:3, B:5}` (elementwise max), and `Value` reads `8`.

## Types

### Numeric types

Numbers form a small subtype lattice: **domain** {`rat`, `int`} × **sign**
{any, `≥0`, `≤0`}.

| Name | Meaning |
|---|---|
| `rat` | any rational |
| `rat0+` | rational ≥ 0 |
| `rat0-` | rational ≤ 0 |
| `int` | any integer |
| `int0+` | integer ≥ 0 |
| `int0-` | integer ≤ 0 |

At runtime a number is an **exact rational** (`math/big.Rat`) — never a float.

**Assignability** is by subtyping, not equality: an `int0+` value flows anywhere a
`rat0+` (or `rat`, `int`) is wanted. Operators accept *any* numeric operand and
compute the **tightest sound** result type:

- `int + int0+ → int`
- `rat0+ - rat0+ → rat` (a difference of non-negatives can be negative)
- `rat0+ * rat0+ → rat0+`

So a `vector rat0+` counter rejects `local (- k)` at build time, and a negative
`Add` at runtime.

### Literals

Source numeric literals are **exact finite decimals**: `0.1` is precisely `1/10`,
`-2.5` is `-5/2`. There is no `/` division form in source (rationals only appear
that way on the wire). Watch the spacing: `-5` is a *negative literal*, while
`- 5` is the `-` operator applied to `5`. A negative literal is non-positive, so it
flows into a `rat0-`/`int0-` target but is rejected where a non-negative type is
required.

### Other value types

- **`bool`** — produced by comparisons; used in guards.
- **`string`** — `"..."` literals (escapes `\" \\ \n \t`); a storable slot leaf.
  String order is lexicographic (UTF-8 bytewise = code-point order).
- **`struct`** — named fields; see [Structs](#structs).

## Expressions

Everything is **prefix application**: `+ a b`, `max b (+ c 1)`.

**Partial application** binds the **leftmost** argument first. `(+ k)` is
`\x -> + k x`, and a combinator supplies the slot as the *next* argument. This is
the one uniform rule — there is no right-section. For non-commutative operators
where you want the slot on the left (`x - k`), define a helper with that param
order:

```
fn rsub k::rat x::rat = - x k          # then: local (rsub k)  ⇒  x - k
```

`+ * max min` are commutative, so this only matters for `-`.

Application may **under**-saturate (that's partial application) but never
**over**-saturate (a build error).

**Operators:** `+`, `*`, `-`, and comparisons `> < >= <= == /=`. `max` and `min`
are ordinary identifiers recognized as primitives (so `maxValue` is just a name).
Comparisons take numeric **or** string operands (both the same kind — mixing is a
build error) and yield `bool`.

## Functions

```
fn add a::rat b::rat = + a b               # inferred return type
fn add a::rat b::rat -> rat = + a b        # optional -> type annotation
```

- Params are `name::type`, names unique; **at least one** param (zero-arg `fn`s are
  rejected). A param may be numeric, a `string`, a struct, or a whole **vector**.
- The body must be **saturated** (fully applied).
- The return type is **inferred**, or declared with `-> type` after the params.

### Guarded functions

Multi-line, value-typed branches. Each condition is a `bool`; all results share one
type. The final case **must** be `otherwise` (enforced at build time, so matches
are total). An annotation, if present, goes on the header line.

```
fn grade x::rat -> string
| (> x 90) = "A"
| (> x 60) = "C"
| otherwise = "F"
```

### Recursion

A `fn` may call itself. If a base case anchors the return type, inference succeeds
with no annotation. Otherwise-unanchored recursion **requires** a `-> type`
annotation (which seeds inference); an un-annotated unanchored recursion is a build
error. Runtime depth is bounded.

## Vectors & combinators

Three keywords carry a function-valued term and apply it at a specific boundary:

| Keyword | Where | Function shape |
|---|---|---|
| `zip f` | `merge` | `(E, E) -> E` — applied elementwise per node slot |
| `local f` | `update` | `(E) -> E` — mutates **only** the calling node's slot |
| `write f` | `update` | `(X) -> E` — reads the **whole vector** `X`, writes the self slot |

`E` is the element type (numeric, string, or struct); `X` is the whole vector. A
combinator slot takes a single **atom** — a name or a parenthesized term, not a
bare application:

```
merge T = zip max
update T.Add k::rat0+ = local (+ k)
```

`write` is what enables a Lamport clock (a slot computed from all slots) — see the
[LWW example](#worked-example-an-lww-register).

### `reduce` — a pure fold

`reduce fn init vec` folds `fn` over the vector's slots starting from `init`. It is
a **value expression, not a combinator** — usable anywhere a vector value is in
scope (inside a `fn`, a `write`, or a query), because it takes the vector
**explicitly**:

```
fn total v::T = reduce + 0 v            # v is the whole-vector param
```

`init` is a literal (numeric or a struct literal). Because a plain `merge`/`local`
body has only element params (no vector in scope), using `reduce` there is a type
error — which keeps those bodies pure.

## Queries

A query is a **function of the whole vector**: `qf :: X -> result`. The runtime
supplies the vector; you fold it with `reduce`.

```
fn total v::T = reduce + 0 v
query T.Value = total
```

A query may return a scalar, `bool`, `string`, or a whole struct — but **not** a
vector (not serializable). Query params must be numeric.

## Structs

A slot may hold a struct of named fields (numeric or string).

```
type X = {                             # named struct type; fields are `Name Type`
  Pos rat0+
  Neg rat0+
}
type VX = vector X                     # a vector of that struct
```

Structs are first-class values: **construct** with a literal, **access** a field
with a dot, and pass structs as `fn` params.

```
fn J a::X b::X = { Pos: max a.Pos b.Pos, Neg: max a.Neg b.Neg }   # struct literal + a.Pos access
fn incPos k::rat0+ s::X = { Pos: + s.Pos k, Neg: s.Neg }

merge VX = zip J                                                  # whole-struct merge (product lattice)
update VX.AddPos k::rat0+ = local (incPos k)

fn totals v::VX = reduce J { Pos: 0, Neg: 0 } v
query VX.Totals = totals                                         # a query may return a whole struct

collection C = VX
```

Struct assignability is **structural**: exact field set, each field assignable. The
whole-struct merge is proven as a product lattice (per field). A query can fold
structs and then project a field: `(reduce J { Pos: 0, Neg: 0 } v).Pos`.

## Element references (`V.Elem`)

`V.Elem` is a *type token* naming vector `V`'s element type, usable anywhere a type
token is (params, struct/vector element positions). You can also inline a struct in
the vector and name the element later:

```
type VX = vector { Pos rat0+  Neg rat0+ }   # inline struct element
type X = VX.Elem                            # alias the element as a named type
fn J a::VX.Elem b::VX.Elem = { Pos: max a.Pos b.Pos, Neg: max a.Neg b.Neg }
```

Alias and element resolution is on-demand and cycle-checked, so loops like
`type V = vector V.Elem` are rejected.

## Worked example: an LWW register

A last-writer-wins register stores `{ version, value }` and keeps the highest
version. The `version` is a Lamport clock: `1 + max(version over all slots)`, which
needs `write` (it reads every slot).

```
type X = vector { version int0+  value string }   # string is a storable leaf

merge X = zip Merge                                # per-slot argmax-by-version
fn Merge a::X.Elem b::X.Elem -> X.Elem
| (> a.version b.version) = a
| (< a.version b.version) = b
| (> a.value b.value)     = a                      # deterministic tiebreak on the value
| otherwise               = b

fn maxVer acc::int0+ e::X.Elem -> int0+ = max acc e.version
fn NextX s::string v::X -> X.Elem = { version: + 1 (reduce maxVer 0 v), value: s }
update X.Set s::string = write (NextX s)           # write: fn reads the WHOLE vector

fn LatestValue v::X -> string = (reduce Merge { version: 0, value: "" } v).value
query X.Value = LatestValue

collection Reg = X
```

## Convergence: what the builder proves

Every type must be a **CvRDT** (strong eventual consistency). gospr proves two
things via Z3 before it will deploy:

1. **merge is a join-semilattice** — commutative, associative, and idempotent.
2. **every update is inflationary** — `merge(x, update(x)) = update(x)`, so applying
   an update never moves a slot "backward" in merge's order.

Both decompose to scalar SMT obligations (structs flatten to their leaf fields,
strings use the SMT string sort). The proof is faithful to the runtime: Z3's `Real`
sort over-approximates ℚ, and the runtime computes in exact rationals, so a proof
over ℝ holds for every rational — there is no float-rounding gap.

Consequences to expect:

- A `max`/`min` merge proves; a **sum** or **average** merge is rejected (not
  idempotent).
- A negative update on a `rat0+`/`int0+` slot is rejected (not inflationary / not
  in the type).

Run `gospr check file.gos` to see acceptance or the rejection reason without
starting a server.

## Comments

`#` runs to end of line and is insignificant whitespace **everywhere** — whole
lines, trailing a statement, inside `{ … }`, and between guarded cases. There is no
block-comment form. Any other unrecognized non-blank line is a **parse error** (a
typo like `udpate T.Add …` fails loudly rather than being skipped).

## HTTP surface & wire format

| Action | Request |
|---|---|
| Deploy | `POST /api/cluster/deploy` (body = DSL source) |
| Update | `POST /api/collections/{collection}/{action}` |
| Query | `GET /api/collections/{collection}/{query}?params=...` |
| Docs | `GET /api/docs` · `GET /api/swagger.json` |

Numbers cross the wire as **exact-rational strings** — `"5"`, `"1/2"`, and input
`"0.1"` becomes `1/10` — never JSON numbers, so nothing is lost to float at I/O.
(The `p/q` form exists only on the wire; DSL source uses exact decimals.)

**Consistency header** (opt-in, on both update `POST` and query `GET`): a single
presence-based header `X-Gospr-Sync-Ratio`.

- **Absent** → asynchronous, local-only (gossip carries it eventually).
- **Present** → synchronous quorum. The value is a fraction `f ∈ [0,1)` or the
  literal `all`. Quorum is **strict** `holders/N > f`, so `0.5` is a true majority
  and `all` requires every node.
- `f ≥ 0.5` (or `all`) makes read/write quorums overlap ⇒ **linearizable**.
- An unmet quorum → **503**. A 503 on a *write* is indeterminate: the local slot is
  already mutated, so retrying a non-idempotent `Add` can double-count.

Present-but-empty, NaN, ±Inf, out-of-range, or a numeric `≥ 1` are all `400` (use
`all` for "every node").

## Grammar summary

```
# top-level lines (one per line; blank lines and # comments allowed anywhere)
type Name = vector <elem>            # elem: a numtype, a struct type, an inline { ... }, or W.Elem
type Name = { Field Type ... }       # named struct type (fields whitespace/newline separated)
type Name = W.Elem                   # alias another vector's element type

fn name p::type ... = expr           # single-line function (>= 1 param)
fn name p::type ... -> type = expr   # with an explicit return type
fn name p::type ... -> type          # guarded function
| cond = result                      #   cond :: bool
| otherwise = result                 #   final case must be `otherwise`

merge Name = zip fn                  # fn :: (E,E) -> E
update Name.Action p::type ... = local fn    # fn :: (E) -> E   (self slot)
update Name.Action p::type ... = write fn    # fn :: (X) -> E   (whole vector)
query  Name.Query  p::type ... = fn          # fn :: X -> result

collection Name = Type               # instantiate a type

# expressions (prefix application)
expr    ::= atom | app
app     ::= atom atom+               # f a b   (partial application allowed)
atom    ::= name | number | string | (expr) | { Field: expr, ... } | atom.Field
reduce  ::= reduce fn init vec       # a pure fold (a value, usable where a vector is in scope)
op      ::= + | * | - | > | < | >= | <= | == | /=
prim    ::= max | min                # identifiers recognized as primitives
```
