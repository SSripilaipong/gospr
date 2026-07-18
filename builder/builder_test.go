package builder

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gospr/crdt"
	"gospr/numtype"
	"gospr/parser"
)

// ---- unresolved-term builders (parser output shapes) ---------------

func name(n string) parser.Expr { return parser.Expr{Kind: parser.ExprName, Name: n} }
func num(n float64) parser.Expr {
	return parser.Expr{Kind: parser.ExprNumLit, Num: new(big.Rat).SetFloat64(n)}
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
func zip(fn parser.Expr) parser.Expr {
	f := fn
	return parser.Expr{Kind: parser.ExprZip, Fn: &f}
}

// reduce builds a `reduce fn init v` node folding the vector variable named "v"
// (the conventional vector param of the fn-of-vector a query/write body wraps).
func reduce(fn, init parser.Expr) parser.Expr {
	f, i, v := fn, init, name("v")
	return parser.Expr{Kind: parser.ExprReduce, Fn: &f, Init: &i, Vec: &v}
}
func vfn(fnName, typeName string, params []parser.ParamSpec, body parser.Expr) parser.FnDef {
	ps := append(append([]parser.ParamSpec{}, params...), parser.ParamSpec{Name: "v", Type: typeName})
	return parser.FnDef{Name: fnName, Params: ps, Body: body}
}
func local(fn parser.Expr) parser.Expr {
	f := fn
	return parser.Expr{Kind: parser.ExprLocal, Fn: &f}
}
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

// canonicalPlan hard-codes the (unresolved) AST for:
//
//	type T = vector rat
//	fn lub a::rat b::rat = max a b
//	merge T = zip lub
//	query T.Value = reduce + 0
//	update T.Add k::rat0+ = local (+ k)
//
// The update param is rat0+ (not rat): a `+ k` update is only inflationary
// under a max merge when k is non-negative, which the convergence prover checks.
func canonicalPlan() parser.Plan {
	return parser.Plan{
		Types: []parser.TypeDef{{Name: "T", Elem: parser.ElemType{Kind: parser.KindVector, Elem: "rat"}}},
		Functions: []parser.FnDef{
			{Name: "lub", Params: []parser.ParamSpec{{Name: "a", Type: "rat"}, {Name: "b", Type: "rat"}},
				Body: app(name("max"), name("a"), name("b"))},
			// total v::T = reduce + 0 v — the fn-of-vector the Value query applies.
			vfn("total", "T", nil, reduce(name("+"), num(0))),
		},
		Merges: []parser.MergeDef{
			{TypeName: "T", Body: zip(name("lub"))},
		},
		Queries: []parser.QueryDef{
			{TypeName: "T", MethodName: "Value", Body: name("total")},
		},
		Updates: []parser.UpdateDef{
			{TypeName: "T", MethodName: "Add",
				Params: []parser.ParamSpec{{Name: "k", Type: "rat0+"}},
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

	assert.False(t, m.Elem.Struct)
	assert.Equal(t, numtype.NumType{Domain: numtype.DRat, Sign: numtype.SAny}, m.Elem.Num)

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
	assert.Equal(t, "8", got)
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
	assert.Contains(t, err.Error(), "merge for unknown type Ghost")
}

func TestBuild_missingMerge(t *testing.T) {
	plan := canonicalPlan()
	plan.Merges = nil
	_, err := Build(plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "type T has no merge defined")
}

func TestBuild_unknownIdentifier(t *testing.T) {
	plan := canonicalPlan()
	plan.Merges[0].Body = zip(name("wat"))
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_badParamType(t *testing.T) {
	plan := canonicalPlan()
	plan.Updates[0].Params = []parser.ParamSpec{{Name: "k", Type: "bool"}}
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_queryParamResolvedAndTyped(t *testing.T) {
	// query T.Above m::rat = above m ; fn above m::rat v::T = > (reduce max 0 v) m
	// -> a bool result; the param `m` resolves to a Var and type-checks against >.
	plan := canonicalPlan()
	plan.Functions = append(plan.Functions,
		vfn("above", "T", []parser.ParamSpec{{Name: "m", Type: "rat"}},
			app(name(">"), reduce(name("max"), num(0)), name("m"))))
	plan.Queries = append(plan.Queries, parser.QueryDef{
		TypeName: "T", MethodName: "Above",
		Params: []parser.ParamSpec{{Name: "m", Type: "rat"}},
		Body:   app(name("above"), name("m")),
	})
	built, err := Build(plan)
	require.NoError(t, err)

	q := built.Models["T"].Queries["Above"]
	require.Len(t, q.Params, 1)
	assert.Equal(t, "m", q.Params[0].Name)
	assert.Equal(t, parser.TypeBool, q.Result)
}

func TestBuild_queryStructParamRejected(t *testing.T) {
	// A struct-typed query param can't cross the wire (bindParams is scalar-only).
	plan := canonicalPlan()
	plan.Queries[0].Params = []parser.ParamSpec{{Name: "s", Type: "SomeStruct"}}
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_queryUnknownParamRejected(t *testing.T) {
	// A body referencing a name that is neither a param nor a known symbol fails.
	plan := canonicalPlan()
	plan.Queries[0].Body = app(name(">"), reduce(name("max"), num(0)), name("ghost"))
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
	plan.Queries = append(plan.Queries, parser.QueryDef{TypeName: "T", MethodName: "Add", Body: name("total")})
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
		Params: []parser.ParamSpec{{Name: "a", Type: "rat"}, {Name: "b", Type: "rat"}},
		Body:   app(name("min"), name("a"), name("b"))})
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_functionShadowsPrimitive(t *testing.T) {
	plan := canonicalPlan()
	plan.Functions = append(plan.Functions, parser.FnDef{Name: "max",
		Params: []parser.ParamSpec{{Name: "a", Type: "rat"}, {Name: "b", Type: "rat"}},
		Body:   name("a")})
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_duplicateParam(t *testing.T) {
	plan := canonicalPlan()
	plan.Functions[0].Params = []parser.ParamSpec{{Name: "a", Type: "rat"}, {Name: "a", Type: "rat"}}
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

func TestBuild_unanchoredRecursionRejected(t *testing.T) {
	plan := canonicalPlan()
	// fn loop a b = loop a b  — Option A: return type can't be inferred, rejected.
	plan.Functions = append(plan.Functions, parser.FnDef{Name: "loop",
		Params: []parser.ParamSpec{{Name: "a", Type: "rat"}, {Name: "b", Type: "rat"}},
		Body:   app(name("loop"), name("a"), name("b"))})
	_, err := Build(plan)
	require.Error(t, err)
}

func TestBuild_anchoredRecursionBuildsOK(t *testing.T) {
	plan := canonicalPlan()
	// fn f x | (> x 0) = f (- x 1) | otherwise = 0  — anchored by the rat base case.
	body := guards(
		gcase(app(name(">"), name("x"), num(0)), app(name("f"), app(name("-"), name("x"), num(1)))),
		gotherwise(num(0)),
	)
	plan.Functions = append(plan.Functions, parser.FnDef{Name: "f",
		Params: []parser.ParamSpec{{Name: "x", Type: "rat"}},
		Body:   body})
	_, err := Build(plan)
	require.NoError(t, err)
}

// ---- guards / bool / string type checking --------------------------

// mustParse parses DSL source, failing the test on a parse error.
func mustParse(t *testing.T, src string) parser.Plan {
	t.Helper()
	plan, err := parser.Parse(src)
	require.NoError(t, err)
	return plan
}

// Integration: build the guarded grade program and assert the inferred query
// result type is string.
func TestBuild_guardedProgram(t *testing.T) {
	src := `type T = vector rat
fn myScore x::rat
| (> x 90) = "You got a A"
| (>= x 80) = "You got a B"
| otherwise = "You got a F"
merge T = zip max
fn grade v::T = myScore (reduce max 0 v)
query T.Grade = grade
update T.Add k::rat0+ = local (+ k)
collection Scores = T
`
	built, err := Build(mustParse(t, src))
	require.NoError(t, err)

	m := built.Models["T"]
	require.NotNil(t, m)
	grade, ok := m.Queries["Grade"]
	require.True(t, ok)
	assert.Equal(t, parser.TypeString, grade.Result)
}

func TestBuild_boolReturningFnOK(t *testing.T) {
	_, err := Build(mustParse(t, "fn adult x::rat = >= x 18\n"))
	require.NoError(t, err)
}

func TestBuild_guardTypeErrors(t *testing.T) {
	cases := map[string]string{
		"guard cond not bool": `fn bad x::rat
| (+ x 1) = "a"
| otherwise = "b"
`,
		"mismatched branch types": `fn bad x::rat
| (> x 1) = "a"
| otherwise = 0
`,
		"missing final otherwise": `fn bad x::rat
| (> x 1) = "a"
| (> x 2) = "b"
`,
		"otherwise not last": `fn bad x::rat
| otherwise = "a"
| (> x 1) = "b"
`,
		"bool passed to arithmetic": "fn bad x::rat = + (> x 1) 2\n",
		"reduce over non-vector":    "fn bad x::rat = reduce + 0 x\n",
	}
	for nameStr, src := range cases {
		t.Run(nameStr, func(t *testing.T) {
			_, err := Build(mustParse(t, src))
			require.Error(t, err)
		})
	}
}

func TestBuild_applicationTypeMismatch(t *testing.T) {
	// g expects a rat; passing it a bool (> x 1) is a type error.
	src := `fn g y::rat = y
fn bad x::rat = g (> x 1)
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
}

func TestBuild_stringFnInLocalRejected(t *testing.T) {
	// an update's local fn must return rat; a string-returning fn is rejected.
	src := `type T = vector rat
fn label x::rat
| (> x 0) = "pos"
| otherwise = "neg"
merge T = zip max
update T.Set k::rat = local label
collection C = T
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
}

// ---- numeric subtypes / per-operator typing ------------------------

// Subtraction loses the non-negative sign, so `local (- k)` on a `vector rat0+`
// produces a `rat` result that is not assignable back to the `rat0+` slot.
func TestBuild_subtractionOnNonNegRejected(t *testing.T) {
	src := `type T = vector rat0+
merge T = zip max
update T.Sub k::rat0+ = local (- k)
collection C = T
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
}

// Addition preserves the non-negative sign, so the same shape with `+` builds.
func TestBuild_additionOnNonNegOK(t *testing.T) {
	src := `type T = vector rat0+
merge T = zip max
update T.Add k::rat0+ = local (+ k)
collection C = T
`
	_, err := Build(mustParse(t, src))
	require.NoError(t, err)
}

// `reduce + 0` infers the element's numeric type: rat0+ over a rat0+ vector,
// int over an int vector (0 + int widens to int via the literal-0 Zero sign).
func TestBuild_reduceInfersElementType(t *testing.T) {
	cases := map[string]numtype.NumType{
		"rat0+": {Domain: numtype.DRat, Sign: numtype.SNonNeg},
		"int":   {Domain: numtype.DInt, Sign: numtype.SAny},
		"int0+": {Domain: numtype.DInt, Sign: numtype.SNonNeg},
	}
	for elem, want := range cases {
		t.Run(elem, func(t *testing.T) {
			src := "type T = vector " + elem + "\nmerge T = zip max\nfn total v::T = reduce + 0 v\nquery T.Value = total\ncollection C = T\n"
			built, err := Build(mustParse(t, src))
			require.NoError(t, err)
			q := built.Models["T"].Queries["Value"]
			assert.Equal(t, parser.TypeReal, q.Result)
			assert.Equal(t, want, q.ResultNum)
		})
	}
}

// The literal 0 has the internal Zero sign, so it is assignable to a
// non-positive target: `local (+ 0)` builds on a `vector rat0-`.
func TestBuild_literalZeroAssignableToNonPos(t *testing.T) {
	src := `type T = vector rat0-
merge T = zip min
update T.Nop = local (+ 0)
collection C = T
`
	_, err := Build(mustParse(t, src))
	require.NoError(t, err)
}

// A negative literal is typed non-positive (SNonPos), so `+ (-5)` on a `vector
// rat0-` stays non-positive, is assignable to the slot, and is proven inflationary
// under min.
func TestBuild_negativeLiteralAssignableToNonPos(t *testing.T) {
	src := `type T = vector rat0-
merge T = zip min
update T.Dec = local (+ -5)
collection C = T
`
	_, err := Build(mustParse(t, src))
	require.NoError(t, err)
}

// Conversely a negative literal's non-positive sign propagates: `+ a (-5)` over a
// non-negative operand is SAny (`rat`), which is not assignable back to a `rat0+`
// slot, so the merge boundary rejects it.
func TestBuild_negativeLiteralRejectedWhereNonNeg(t *testing.T) {
	src := `type T = vector rat0+
fn addneg a::rat0+ b::rat0+ = + a -5
merge T = zip addneg
collection C = T
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
}

// Mixed domains: a `vector rat` slot with an int0+ increment param builds and
// is proven inflationary under max — the prover declares the slot as Real and
// the param as an integer (is_int), so the cross-domain `+` is well-sorted.
func TestBuild_mixedDomainUpdateProven(t *testing.T) {
	src := `type T = vector rat
merge T = zip max
update T.Add k::int0+ = local (+ k)
collection C = T
`
	_, err := Build(mustParse(t, src))
	require.NoError(t, err)
}

// int0+ is a subtype of rat0+, so a fn expecting rat0+ accepts an int0+ arg.
func TestBuild_intSubtypeWhereRealExpected(t *testing.T) {
	src := `fn f x::rat0+ = + x 1
fn g y::int0+ = f y
`
	_, err := Build(mustParse(t, src))
	require.NoError(t, err)
}

// Anchored recursion where the recursive call is an operand to `+` still builds:
// the recursive call types as vUnknown mid-inference and defers the numeric check.
func TestBuild_anchoredRecursionThroughOperator(t *testing.T) {
	src := `fn f x::rat
| (> x 0) = + 1 (f (- x 1))
| otherwise = 0
`
	_, err := Build(mustParse(t, src))
	require.NoError(t, err)
}

// ---- return-type annotations ---------------------------------------

// A `-> type` annotation on a non-recursive fn builds when the body conforms.
func TestBuild_returnAnnotationBuilds(t *testing.T) {
	_, err := Build(mustParse(t, "fn add a::rat b::rat -> rat = + a b\n"))
	require.NoError(t, err)
}

// A declared return type that the body's type is not assignable to is rejected.
func TestBuild_returnAnnotationMismatchRejected(t *testing.T) {
	// body has type rat, declared int — rat is not <: int.
	_, err := Build(mustParse(t, "fn f x::rat -> int = x\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declared return type")
}

// The annotation seeds inference, so otherwise-unanchored recursion builds — and
// the same fn WITHOUT the annotation is rejected.
func TestBuild_returnAnnotationEnablesUnanchoredRecursion(t *testing.T) {
	_, err := Build(mustParse(t, "fn g x::rat -> rat = g x\n"))
	require.NoError(t, err)

	_, err = Build(mustParse(t, "fn g x::rat = g x\n"))
	require.Error(t, err) // no annotation, unanchored
}

// Finding 1: an annotated fn whose body routes through an un-annotated,
// non-anchoring fn must fail at BUILD time, not slip through via vUnknown.
func TestBuild_annotatedThroughUnanchoredRejected(t *testing.T) {
	src := `fn helper x::rat = helper x
fn g x::rat -> rat = helper x
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
}

// A vkUnknown buried inside a struct field must not slip past the strict check —
// subVtype's unknown-defers rule would otherwise accept it structurally.
func TestBuild_nestedUnknownInStructResultRejected(t *testing.T) {
	src := `type X = { A rat }
fn helper x::rat = { A: helper x }
fn g x::rat -> X = helper x
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
}

// bool and string return annotations resolve.
func TestBuild_returnAnnotationBoolString(t *testing.T) {
	_, err := Build(mustParse(t, "fn p x::rat -> bool = > x 0\n"))
	require.NoError(t, err)

	_, err = Build(mustParse(t, "fn s x::rat -> string = \"hi\"\n"))
	require.NoError(t, err)
}

// An unresolvable return-annotation token is a build error.
func TestBuild_unknownReturnAnnotationRejected(t *testing.T) {
	_, err := Build(mustParse(t, "fn f x::rat -> nope = x\n"))
	require.Error(t, err)
}

// Finding 2: `bool`/`string` are reserved type names (they are the builtin return
// types), so a user `type bool`/`type string` is rejected.
func TestBuild_reservedBoolStringTypeNames(t *testing.T) {
	for _, name := range []string{"bool", "string"} {
		_, err := Build(mustParse(t, "type "+name+" = { A rat0+ }\n"))
		require.Error(t, err, "type %s", name)
		assert.Contains(t, err.Error(), "reserved", "type %s", name)
	}
}

// ---- struct vectors ------------------------------------------------

// The canonical struct-vector PN-counter builds: struct type, vector of it,
// whole-struct max merge (a product join-semilattice), struct-typed updates
// proven inflationary, and struct construction / field access type-check.
func TestBuild_structVectorAccepts(t *testing.T) {
	const src = `type X = {
  Pos rat0+
  Neg rat0+
}
type VX = vector X
fn J a::X b::X = { Pos: max a.Pos b.Pos, Neg: max a.Neg b.Neg }
fn incPos k::rat0+ s::X = { Pos: + s.Pos k, Neg: s.Neg }
merge VX = zip J
update VX.AddPos k::rat0+ = local (incPos k)
fn net v::VX = (reduce J { Pos: 0, Neg: 0 } v).Pos
query VX.Net = net
collection C = VX
`
	built, err := Build(mustParse(t, src))
	require.NoError(t, err)
	m := built.Models["VX"]
	require.NotNil(t, m)
	require.True(t, m.Elem.Struct)
	require.Len(t, m.Elem.Fields, 2)
	assert.Equal(t, "Pos", m.Elem.Fields[0].Name)
	assert.Equal(t, numtype.NumType{Domain: numtype.DRat, Sign: numtype.SNonNeg}, m.Elem.Fields[0].Type.Num)
	// A struct type is not itself a collectable model.
	_, isModel := built.Models["X"]
	assert.False(t, isModel)
}

// A per-field SUM merge is commutative/associative but NOT idempotent, so the
// convergence prover rejects it (the classic counter mistake).
func TestBuild_structPerFieldSumMergeRejected(t *testing.T) {
	const src = `type X = { Pos rat0+  Neg rat0+ }
type VX = vector X
fn S a::X b::X = { Pos: + a.Pos b.Pos, Neg: + a.Neg b.Neg }
merge VX = zip S
collection C = VX
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "convergence")
}

// Accessing a field the struct does not have is a build error.
func TestBuild_unknownFieldRejected(t *testing.T) {
	const src = `type X = { Pos rat0+ }
type VX = vector X
fn J a::X b::X = { Pos: max a.Pos b.Pos }
fn bad s::X = { Pos: s.Nope }
merge VX = zip J
update VX.U = local bad
collection C = VX
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Nope")
}

// A struct literal whose field type is not assignable to the element field type
// (here `- s.Pos 1` loses the non-negative sign of a rat0+ field) is rejected.
func TestBuild_structFieldTypeMismatchRejected(t *testing.T) {
	const src = `type X = { Pos rat0+ }
type VX = vector X
fn J a::X b::X = { Pos: max a.Pos b.Pos }
fn bad s::X = { Pos: - s.Pos 1 }
merge VX = zip J
update VX.U = local bad
collection C = VX
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not assignable")
}

// A struct type cannot be instantiated as a collection (only vectors can).
func TestBuild_structCollectionRejected(t *testing.T) {
	const src = `type X = { Pos rat0+ }
collection C = X
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
}

// Dot access on a scalar (non-struct) value is a build error.
func TestBuild_fieldOfScalarRejected(t *testing.T) {
	const src = `type T = vector rat0+
merge T = zip max
fn bad v::T = (reduce max 0 v).Pos
query T.Bad = bad
collection C = T
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-struct")
}

// A user type name may not shadow a numeric type name.
func TestBuild_reservedTypeNameRejected(t *testing.T) {
	const src = `type int = { A rat0+ }
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserved")
}

// A struct-typed update param is rejected (update params cross the wire as
// scalars).
func TestBuild_structUpdateParamRejected(t *testing.T) {
	const src = `type X = { Pos rat0+ }
type VX = vector X
fn J a::X b::X = { Pos: max a.Pos b.Pos }
fn pick a::X b::X = { Pos: max a.Pos b.Pos }
merge VX = zip J
update VX.U p::X = local (pick p)
collection C = VX
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "numeric type")
}

// ---- vector element access (V.Elem), inline struct elements, aliases -------

// An inline struct vector element (`vector { ... }`) plus `V.Elem` fn params
// builds and proves — no separate named struct type needed.
func TestBuild_inlineVectorStructWithElemParams(t *testing.T) {
	const src = `type V = vector {
  Pos rat0+
  Neg rat0+
}
fn J a::V.Elem b::V.Elem = { Pos: max a.Pos b.Pos, Neg: max a.Neg b.Neg }
fn incPos k::rat0+ s::V.Elem = { Pos: + s.Pos k, Neg: s.Neg }
merge V = zip J
update V.AddPos k::rat0+ = local (incPos k)
fn totals v::V = reduce J { Pos: 0, Neg: 0 } v
query V.Totals = totals
collection C = V
`
	built, err := Build(mustParse(t, src))
	require.NoError(t, err)
	m := built.Models["V"]
	require.NotNil(t, m)
	require.True(t, m.Elem.Struct)
	require.Len(t, m.Elem.Fields, 2)
	assert.Equal(t, "Pos", m.Elem.Fields[0].Name)
	// A whole-struct query returns the struct element type.
	require.NotNil(t, m.Queries["Totals"].ResultStruct)
	assert.True(t, m.Queries["Totals"].ResultStruct.Struct)
}

// `type X = V.Elem` naming a NUMERIC vector element, used as an update/query
// param, must normalize the stored param token to the concrete numtype name so
// bindParams/prover/swagger (which numtype.Parse the token) accept it.
func TestBuild_numericElemAliasParamNormalized(t *testing.T) {
	const src = `type V = vector rat0+
type Amount = V.Elem
fn add a::rat0+ b::rat0+ = + a b
merge V = zip max
update V.Add k::Amount = local (add k)
fn atleast k::Amount v::V = reduce max 0 v
query V.AtLeast k::Amount = atleast k
collection C = V
`
	built, err := Build(mustParse(t, src))
	require.NoError(t, err)
	m := built.Models["V"]
	require.NotNil(t, m)
	// Stored tokens must be the concrete numtype name, not "V.Elem"/"Amount".
	assert.Equal(t, "rat0+", m.Updates["Add"].Params[0].Type)
	assert.Equal(t, "rat0+", m.Queries["AtLeast"].Params[0].Type)
}

// An alias resolves DURING type resolution, so it can be used as a vector element.
func TestBuild_aliasAsVectorElement(t *testing.T) {
	const src = `type V = vector rat0+
type Amount = V.Elem
type W = vector Amount
merge V = zip max
merge W = zip max
collection C = V
collection D = W
`
	built, err := Build(mustParse(t, src))
	require.NoError(t, err)
	w := built.Models["W"]
	require.NotNil(t, w)
	assert.Equal(t, numtype.NumType{Domain: numtype.DRat, Sign: numtype.SNonNeg}, w.Elem.Num)
}

// A struct `V.Elem` used as an update param (a scalar wire position) is rejected.
func TestBuild_structElemUpdateParamRejected(t *testing.T) {
	const src = `type V = vector { Pos rat0+ }
fn J a::V.Elem b::V.Elem = { Pos: max a.Pos b.Pos }
fn pick a::V.Elem b::V.Elem = { Pos: max a.Pos b.Pos }
merge V = zip J
update V.U p::V.Elem = local (pick p)
collection C = V
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "numeric type")
}

// `.Elem` on a non-vector (a struct type) is a build error.
func TestBuild_elemOfNonVectorRejected(t *testing.T) {
	const src = `type S = { A rat0+ }
type X = S.Elem
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a vector")
}

// A member other than `.Elem` is rejected.
func TestBuild_unknownTypeMemberRejected(t *testing.T) {
	const src = `type V = vector rat0+
type X = V.Foo
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Foo")
}

// A self-referential vector element (`vector V.Elem`) is a cycle.
func TestBuild_recursiveVectorElemRejected(t *testing.T) {
	const src = `type V = vector V.Elem
merge V = zip max
collection C = V
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recursive")
}

// An alias<->vector cycle (`type X = V.Elem; type V = vector X`) is caught by the
// shared cycle-detection set.
func TestBuild_aliasVectorCycleRejected(t *testing.T) {
	const src = `type X = V.Elem
type V = vector X
merge V = zip max
collection C = V
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recursive")
}

// ---- LWW register: string leaves, write, vector-value reduce -------

// lwwSrc is the target LWW register: a { version, value } slot, per-slot
// argmax-by-version merge (string tiebreak), and a Lamport `write` update.
const lwwSrc = `type X = vector {
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

// The full LWW register builds: string slot leaf, string tiebreak comparison,
// the `write` combinator, and the reduce-domination convergence proof all pass.
func TestBuild_lwwStringRegister(t *testing.T) {
	built, err := Build(mustParse(t, lwwSrc))
	require.NoError(t, err)
	m := built.Models["X"]
	require.NotNil(t, m)
	require.True(t, m.Elem.Struct)
	require.Len(t, m.Elem.Fields, 2)
	assert.True(t, m.Elem.Fields[1].Type.Str) // value is a string leaf
	assert.Equal(t, parser.ExprWrite, m.Updates["Set"].Body.Kind)
	assert.Equal(t, "string", m.Updates["Set"].Params[0].Type)
	assert.Equal(t, parser.TypeString, m.Queries["Value"].Result)
}

// Dropping the string tiebreak leaves a non-commutative merge (two equal-version
// slots pick differently by argument order), which the prover rejects.
func TestBuild_lwwNoTiebreakRejected(t *testing.T) {
	src := `type X = vector { version int0+  value string }
merge X = zip Merge
fn Merge a::X.Elem b::X.Elem -> X.Elem
| (> a.version b.version) = a
| otherwise               = b
fn maxVer acc::int0+ e::X.Elem -> int0+ = max acc e.version
fn NextX s::string v::X -> X.Elem = { version: + 1 (reduce maxVer 0 v), value: s }
update X.Set s::string = write (NextX s)
collection Reg = X
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "convergence")
}

// A `write` whose version bump folds with `+` (a sum, not a max) is not a Lamport
// clock: the reduce-domination shape recognizer rejects the fold.
func TestBuild_writeNonMaxFoldRejected(t *testing.T) {
	src := `type X = vector { version int0+  value string }
merge X = zip Merge
fn Merge a::X.Elem b::X.Elem -> X.Elem
| (> a.version b.version) = a
| (< a.version b.version) = b
| (> a.value b.value)     = a
| otherwise               = b
fn sumVer acc::int0+ e::X.Elem -> int0+ = + acc e.version
fn NextX s::string v::X -> X.Elem = { version: + 1 (reduce sumVer 0 v), value: s }
update X.Set s::string = write (NextX s)
collection Reg = X
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max/min")
}

// A query may not return a whole vector (it is not serializable).
func TestBuild_vectorEscapingQueryRejected(t *testing.T) {
	src := `type X = vector { version int0+  value string }
merge X = zip Merge
fn Merge a::X.Elem b::X.Elem -> X.Elem
| (> a.version b.version) = a
| (< a.version b.version) = b
| (> a.value b.value)     = a
| otherwise               = b
fn Id v::X -> X = v
query X.Bad = Id
collection Reg = X
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vector")
}

// A string query param is rejected: the GET `?params=` wire contract can't carry
// arbitrary strings (only update params may be strings).
func TestBuild_stringQueryParamRejected(t *testing.T) {
	src := `type T = vector rat0+
merge T = zip max
fn total v::T = reduce + 0 v
query T.Q s::string = total
collection C = T
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "string")
}

// A comparison over mismatched operand kinds (numeric vs string) is a type error.
func TestBuild_mixedComparisonRejected(t *testing.T) {
	src := `type X = vector { version int0+  value string }
fn bad a::X.Elem = > a.value a.version
merge X = zip max
collection Reg = X
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "same type")
}

// ---- functions are first-class in the type model, not runtime values -------

// Functions are typed (vkFunc), but they are not values: passing a bare function
// where a value is expected — here the primitive `max` as an argument to `+` — is
// a type error caught via the isFunc check, not an arity miscount.
func TestBuild_functionAsArgumentRejected(t *testing.T) {
	const src = `type T = vector rat
fn bad a::rat = + a max
merge T = zip max
update T.Add k::rat = local (+ k)
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot pass a function as an argument")
}

// A function in a struct-literal field position is likewise not a value.
func TestBuild_functionAsStructFieldRejected(t *testing.T) {
	const src = `type X = { Pos rat  Neg rat }
type VX = vector X
fn bad a::X = { Pos: max, Neg: 0 }
merge VX = zip max
collection C = VX
`
	_, err := Build(mustParse(t, src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a value")
}

// ---- deterministic ordering / builder invariants -------------------------

func TestResolveTypes_recordsDeclarationOrder(t *testing.T) {
	types := []parser.TypeDef{
		{Name: "S", Elem: parser.ElemType{Kind: parser.KindStruct, Fields: []parser.FieldSpec{{Name: "N", Type: "rat"}}}},
		{Name: "V", Elem: parser.ElemType{Kind: parser.KindVector, Elem: "rat"}},
		{Name: "A", Elem: parser.ElemType{Kind: parser.KindElemRef, Elem: "V.Elem"}},
		{Name: "W", Elem: parser.ElemType{Kind: parser.KindVector, Elem: "rat"}},
	}
	reg, err := resolveTypes(types)
	require.NoError(t, err)
	assert.Equal(t, []string{"S", "V", "A", "W"}, reg.typeOrder)
	assert.Equal(t, []string{"S"}, reg.structOrder)
	assert.Equal(t, []string{"A"}, reg.aliasOrder)
	assert.Equal(t, []string{"V", "W"}, reg.vectorOrder)
}

func TestBuild_typeResolutionErrorsFollowDeclarationOrder(t *testing.T) {
	plan := parser.Plan{Types: []parser.TypeDef{
		{Name: "First", Elem: parser.ElemType{Kind: parser.KindVector, Elem: "Missing"}},
		{Name: "Second", Elem: parser.ElemType{Kind: parser.KindStruct, Fields: []parser.FieldSpec{{Name: "Self", Type: "Second"}}}},
	}}
	_, err := Build(plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "type First")
}

func TestBuild_missingMergeErrorsFollowVectorDeclarationOrder(t *testing.T) {
	plan := parser.Plan{Types: []parser.TypeDef{
		{Name: "First", Elem: parser.ElemType{Kind: parser.KindVector, Elem: "rat"}},
		{Name: "Second", Elem: parser.ElemType{Kind: parser.KindVector, Elem: "rat"}},
	}}
	_, err := Build(plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "type First has no merge defined")
}

func TestBuild_proofErrorsFollowVectorDeclarationOrder(t *testing.T) {
	plan := parser.Plan{
		Types: []parser.TypeDef{
			{Name: "First", Elem: parser.ElemType{Kind: parser.KindVector, Elem: "rat"}},
			{Name: "Second", Elem: parser.ElemType{Kind: parser.KindVector, Elem: "rat"}},
		},
		Merges: []parser.MergeDef{
			{TypeName: "First", Body: zip(name("+"))},
			{TypeName: "Second", Body: zip(name("+"))},
		},
	}
	_, err := Build(plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "convergence proof failed for type First")
}

func TestValidateResolvedExpr_findsNamesInEveryChildPosition(t *testing.T) {
	resolved := parser.Expr{Kind: parser.ExprVar, Name: "x"}
	ref := parser.Expr{Kind: parser.ExprRef, Name: "+", Ref: parser.RefPrimitive, Arity: 2}
	unresolved := func() *parser.Expr {
		e := name("leaked")
		return &e
	}
	value := func() *parser.Expr {
		e := resolved
		return &e
	}
	fn := func() *parser.Expr {
		e := ref
		return &e
	}

	tests := []struct {
		name string
		expr parser.Expr
	}{
		{"application head", parser.Expr{Kind: parser.ExprApp, Head: unresolved()}},
		{"application argument", parser.Expr{Kind: parser.ExprApp, Head: fn(), Args: []*parser.Expr{unresolved()}}},
		{"guard condition", parser.Expr{Kind: parser.ExprGuards, Cases: []parser.GuardCase{{Cond: unresolved(), Result: value()}}}},
		{"guard result", parser.Expr{Kind: parser.ExprGuards, Cases: []parser.GuardCase{{Cond: value(), Result: unresolved()}}}},
		{"struct field", parser.Expr{Kind: parser.ExprStructLit, StructFields: []parser.StructField{{Name: "N", Value: unresolved()}}}},
		{"field target", parser.Expr{Kind: parser.ExprField, Target: unresolved(), Field: "N"}},
		{"reduce function", parser.Expr{Kind: parser.ExprReduce, Fn: unresolved(), Init: value(), Vec: value()}},
		{"reduce init", parser.Expr{Kind: parser.ExprReduce, Fn: fn(), Init: unresolved(), Vec: value()}},
		{"reduce vector", parser.Expr{Kind: parser.ExprReduce, Fn: fn(), Init: value(), Vec: unresolved()}},
		{"zip function", parser.Expr{Kind: parser.ExprZip, Fn: unresolved()}},
		{"local function", parser.Expr{Kind: parser.ExprLocal, Fn: unresolved()}},
		{"write function", parser.Expr{Kind: parser.ExprWrite, Fn: unresolved()}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateResolvedExpr(test.expr)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `unresolved name "leaked" survived Build`)
		})
	}
}

func TestValidateResolvedExpr_rejectsMalformedAndUnknownKinds(t *testing.T) {
	tests := []struct {
		name string
		expr parser.Expr
	}{
		{"numeric literal", parser.Expr{Kind: parser.ExprNumLit}},
		{"application head", parser.Expr{Kind: parser.ExprApp}},
		{"guard condition", parser.Expr{Kind: parser.ExprGuards, Cases: []parser.GuardCase{{}}}},
		{"struct field", parser.Expr{Kind: parser.ExprStructLit, StructFields: []parser.StructField{{Name: "N"}}}},
		{"field target", parser.Expr{Kind: parser.ExprField}},
		{"reduce function", parser.Expr{Kind: parser.ExprReduce}},
		{"combinator function", parser.Expr{Kind: parser.ExprZip}},
		{"unknown kind", parser.Expr{Kind: parser.ExprKind(999)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateResolvedExpr(test.expr)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "internal:")
		})
	}
}

func TestValidateResolvedExpr_acceptsResolvedTree(t *testing.T) {
	ref := parser.Expr{Kind: parser.ExprRef, Name: "+", Ref: parser.RefPrimitive, Arity: 2}
	variable := parser.Expr{Kind: parser.ExprVar, Name: "v"}
	zero := parser.Expr{Kind: parser.ExprNumLit, Num: new(big.Rat)}
	expr := parser.Expr{Kind: parser.ExprReduce, Fn: &ref, Init: &zero, Vec: &variable}
	require.NoError(t, validateResolvedExpr(expr))
}

func TestCheckerInvariantHelpersReturnErrors(t *testing.T) {
	chk := newChecker(typeReg{models: map[string]*Model{}, structs: map[string]crdt.ElemT{}, aliases: map[string]crdt.ElemT{}})
	_, err := chk.paramVtype("Missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "internal: parameter type")

	_, err = chk.paramScope([]parser.ParamSpec{{Name: "x", Type: "Missing"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "param x")

	_, err = elemTOf(vBool)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "internal: cannot lower bool")
}
