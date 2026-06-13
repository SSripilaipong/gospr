package builder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gospr/parser"
)

// ---- unresolved-term builders (parser output shapes) ---------------

func name(n string) parser.Expr { return parser.Expr{Kind: parser.ExprName, Name: n} }
func num(n float64) parser.Expr { return parser.Expr{Kind: parser.ExprNumLit, Num: n} }
func app(head parser.Expr, args ...parser.Expr) parser.Expr {
	h := head
	ps := make([]*parser.Expr, len(args))
	for i := range args {
		a := args[i]
		ps[i] = &a
	}
	return parser.Expr{Kind: parser.ExprApp, Head: &h, Args: ps}
}
func zip(fn parser.Expr) parser.Expr {
	f := fn
	return parser.Expr{Kind: parser.ExprZip, Fn: &f}
}
func reduce(fn, init parser.Expr) parser.Expr {
	f, i := fn, init
	return parser.Expr{Kind: parser.ExprReduce, Fn: &f, Init: &i}
}
func local(fn parser.Expr) parser.Expr {
	f := fn
	return parser.Expr{Kind: parser.ExprLocal, Fn: &f}
}

// canonicalPlan hard-codes the (unresolved) AST for:
//
//	type T = vector real
//	fn lub a::real b::real = max a b
//	merge T = zip lub
//	query T.Value = reduce + 0
//	update T.Add k::real = local (+ k)
func canonicalPlan() parser.Plan {
	return parser.Plan{
		Types: []parser.TypeDef{{Name: "T", Elem: parser.ElemType{Kind: parser.KindReal}}},
		Functions: []parser.FnDef{
			{Name: "lub", Params: []parser.ParamSpec{{Name: "a", Type: "real"}, {Name: "b", Type: "real"}},
				Body: app(name("max"), name("a"), name("b"))},
		},
		Merges: []parser.MergeDef{
			{TypeName: "T", Body: zip(name("lub"))},
		},
		Queries: []parser.QueryDef{
			{TypeName: "T", MethodName: "Value", Body: reduce(name("+"), num(0))},
		},
		Updates: []parser.UpdateDef{
			{TypeName: "T", MethodName: "Add",
				Params: []parser.ParamSpec{{Name: "k", Type: "real"}},
				Body:   local(app(name("+"), name("k")))},
		},
	}
}

// Mandated integration test: hard-coded AST -> a correct, fully-resolved model.
func TestBuild_integration(t *testing.T) {
	built, err := Build(canonicalPlan())
	require.NoError(t, err)

	require.Contains(t, built.Models, "T")
	m := built.Models["T"]

	assert.Equal(t, parser.KindReal, m.Elem.Kind)

	// merge: zip lub -> Fn resolved to a user-function Ref of arity 2.
	assert.Equal(t, parser.ExprZip, m.Merge.Kind)
	require.NotNil(t, m.Merge.Fn)
	assert.Equal(t, parser.ExprRef, m.Merge.Fn.Kind)
	assert.Equal(t, "lub", m.Merge.Fn.Name)
	assert.Equal(t, parser.RefFunction, m.Merge.Fn.Ref)
	assert.Equal(t, 2, m.Merge.Fn.Arity)

	// function table: body resolved to App(Ref max, [Var a, Var b]).
	require.Contains(t, built.Functions, "lub")
	body := built.Functions["lub"].Body
	require.Equal(t, parser.ExprApp, body.Kind)
	require.NotNil(t, body.Head)
	assert.Equal(t, parser.ExprRef, body.Head.Kind)
	assert.Equal(t, parser.RefPrimitive, body.Head.Ref)
	require.Len(t, body.Args, 2)
	assert.Equal(t, parser.ExprVar, body.Args[0].Kind)
	assert.Equal(t, "a", body.Args[0].Name)

	assert.Contains(t, m.Queries, "Value")
	assert.Contains(t, m.Updates, "Add")

	// The built model must produce a working runtime instance using the fn merge.
	a := m.New("nodeA")
	b := m.New("nodeB")
	require.NoError(t, a.Apply("Add", []any{3.0}))
	require.NoError(t, b.Apply("Add", []any{5.0}))
	require.NoError(t, a.Merge(b.Snapshot())) // zip lub == max per slot
	got, err := a.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, 8.0, got)
}

