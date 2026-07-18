package parser

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mandated integration test: parse the canonical snippet (now including a
// user-defined function used in merge) into the AST.
func TestParse_integration(t *testing.T) {
	src := `type T = vector rat

fn lub a::rat b::rat = max a b

merge T = zip lub

query T.Value = reduce + 0 v

update T.Add k::rat = local (+ k)
`
	plan, err := Parse(src)
	require.NoError(t, err)

	// --- types ---
	require.Len(t, plan.Types, 1)
	td := plan.Types[0]
	assert.Equal(t, "T", td.Name)
	assert.Equal(t, KindVector, td.Elem.Kind)
	assert.Equal(t, "rat", td.Elem.Elem)

	// --- function: fn lub a b = max a b ---
	require.Len(t, plan.Functions, 1)
	fn := plan.Functions[0]
	assert.Equal(t, "lub", fn.Name)
	require.Len(t, fn.Params, 2)
	assert.Equal(t, "a", fn.Params[0].Name)
	assert.Equal(t, "rat", fn.Params[0].Type)
	assert.Equal(t, "b", fn.Params[1].Name)
	// body is `max a b` == App(Name max, [Name a, Name b]) (unresolved)
	require.Equal(t, ExprApp, fn.Body.Kind)
	require.NotNil(t, fn.Body.Head)
	assert.Equal(t, ExprName, fn.Body.Head.Kind)
	assert.Equal(t, "max", fn.Body.Head.Name)
	require.Len(t, fn.Body.Args, 2)
	assert.Equal(t, ExprName, fn.Body.Args[0].Kind)
	assert.Equal(t, "a", fn.Body.Args[0].Name)
	assert.Equal(t, "b", fn.Body.Args[1].Name)

	// --- merge: zip lub (a name atom) ---
	require.Len(t, plan.Merges, 1)
	md := plan.Merges[0]
	assert.Equal(t, "T", md.TypeName)
	assert.Equal(t, ExprZip, md.Body.Kind)
	require.NotNil(t, md.Body.Fn)
	assert.Equal(t, ExprName, md.Body.Fn.Kind)
	assert.Equal(t, "lub", md.Body.Fn.Name)

	// --- query: reduce + 0 ---
	require.Len(t, plan.Queries, 1)
	qd := plan.Queries[0]
	assert.Equal(t, "T", qd.TypeName)
	assert.Equal(t, "Value", qd.MethodName)
	assert.Empty(t, qd.Params)
	assert.Equal(t, ExprReduce, qd.Body.Kind)
	require.NotNil(t, qd.Body.Fn)
	assert.Equal(t, ExprName, qd.Body.Fn.Kind)
	assert.Equal(t, "+", qd.Body.Fn.Name)
	require.NotNil(t, qd.Body.Init)
	assert.Equal(t, ExprNumLit, qd.Body.Init.Kind)
	assert.Equal(t, big.NewRat(0, 1), qd.Body.Init.Num)
	require.NotNil(t, qd.Body.Vec)
	assert.Equal(t, ExprName, qd.Body.Vec.Kind)
	assert.Equal(t, "v", qd.Body.Vec.Name)

	// --- update: local (+ k) == local applied to a partial application ---
	require.Len(t, plan.Updates, 1)
	ud := plan.Updates[0]
	assert.Equal(t, "T", ud.TypeName)
	assert.Equal(t, "Add", ud.MethodName)
	require.Len(t, ud.Params, 1)
	assert.Equal(t, "k", ud.Params[0].Name)
	assert.Equal(t, ExprLocal, ud.Body.Kind)
	require.NotNil(t, ud.Body.Fn)
	sec := ud.Body.Fn
	require.Equal(t, ExprApp, sec.Kind)
	require.NotNil(t, sec.Head)
	assert.Equal(t, "+", sec.Head.Name)
	require.Len(t, sec.Args, 1)
	assert.Equal(t, ExprName, sec.Args[0].Kind)
	assert.Equal(t, "k", sec.Args[0].Name)

	assert.Empty(t, plan.Collections)
}

// Integration: a full program with a multi-line guarded function (comparisons
// + string results + otherwise) and a query that wraps reduce.
func TestParse_guardedFunctionProgram(t *testing.T) {
	src := `type T = vector rat
fn myScore x::rat
| (> x 90) = "You got a A"
| (>= x 80) = "You got a B"
| otherwise = "You got a F"
merge T = zip max
query T.Grade = myScore (reduce max 0 v)
update T.Add k::rat = local (+ k)
collection Scores = T
`
	plan, err := Parse(src)
	require.NoError(t, err)

	// --- guarded function ---
	require.Len(t, plan.Functions, 1)
	fn := plan.Functions[0]
	assert.Equal(t, "myScore", fn.Name)
	require.Len(t, fn.Params, 1)
	require.Equal(t, ExprGuards, fn.Body.Kind)
	require.Len(t, fn.Body.Cases, 3)

	c0 := fn.Body.Cases[0]
	assert.False(t, c0.Otherwise)
	require.NotNil(t, c0.Cond)
	require.Equal(t, ExprApp, c0.Cond.Kind)
	assert.Equal(t, ">", c0.Cond.Head.Name)
	require.Equal(t, ExprStrLit, c0.Result.Kind)
	assert.Equal(t, "You got a A", c0.Result.Str)

	assert.Equal(t, ">=", fn.Body.Cases[1].Cond.Head.Name)

	cLast := fn.Body.Cases[2]
	assert.True(t, cLast.Otherwise)
	assert.Nil(t, cLast.Cond)
	assert.Equal(t, "You got a F", cLast.Result.Str)

	// --- query body: myScore (reduce max 0) ---
	require.Len(t, plan.Queries, 1)
	qb := plan.Queries[0].Body
	require.Equal(t, ExprApp, qb.Kind)
	assert.Equal(t, "myScore", qb.Head.Name)
	require.Len(t, qb.Args, 1)
	require.Equal(t, ExprReduce, qb.Args[0].Kind)
	assert.Equal(t, "max", qb.Args[0].Fn.Name)
	assert.Equal(t, big.NewRat(0, 1), qb.Args[0].Init.Num)

	require.Len(t, plan.Collections, 1)
	assert.Equal(t, "Scores", plan.Collections[0].Name)
}

func TestParse_singleLineFnStillWorks(t *testing.T) {
	plan, err := Parse("fn lub a::rat b::rat = max a b\n")
	require.NoError(t, err)
	require.Len(t, plan.Functions, 1)
	assert.Equal(t, ExprApp, plan.Functions[0].Body.Kind)
	assert.Equal(t, "", plan.Functions[0].RetType) // un-annotated
}

func TestParse_singleLineFnReturnAnnotation(t *testing.T) {
	plan, err := Parse("fn add a::rat b::rat -> rat = + a b\n")
	require.NoError(t, err)
	require.Len(t, plan.Functions, 1)
	assert.Equal(t, "rat", plan.Functions[0].RetType)
	assert.Equal(t, ExprApp, plan.Functions[0].Body.Kind)
}

func TestParse_guardedFnReturnAnnotation(t *testing.T) {
	src := "fn grade x::rat -> string\n| (> x 90) = \"A\"\n| otherwise = \"F\"\n"
	plan, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, plan.Functions, 1)
	assert.Equal(t, "string", plan.Functions[0].RetType)
	assert.Equal(t, ExprGuards, plan.Functions[0].Body.Kind)
}

