package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mandated integration test: parse the canonical snippet into the AST.
func TestParse_integration(t *testing.T) {
	src := `type T = vector real

merge T = zip max

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

	// --- merge ---
	require.Len(t, plan.Merges, 1)
	md := plan.Merges[0]
	assert.Equal(t, "T", md.TypeName)
	assert.Equal(t, ExprZip, md.Body.Kind)
	require.NotNil(t, md.Body.Fn)
	assert.Equal(t, ExprFuncRef, md.Body.Fn.Kind)
	assert.Equal(t, "max", md.Body.Fn.Op)

	// --- query ---
	require.Len(t, plan.Queries, 1)
	qd := plan.Queries[0]
	assert.Equal(t, "T", qd.TypeName)
	assert.Equal(t, "Value", qd.MethodName)
	assert.Empty(t, qd.Params)
	assert.Equal(t, ExprReduce, qd.Body.Kind)
	require.NotNil(t, qd.Body.Fn)
	assert.Equal(t, "+", qd.Body.Fn.Op)
	require.NotNil(t, qd.Body.Init)
	assert.Equal(t, ExprNumLit, qd.Body.Init.Kind)
	assert.Equal(t, float64(0), qd.Body.Init.Num)

	// --- update ---
	require.Len(t, plan.Updates, 1)
	ud := plan.Updates[0]
	assert.Equal(t, "T", ud.TypeName)
	assert.Equal(t, "Add", ud.MethodName)
	require.Len(t, ud.Params, 1)
	assert.Equal(t, "k", ud.Params[0].Name)
	assert.Equal(t, "real", ud.Params[0].Type)
	assert.Equal(t, ExprLocal, ud.Body.Kind)
	require.NotNil(t, ud.Body.Fn)
	sec := ud.Body.Fn
	assert.Equal(t, ExprSection, sec.Kind)
	assert.Equal(t, "+", sec.Op)
	require.NotNil(t, sec.Arg)
	assert.Equal(t, ExprParamRef, sec.Arg.Kind)
	assert.Equal(t, "k", sec.Arg.Param)

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
	assert.Equal(t, "+", sec.Op)
	require.NotNil(t, sec.Arg)
	assert.Equal(t, ExprNumLit, sec.Arg.Kind)
	assert.Equal(t, float64(1), sec.Arg.Num)
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
		assert.Equal(t, want, plan.Merges[0].Body.Fn.Op, "src: %q", src)
	}
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

func TestParse_malformedMergeIsError(t *testing.T) {
	_, err := Parse("merge T = zip notAnOp\n")
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
