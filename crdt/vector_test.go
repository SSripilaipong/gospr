package crdt

import (
	"testing"

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
	if got, _ := v.Query("Value", nil); got != 0.0 {
		t.Fatalf("empty Value = %v, want 0", got)
	}
	if err := v.Apply("Add", []any{3.0}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := v.Apply("Add", []any{2.0}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got, _ := v.Query("Value", nil); got != 5.0 {
		t.Fatalf("Value = %v, want 5", got)
	}
}

func TestVector_localOnlyAffectsLocalSlot(t *testing.T) {
	v := newT("nodeA")
	v.Apply("Add", []any{4.0})
	snap := v.Snapshot().(map[string]float64)
	if len(snap) != 1 || snap["nodeA"] != 4.0 {
		t.Fatalf("snapshot = %v, want {nodeA:4}", snap)
	}
}

func TestVector_mergeIsElementwiseMax(t *testing.T) {
	a := newT("nodeA")
	b := newT("nodeB")
	a.Apply("Add", []any{3.0}) // a: {nodeA:3}
	b.Apply("Add", []any{5.0}) // b: {nodeB:5}

	if err := a.Merge(b.Snapshot()); err != nil {
		t.Fatalf("merge: %v", err)
	}
	// union: {nodeA:3, nodeB:5} -> reduce + = 8
	if got, _ := a.Query("Value", nil); got != 8.0 {
		t.Fatalf("Value after merge = %v, want 8", got)
	}

	// merge is max per slot: a re-adds, then a stale lower snapshot must not lower it.
	a.Apply("Add", []any{10.0}) // nodeA: 13
	stale := map[string]float64{"nodeA": 1.0}
	a.Merge(stale)
	snap := a.Snapshot().(map[string]float64)
	if snap["nodeA"] != 13.0 {
		t.Fatalf("nodeA slot = %v, want 13 (max wins)", snap["nodeA"])
	}
}

func TestVector_unknownActionAndQuery(t *testing.T) {
	v := newT("nodeA")
	if err := v.Apply("Nope", nil); err == nil {
		t.Fatalf("expected error for unknown action")
	}
	if _, err := v.Query("Nope", nil); err == nil {
		t.Fatalf("expected error for unknown query")
	}
}

func TestVector_paramCountMismatch(t *testing.T) {
	v := newT("nodeA")
	if err := v.Apply("Add", nil); err == nil {
		t.Fatalf("expected error for missing param")
	}
}