func TestParse_returnAnnotationTokens(t *testing.T) {
	// A return annotation reuses typeNameP: numtype signs, struct names, V.Elem.
	for _, ret := range []string{"rat0+", "int0-", "X", "V.Elem", "bool"} {
		plan, err := Parse("fn f x::rat -> " + ret + " = x\n")
		require.NoError(t, err, "ret %q", ret)
		assert.Equal(t, ret, plan.Functions[0].RetType, "ret %q", ret)
	}
}

func TestParse_stringLiteralEscapes(t *testing.T) {
	plan, err := Parse("fn f x::rat = \"a\\\"b\\\\c\"\n")
	require.NoError(t, err)
	body := plan.Functions[0].Body
	require.Equal(t, ExprStrLit, body.Kind)
	assert.Equal(t, "a\"b\\c", body.Str)
}

func TestParse_unterminatedStringIsError(t *testing.T) {
	_, err := Parse("fn f x::rat = \"oops\n")
	require.Error(t, err)
}

func TestParse_comparisonOperators(t *testing.T) {
	for _, op := range []string{">", "<", ">=", "<=", "==", "/="} {
		plan, err := Parse("fn f x::rat = " + op + " x 1\n")
		require.NoError(t, err, "op %q", op)
		body := plan.Functions[0].Body
		require.Equal(t, ExprApp, body.Kind, "op %q", op)
		assert.Equal(t, op, body.Head.Name, "op %q", op)
	}
}

