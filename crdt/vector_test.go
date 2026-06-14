package crdt

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gospr/parser"
)

// ---- resolved-term builders (post-Build shapes) --------------------

func prim(name string) parser.Expr {
	return parser.Expr{Kind: parser.ExprRef, Name: name, Arity: 2, Ref: parser.RefPrimitive}
}
func fnRef(name string, arity int) parser.Expr {
	return parser.Expr{Kind: parser.ExprRef, Name: name, Arity: arity, Ref: parser.RefFunction}
}

// bigF builds an exact rational from a float for test fixtures.
func bigF(n float64) *big.Rat { return new(big.Rat).SetFloat64(n) }

func lit(n float64) parser.Expr { return parser.Expr{Kind: parser.ExprNumLit, Num: bigF(n)} }
func vr(name string) parser.Expr {
	return parser.Expr{Kind: parser.ExprVar, Name: name}
}
func app(head parser.Expr, args ...parser.Expr) parser.Expr {
	h := head
	ps := make([]*parser.Expr, len(args))
	for i := range args {
		a := args[i]
		ps[i] = &a
	}
	return parser.Expr{Kind: parser.ExprApp, Head: &h, Args: ps}
}

// helpers to build the canonical T model's exprs.
func zipMax() parser.Expr {
	fn := prim("max")
	return parser.Expr{Kind: parser.ExprZip, Fn: &fn}
}

func reducePlus0() Method {
	fn := prim("+")
	init := lit(0)
	return Method{Body: parser.Expr{Kind: parser.ExprReduce, Fn: &fn, Init: &init}}
}

func localAddK() Method {
	sec := app(prim("+"), vr("k")) // (+ k) == partial application
	return Method{
		Params: []parser.ParamSpec{{Name: "k", Type: "rat"}},
		Body:   parser.Expr{Kind: parser.ExprLocal, Fn: &sec},
	}
}

func newT(nodeID string) *VectorCRDT {
	return NewVector(nodeID, zipMax(),
		map[string]Method{"Value": reducePlus0()},
		map[string]Method{"Add": localAddK()},
		nil)
}

func TestVector_addAndValue(t *testing.T) {
	v := newT("nodeA")
	got, err := v.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, "0", got)

	require.NoError(t, v.Apply("Add", []any{"3"}))
	require.NoError(t, v.Apply("Add", []any{"2"}))

	got, err = v.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, "5", got)
}

// Exact rationals: ten increments of 0.1 sum to exactly 1, which IEEE floats
// would miss. This is the soundness payoff of the rat runtime.
func TestVector_exactRationalArithmetic(t *testing.T) {
	v := newT("nodeA")
	for i := 0; i < 10; i++ {
		require.NoError(t, v.Apply("Add", []any{"0.1"}))
	}
	got, err := v.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, "1", got)
}

func TestVector_localOnlyAffectsLocalSlot(t *testing.T) {
	v := newT("nodeA")
	v.Apply("Add", []any{"4"})
	snap := v.Snapshot().(map[string]*big.Rat)
	assert.Len(t, snap, 1)
	assert.Equal(t, big.NewRat(4, 1), snap["nodeA"])
}

func TestVector_mergeIsElementwiseMax(t *testing.T) {
	a := newT("nodeA")
	b := newT("nodeB")
	a.Apply("Add", []any{"3"}) // a: {nodeA:3}
	b.Apply("Add", []any{"5"}) // b: {nodeB:5}

	require.NoError(t, a.Merge(b.Snapshot()))
	// union: {nodeA:3, nodeB:5} -> reduce + = 8
	got, err := a.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, "8", got)

	// merge is max per slot: a re-adds, then a stale lower snapshot must not lower it.
	a.Apply("Add", []any{"10"}) // nodeA: 13
	stale := map[string]*big.Rat{"nodeA": big.NewRat(1, 1)}
	a.Merge(stale)
	snap := a.Snapshot().(map[string]*big.Rat)
	assert.Equal(t, big.NewRat(13, 1), snap["nodeA"])
}

// Snapshot must hand out deep copies: mutating a returned *big.Rat must not
// change the CRDT's own state (big.Rat is mutable).
func TestVector_snapshotIsDeepCopied(t *testing.T) {
	v := newT("nodeA")
	v.Apply("Add", []any{"4"})
	snap := v.Snapshot().(map[string]*big.Rat)
	snap["nodeA"].Add(snap["nodeA"], big.NewRat(100, 1)) // mutate the handed-out value

	again := v.Snapshot().(map[string]*big.Rat)
	assert.Equal(t, big.NewRat(4, 1), again["nodeA"], "CRDT state must be unaffected by snapshot mutation")
}

