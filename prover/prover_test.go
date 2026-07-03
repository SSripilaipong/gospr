package prover

import (
	"fmt"
	"math/big"
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
func numE(n float64) parser.Expr {
	return parser.Expr{Kind: parser.ExprNumLit, Num: new(big.Rat).SetFloat64(n)}
}
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

var tRat = crdt.ElemT{Num: numtype.NumType{Domain: numtype.DRat, Sign: numtype.SAny}}

func update(body parser.Expr, params ...parser.ParamSpec) crdt.Method {
	return crdt.Method{Params: params, Body: body, Result: parser.TypeReal}
}

// ---- merge-law obligations -----------------------------------------

func TestProve_maxIsJoin(t *testing.T) {
	require.NoError(t, Prove(tRat, zipE(prim("max")), nil, nil))
}

func TestProve_minIsJoin(t *testing.T) {
	require.NoError(t, Prove(tRat, zipE(prim("min")), nil, nil))
}

// `zip +` sums slots: add(x,x) = 2x != x, so it is not idempotent and must be
// rejected as a non-lattice merge.
func TestProve_sumMergeRejected(t *testing.T) {
	err := Prove(tRat, zipE(prim("+")), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "join-semilattice")
}

// A user merge fn that inlines to max is accepted; one that inlines to an
// average (idempotent but not associative) is rejected.
func TestProve_userMergeFns(t *testing.T) {
	maxFn := crdt.Function{Name: "lub", Params: []parser.ParamSpec{{Name: "a", Type: "rat"}, {Name: "b", Type: "rat"}},
		Body: appE(prim("max"), varE("a"), varE("b"))}
	require.NoError(t, Prove(tRat, zipE(fnRef("lub", 2)), nil, map[string]crdt.Function{"lub": maxFn}))

	// avg a b = * (+ a b) 0.5  — idempotent but not associative.
	avgFn := crdt.Function{Name: "avg", Params: []parser.ParamSpec{{Name: "a", Type: "rat"}, {Name: "b", Type: "rat"}},
		Body: appE(prim("*"), appE(prim("+"), varE("a"), varE("b")), numE(0.5))}
	err := Prove(tRat, zipE(fnRef("avg", 2)), nil, map[string]crdt.Function{"avg": avgFn})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "associativity")
}

func TestProve_recursiveMergeRejected(t *testing.T) {
	loop := crdt.Function{Name: "loop", Params: []parser.ParamSpec{{Name: "a", Type: "rat"}, {Name: "b", Type: "rat"}},
		Body: appE(fnRef("loop", 2), varE("a"), varE("b"))}
	err := Prove(tRat, zipE(fnRef("loop", 2)), nil, map[string]crdt.Function{"loop": loop})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recursive")
}

// ---- update inflationary obligations -------------------------------

func TestProve_additionInflationary(t *testing.T) {
	updates := map[string]crdt.Method{
		"Add": update(localE(appE(prim("+"), varE("k"))), parser.ParamSpec{Name: "k", Type: "rat0+"}),
	}
	require.NoError(t, Prove(tRat, zipE(prim("max")), updates, nil))
}

