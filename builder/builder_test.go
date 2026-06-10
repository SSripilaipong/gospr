package builder

import (
	"testing"

	"gospr/parser"
)

// canonicalPlan hard-codes the AST for:
//
//	type T = vector real
//	merge T = zip max
//	query T.Value = reduce + 0
//	update T.Add k::real = local (+ k)
func canonicalPlan() parser.Plan {
	maxFn := parser.Expr{Kind: parser.ExprFuncRef, Op: "max"}
	plusFn := parser.Expr{Kind: parser.ExprFuncRef, Op: "+"}
	zero := parser.Expr{Kind: parser.ExprNumLit, Num: 0}
	kRef := parser.Expr{Kind: parser.ExprParamRef, Param: "k"}
	section := parser.Expr{Kind: parser.ExprSection, Op: "+", Arg: &kRef}

	return parser.Plan{
		Types: []parser.TypeDef{{Name: "T", Elem: parser.ElemType{Kind: parser.KindReal}}},
		Merges: []parser.MergeDef{
			{TypeName: "T", Body: parser.Expr{Kind: parser.ExprZip, Fn: &maxFn}},
		},
		Queries: []parser.QueryDef{
			{TypeName: "T", MethodName: "Value",
				Body: parser.Expr{Kind: parser.ExprReduce, Fn: &plusFn, Init: &zero}},
		},
		Updates: []parser.UpdateDef{
			{TypeName: "T", MethodName: "Add",
				Params: []parser.ParamSpec{{Name: "k", Type: "real"}},
				Body:   parser.Expr{Kind: parser.ExprLocal, Fn: &section}},
		},
	}
}