func TestParse_queryWrapsReduceAtom(t *testing.T) {
	plan, err := Parse("query T.Grade = f (reduce max 0 v)\n")
	require.NoError(t, err)
	qb := plan.Queries[0].Body
	require.Equal(t, ExprApp, qb.Kind)
	require.Len(t, qb.Args, 1)
	assert.Equal(t, ExprReduce, qb.Args[0].Kind)
}

// A stray `| ...` line — a malformed guard after `otherwise`, or a `|` with no
// `fn` header — is a parse error, never silently skipped.
func TestParse_strayBarIsError(t *testing.T) {
	cases := []string{
		"fn f x::rat\n| otherwise = \"a\"\n| broken\n", // malformed guard after otherwise
		"type T = vector rat\n| broken\n",              // `|` with no fn header
	}
	for _, src := range cases {
		_, err := Parse(src)
		require.Error(t, err, "src: %q", src)
	}
}

// All six numeric type names parse, both as a vector element and as a param type.
// `rat0+` must win over the `rat` prefix (longest match).
func TestParse_numericTypeNames(t *testing.T) {
	for _, name := range []string{"rat", "rat0+", "rat0-", "int", "int0+", "int0-"} {
		plan, err := Parse("type T = vector " + name + "\n")
		require.NoError(t, err, "elem %q", name)
		require.Len(t, plan.Types, 1, "elem %q", name)
		assert.Equal(t, name, plan.Types[0].Elem.Elem, "elem %q", name)

		plan, err = Parse("fn f k::" + name + " = k\n")
		require.NoError(t, err, "param %q", name)
		require.Len(t, plan.Functions, 1, "param %q", name)
		assert.Equal(t, name, plan.Functions[0].Params[0].Type, "param %q", name)
	}
}

// A struct-vector program parses: a named struct type spanning multiple lines, a
// vector of it, a merge/update fn using struct params, struct literals, and dot
// field access, plus a query projecting a field off a `reduce` fold.
func TestParse_structVector(t *testing.T) {
	src := `type X = {
  Pos rat0+
  Neg rat0+
}

type VX = vector X

fn J a::X b::X = {
  Pos: max a.Pos b.Pos,
  Neg: max a.Neg b.Neg,
}

fn incPos k::rat0+ s::X = { Pos: + s.Pos k, Neg: s.Neg }

merge VX = zip J

update VX.AddPos k::rat0+ = local (incPos k)

query VX.Net = - (reduce J { Pos: 0, Neg: 0 } v).Pos (reduce J { Pos: 0, Neg: 0 } v).Neg

collection C = VX
`
	plan, err := Parse(src)
	require.NoError(t, err)

	require.Len(t, plan.Types, 2)
	x := plan.Types[0]
	assert.Equal(t, "X", x.Name)
	assert.Equal(t, KindStruct, x.Elem.Kind)
	require.Len(t, x.Elem.Fields, 2)
	assert.Equal(t, "Pos", x.Elem.Fields[0].Name)
	assert.Equal(t, "rat0+", x.Elem.Fields[0].Type)
	assert.Equal(t, "Neg", x.Elem.Fields[1].Name)

	vx := plan.Types[1]
	assert.Equal(t, KindVector, vx.Elem.Kind)
	assert.Equal(t, "X", vx.Elem.Elem)

	require.Len(t, plan.Functions, 2)
	j := plan.Functions[0]
	assert.Equal(t, "J", j.Name)
	assert.Equal(t, "X", j.Params[0].Type)
	require.Equal(t, ExprStructLit, j.Body.Kind)
	require.Len(t, j.Body.StructFields, 2)
	assert.Equal(t, "Pos", j.Body.StructFields[0].Name)

	require.Len(t, plan.Updates, 1)
	require.Len(t, plan.Queries, 1)
	q := plan.Queries[0]
	assert.Equal(t, "Net", q.MethodName)
	// - <field> <field>
	require.Equal(t, ExprApp, q.Body.Kind)
	require.Len(t, q.Body.Args, 2)
	assert.Equal(t, ExprField, q.Body.Args[0].Kind)
	assert.Equal(t, "Pos", q.Body.Args[0].Field)
	assert.Equal(t, ExprReduce, q.Body.Args[0].Target.Kind)
}

