package builder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	require.NoError(t, err)

	require.Contains(t, built.Models, "T")
	m := built.Models["T"]

	assert.Equal(t, parser.KindReal, m.Elem.Kind)
	assert.Equal(t, parser.ExprZip, m.Merge.Kind)
	require.NotNil(t, m.Merge.Fn)
	assert.Equal(t, "max", m.Merge.Fn.Op)
	assert.Contains(t, m.Queries, "Value")
	assert.Contains(t, m.Updates, "Add")

	// The built model must produce a working runtime instance.
	c := m.New("nodeA")
	require.NoError(t, c.Apply("Add", []any{3.0}))
	got, err := c.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, 3.0, got)
}

func TestBuild_collectionReferencesType(t *testing.T) {
	plan := canonicalPlan()
	plan.Collections = []parser.CollectionSpec{{Name: "MyVec", Type: "T"}}
	built, err := Build(plan)
	require.NoError(t, err)
	require.Len(t, built.Collections, 1)
	assert.Equal(t, "MyVec", built.Collections[0].Name)
	// The collection spec is the model.
	c := built.Collections[0].Spec.New("nodeA")
	_, err = c.Query("Value", nil)
	require.NoError(t, err)
}

func TestBuild_unknownTypeForMerge(t *testing.T) {
	plan := canonicalPlan()
	plan.Merges = append(plan.Merges, parser.MergeDef{
		TypeName: "Ghost",
		Body:     plan.Merges[0].Body,
	})
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_missingMerge(t *testing.T) {
	plan := canonicalPlan()
	plan.Merges = nil
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_unknownOp(t *testing.T) {
	plan := canonicalPlan()
	bad := parser.Expr{Kind: parser.ExprFuncRef, Op: "wat"}
	plan.Merges[0].Body = parser.Expr{Kind: parser.ExprZip, Fn: &bad}
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_badParamType(t *testing.T) {
	plan := canonicalPlan()
	plan.Updates[0].Params = []parser.ParamSpec{{Name: "k", Type: "int"}}
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_queryParamsRejected(t *testing.T) {
	plan := canonicalPlan()
	plan.Queries[0].Params = []parser.ParamSpec{{Name: "m", Type: "real"}}
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_duplicateMerge(t *testing.T) {
	plan := canonicalPlan()
	minFn := parser.Expr{Kind: parser.ExprFuncRef, Op: "min"}
	plan.Merges = append(plan.Merges, parser.MergeDef{
		TypeName: "T",
		Body:     parser.Expr{Kind: parser.ExprZip, Fn: &minFn},
	})
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_duplicateQuery(t *testing.T) {
	plan := canonicalPlan()
	starFn := parser.Expr{Kind: parser.ExprFuncRef, Op: "*"}
	one := parser.Expr{Kind: parser.ExprNumLit, Num: 1}
	plan.Queries = append(plan.Queries, parser.QueryDef{
		TypeName: "T", MethodName: "Value",
		Body: parser.Expr{Kind: parser.ExprReduce, Fn: &starFn, Init: &one},
	})
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_duplicateUpdate(t *testing.T) {
	plan := canonicalPlan()
	two := parser.Expr{Kind: parser.ExprNumLit, Num: 2}
	plan.Updates = append(plan.Updates, parser.UpdateDef{
		TypeName: "T", MethodName: "Add",
		Body: parser.Expr{Kind: parser.ExprLocal,
			Fn: &parser.Expr{Kind: parser.ExprSection, Op: "+", Arg: &two}},
	})
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_duplicateCollection(t *testing.T) {
	plan := canonicalPlan()
	plan.Collections = []parser.CollectionSpec{
		{Name: "MyVec", Type: "T"},
		{Name: "MyVec", Type: "T"},
	}
	_, err := Build(plan)
	require.Error(t, err)
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
	_, err := Build(plan)
	require.NoError(t, err)
}

func TestBuild_sectionUnknownParam(t *testing.T) {
	plan := canonicalPlan()
	badRef := parser.Expr{Kind: parser.ExprParamRef, Param: "nope"}
	plan.Updates[0].Body = parser.Expr{Kind: parser.ExprLocal,
		Fn: &parser.Expr{Kind: parser.ExprSection, Op: "+", Arg: &badRef}}
	_, err := Build(plan)
	require.Error(t, err)
}