// Mandated integration test: hard-coded AST -> a correct model.
func TestBuild_integration(t *testing.T) {
	built, err := Build(canonicalPlan())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := built.Models["T"]
	if !ok {
		t.Fatalf("model T not built; models = %v", built.Models)
	}
	if m.Elem.Kind != parser.KindReal {
		t.Fatalf("elem kind = %v, want real", m.Elem.Kind)
	}
	if m.Merge.Kind != parser.ExprZip || m.Merge.Fn.Op != "max" {
		t.Fatalf("merge = %+v, want zip max", m.Merge)
	}
	if _, ok := m.Queries["Value"]; !ok {
		t.Fatalf("missing query Value")
	}
	if _, ok := m.Updates["Add"]; !ok {
		t.Fatalf("missing update Add")
	}

	// The built model must produce a working runtime instance.
	c := m.New("nodeA")
	if err := c.Apply("Add", []any{3.0}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := c.Query("Value", nil)
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if got != 3.0 {
		t.Fatalf("Value = %v, want 3", got)
	}
}

func TestBuild_collectionReferencesType(t *testing.T) {
	plan := canonicalPlan()
	plan.Collections = []parser.CollectionSpec{{Name: "MyVec", Type: "T"}}
	built, err := Build(plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(built.Collections) != 1 || built.Collections[0].Name != "MyVec" {
		t.Fatalf("collections = %+v", built.Collections)
	}
	// The collection spec is the model.
	c := built.Collections[0].Spec.New("nodeA")
	if _, err := c.Query("Value", nil); err != nil {
		t.Fatalf("query via collection: %v", err)
	}
}

func TestBuild_unknownTypeForMerge(t *testing.T) {
	plan := canonicalPlan()
	plan.Merges = append(plan.Merges, parser.MergeDef{
		TypeName: "Ghost",
		Body:     plan.Merges[0].Body,
	})
	if _, err := Build(plan); err == nil {
		t.Fatalf("expected error for merge on unknown type")
	}
}

func TestBuild_missingMerge(t *testing.T) {
	plan := canonicalPlan()
	plan.Merges = nil
	if _, err := Build(plan); err == nil {
		t.Fatalf("expected error for type without merge")
	}
}

func TestBuild_unknownOp(t *testing.T) {
	plan := canonicalPlan()
	bad := parser.Expr{Kind: parser.ExprFuncRef, Op: "wat"}
	plan.Merges[0].Body = parser.Expr{Kind: parser.ExprZip, Fn: &bad}
	if _, err := Build(plan); err == nil {
		t.Fatalf("expected error for unknown op")
	}
}

func TestBuild_badParamType(t *testing.T) {
	plan := canonicalPlan()
	plan.Updates[0].Params = []parser.ParamSpec{{Name: "k", Type: "int"}}
	if _, err := Build(plan); err == nil {
		t.Fatalf("expected error for non-real param type")
	}
}

func TestBuild_queryParamsRejected(t *testing.T) {
	plan := canonicalPlan()
	plan.Queries[0].Params = []parser.ParamSpec{{Name: "m", Type: "real"}}
	if _, err := Build(plan); err == nil {
		t.Fatalf("expected error: query params not yet supported")
	}
}

func TestBuild_duplicateMerge(t *testing.T) {
	plan := canonicalPlan()
	minFn := parser.Expr{Kind: parser.ExprFuncRef, Op: "min"}
	plan.Merges = append(plan.Merges, parser.MergeDef{
		TypeName: "T",
		Body:     parser.Expr{Kind: parser.ExprZip, Fn: &minFn},
	})
	if _, err := Build(plan); err == nil {
		t.Fatalf("expected error for duplicate merge on T")
	}
}

func TestBuild_duplicateQuery(t *testing.T) {
	plan := canonicalPlan()
	starFn := parser.Expr{Kind: parser.ExprFuncRef, Op: "*"}
	one := parser.Expr{Kind: parser.ExprNumLit, Num: 1}
	plan.Queries = append(plan.Queries, parser.QueryDef{
		TypeName: "T", MethodName: "Value",
		Body: parser.Expr{Kind: parser.ExprReduce, Fn: &starFn, Init: &one},
	})
	if _, err := Build(plan); err == nil {
		t.Fatalf("expected error for duplicate query T.Value")
	}
}

func TestBuild_duplicateUpdate(t *testing.T) {
	plan := canonicalPlan()
	two := parser.Expr{Kind: parser.ExprNumLit, Num: 2}
	plan.Updates = append(plan.Updates, parser.UpdateDef{
		TypeName: "T", MethodName: "Add",
		Body: parser.Expr{Kind: parser.ExprLocal,
			Fn: &parser.Expr{Kind: parser.ExprSection, Op: "+", Arg: &two}},
	})
	if _, err := Build(plan); err == nil {
		t.Fatalf("expected error for duplicate update T.Add")
	}
}

func TestBuild_duplicateCollection(t *testing.T) {
	plan := canonicalPlan()
	plan.Collections = []parser.CollectionSpec{
		{Name: "MyVec", Type: "T"},
		{Name: "MyVec", Type: "T"},
	}
	if _, err := Build(plan); err == nil {
		t.Fatalf("expected error for duplicate collection MyVec")
	}
}

func TestBuild_sameQueryAndUpdateNameAllowed(t *testing.T) {
	// query and update live in separate namespaces (GET vs POST), so a query
	// and an update may share a name.
	plan := canonicalPlan()
	plusFn := parser.Expr{Kind: parser.ExprFuncRef, Op: "+"}
	zero := parser.Expr{Kind: parser.ExprNumLit, Num: 0}
	plan.Queries = append(plan.Queries, parser.QueryDef{
		TypeName: "T", MethodName: "Add", // same name as the update
		Body: parser.Expr{Kind: parser.ExprReduce, Fn: &plusFn, Init: &zero},
	})
	if _, err := Build(plan); err != nil {
		t.Fatalf("query and update sharing a name should be allowed, got: %v", err)
	}
}

func TestBuild_sectionUnknownParam(t *testing.T) {
	plan := canonicalPlan()
	badRef := parser.Expr{Kind: parser.ExprParamRef, Param: "nope"}
	plan.Updates[0].Body = parser.Expr{Kind: parser.ExprLocal,
		Fn: &parser.Expr{Kind: parser.ExprSection, Op: "+", Arg: &badRef}}
	if _, err := Build(plan); err == nil {
		t.Fatalf("expected error for unresolved section param")
	}
}
