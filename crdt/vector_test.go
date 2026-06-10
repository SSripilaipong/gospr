package crdt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gospr/parser"
)

// helpers to build the canonical T model's exprs.
func zipMax() parser.Expr {
	fn := parser.Expr{Kind: parser.ExprFuncRef, Op: "max"}
	return parser.Expr{Kind: parser.ExprZip, Fn: &fn}
}

func reducePlus0() Method {
	fn := parser.Expr{Kind: parser.ExprFuncRef, Op: "+"}
	init := parser.Expr{Kind: parser.ExprNumLit, Num: 0}
	return Method{Body: parser.Expr{Kind: parser.ExprReduce, Fn: &fn, Init: &init}}
}

func localAddK() Method {
	arg := parser.Expr{Kind: parser.ExprParamRef, Param: "k"}
	sec := parser.Expr{Kind: parser.ExprSection, Op: "+", Arg: &arg}
	return Method{
		Params: []parser.ParamSpec{{Name: "k", Type: "real"}},
		Body:   parser.Expr{Kind: parser.ExprLocal, Fn: &sec},
	}
}

func newT(nodeID string) *VectorCRDT {
	return NewVector(nodeID, zipMax(),
		map[string]Method{"Value": reducePlus0()},
		map[string]Method{"Add": localAddK()})
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
