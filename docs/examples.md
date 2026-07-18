# gospr by example

A ladder of complete `.gos` programs. Each one is a full, deployable program that
**builds as-is**, and each teaches **one new idea** on top of the previous. Read
top to bottom.

Run any example either way:

```bash
gospr check file.gos       # parse + type-check + prove convergence, no server
gospr sandbox run          # then paste the program into the deploy modal
```

For the full language, see [dsl.md](dsl.md).

> These programs are the authoritative example set: `e2e/examples_doc_test.go`
> builds every one of them, so anything shown here is guaranteed to pass
> `gospr check`.

## 1. Grow-only counter

The anatomy of a gospr program — every keyword you need for a working CRDT.

```gos
type T = vector rat0+                  # a vector: one non-negative slot per node
merge T = zip max                      # combine two vectors elementwise with max

fn total v::T = reduce + 0 v           # fold every slot into a running sum

query T.Value = total                  # expose that sum as a read
update T.Add k::rat0+ = local (+ k)    # add k to the calling node's OWN slot

collection Counter = T                 # instantiate the type
```

- A **type** is a vector (`nodeID -> value`); each node only ever writes its own slot.
- `local (+ k)` is a partial application — the combinator supplies the slot as the next argument, so it computes `slot + k`.
- `merge` merges slots with `max`, and `reduce + 0 v` sums them; two concurrent `Add`s on different nodes both survive.
- `rat0+` is a non-negative rational: `Add -1` is rejected at build time. → [numeric types](dsl.md#numeric-types)

## 1a. A keyed collection

One declaration can expose an independent counter for every user ID. Documents
need no create step; querying an untouched ID reads the type's zero state.

```gos
type T = vector rat0+
merge T = zip max

fn total v::T = reduce + 0 v
query T.Value = total
update T.Add k::rat0+ = local (+ k)

collection Counters[userId::string] = T
```

- Update one document with `POST /api/collections/Counters/alice/Add`.
- Query it with `GET /api/collections/Counters/alice/Value`.
- The key names the URL parameter only; it is not visible inside DSL functions.

## 2. High-water gauge

A different lattice: the update isn't accumulation, and the query returns an extremum.

```gos
type G = vector rat0+
merge G = zip max

fn highest v::G = reduce max 0 v          # the largest slot, not the sum

update G.Observe k::rat0+ = local (max k) # raise this node's slot to at least k
query G.Max = highest

collection Gauge = G
```

- An update can be **any inflationary operation**, not just `+` — here `max k` only ever raises a slot, so it converges.
- `reduce max 0 v` reads the maximum across nodes: a distributed high-water mark.

## 3. A query with a parameter

Queries can take arguments and return a `bool`.

```gos
type T = vector rat0+
merge T = zip max

fn total v::T = reduce + 0 v
fn reached goal::rat0+ v::T = >= (reduce + 0 v) goal  # compares the sum to a goal

update T.Add k::rat0+ = local (+ k)
query T.Total = total
query T.Reached goal::rat0+ = reached goal            # goal comes from the request

collection Counter = T
```

- A **query is a function of the whole vector** (`T -> result`); extra params (like `goal`) are supplied per request, and `reached goal` partially applies to leave a `T -> bool`.
- Comparison operators `> < >= <= == /=` yield a `bool`. → [expressions](dsl.md#expressions)
- Query params must be numeric.

## 4. Grades with a guarded function

Multi-clause functions and `string` results.

```gos
type T = vector rat0+
merge T = zip max

fn grade x::rat0+ -> string             # -> type annotates the return type
| (>= x 100) = "gold"                   # each guard is a bool; first match wins
| (>= x 50)  = "silver"
| otherwise  = "bronze"                 # otherwise is mandatory and must be last

fn rank v::T = grade (reduce + 0 v)     # compose: grade applied to the total

update T.Add k::rat0+ = local (+ k)
query T.Rank = rank

collection Counter = T
```

- A **guarded `fn`** picks the first branch whose condition holds; all branches must share one result type, and the final case must be `otherwise` (so matches are total).
- Strings are `"..."` literals and a valid query result.
- Functions compose freely — `rank` feeds the folded total into `grade`. → [functions](dsl.md#functions)

## 5. PN-counter (a struct slot)

A slot can hold a **struct** — here two grow-only halves, so the value can go down as well as up.

```gos
type PN = {
  Pos rat0+
  Neg rat0+
}
type C = vector PN

fn Join a::PN b::PN = { Pos: max a.Pos b.Pos, Neg: max a.Neg b.Neg }  # per-field merge
fn Sum  a::PN b::PN = { Pos: + a.Pos b.Pos,   Neg: + a.Neg b.Neg }    # per-field sum (fold)

fn addPos k::rat0+ s::PN = { Pos: + s.Pos k, Neg: s.Neg }
fn addNeg k::rat0+ s::PN = { Pos: s.Pos, Neg: + s.Neg k }

fn total v::C = reduce Sum { Pos: 0, Neg: 0 } v
fn net v::C = - (total v).Pos (total v).Neg           # field access on a struct value

merge C = zip Join

update C.Inc k::rat0+ = local (addPos k)
update C.Dec k::rat0+ = local (addNeg k)

query C.Net = net                                     # project one number out of the fold
query C.Totals = total                                # ...or return the whole struct

collection Counter = C
```

- `type X = { ... }` defines a struct; `{ Pos: e, Neg: e }` constructs one; `a.Pos` reads a field.
- Structs are first-class `fn` params, and the whole-struct `merge` is proven field-by-field (a product lattice).
- Decrement works because both halves only grow; `net = Pos - Neg`. A query may return a scalar (`Net`) **or** a whole struct (`Totals`). → [structs](dsl.md#structs)

## 6. The same, with less boilerplate

Identical behavior to example 5, but the element type is written inline and named via `.Elem`.

```gos
type C = vector { Pos rat0+  Neg rat0+ }   # inline struct element — no separate `type PN`
type PN = C.Elem                           # alias the element type when you want a name

fn Join a::C.Elem b::C.Elem = { Pos: max a.Pos b.Pos, Neg: max a.Neg b.Neg }
fn addPos k::rat0+ s::PN = { Pos: + s.Pos k, Neg: s.Neg }

merge C = zip Join
update C.Inc k::rat0+ = local (addPos k)

collection Counter = C
```

- `C.Elem` is a type token for the vector's element, usable anywhere a type is; `type PN = C.Elem` gives it a name.
- Lets you define a vector-of-struct without a standalone struct declaration. → [element references](dsl.md#element-references-velem)

## 7. LWW register (last-writer-wins)

The capstone: an update that reads the **whole** vector to build a Lamport clock, so the latest write wins.

```gos
type X = vector { version int0+  value string }   # a string is a storable slot leaf

merge X = zip Merge                                # keep the highest-versioned slot
fn Merge a::X.Elem b::X.Elem -> X.Elem
| (> a.version b.version) = a
| (< a.version b.version) = b
| (> a.value b.value)     = a                      # tiebreak on the string value
| otherwise               = b

fn maxVer acc::int0+ e::X.Elem -> int0+ = max acc e.version
fn NextX s::string v::X -> X.Elem = { version: + 1 (reduce maxVer 0 v), value: s }
update X.Set s::string = write (NextX s)           # write: the fn sees the WHOLE vector

fn LatestValue v::X -> string = (reduce Merge { version: 0, value: "" } v).value
query X.Value = LatestValue

collection Reg = X
```

- **`write f`** (unlike `local f`) applies `f` to the entire vector, which is what a Lamport clock needs: `version = 1 + max(version over all slots)`.
- `merge` is argmax-by-version with a deterministic string tiebreak, so every replica converges on the same latest write.
- `string` values compare lexicographically, both in guards and in the convergence proof. → [worked example](dsl.md#worked-example-an-lww-register)

---

That's the whole language surface worth a dedicated example. A few finer points —
the leftmost-argument rule for partial application (and the `rsub` workaround for
non-commutative operators), recursive functions, and return-type inference — are
covered in [dsl.md](dsl.md).