// Merge must not alias the remote snapshot into local state: mutating a remote
// value after a merge must not change the local CRDT (for both the merged-slot
// and the adopt-remote paths).
func TestVector_mergeDoesNotAliasRemote(t *testing.T) {
	a := newT("nodeA")
	a.Apply("Add", []any{"3"}) // a: {nodeA:3}

	remote := map[string]*big.Rat{
		"nodeA": big.NewRat(5, 1), // overlapping slot -> max(3,5)=5
		"nodeB": big.NewRat(7, 1), // absent locally -> adopted
	}
	require.NoError(t, a.Merge(remote))

	// Mutate the remote values after the merge.
	remote["nodeA"].Add(remote["nodeA"], big.NewRat(100, 1))
	remote["nodeB"].Add(remote["nodeB"], big.NewRat(100, 1))

	snap := a.Snapshot().(map[string]*big.Rat)
	assert.Equal(t, big.NewRat(5, 1), snap["nodeA"], "merged slot must not alias remote")
	assert.Equal(t, big.NewRat(7, 1), snap["nodeB"], "adopted slot must not alias remote")
}

func TestVector_unknownActionAndQuery(t *testing.T) {
	v := newT("nodeA")
	require.Error(t, v.Apply("Nope", nil))
	_, err := v.Query("Nope", nil)
	require.Error(t, err)
}

func TestVector_paramCountMismatch(t *testing.T) {
	v := newT("nodeA")
	require.Error(t, v.Apply("Add", nil))
}

// A value outside a param's declared numeric type is rejected at apply time:
// a negative increment on a rat0+ counter, and a non-integer on an int slot.
func TestVector_runtimeParamValidation(t *testing.T) {
	nonNeg := func() Method {
		sec := app(prim("+"), vr("k"))
		return Method{
			Params: []parser.ParamSpec{{Name: "k", Type: "rat0+"}},
			Body:   parser.Expr{Kind: parser.ExprLocal, Fn: &sec},
		}
	}
	v := NewVector("nodeA", zipMax(), nil, map[string]Method{"Add": nonNeg()}, nil)
	require.NoError(t, v.Apply("Add", []any{"5"}))
	require.Error(t, v.Apply("Add", []any{"-1"}), "negative value must be rejected for rat0+")

	intOnly := func() Method {
		sec := app(prim("+"), vr("k"))
		return Method{
			Params: []parser.ParamSpec{{Name: "k", Type: "int"}},
			Body:   parser.Expr{Kind: parser.ExprLocal, Fn: &sec},
		}
	}
	w := NewVector("nodeA", zipMax(), nil, map[string]Method{"Add": intOnly()}, nil)
	require.NoError(t, w.Apply("Add", []any{"3"}))
	require.Error(t, w.Apply("Add", []any{"2.5"}), "non-integer must be rejected for int")
}

// ---- evaluator unit tests ------------------------------------------

func TestEval_application(t *testing.T) {
	// fn add a b = + a b
	add := Function{Name: "add", Params: []parser.ParamSpec{{Name: "a", Type: "rat"}, {Name: "b", Type: "rat"}},
		Body: app(prim("+"), vr("a"), vr("b"))}
	v := NewVector("n", zipMax(), nil, nil, map[string]Function{"add": add})

	got, err := v.eval(app(fnRef("add", 2), lit(2), lit(3)), nil, 0)
	require.NoError(t, err)
	require.Equal(t, kNum, got.kind)
	assert.Equal(t, big.NewRat(5, 1), got.num)
}

func TestEval_partialApplication(t *testing.T) {
	v := NewVector("n", zipMax(), nil, nil, nil)
	// (+ 1) is a unary function value
	got, err := v.eval(app(prim("+"), lit(1)), nil, 0)
	require.NoError(t, err)
	require.Equal(t, kFunc, got.kind)
	assert.Equal(t, 1, got.arity)
	r, err := got.call([]rtVal{numVal(bigF(4))})
	require.NoError(t, err)
	assert.Equal(t, big.NewRat(5, 1), r.num)
}

