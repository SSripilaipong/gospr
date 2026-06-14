package prover

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gospr/crdt"
	"gospr/numtype"
	"gospr/parser"
)

// ---- resolved-Expr builders (post-Build shapes) --------------------

func prim(name string) parser.Expr {
	return parser.Expr{Kind: parser.ExprRef, Name: name, Arity: 2, Ref: parser.RefPrimitive}
}
func fnRef(name string, arity int) parser.Expr {
	return parser.Expr{Kind: parser.ExprRef, Name: name, Arity: arity, Ref: parser.RefFunction}
}
func varE(name string) parser.Expr { return parser.Expr{Kind: parser.ExprVar, Name: name} }
func numE(n float64) parser.Expr   { return parser.Expr{Kind: parser.ExprNumLit, Num: n} }
func appE(head parser.Expr, args ...parser.Expr) parser.Expr {
	h := head
	ps := make([]*parser.Expr, len(args))
	for i := range args {
		a := args[i]
		ps[i] = &a
	}
	return parser.Expr{Kind: parser.ExprApp, Head: &h, Args: ps}
}
func zipE(fn parser.Expr) parser.Expr {
	f := fn
	return parser.Expr{Kind: parser.ExprZip, Fn: &f}
}
func localE(fn parser.Expr) parser.Expr {
	f := fn
	return parser.Expr{Kind: parser.ExprLocal, Fn: &f}
}

var tReal = numtype.NumType{Domain: numtype.DReal, Sign: numtype.SAny}

func update(body parser.Expr, params ...parser.ParamSpec) crdt.Method {
	return crdt.Method{Params: params, Body: body, Result: parser.TypeReal}
}

// ---- merge-law obligations -----------------------------------------

func TestProve_maxIsJoin(t *testing.T) {
	require.NoError(t, Prove(tReal, zipE(prim("max")), nil, nil))
}

func TestProve_minIsJoin(t *testing.T) {
	require.NoError(t, Prove(tReal, zipE(prim("min")), nil, nil))
}

// `zip +` sums slots: add(x,x) = 2x != x, so it is not idempotent and must be
// rejected as a non-lattice merge.
func TestProve_sumMergeRejected(t *testing.T) {
	err := Prove(tReal, zipE(prim("+")), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "join-semilattice")
}

// A user merge fn that inlines to max is accepted; one that inlines to an
// average (idempotent but not associative) is rejected.
func TestProve_userMergeFns(t *testing.T) {
	maxFn := crdt.Function{Name: "lub", Params: []parser.ParamSpec{{Name: "a", Type: "real"}, {Name: "b", Type: "real"}},
		Body: appE(prim("max"), varE("a"), varE("b"))}
	require.NoError(t, Prove(tReal, zipE(fnRef("lub", 2)), nil, map[string]crdt.Function{"lub": maxFn}))

	// avg a b = * (+ a b) 0.5  — idempotent but not associative.
	avgFn := crdt.Function{Name: "avg", Params: []parser.ParamSpec{{Name: "a", Type: "real"}, {Name: "b", Type: "real"}},
		Body: appE(prim("*"), appE(prim("+"), varE("a"), varE("b")), numE(0.5))}
	err := Prove(tReal, zipE(fnRef("avg", 2)), nil, map[string]crdt.Function{"avg": avgFn})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "associativity")
}

func TestProve_recursiveMergeRejected(t *testing.T) {
	loop := crdt.Function{Name: "loop", Params: []parser.ParamSpec{{Name: "a", Type: "real"}, {Name: "b", Type: "real"}},
		Body: appE(fnRef("loop", 2), varE("a"), varE("b"))}
	err := Prove(tReal, zipE(fnRef("loop", 2)), nil, map[string]crdt.Function{"loop": loop})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recursive")
}

// ---- update inflationary obligations -------------------------------

func TestProve_additionInflationary(t *testing.T) {
	updates := map[string]crdt.Method{
		"Add": update(localE(appE(prim("+"), varE("k"))), parser.ParamSpec{Name: "k", Type: "real0+"}),
	}
	require.NoError(t, Prove(tReal, zipE(prim("max")), updates, nil))
}

// `+ k` with k::real (sign unknown) can decrease the slot, so it is not
// inflationary under max.
func TestProve_unsignedAdditionRejected(t *testing.T) {
	updates := map[string]crdt.Method{
		"Add": update(localE(appE(prim("+"), varE("k"))), parser.ParamSpec{Name: "k", Type: "real"}),
	}
	err := Prove(tReal, zipE(prim("max")), updates, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not inflationary")
}

// Mixed-domain: a `vector real` slot (Real) with an int0+ param (Int via
// is_int) — the obligation declares each variable from its own numeric type.
func TestProve_mixedDomainParam(t *testing.T) {
	updates := map[string]crdt.Method{
		"Add": update(localE(appE(prim("+"), varE("k"))), parser.ParamSpec{Name: "k", Type: "int0+"}),
	}
	require.NoError(t, Prove(tReal, zipE(prim("max")), updates, nil))
}

// DSL identifiers may be Unicode (the parser uses unicode.IsLetter), which is
// not a valid SMT-LIB symbol. The prover must rename params to internal SMT
// names rather than emit them verbatim, else Z3 rejects the script.
func TestProve_unicodeParamName(t *testing.T) {
	updates := map[string]crdt.Method{
		"Add": update(localE(appE(prim("+"), varE("κ"))), parser.ParamSpec{Name: "κ", Type: "real0+"}),
	}
	require.NoError(t, Prove(tReal, zipE(prim("max")), updates, nil))
}

// A min merge induces the reverse order, so a non-positive increment is the
// inflationary one there.
func TestProve_minMergeNonPosUpdate(t *testing.T) {
	tRealNonPos := numtype.NumType{Domain: numtype.DReal, Sign: numtype.SNonPos}
	updates := map[string]crdt.Method{
		"Dec": update(localE(appE(prim("+"), varE("k"))), parser.ParamSpec{Name: "k", Type: "real0-"}),
	}
	require.NoError(t, Prove(tRealNonPos, zipE(prim("min")), updates, nil))
}

// ---- solver-availability seam --------------------------------------

func TestProve_z3MissingIsActionableError(t *testing.T) {
	orig := lookPath
	lookPath = func(string) (string, error) { return "", fmt.Errorf("not found") }
	defer func() { lookPath = orig }()

	err := Prove(tReal, zipE(prim("max")), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "z3")
}