func TestParse_collection(t *testing.T) {
	plan, err := Parse("collection MyVec = T\n")
	require.NoError(t, err)
	require.Len(t, plan.Collections, 1)
	c := plan.Collections[0]
	assert.Equal(t, "MyVec", c.Name)
	assert.Nil(t, c.Key)
	assert.Equal(t, "T", c.Type)
}

func TestParse_keyedCollection(t *testing.T) {
	plan, err := Parse("collection Users[id::string] = T\n")
	require.NoError(t, err)
	require.Len(t, plan.Collections, 1)
	c := plan.Collections[0]
	assert.Equal(t, "Users", c.Name)
	require.NotNil(t, c.Key)
	assert.Equal(t, ParamSpec{Name: "id", Type: "string"}, *c.Key)
	assert.Equal(t, "T", c.Type)
}

func TestParse_keyedCollectionRejectsMultipleKeys(t *testing.T) {
	_, err := Parse("collection Users[id::string, tenant::string] = T\n")
	require.Error(t, err)
}

func TestParse_sectionNumberLiteral(t *testing.T) {
	plan, err := Parse("update T.Inc = local (+ 1)\n")
	require.NoError(t, err)
	sec := plan.Updates[0].Body.Fn
	require.Equal(t, ExprApp, sec.Kind)
	assert.Equal(t, "+", sec.Head.Name)
	require.Len(t, sec.Args, 1)
	assert.Equal(t, ExprNumLit, sec.Args[0].Kind)
	assert.Equal(t, big.NewRat(1, 1), sec.Args[0].Num)
}

func TestParse_decimalLiteral(t *testing.T) {
	plan, err := Parse("query T.V = reduce + 2.5 v\n")
	require.NoError(t, err)
	assert.Equal(t, big.NewRat(5, 2), plan.Queries[0].Body.Init.Num)
}

func TestParse_operators(t *testing.T) {
	cases := map[string]string{
		"merge T = zip max\n": "max",
		"merge T = zip min\n": "min",
		"merge T = zip +\n":   "+",
		"merge T = zip *\n":   "*",
		"merge T = zip -\n":   "-",
	}
	for src, want := range cases {
		plan, err := Parse(src)
		require.NoError(t, err, "src: %q", src)
		assert.Equal(t, want, plan.Merges[0].Body.Fn.Name, "src: %q", src)
	}
}

// fn body with nested parenthesised application: + a (max b c)
func TestParse_nestedApplication(t *testing.T) {
	plan, err := Parse("fn f a::rat b::rat c::rat = + a (max b c)\n")
	require.NoError(t, err)
	require.Len(t, plan.Functions, 1)
	body := plan.Functions[0].Body
	require.Equal(t, ExprApp, body.Kind)
	assert.Equal(t, "+", body.Head.Name)
	require.Len(t, body.Args, 2)
	assert.Equal(t, "a", body.Args[0].Name)
	// second arg is (max b c)
	inner := body.Args[1]
	require.Equal(t, ExprApp, inner.Kind)
	assert.Equal(t, "max", inner.Head.Name)
	require.Len(t, inner.Args, 2)
	assert.Equal(t, "b", inner.Args[0].Name)
	assert.Equal(t, "c", inner.Args[1].Name)
}