func TestEval_primitiveRef(t *testing.T) {
	v := NewVector("n", zipMax(), nil, nil, nil)
	got, err := v.eval(prim("max"), nil, 0)
	require.NoError(t, err)
	require.Equal(t, kFunc, got.kind)
	r, err := got.call([]rtVal{numVal(bigF(3)), numVal(bigF(7))})
	require.NoError(t, err)
	assert.Equal(t, big.NewRat(7, 1), r.num)
}

// ---- bool / string / guards / reduce-in-expression -----------------

func str(s string) parser.Expr { return parser.Expr{Kind: parser.ExprStrLit, Str: s} }
func guards(cases ...parser.GuardCase) parser.Expr {
	return parser.Expr{Kind: parser.ExprGuards, Cases: cases}
}
func gcase(cond, result parser.Expr) parser.GuardCase {
	c, r := cond, result
	return parser.GuardCase{Cond: &c, Result: &r}
}
func gotherwise(result parser.Expr) parser.GuardCase {
	r := result
	return parser.GuardCase{Otherwise: true, Result: &r}
}

func TestEval_comparisonReturnsBool(t *testing.T) {
	v := NewVector("n", zipMax(), nil, nil, nil)
	got, err := v.eval(app(prim(">"), lit(5), lit(3)), nil, 0)
	require.NoError(t, err)
	require.Equal(t, kBool, got.kind)
	assert.True(t, got.b)

	got, err = v.eval(app(prim(">"), lit(2), lit(3)), nil, 0)
	require.NoError(t, err)
	assert.False(t, got.b)
}

func TestEval_stringLiteral(t *testing.T) {
	v := NewVector("n", zipMax(), nil, nil, nil)
	got, err := v.eval(str("hi"), nil, 0)
	require.NoError(t, err)
	require.Equal(t, kStr, got.kind)
	assert.Equal(t, "hi", got.str)
}

func TestEval_guardsSelectFirstTrueElseOtherwise(t *testing.T) {
	// fn grade x | (>= x 90) = "A" | (>= x 80) = "B" | otherwise = "F"
	body := guards(
		gcase(app(prim(">="), vr("x"), lit(90)), str("A")),
		gcase(app(prim(">="), vr("x"), lit(80)), str("B")),
		gotherwise(str("F")),
	)
	grade := Function{Name: "grade", Params: []parser.ParamSpec{{Name: "x", Type: "rat"}}, Body: body}
	v := NewVector("n", zipMax(), nil, nil, map[string]Function{"grade": grade})

	call := func(x float64) string {
		got, err := v.eval(app(fnRef("grade", 1), lit(x)), nil, 0)
		require.NoError(t, err)
		require.Equal(t, kStr, got.kind)
		return got.str
	}
	assert.Equal(t, "A", call(95))
	assert.Equal(t, "B", call(85))
	assert.Equal(t, "F", call(50)) // otherwise fall-through
}

func TestEval_reduceInExpression(t *testing.T) {
	// query body `+ 1 (reduce + 0)` folds the vector then adds 1.
	fn := prim("+")
	init := lit(0)
	reduceBody := parser.Expr{Kind: parser.ExprReduce, Fn: &fn, Init: &init}
	body := app(prim("+"), lit(1), reduceBody)
	v := NewVector("n", zipMax(), map[string]Method{"Q": {Body: body, Result: parser.TypeReal}}, nil, nil)
	v.state["a"] = big.NewRat(2, 1)
	v.state["b"] = big.NewRat(3, 1)
	got, err := v.Query("Q", nil)
	require.NoError(t, err)
	assert.Equal(t, "6", got) // 1 + (2 + 3)
}

// A non-terminating recursive merge fn must error (depth guard), not hang,
// and must leave state untouched (merge atomicity).
func TestVector_recursionGuardAndMergeAtomicity(t *testing.T) {
	loop := Function{Name: "loop", Params: []parser.ParamSpec{{Name: "a", Type: "rat"}, {Name: "b", Type: "rat"}},
		Body: app(fnRef("loop", 2), vr("a"), vr("b"))}
	zipLoop := func() parser.Expr {
		fn := fnRef("loop", 2)
		return parser.Expr{Kind: parser.ExprZip, Fn: &fn}
	}
	v := NewVector("nodeA", zipLoop(), nil, nil, map[string]Function{"loop": loop})
	v.state["nodeA"] = big.NewRat(1, 1)

	err := v.Merge(map[string]*big.Rat{"nodeA": big.NewRat(2, 1)}) // overlapping slot triggers loop
	require.Error(t, err)
	assert.Equal(t, big.NewRat(1, 1), v.state["nodeA"], "state must be unchanged after a failing merge")
}