// `+ k` with k::rat (sign unknown) can decrease the slot, so it is not
// inflationary under max.
func TestProve_unsignedAdditionRejected(t *testing.T) {
	updates := map[string]crdt.Method{
		"Add": update(localE(appE(prim("+"), varE("k"))), parser.ParamSpec{Name: "k", Type: "rat"}),
	}
	err := Prove(tRat, zipE(prim("max")), updates, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not inflationary")
}

// Mixed-domain: a `vector rat` slot (Real) with an int0+ param (Int via
// is_int) — the obligation declares each variable from its own numeric type.
func TestProve_mixedDomainParam(t *testing.T) {
	updates := map[string]crdt.Method{
		"Add": update(localE(appE(prim("+"), varE("k"))), parser.ParamSpec{Name: "k", Type: "int0+"}),
	}
	require.NoError(t, Prove(tRat, zipE(prim("max")), updates, nil))
}

// DSL identifiers may be Unicode (the parser uses unicode.IsLetter), which is
// not a valid SMT-LIB symbol. The prover must rename params to internal SMT
// names rather than emit them verbatim, else Z3 rejects the script.
func TestProve_unicodeParamName(t *testing.T) {
	updates := map[string]crdt.Method{
		"Add": update(localE(appE(prim("+"), varE("κ"))), parser.ParamSpec{Name: "κ", Type: "rat0+"}),
	}
	require.NoError(t, Prove(tRat, zipE(prim("max")), updates, nil))
}

// A min merge induces the reverse order, so a non-positive increment is the
// inflationary one there.
func TestProve_minMergeNonPosUpdate(t *testing.T) {
	tRatNonPos := crdt.ElemT{Num: numtype.NumType{Domain: numtype.DRat, Sign: numtype.SNonPos}}
	updates := map[string]crdt.Method{
		"Dec": update(localE(appE(prim("+"), varE("k"))), parser.ParamSpec{Name: "k", Type: "rat0-"}),
	}
	require.NoError(t, Prove(tRatNonPos, zipE(prim("min")), updates, nil))
}

// ---- solver-availability seam --------------------------------------

func TestProve_z3MissingIsActionableError(t *testing.T) {
	orig := lookPath
	lookPath = func(string) (string, error) { return "", fmt.Errorf("not found") }
	defer func() { lookPath = orig }()

	err := Prove(tRat, zipE(prim("max")), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "z3")
}

// ---- struct element convergence ------------------------------------

// structElem is a { Pos rat0+, Neg rat0+ } descriptor for the struct proofs.
var structElem = crdt.ElemT{
	Struct: true,
	Name:   "X",
	Fields: []crdt.FieldT{
		{Name: "Pos", Type: crdt.ElemT{Num: numtype.NumType{Domain: numtype.DRat, Sign: numtype.SNonNeg}}},
		{Name: "Neg", Type: crdt.ElemT{Num: numtype.NumType{Domain: numtype.DRat, Sign: numtype.SNonNeg}}},
	},
}

func fieldE(target parser.Expr, name string) parser.Expr {
	t := target
	return parser.Expr{Kind: parser.ExprField, Target: &t, Field: name}
}
func structLitE(names []string, vals []parser.Expr) parser.Expr {
	fields := make([]parser.StructField, len(names))
	for i := range names {
		v := vals[i]
		fields[i] = parser.StructField{Name: names[i], Value: &v}
	}
	return parser.Expr{Kind: parser.ExprStructLit, StructFields: fields}
}

// jMax is `fn J a b = { Pos: max a.Pos b.Pos, Neg: max a.Neg b.Neg }` — a product
// join-semilattice, so the struct merge is provable.
func jMax() crdt.Function {
	body := structLitE(
		[]string{"Pos", "Neg"},
		[]parser.Expr{
			appE(prim("max"), fieldE(varE("a"), "Pos"), fieldE(varE("b"), "Pos")),
			appE(prim("max"), fieldE(varE("a"), "Neg"), fieldE(varE("b"), "Neg")),
		},
	)
	return crdt.Function{Name: "J", Params: []parser.ParamSpec{{Name: "a", Type: "X"}, {Name: "b", Type: "X"}}, Body: body}
}

// A per-field max struct merge is a product lattice: commutative, associative,
// idempotent — proven over the flattened leaf scalars.
func TestProve_structPerFieldMaxAccepted(t *testing.T) {
	require.NoError(t, Prove(structElem, zipE(fnRef("J", 2)), nil, map[string]crdt.Function{"J": jMax()}))
}

// A per-field SUM struct merge is not idempotent, so it is rejected.
func TestProve_structPerFieldSumRejected(t *testing.T) {
	sum := crdt.Function{Name: "S", Params: []parser.ParamSpec{{Name: "a", Type: "X"}, {Name: "b", Type: "X"}},
		Body: structLitE([]string{"Pos", "Neg"}, []parser.Expr{
			appE(prim("+"), fieldE(varE("a"), "Pos"), fieldE(varE("b"), "Pos")),
			appE(prim("+"), fieldE(varE("a"), "Neg"), fieldE(varE("b"), "Neg")),
		})}
	err := Prove(structElem, zipE(fnRef("S", 2)), nil, map[string]crdt.Function{"S": sum})
	require.Error(t, err)
}

// A struct update that increments one field by a rat0+ param is inflationary
// under the per-field max merge (max(x, x+k) = x+k for k >= 0).
func TestProve_structUpdateInflationary(t *testing.T) {
	incPos := crdt.Function{Name: "incPos", Params: []parser.ParamSpec{{Name: "k", Type: "rat0+"}, {Name: "s", Type: "X"}},
		Body: structLitE([]string{"Pos", "Neg"}, []parser.Expr{
			appE(prim("+"), fieldE(varE("s"), "Pos"), varE("k")),
			fieldE(varE("s"), "Neg"),
		})}
	updates := map[string]crdt.Method{
		"AddPos": {Params: []parser.ParamSpec{{Name: "k", Type: "rat0+"}}, Body: localE(appE(fnRef("incPos", 2), varE("k")))},
	}
	require.NoError(t, Prove(structElem, zipE(fnRef("J", 2)), updates, map[string]crdt.Function{"J": jMax(), "incPos": incPos}))
}