func TestBuild_collectionReferencesType(t *testing.T) {
	plan := canonicalPlan()
	plan.Collections = []parser.CollectionSpec{{Name: "MyVec", Type: "T"}}
	built, err := Build(plan)
	require.NoError(t, err)
	require.Len(t, built.Collections, 1)
	assert.Equal(t, "MyVec", built.Collections[0].Name)
	c := built.Collections[0].Spec.New("nodeA")
	_, err = c.Query("Value", nil)
	require.NoError(t, err)
}

func TestBuild_unknownTypeForMerge(t *testing.T) {
	plan := canonicalPlan()
	plan.Merges = append(plan.Merges, parser.MergeDef{TypeName: "Ghost", Body: zip(name("max"))})
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_missingMerge(t *testing.T) {
	plan := canonicalPlan()
	plan.Merges = nil
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_unknownIdentifier(t *testing.T) {
	plan := canonicalPlan()
	plan.Merges[0].Body = zip(name("wat"))
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
	plan.Merges = append(plan.Merges, parser.MergeDef{TypeName: "T", Body: zip(name("min"))})
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_duplicateQuery(t *testing.T) {
	plan := canonicalPlan()
	plan.Queries = append(plan.Queries, parser.QueryDef{TypeName: "T", MethodName: "Value", Body: reduce(name("*"), num(1))})
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_duplicateUpdate(t *testing.T) {
	plan := canonicalPlan()
	plan.Updates = append(plan.Updates, parser.UpdateDef{TypeName: "T", MethodName: "Add", Body: local(app(name("+"), num(2)))})
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_duplicateCollection(t *testing.T) {
	plan := canonicalPlan()
	plan.Collections = []parser.CollectionSpec{{Name: "MyVec", Type: "T"}, {Name: "MyVec", Type: "T"}}
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_sameQueryAndUpdateNameAllowed(t *testing.T) {
	// query and update live in separate namespaces (GET vs POST).
	plan := canonicalPlan()
	plan.Queries = append(plan.Queries, parser.QueryDef{TypeName: "T", MethodName: "Add", Body: reduce(name("+"), num(0))})
	_, err := Build(plan)
	require.NoError(t, err)
}

func TestBuild_sectionUnknownParam(t *testing.T) {
	plan := canonicalPlan()
	plan.Updates[0].Body = local(app(name("+"), name("nope")))
	_, err := Build(plan)
	require.Error(t, err)
}

// ---- function-specific cases ---------------------------------------

func TestBuild_duplicateFunction(t *testing.T) {
	plan := canonicalPlan()
	plan.Functions = append(plan.Functions, parser.FnDef{Name: "lub",
		Params: []parser.ParamSpec{{Name: "a", Type: "real"}, {Name: "b", Type: "real"}},
		Body:   app(name("min"), name("a"), name("b"))})
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_functionShadowsPrimitive(t *testing.T) {
	plan := canonicalPlan()
	plan.Functions = append(plan.Functions, parser.FnDef{Name: "max",
		Params: []parser.ParamSpec{{Name: "a", Type: "real"}, {Name: "b", Type: "real"}},
		Body:   name("a")})
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_duplicateParam(t *testing.T) {
	plan := canonicalPlan()
	plan.Functions[0].Params = []parser.ParamSpec{{Name: "a", Type: "real"}, {Name: "a", Type: "real"}}
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_zeroParamFunctionRejected(t *testing.T) {
	plan := canonicalPlan()
	plan.Functions = append(plan.Functions, parser.FnDef{Name: "five", Body: num(5)})
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_overApplication(t *testing.T) {
	plan := canonicalPlan()
	// + takes 2 args, given 3
	plan.Functions[0].Body = app(name("+"), name("a"), name("b"), num(1))
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_functionBodyNotSaturated(t *testing.T) {
	plan := canonicalPlan()
	plan.Functions[0].Body = app(name("+"), name("a")) // arity 1, not a value
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_combinatorArityMismatch(t *testing.T) {
	plan := canonicalPlan()
	plan.Updates[0].Body = local(name("+")) // local wants arity 1, + is arity 2
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_recursionBuildsOK(t *testing.T) {
	plan := canonicalPlan()
	// fn loop a b = loop a b  (self-referential; allowed at build time)
	plan.Functions = append(plan.Functions, parser.FnDef{Name: "loop",
		Params: []parser.ParamSpec{{Name: "a", Type: "real"}, {Name: "b", Type: "real"}},
		Body:   app(name("loop"), name("a"), name("b"))})
	_, err := Build(plan)
	require.NoError(t, err)
}
