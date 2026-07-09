package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gospr/builder"
	"gospr/crdt"
	"gospr/parser"
)

const src = `type T = vector rat0+

fn lub a::rat0+ b::rat0+ = max a b

merge T = zip lub

fn total v::T = reduce + 0 v
query T.Value = total

update T.Add k::rat0+ = local (+ k)
`

// End-to-end: code string -> parse -> build -> model instance -> behaviors.
func TestE2E_vectorModel(t *testing.T) {
	plan, err := parser.Parse(src)
	require.NoError(t, err)

	built, err := builder.Build(plan)
	require.NoError(t, err)

	require.Contains(t, built.Models, "T")
	model := built.Models["T"]

	a := model.New("nodeA")
	b := model.New("nodeB")

	// Empty vector reduces to the init (0).
	got, err := a.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, "0", got)

	// `local (+ k)` updates only the calling node's slot.
	require.NoError(t, a.Apply("Add", []any{"3"}))
	require.NoError(t, b.Apply("Add", []any{"5"}))

	got, err = a.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, "3", got)

	got, err = b.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, "5", got)

	// `zip max` merge unions the slots elementwise; reduce + sums them.
	require.NoError(t, a.Merge(b.Snapshot()))
	require.NoError(t, b.Merge(a.Snapshot()))

	got, err = a.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, "8", got)

	got, err = b.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, "8", got)

	// Merge takes per-slot max: a stale lower value must not lower a slot.
	a.Apply("Add", []any{"10"}) // nodeA slot -> 13
	stale := crdt.WireSnapshot{Slots: map[string]crdt.SlotWire{"nodeA": {Num: "1"}}}
	require.NoError(t, a.MergeWire(stale))

	got, err = a.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, "18", got) // 13 (nodeA) + 5 (nodeB)
}

// End-to-end parameterized query: source -> parse -> build (type-checks the
// param'd body) -> instance -> Query with a param that varies the result.
func TestE2E_queryParam(t *testing.T) {
	src := `type T = vector rat0+
fn lub a::rat0+ b::rat0+ = max a b
merge T = zip lub
fn above m::rat0+ v::T = > (reduce max 0 v) m
query T.Above m::rat0+ = above m
update T.Add k::rat0+ = local (+ k)
`
	plan, err := parser.Parse(src)
	require.NoError(t, err)
	built, err := builder.Build(plan)
	require.NoError(t, err)

	a := built.Models["T"].New("nodeA")
	require.NoError(t, a.Apply("Add", []any{"10"}))

	got, err := a.Query("Above", []any{"5"})
	require.NoError(t, err)
	assert.Equal(t, true, got)

	got, err = a.Query("Above", []any{"20"})
	require.NoError(t, err)
	assert.Equal(t, false, got)

	// A missing / out-of-domain param is rejected before eval.
	_, err = a.Query("Above", nil)
	require.Error(t, err)
	_, err = a.Query("Above", []any{"-1"}) // negative not a valid rat0+
	require.Error(t, err)
}

// End-to-end return annotation: a recursive helper `sumdown` carries a `-> int0+`
// return type (which resolves its recursive call's type at build time), is called
// from a query, and evaluates correctly at runtime. Confirms the annotation is a
// build-time contract that leaves the runtime path intact.
func TestE2E_returnAnnotatedRecursiveQueryFn(t *testing.T) {
	src := `type T = vector int0+
fn sumdown n::int -> int
| (> n 0) = + n (sumdown (- n 1))
| otherwise = 0
merge T = zip max
fn triangle v::T = sumdown (reduce max 0 v)
query T.Triangle = triangle
update T.Set k::int0+ = local (max k)
`
	plan, err := parser.Parse(src)
	require.NoError(t, err)
	built, err := builder.Build(plan)
	require.NoError(t, err)

	a := built.Models["T"].New("nodeA")
	require.NoError(t, a.Apply("Set", []any{"4"}))

	got, err := a.Query("Triangle", nil)
	require.NoError(t, err)
	assert.Equal(t, "10", got) // 4+3+2+1+0
}