// An identifier with a primitive prefix must parse as one name, not as the
// operator token max/min followed by a remainder.
func TestParse_primitivePrefixedIdentifier(t *testing.T) {
	plan, err := Parse("fn f maximum::rat = maximum\n")
	require.NoError(t, err)
	fn := plan.Functions[0]
	require.Len(t, fn.Params, 1)
	assert.Equal(t, "maximum", fn.Params[0].Name)
	require.Equal(t, ExprName, fn.Body.Kind)
	assert.Equal(t, "maximum", fn.Body.Name)
}

// `reduce + 0`: the fn slot is a single atom, so the init number is not
// swallowed as an argument to `+`.
func TestParse_reduceFnIsAtomNotApplication(t *testing.T) {
	plan, err := Parse("query T.V = reduce + 0 v\n")
	require.NoError(t, err)
	body := plan.Queries[0].Body
	require.Equal(t, ExprReduce, body.Kind)
	assert.Equal(t, ExprName, body.Fn.Kind) // not an ExprApp
	assert.Equal(t, "+", body.Fn.Name)
	assert.Equal(t, big.NewRat(0, 1), body.Init.Num)
}

func TestParse_skipBlankAndCommentLines(t *testing.T) {
	plan, err := Parse("\n# a comment\ntype T = vector rat\n\n")
	require.NoError(t, err)
	assert.Len(t, plan.Types, 1)
}

func TestParse_unknownLineIsError(t *testing.T) {
	// A typo like `udpate` matches no keyword and must fail loudly rather than
	// being silently swallowed.
	_, err := Parse("type T = vector rat0+\nudpate T.Add k::rat0+ = local (+ k)\ncollection X = T\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized statement")
}

func TestParse_trailingComments(t *testing.T) {
	src := "type T = vector rat0+   # a counter\n" +
		"merge T = zip max   # elementwise max\n" +
		"update T.Add k::rat0+ = local (+ k)   # bump\n" +
		"collection X = T   # instance\n"
	plan, err := Parse(src)
	require.NoError(t, err)
	assert.Len(t, plan.Types, 1)
	assert.Len(t, plan.Merges, 1)
	assert.Len(t, plan.Updates, 1)
	assert.Len(t, plan.Collections, 1)
}

func TestParse_commentInsideStructBody(t *testing.T) {
	// A comment on the `{` line and on a field line (the form used in the docs).
	src := "type X = {   # a struct\n  Pos rat0+   # positive part\n  Neg rat0+\n}\n"
	plan, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, plan.Types, 1)
	assert.Equal(t, KindStruct, plan.Types[0].Elem.Kind)
	assert.Len(t, plan.Types[0].Elem.Fields, 2)
}

func TestParse_commentBetweenGuardCases(t *testing.T) {
	src := "fn grade x::rat\n" +
		"| (> x 90) = \"A\"\n" +
		"# comment between guards\n" +
		"| otherwise = \"F\"\n"
	plan, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, plan.Functions, 1)
	body := plan.Functions[0].Body
	require.Equal(t, ExprGuards, body.Kind)
	assert.Len(t, body.Cases, 2)
}

func TestParse_negativeLiterals(t *testing.T) {
	// `-5` and `-2.5` are negative literals; `- 5` (with a space) is the operator.
	plan, err := Parse("fn f a::rat = max a -5\n")
	require.NoError(t, err)
	neg := plan.Functions[0].Body.Args[1] // max a (-5)
	require.Equal(t, ExprNumLit, neg.Kind)
	assert.Equal(t, big.NewRat(-5, 1), neg.Num)

	plan, err = Parse("fn g a::rat = max a -2.5\n")
	require.NoError(t, err)
	neg = plan.Functions[0].Body.Args[1]
	require.Equal(t, ExprNumLit, neg.Kind)
	assert.Equal(t, big.NewRat(-5, 2), neg.Num)

	// `- 5` stays the subtraction operator applied to two args.
	plan, err = Parse("fn h a::rat = - a 5\n")
	require.NoError(t, err)
	body := plan.Functions[0].Body
	require.Equal(t, ExprApp, body.Kind)
	assert.Equal(t, "-", body.Head.Name)
	assert.Len(t, body.Args, 2)
}

