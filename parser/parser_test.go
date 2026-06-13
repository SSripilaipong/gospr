package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mandated integration test: parse the canonical snippet (now including a
// user-defined function used in merge) into the AST.
func TestParse_integration(t *testing.T) {
	src := `type T = vector real

fn lub a::real b::real = max a b

merge T = zip lub

query T.Value = reduce + 0

update T.Add k::real = local (+ k)
`
	plan, err := Parse(src)
	require.NoError(t, err)

	// --- types ---
	require.Len(t, plan.Types, 1)
	td := plan.Types[0]
	assert.Equal(t, "T", td.Name)
	assert.Equal(t, KindReal, td.Elem.Kind)

	// --- function: fn lub a b = max a b ---
	require.Len(t, plan.Functions, 1)
	fn := plan.Functions[0]
	assert.Equal(t, "lub", fn.Name)
	require.Len(t, fn.Params, 2)
	assert.Equal(t, "a", fn.Params[0].Name)
	assert.Equal(t, "real", fn.Params[0].Type)
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
	assert.Equal(t, float64(0), qd.Body.Init.Num)

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

func TestParse_collection(t *testing.T) {
	plan, err := Parse("collection MyVec = T\n")
	require.NoError(t, err)
	require.Len(t, plan.Collections, 1)
	c := plan.Collections[0]
	assert.Equal(t, "MyVec", c.Name)
	assert.Equal(t, "T", c.Type)
}

func TestParse_sectionNumberLiteral(t *testing.T) {
	plan, err := Parse("update T.Inc = local (+ 1)\n")
	require.NoError(t, err)
	sec := plan.Updates[0].Body.Fn
	require.Equal(t, ExprApp, sec.Kind)
	assert.Equal(t, "+", sec.Head.Name)
	require.Len(t, sec.Args, 1)
	assert.Equal(t, ExprNumLit, sec.Args[0].Kind)
	assert.Equal(t, float64(1), sec.Args[0].Num)
}

func TestParse_decimalLiteral(t *testing.T) {
	plan, err := Parse("query T.V = reduce + 2.5\n")
	require.NoError(t, err)
	assert.Equal(t, 2.5, plan.Queries[0].Body.Init.Num)
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
	plan, err := Parse("fn f a::real b::real c::real = + a (max b c)\n")
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
	plan, err := Parse("fn f maximum::real = maximum\n")
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
	plan, err := Parse("query T.V = reduce + 0\n")
	require.NoError(t, err)
	body := plan.Queries[0].Body
	require.Equal(t, ExprReduce, body.Kind)
	assert.Equal(t, ExprName, body.Fn.Kind) // not an ExprApp
	assert.Equal(t, "+", body.Fn.Name)
	assert.Equal(t, float64(0), body.Init.Num)
}

func TestParse_skipBlankAndUnknownLines(t *testing.T) {
	plan, err := Parse("\n# a comment\ntype T = vector real\n\n")
	require.NoError(t, err)
	assert.Len(t, plan.Types, 1)
}

func TestParse_empty(t *testing.T) {
	plan, err := Parse("")
	require.NoError(t, err)
	assert.Empty(t, plan.Types)
	assert.Empty(t, plan.Collections)
}

// A recognized-but-malformed line is a parse error, not silently skipped,
// because Or is committed once the keyword prefix is consumed.
func TestParse_malformedTypeIsError(t *testing.T) {
	_, err := Parse("type T = vector foo\n")
	require.Error(t, err)
}

// `fn` with no params is rejected (zero-arg functions are not allowed).
func TestParse_fnWithoutParamsIsError(t *testing.T) {
	_, err := Parse("fn five = 5\n")
	require.Error(t, err)
}

func TestParse_errorHasPosition(t *testing.T) {
	_, err := Parse("type T = vector foo\n")
	require.Error(t, err)
	var pe ParseError
	require.ErrorAs(t, err, &pe)
	assert.NotZero(t, pe.Line)
	assert.NotZero(t, pe.Col)
}