// End-to-end struct vector: a PN-counter whose slot is a struct { Pos, Neg }.
// Exercises struct types, struct-typed fn params, struct construction literals,
// dot field access, a whole-struct `zip` merge (product lattice: per-field max),
// struct-typed `local` updates, and a query that folds struct slots with a
// (non-idempotent, query-only) per-field sum then projects a field. The whole
// pipeline runs — including the z3 convergence proof of the struct merge/updates.
func TestE2E_structVectorPNCounter(t *testing.T) {
	src := `type X = {
  Pos rat0+
  Neg rat0+
}
type VX = vector X
fn J a::X b::X = { Pos: max a.Pos b.Pos, Neg: max a.Neg b.Neg }
fn S a::X b::X = { Pos: + a.Pos b.Pos, Neg: + a.Neg b.Neg }
fn incPos k::rat0+ s::X = { Pos: + s.Pos k, Neg: s.Neg }
fn incNeg k::rat0+ s::X = { Pos: s.Pos, Neg: + s.Neg k }
merge VX = zip J
update VX.AddPos k::rat0+ = local (incPos k)
update VX.AddNeg k::rat0+ = local (incNeg k)
fn net v::VX = - (reduce S { Pos: 0, Neg: 0 } v).Pos (reduce S { Pos: 0, Neg: 0 } v).Neg
query VX.Net = net
fn totalsFn v::VX = reduce S { Pos: 0, Neg: 0 } v
query VX.Totals = totalsFn
collection C = VX
`
	plan, err := parser.Parse(src)
	require.NoError(t, err)
	built, err := builder.Build(plan)
	require.NoError(t, err)

	m := built.Models["VX"]
	require.NotNil(t, m)
	require.True(t, m.Elem.Struct)

	a := m.New("nodeA")
	b := m.New("nodeB")
	require.NoError(t, a.Apply("AddPos", []any{"5"}))
	require.NoError(t, a.Apply("AddNeg", []any{"2"}))
	require.NoError(t, b.Apply("AddPos", []any{"3"}))

	// A negative increment on a rat0+ field is rejected at the wire boundary.
	require.Error(t, a.Apply("AddPos", []any{"-1"}))

	require.NoError(t, a.Merge(b.Snapshot()))
	require.NoError(t, b.Merge(a.Snapshot()))

	// Net = (5+3) - 2 = 6 on both nodes after convergence.
	for _, c := range []struct {
		name string
		crdt interface {
			Query(string, []any) (any, error)
		}
	}{{"a", a}, {"b", b}} {
		got, err := c.crdt.Query("Net", nil)
		require.NoError(t, err, c.name)
		assert.Equal(t, "6", got, c.name)
	}

	// A struct-valued query returns a JSON object of exact-rational strings.
	totals, err := a.Query("Totals", nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"Pos": "8", "Neg": "2"}, totals)

	// The wire snapshot carries struct slots and round-trips through MergeWire.
	c := m.New("nodeC")
	require.NoError(t, c.MergeWire(a.SnapshotWire()))
	got, err := c.Query("Net", nil)
	require.NoError(t, err)
	assert.Equal(t, "6", got)
}

// End-to-end: a guarded, string-returning function consumed by a query that
// maps the reduced vector to a grade. Exercises guards, comparisons, string
// results, otherwise, and reduce-in-a-query-expression together.
func TestE2E_guardedGradeQuery(t *testing.T) {
	src := `type Scores = vector rat0+
fn myScore x::rat
| (> x 90) = "You got a A"
| (> x 80) = "You got a B"
| (> x 70) = "You got a C"
| otherwise = "You got a F"
merge Scores = zip max
fn gradeOf v::Scores = myScore (reduce max 0 v)
query Scores.Grade = gradeOf
update Scores.Add k::rat0+ = local (+ k)
collection Scores = Scores
`
	plan, err := parser.Parse(src)
	require.NoError(t, err)
	built, err := builder.Build(plan)
	require.NoError(t, err)
	m := built.Models["Scores"]

	// empty vector folds to 0 -> otherwise branch
	got, err := m.New("nodeA").Query("Grade", nil)
	require.NoError(t, err)
	assert.Equal(t, "You got a F", got)

	v := m.New("nodeA")
	require.NoError(t, v.Apply("Add", []any{"95"}))
	got, err = v.Query("Grade", nil)
	require.NoError(t, err)
	assert.Equal(t, "You got a A", got)

	// lower the working set: a fresh node summing to 75 -> grade C
	w := m.New("nodeB")
	require.NoError(t, w.Apply("Add", []any{"75"}))
	got, err = w.Query("Grade", nil)
	require.NoError(t, err)
	assert.Equal(t, "You got a C", got)
}