func TestParse_empty(t *testing.T) {
	plan, err := Parse("")
	require.NoError(t, err)
	assert.Empty(t, plan.Types)
	assert.Empty(t, plan.Collections)
}

// A recognized-but-malformed line is a parse error, not silently skipped,
// because Or is committed once the keyword prefix is consumed. (An unknown but
// well-formed element token like `vector foo` is now a build error, not a parse
// error — the parser is semantics-free about type names — so this uses a genuine
// syntax error: `vector` with no element token.)
func TestParse_malformedTypeIsError(t *testing.T) {
	_, err := Parse("type T = vector\n")
	require.Error(t, err)
}

// `fn` with no params is rejected (zero-arg functions are not allowed).
func TestParse_fnWithoutParamsIsError(t *testing.T) {
	_, err := Parse("fn five = 5\n")
	require.Error(t, err)
}

func TestParse_errorHasPosition(t *testing.T) {
	_, err := Parse("type T = vector\n")
	require.Error(t, err)
	var pe ParseError
	require.ErrorAs(t, err, &pe)
	assert.NotZero(t, pe.Line)
	assert.NotZero(t, pe.Col)
}

// ---- V.Elem parsing, inline vector struct, element-ref aliases -------------

// An inline struct vector element parses as KindVector carrying an Inner struct;
// `V.Elem` param and field tokens survive verbatim; `type X = V.Elem` is KindElemRef.
func TestParse_inlineVectorStructAndElemRef(t *testing.T) {
	src := `type V = vector {
  Pos rat0+
  Neg rat0+
}
fn J a::V.Elem b::V.Elem = { Pos: max a.Pos b.Pos, Neg: max a.Neg b.Neg }
type X = V.Elem
`
	plan, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, plan.Types, 2)

	v := plan.Types[0]
	assert.Equal(t, "V", v.Name)
	assert.Equal(t, KindVector, v.Elem.Kind)
	assert.Equal(t, "", v.Elem.Elem) // inline: no token
	require.NotNil(t, v.Elem.Inner)
	assert.Equal(t, KindStruct, v.Elem.Inner.Kind)
	require.Len(t, v.Elem.Inner.Fields, 2)
	assert.Equal(t, "Pos", v.Elem.Inner.Fields[0].Name)
	assert.Equal(t, "rat0+", v.Elem.Inner.Fields[0].Type)

	j := plan.Functions[0]
	assert.Equal(t, "V.Elem", j.Params[0].Type)
	assert.Equal(t, "V.Elem", j.Params[1].Type)

	x := plan.Types[1]
	assert.Equal(t, "X", x.Name)
	assert.Equal(t, KindElemRef, x.Elem.Kind)
	assert.Equal(t, "V.Elem", x.Elem.Elem)
}

// A type name beginning with the `vector` keyword must not be swallowed by the
// vector alternative — `vectorClock.Elem` parses as an element-ref (guards the
// Try(vector ...) committed-choice fix).
func TestParse_vectorPrefixedNameBoundary(t *testing.T) {
	src := `type vectorClock = vector rat0+
type X = vectorClock.Elem
`
	plan, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, plan.Types, 2)
	assert.Equal(t, KindVector, plan.Types[0].Elem.Kind)
	assert.Equal(t, "rat0+", plan.Types[0].Elem.Elem)
	assert.Equal(t, KindElemRef, plan.Types[1].Elem.Kind)
	assert.Equal(t, "vectorClock.Elem", plan.Types[1].Elem.Elem)
}

// A vector whose element is itself a `.Elem` token keeps the token in Elem.
func TestParse_vectorOfElemRefToken(t *testing.T) {
	src := `type W = vector V.Elem
`
	plan, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, plan.Types, 1)
	assert.Equal(t, KindVector, plan.Types[0].Elem.Kind)
	assert.Equal(t, "V.Elem", plan.Types[0].Elem.Elem)
	assert.Nil(t, plan.Types[0].Elem.Inner)
}
