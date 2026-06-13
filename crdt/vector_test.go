package crdt

import (
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
func lit(n float64) parser.Expr { return parser.Expr{Kind: parser.ExprNumLit, Num: n} }
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
		Params: []parser.ParamSpec{{Name: "k", Type: "real"}},
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
	assert.Equal(t, 0.0, got)

	require.NoError(t, v.Apply("Add", []any{3.0}))
	require.NoError(t, v.Apply("Add", []any{2.0}))

	got, err = v.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, 5.0, got)
}

func TestVector_localOnlyAffectsLocalSlot(t *testing.T) {
	v := newT("nodeA")
	v.Apply("Add", []any{4.0})
	snap := v.Snapshot().(map[string]float64)
	assert.Len(t, snap, 1)
	assert.Equal(t, 4.0, snap["nodeA"])
}

func TestVector_mergeIsElementwiseMax(t *testing.T) {
	a := newT("nodeA")
	b := newT("nodeB")
	a.Apply("Add", []any{3.0}) // a: {nodeA:3}
	b.Apply("Add", []any{5.0}) // b: {nodeB:5}

	require.NoError(t, a.Merge(b.Snapshot()))
	// union: {nodeA:3, nodeB:5} -> reduce + = 8
	got, err := a.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, 8.0, got)

	// merge is max per slot: a re-adds, then a stale lower snapshot must not lower it.
	a.Apply("Add", []any{10.0}) // nodeA: 13
	stale := map[string]float64{"nodeA": 1.0}
	a.Merge(stale)
	snap := a.Snapshot().(map[string]float64)
	assert.Equal(t, 13.0, snap["nodeA"])
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

// ---- evaluator unit tests ------------------------------------------

func TestEval_application(t *testing.T) {
	// fn add a b = + a b
	add := Function{Name: "add", Params: []parser.ParamSpec{{Name: "a", Type: "real"}, {Name: "b", Type: "real"}},
		Body: app(prim("+"), vr("a"), vr("b"))}
	v := NewVector("n", zipMax(), nil, nil, map[string]Function{"add": add})

	got, err := v.eval(app(fnRef("add", 2), lit(2), lit(3)), nil, 0)
	require.NoError(t, err)
	require.False(t, got.isFunc)
	assert.Equal(t, 5.0, got.num)
}

func TestEval_partialApplication(t *testing.T) {
	v := NewVector("n", zipMax(), nil, nil, nil)
	// (+ 1) is a unary function value
	got, err := v.eval(app(prim("+"), lit(1)), nil, 0)
	require.NoError(t, err)
	require.True(t, got.isFunc)
	assert.Equal(t, 1, got.arity)
	r, err := got.call([]float64{4})
	require.NoError(t, err)
	assert.Equal(t, 5.0, r.num)
}

func TestEval_primitiveRef(t *testing.T) {
	v := NewVector("n", zipMax(), nil, nil, nil)
	got, err := v.eval(prim("max"), nil, 0)
	require.NoError(t, err)
	require.True(t, got.isFunc)
	r, err := got.call([]float64{3, 7})
	require.NoError(t, err)
	assert.Equal(t, 7.0, r.num)
}

// A non-terminating recursive merge fn must error (depth guard), not hang,
// and must leave state untouched (merge atomicity).
func TestVector_recursionGuardAndMergeAtomicity(t *testing.T) {
	loop := Function{Name: "loop", Params: []parser.ParamSpec{{Name: "a", Type: "real"}, {Name: "b", Type: "real"}},
		Body: app(fnRef("loop", 2), vr("a"), vr("b"))}
	zipLoop := func() parser.Expr {
		fn := fnRef("loop", 2)
		return parser.Expr{Kind: parser.ExprZip, Fn: &fn}
	}
	v := NewVector("nodeA", zipLoop(), nil, nil, map[string]Function{"loop": loop})
	v.state["nodeA"] = 1.0

	err := v.Merge(map[string]float64{"nodeA": 2.0}) // overlapping slot triggers loop
	require.Error(t, err)
	assert.Equal(t, 1.0, v.state["nodeA"], "state must be unchanged after a failing merge")
}