// End-to-end LWW register: a `{ version, value }` slot whose merge is per-slot
// argmax-by-version (string-value tiebreak) and whose update is a `write` that
// stamps version = 1 + max(version over the WHOLE vector) — a Lamport clock. This
// exercises string slot leaves, the `write` combinator, the vector-value `reduce`,
// and (at build time) the reduce-domination convergence proof. It asserts real LWW
// convergence: concurrent writes on two nodes converge to the same value, and a
// causally-later write (higher version) wins regardless of merge order.
func TestE2E_lwwRegister(t *testing.T) {
	const src = `type X = vector {
  version int0+
  value   string
}
merge X = zip Merge
fn Merge a::X.Elem b::X.Elem -> X.Elem
| (> a.version b.version) = a
| (< a.version b.version) = b
| (> a.value b.value)     = a
| otherwise               = b
fn maxVer acc::int0+ e::X.Elem -> int0+ = max acc e.version
fn NextX s::string v::X -> X.Elem = { version: + 1 (reduce maxVer 0 v), value: s }
update X.Set s::string = write (NextX s)
fn LatestValue v::X -> string = (reduce Merge { version: 0, value: "" } v).value
query  X.Value = LatestValue
collection Reg = X
`
	plan, err := parser.Parse(src)
	require.NoError(t, err)
	built, err := builder.Build(plan)
	require.NoError(t, err)
	m := built.Models["X"]
	require.NotNil(t, m)

	a := m.New("nodeA")
	b := m.New("nodeB")

	// Concurrent writes: each node reads its own (empty) vector, so both stamp
	// version 1 — the tie is broken deterministically by the larger string value.
	require.NoError(t, a.Apply("Set", []any{"hello"}))
	require.NoError(t, b.Apply("Set", []any{"world"}))

	require.NoError(t, a.Merge(b.Snapshot()))
	require.NoError(t, b.Merge(a.Snapshot()))

	for _, c := range []struct {
		name string
		crdt crdt.CRDT
	}{{"a", a}, {"b", b}} {
		got, err := c.crdt.Query("Value", nil)
		require.NoError(t, err, c.name)
		assert.Equal(t, "world", got, c.name) // tie on version 1 -> "world" > "hello"
	}

	// A causally-later write on nodeA (which has seen version 1) stamps version 2,
	// so it dominates regardless of the string ordering — real last-writer-wins.
	require.NoError(t, a.Apply("Set", []any{"abc"}))
	require.NoError(t, b.Merge(a.Snapshot()))
	got, err := b.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, "abc", got) // version 2 beats version 1, though "abc" < "world"
}

// `local (- k)` under a max merge is NOT inflationary — partial application
// binds the LEFT operand first, so it means \x -> k - x (k minus the current
// slot), which can shrink the slot. The convergence prover rejects it at build
// time: max(x, k-x) != k-x in general (e.g. x=5, k=0 gives max(5,-5)=5 != -5).
// (The left-binding application semantics themselves are exercised at the
// runtime level in crdt/vector_test.go's TestEval_partialApplication.)
func TestE2E_nonInflationaryUpdateRejected(t *testing.T) {
	src := `type C = vector rat
merge C = zip max
fn total v::C = reduce + 0 v
query C.Value = total
update C.Sub k::rat = local (- k)
`
	plan, err := parser.Parse(src)
	require.NoError(t, err)
	_, err = builder.Build(plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not inflationary")
}
