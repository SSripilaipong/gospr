// Package prover discharges the two CvRDT correctness obligations for every
// vector type at build time, rejecting a deploy whose merge/update pair cannot
// be proven convergent:
//
//  1. Merge is a join-semilattice: its scalar binary fn is commutative,
//     associative, and idempotent.
//  2. Every update is inflationary in merge's induced order: merge(x, h(x)) = h(x).
//
// Because state is a vector merged elementwise (zip) and updates are local
// (unary on one slot), both obligations decompose to scalar reasoning over a
// single slot. Each obligation is lowered to a small, quantifier-free SMT
// formula and discharged by Z3 (see smt.go) — there is no pure-Go fast path, so
// the `z3` executable is a hard prerequisite for every build that proves a type.
//
// prover imports parser/numtype/crdt only (never builder), so builder can call
// it without an import cycle.
package prover

import (
	"fmt"
	"math/big"
	"sort"

	"gospr/crdt"
	"gospr/numtype"
	"gospr/parser"
)

// ---- symbolic IR ---------------------------------------------------
//
// sym is a tiny symbolic numeric/boolean term. It decouples expression-walking
// (lower) from SMT serialization (smt.go) and is independently testable. It is
// never pattern-matched for a "fast path" — everything is serialized to Z3.

type symKind int

const (
	symConst  symKind = iota // num (sortNum) or str (sortStr)
	symVar                   // name
	symBin                   // op in + - * max min, operands a,b
	symCmp                   // op in > < >= <= == /=, operands a,b (a boolean term)
	symIte                   // if cond then a else b
	symStruct                // struct value: fields (each a sym)
	symVector                // a whole-vector marker (write obligation): self slot + element type
)

// symSort is the SMT sort of a scalar sym: a Real (numeric leaf), a String (string
// leaf), or a Bool (a comparison). It selects the declared const sort and the
// comparison serialization (str.< vs <). Structs are not scalar and carry no sort.
type symSort int

const (
	sortNum  symSort = iota // Real
	sortStr                 // String
	sortBool                // Bool (comparison result)
)

type sym struct {
	kind   symKind
	sort   symSort
	num    *big.Rat
	str    string // symConst with sort==sortStr: the string value
	name   string
	op     string
	cond   *sym
	a, b   *sym
	fields map[string]sym // symStruct
	// symVector (write obligation marker): self is the calling node's own slot
	// (x = v[self], a syntactic fact so the reduce-domination bound applies), and
	// elemT is the vector's element type (to declare fresh fold-lemma vars).
	self  *sym
	elemT *crdt.ElemT
}

func vectorMarker(self sym, elem crdt.ElemT) sym {
	return sym{kind: symVector, self: &self, elemT: &elem}
}

func cst(f *big.Rat) sym  { return sym{kind: symConst, sort: sortNum, num: f} }
func strCst(s string) sym { return sym{kind: symConst, sort: sortStr, str: s} }
func vr(name string) sym  { return sym{kind: symVar, sort: sortNum, name: name} }
func strVr(name string) sym {
	return sym{kind: symVar, sort: sortStr, name: name}
}
func structSym(fields map[string]sym) sym {
	return sym{kind: symStruct, fields: fields}
}
func bin(op string, a, b sym) sym {
	return sym{kind: symBin, sort: sortNum, op: op, a: &a, b: &b}
}
func cmpSym(op string, a, b sym) sym {
	return sym{kind: symCmp, sort: sortBool, op: op, a: &a, b: &b}
}
func ite(cond, then, els sym) sym {
	return sym{kind: symIte, sort: then.sort, cond: &cond, a: &then, b: &els}
}

// symFn is a symbolic function value awaiting `arity` more arguments. Partial
// application (e.g. `(+ k)`) makes this uniform, mirroring crdt's rtVal closure.
type symFn struct {
	arity int
	call  func(args []sym) (sym, error)
}

var cmpOps = map[string]bool{">": true, "<": true, ">=": true, "<=": true, "==": true, "/=": true}

// slotVar is the internal SMT name for the single CRDT slot the obligations
// reason about. Like the merge-law vars (a/b/c) and the param vars (p0, p1, …)
// it is a solver-internal name — DSL identifiers never reach the solver.
const slotVar = "slot"

// ---- lowering ------------------------------------------------------

type prover struct {
	funcs map[string]crdt.Function
	// Accumulators populated while lowering a single `write` update body: each
	// `reduce` over the vector marker introduces a fresh symbolic result var
	// (extraDecls) and its sound domination hypotheses (extraAssume, asserted — not
	// negated — in the obligation). Reset before each write body is lowered.
	extraDecls  []varDecl
	extraAssume []sym
	reduceCount int
}

// eval lowers a resolved Expr to either a value sym or a symFn (exactly one is
// non-nil). env binds free variables (update params); visited tracks the
// user-function inlining chain so recursion is rejected rather than unfolded.
func (p *prover) eval(e parser.Expr, env map[string]sym, visited map[string]bool) (sym, *symFn, error) {
	switch e.Kind {
	case parser.ExprNumLit:
		return cst(e.Num), nil, nil
	case parser.ExprVar:
		v, ok := env[e.Name]
		if !ok {
			return sym{}, nil, fmt.Errorf("unbound variable %q", e.Name)
		}
		return v, nil, nil
	case parser.ExprRef:
		fn, err := p.refFn(e, visited)
		if err != nil {
			return sym{}, nil, err
		}
		return sym{}, fn, nil
	case parser.ExprApp:
		return p.evalApp(e, env, visited)
	case parser.ExprGuards:
		return p.evalGuards(e, env, visited)
	case parser.ExprStructLit:
		fields := make(map[string]sym, len(e.StructFields))
		for _, sf := range e.StructFields {
			if sf.Value == nil {
				return sym{}, nil, fmt.Errorf("struct field %q has no value", sf.Name)
			}
			v, fn, err := p.eval(*sf.Value, env, visited)
			if err != nil {
				return sym{}, nil, err
			}
			if fn != nil {
				return sym{}, nil, fmt.Errorf("struct field %q is not a value", sf.Name)
			}
			fields[sf.Name] = v
		}
		return structSym(fields), nil, nil
	case parser.ExprField:
		if e.Target == nil {
			return sym{}, nil, fmt.Errorf("field access has no target")
		}
		tv, tf, err := p.eval(*e.Target, env, visited)
		if err != nil {
			return sym{}, nil, err
		}
		if tf != nil {
			return sym{}, nil, fmt.Errorf("cannot access a field of a function")
		}
		if tv.kind != symStruct {
			return sym{}, nil, fmt.Errorf("cannot access field %q of a non-struct value", e.Field)
		}
		fv, ok := tv.fields[e.Field]
		if !ok {
			return sym{}, nil, fmt.Errorf("struct has no field %q", e.Field)
		}
		return fv, nil, nil
	case parser.ExprStrLit:
		return strCst(e.Str), nil, nil
	case parser.ExprReduce:
		v, err := p.evalReduce(e, env, visited)
		return v, nil, err
	case parser.ExprZip, parser.ExprLocal, parser.ExprWrite:
		return sym{}, nil, fmt.Errorf("a combinator cannot appear inside a merge/update function")
	default:
		return sym{}, nil, fmt.Errorf("cannot lower expression of kind %d", e.Kind)
	}
}

func (p *prover) evalApp(e parser.Expr, env map[string]sym, visited map[string]bool) (sym, *symFn, error) {
	if e.Head == nil {
		return sym{}, nil, fmt.Errorf("application has no head")
	}
	_, hf, err := p.eval(*e.Head, env, visited)
	if err != nil {
		return sym{}, nil, err
	}
	if hf == nil {
		return sym{}, nil, fmt.Errorf("cannot apply a non-function")
	}
	args := make([]sym, len(e.Args))
	for i, a := range e.Args {
		av, af, err := p.eval(*a, env, visited)
		if err != nil {
			return sym{}, nil, err
		}
		if af != nil {
			return sym{}, nil, fmt.Errorf("cannot pass a function as an argument")
		}
		args[i] = av
	}
	switch {
	case len(args) == hf.arity:
		r, err := hf.call(args)
		return r, nil, err
	case len(args) < hf.arity:
		bound := append([]sym(nil), args...)
		call := hf.call
		return sym{}, &symFn{arity: hf.arity - len(args), call: func(rest []sym) (sym, error) {
			return call(append(append([]sym(nil), bound...), rest...))
		}}, nil
	default:
		return sym{}, nil, fmt.Errorf("too many arguments: expected %d, got %d", hf.arity, len(args))
	}
}

// refFn turns a resolved Ref into a symFn. Primitives build a sym node;
// user functions inline their body, detecting recursion via visited.
func (p *prover) refFn(e parser.Expr, visited map[string]bool) (*symFn, error) {
	switch e.Ref {
	case parser.RefPrimitive:
		op := e.Name
		return &symFn{arity: 2, call: func(args []sym) (sym, error) {
			if cmpOps[op] {
				return cmpSym(op, args[0], args[1]), nil
			}
			return bin(op, args[0], args[1]), nil
		}}, nil
	case parser.RefFunction:
		def, ok := p.funcs[e.Name]
		if !ok {
			return nil, fmt.Errorf("unknown function %q", e.Name)
		}
		name := e.Name
		captured := visited
		return &symFn{arity: len(def.Params), call: func(args []sym) (sym, error) {
			if captured[name] {
				return sym{}, fmt.Errorf("cannot prove convergence of recursive function %q", name)
			}
			child := make(map[string]bool, len(captured)+1)
			for k := range captured {
				child[k] = true
			}
			child[name] = true
			callEnv := make(map[string]sym, len(def.Params))
			for i, prm := range def.Params {
				callEnv[prm.Name] = args[i]
			}
			v, fn, err := p.eval(def.Body, callEnv, child)
			if err != nil {
				return sym{}, err
			}
			if fn != nil {
				return sym{}, fmt.Errorf("function %q did not return a value", name)
			}
			return v, nil
		}}, nil
	default:
		return nil, fmt.Errorf("unknown reference kind for %q", e.Name)
	}
}

// evalGuards lowers a guarded body to a right-nested ite chain. The terminal
// otherwise case is the base of the chain (the build guarantees it is present
// and last), so the result is total.
func (p *prover) evalGuards(e parser.Expr, env map[string]sym, visited map[string]bool) (sym, *symFn, error) {
	n := len(e.Cases)
	if n == 0 {
		return sym{}, nil, fmt.Errorf("guarded body has no cases")
	}
	last := e.Cases[n-1]
	if !last.Otherwise || last.Result == nil {
		return sym{}, nil, fmt.Errorf("guarded body must end with an `otherwise` case")
	}
	acc, fn, err := p.eval(*last.Result, env, visited)
	if err != nil {
		return sym{}, nil, err
	}
	if fn != nil {
		return sym{}, nil, fmt.Errorf("guard result is not a value")
	}
	for i := n - 2; i >= 0; i-- {
		gc := e.Cases[i]
		if gc.Cond == nil || gc.Result == nil {
			return sym{}, nil, fmt.Errorf("malformed guard case")
		}
		cv, cf, err := p.eval(*gc.Cond, env, visited)
		if err != nil {
			return sym{}, nil, err
		}
		rv, rf, err := p.eval(*gc.Result, env, visited)
		if err != nil {
			return sym{}, nil, err
		}
		if cf != nil || rf != nil {
			return sym{}, nil, fmt.Errorf("guard condition/result is not a value")
		}
		acc, err = iteSym(cv, rv, acc)
		if err != nil {
			return sym{}, nil, err
		}
	}
	return acc, nil, nil
}

// iteSym builds `if cond then a else b`, distributing over struct fields when the
// branches are structs: ite(c, {p:tp,…}, {p:ep,…}) == {p: ite(c,tp,ep), …}. This
// keeps guarded struct-valued functions faithful — a scalar ite wrapping whole
// struct branches would hide the per-field structure from the SMT flattening
// (leafEqs sees a symIte leaf, not a symStruct), making a non-lattice merge like
// `fn First a b | c = a | otherwise = b` spuriously provable.
func iteSym(cond, a, b sym) (sym, error) {
	if a.kind == symStruct || b.kind == symStruct {
		if a.kind != symStruct || b.kind != symStruct {
			return sym{}, fmt.Errorf("guard branches have mismatched struct/scalar shape")
		}
		fields := make(map[string]sym, len(a.fields))
		for name, af := range a.fields {
			bf, ok := b.fields[name]
			if !ok {
				return sym{}, fmt.Errorf("guard branches have mismatched struct fields (missing %q)", name)
			}
			f, err := iteSym(cond, af, bf)
			if err != nil {
				return sym{}, err
			}
			fields[name] = f
		}
		return structSym(fields), nil
	}
	return ite(cond, a, b), nil
}

// evalFn lowers a function-valued term and asserts its arity, returning the
// symFn so callers can apply it to symbolic arguments.
func (p *prover) evalFn(e parser.Expr, env map[string]sym, want int) (*symFn, error) {
	_, fn, err := p.eval(e, env, map[string]bool{})
	if err != nil {
		return nil, err
	}
	if fn == nil || fn.arity != want {
		got := 0
		if fn != nil {
			got = fn.arity
		}
		return nil, fmt.Errorf("expected a function of %d argument(s), got one of %d", want, got)
	}
	return fn, nil
}

// evalReduce lowers `reduce <fold-fn> <init> <vec>` inside a `write` update body.
// The vector must be the write marker (so x = v[self] is a syntactic slot of it).
// The fold's result is an opaque fresh symbol M constrained by SOUND domination
// hypotheses: because x is one folded slot and the fold is a proven max/min over a
// fixed field projection, M >= init and M >= x.<proj> (>= for max, <= for min).
// The hypotheses are discharged as SMT lemmas over the fold fn (checkFoldLemmas)
// before being assumed, so a mis-recognized fold cannot smuggle an unsound bound.
func (p *prover) evalReduce(e parser.Expr, env map[string]sym, visited map[string]bool) (sym, error) {
	if e.Fn == nil || e.Init == nil || e.Vec == nil {
		return sym{}, fmt.Errorf("malformed reduce")
	}
	vv, vf, err := p.eval(*e.Vec, env, visited)
	if err != nil {
		return sym{}, err
	}
	if vf != nil || vv.kind != symVector {
		return sym{}, fmt.Errorf("reduce can only be proven when folding the whole vector passed to a `write` update")
	}
	iv, ifn, err := p.eval(*e.Init, env, visited)
	if err != nil {
		return sym{}, err
	}
	if ifn != nil {
		return sym{}, fmt.Errorf("reduce init must be a value")
	}
	// Recognize the concrete max/min-fold shape and extract the projected field path.
	op, path, err := foldProjection(*e.Fn, p.funcs)
	if err != nil {
		return sym{}, fmt.Errorf("reduce fold: %w", err)
	}
	// Verify the extracted bound in Z3 before trusting it.
	if err := p.checkFoldLemmas(*e.Fn, *vv.elemT, op, path); err != nil {
		return sym{}, err
	}
	selfProj, err := projectSym(*vv.self, path) // x.<proj>
	if err != nil {
		return sym{}, fmt.Errorf("reduce projection: %w", err)
	}
	name := fmt.Sprintf("reduceM%d", p.reduceCount)
	p.reduceCount++
	p.extraDecls = append(p.extraDecls, varDecl{name: name}) // unconstrained Real; bounds come from assumptions
	M := vr(name)
	rel := ">="
	if op == "min" {
		rel = "<="
	}
	p.extraAssume = append(p.extraAssume, cmpSym(rel, M, iv), cmpSym(rel, M, selfProj))
	return M, nil
}

// foldProjection recognizes the concrete fold shape the write proof supports and
// returns its operator ("max"/"min") and the field-access path it projects out of
// the element. Two forms are accepted: a bare max/min primitive (folding a scalar
// vector — identity projection, empty path), or a user function of exactly the
// shape `max acc e.<field…>` / `min acc e.<field…>` (one operand the accumulator
// param, the other a field projection of the element param). Anything else is
// rejected, so the emitted bound is always structurally justified.
func foldProjection(fn parser.Expr, funcs map[string]crdt.Function) (string, []string, error) {
	if fn.Kind == parser.ExprRef && fn.Ref == parser.RefPrimitive {
		if fn.Name == "max" || fn.Name == "min" {
			return fn.Name, nil, nil
		}
		return "", nil, fmt.Errorf("only max/min folds can be proven, got primitive %q", fn.Name)
	}
	if fn.Kind != parser.ExprRef || fn.Ref != parser.RefFunction {
		return "", nil, fmt.Errorf("fold must be a max/min primitive or a user function of shape `max acc e.<field>`")
	}
	def, ok := funcs[fn.Name]
	if !ok {
		return "", nil, fmt.Errorf("unknown function %q", fn.Name)
	}
	if len(def.Params) != 2 {
		return "", nil, fmt.Errorf("fold function %q must take two arguments", fn.Name)
	}
	accName, elemName := def.Params[0].Name, def.Params[1].Name
	body := def.Body
	if body.Kind != parser.ExprApp || body.Head == nil {
		return "", nil, fmt.Errorf("fold function %q body must be `max acc e.<field>`", fn.Name)
	}
	h := *body.Head
	if h.Kind != parser.ExprRef || h.Ref != parser.RefPrimitive || (h.Name != "max" && h.Name != "min") {
		return "", nil, fmt.Errorf("fold function %q must apply max/min", fn.Name)
	}
	if len(body.Args) != 2 {
		return "", nil, fmt.Errorf("fold function %q must apply max/min to two operands", fn.Name)
	}
	a0, a1 := *body.Args[0], *body.Args[1]
	if isVarNamed(a0, accName) {
		path, err := projectionPath(a1, elemName)
		return h.Name, path, err
	}
	if isVarNamed(a1, accName) {
		path, err := projectionPath(a0, elemName)
		return h.Name, path, err
	}
	return "", nil, fmt.Errorf("fold function %q must be `max acc e.<field>` (one operand the bare accumulator)", fn.Name)
}

func isVarNamed(e parser.Expr, name string) bool {
	return e.Kind == parser.ExprVar && e.Name == name
}

// projectionPath extracts the field-access chain rooted at variable `root`,
// returning field names in navigation (outermost-first) order. A bare `root`
// variable yields an empty path (a scalar element projected as itself).
func projectionPath(e parser.Expr, root string) ([]string, error) {
	var rev []string
	cur := e
	for cur.Kind == parser.ExprField {
		if cur.Target == nil {
			return nil, fmt.Errorf("malformed field access")
		}
		rev = append(rev, cur.Field)
		cur = *cur.Target
	}
	if !isVarNamed(cur, root) {
		return nil, fmt.Errorf("fold operand must project the element parameter %q", root)
	}
	path := make([]string, len(rev))
	for i, f := range rev {
		path[len(rev)-1-i] = f
	}
	return path, nil
}

// projectSym navigates a struct sym down a field path (as extracted by
// projectionPath). An empty path returns s unchanged (scalar element).
func projectSym(s sym, path []string) (sym, error) {
	for _, f := range path {
		if s.kind != symStruct {
			return sym{}, fmt.Errorf("cannot project field %q of a non-struct", f)
		}
		fv, ok := s.fields[f]
		if !ok {
			return sym{}, fmt.Errorf("struct has no field %q", f)
		}
		s = fv
	}
	return s, nil
}

// checkFoldLemmas discharges the two SMT lemmas that justify the reduce-domination
// bound: for a max-fold f(acc,e), f(acc,e) >= acc (inflationary in the accumulator)
// and f(acc,e) >= e.<proj> (dominates the projected field); for a min-fold both
// with <=. Proving these makes the emitted `M >= init` / `M >= x.<proj>`
// hypotheses sound, since M folds f over the slots (of which x is one) from init.
func (p *prover) checkFoldLemmas(fnExpr parser.Expr, elemT crdt.ElemT, op string, path []string) error {
	foldFn, err := p.evalFn(fnExpr, nil, 2)
	if err != nil {
		return err
	}
	decls := []varDecl{{name: "acc"}} // unconstrained Real accumulator
	acc := vr("acc")
	elemSym := symVarOf("e", elemT, &decls)
	fBody, err := foldFn.call([]sym{acc, elemSym})
	if err != nil {
		return err
	}
	proj, err := projectSym(elemSym, path)
	if err != nil {
		return err
	}
	rel := ">="
	if op == "min" {
		rel = "<="
	}
	inflated := cmpSym(rel, fBody, acc)
	if err := checkGoal(goal{name: "fold-inflationary", vars: decls, claim: &inflated}); err != nil {
		return fmt.Errorf("fold is not inflationary in the accumulator: %w", err)
	}
	dominates := cmpSym(rel, fBody, proj)
	if err := checkGoal(goal{name: "fold-dominates", vars: decls, claim: &dominates}); err != nil {
		return fmt.Errorf("fold does not dominate the projected field: %w", err)
	}
	return nil
}

// ---- obligations ---------------------------------------------------

type varDecl struct {
	name string
	typ  numtype.NumType // meaningful when !str
	str  bool            // string-sorted leaf (declared String, no domain/sign asserts)
}

// goal is one obligation. Normally it proves lhs == rhs for all in-domain values
// of vars. When claim is non-nil, it instead proves that boolean sym valid (used
// for the fold-lemma inequalities). assume holds boolean hypotheses asserted
// (positively) before the negated conclusion — the sound domination bounds a
// `write`'s reduce introduces.
type goal struct {
	name   string
	vars   []varDecl
	lhs    sym
	rhs    sym
	claim  *sym
	assume []sym
}

// symVarOf builds a symbolic value for a variable of element type t named after
// path-index prefixes (a struct field i of "a" becomes "a_i", nesting to
// "a_i_j"), appending each scalar leaf's varDecl. Path-index names keep DSL
// identifiers (possibly Unicode) out of the solver, exactly as update params map
// to p0/p1.
func symVarOf(prefix string, t crdt.ElemT, decls *[]varDecl) sym {
	if t.Struct {
		fields := make(map[string]sym, len(t.Fields))
		for i, f := range t.Fields {
			fields[f.Name] = symVarOf(fmt.Sprintf("%s_%d", prefix, i), f.Type, decls)
		}
		return structSym(fields)
	}
	if t.Str {
		*decls = append(*decls, varDecl{name: prefix, str: true})
		return strVr(prefix)
	}
	*decls = append(*decls, varDecl{name: prefix, typ: t.Num})
	return vr(prefix)
}

// Prove discharges both CvRDT obligations for a type. It proves the merge laws
// first (a certified join is what makes the induced-order update check
// meaningful), then each update's inflationary property. Any obligation that
// cannot be proven yields an error, which builder turns into a deploy rejection.
// For a struct element the merge fn is a product (or joint) map over the fields;
// each obligation's struct equality is discharged as the conjunction of its leaf
// scalar equalities in one Z3 call (see smt.go).
func Prove(elem crdt.ElemT, merge parser.Expr, updates map[string]crdt.Method, funcs map[string]crdt.Function) error {
	if merge.Kind != parser.ExprZip || merge.Fn == nil {
		return fmt.Errorf("merge is not a zip expr")
	}
	p := &prover{funcs: funcs}
	f, err := p.evalFn(*merge.Fn, nil, 2)
	if err != nil {
		return fmt.Errorf("merge: %w", err)
	}
	apply2 := func(args ...sym) (sym, error) { return f.call(args) }

	// Commutativity: f(a,b) = f(b,a).
	var commDecls []varDecl
	ca := symVarOf("a", elem, &commDecls)
	cb := symVarOf("b", elem, &commDecls)
	fab, err := apply2(ca, cb)
	if err != nil {
		return err
	}
	fba, err := apply2(cb, ca)
	if err != nil {
		return err
	}
	// Associativity: f(a,f(b,c)) = f(f(a,b),c).
	var assocDecls []varDecl
	aa := symVarOf("a", elem, &assocDecls)
	ab := symVarOf("b", elem, &assocDecls)
	ac := symVarOf("c", elem, &assocDecls)
	fbc, err := apply2(ab, ac)
	if err != nil {
		return err
	}
	left, err := apply2(aa, fbc)
	if err != nil {
		return err
	}
	fab2, err := apply2(aa, ab)
	if err != nil {
		return err
	}
	right, err := apply2(fab2, ac)
	if err != nil {
		return err
	}
	// Idempotence: f(a,a) = a.
	var idemDecls []varDecl
	ia := symVarOf("a", elem, &idemDecls)
	faa, err := apply2(ia, ia)
	if err != nil {
		return err
	}

	mergeGoals := []goal{
		{name: "commutativity", vars: commDecls, lhs: fab, rhs: fba},
		{name: "associativity", vars: assocDecls, lhs: left, rhs: right},
		{name: "idempotence", vars: idemDecls, lhs: faa, rhs: ia},
	}
	for _, g := range mergeGoals {
		if err := checkGoal(g); err != nil {
			return fmt.Errorf("merge is not a join-semilattice (%s): %w", g.name, err)
		}
	}

	// Updates: prove inflationary in merge's induced order, merge(x, h(x)) = h(x).
	// A `local` update's fn reads only the slot x; a `write` update's fn reads the
	// WHOLE vector, of which x = v[self] is one slot (a vector marker makes this a
	// syntactic fact). Both prove the same equality; the write case additionally
	// carries the reduce-domination hypotheses accumulated during lowering.
	for _, name := range sortedKeys(updates) {
		m := updates[name]
		if m.Body.Fn == nil || (m.Body.Kind != parser.ExprLocal && m.Body.Kind != parser.ExprWrite) {
			return fmt.Errorf("update %s is not a local/write expr", name)
		}
		var decls []varDecl
		x := symVarOf(slotVar, elem, &decls)
		env, err := bindUpdateParams(m.Params, &decls)
		if err != nil {
			return fmt.Errorf("update %s: %w", name, err)
		}
		h, err := p.evalFn(*m.Body.Fn, env, 1)
		if err != nil {
			return fmt.Errorf("update %s: %w", name, err)
		}
		// The update fn receives the slot x (local) or the whole-vector marker whose
		// self slot is x (write). Reset the reduce accumulators so any domination
		// hypotheses collected belong to this obligation only.
		p.extraDecls, p.extraAssume = nil, nil
		arg := x
		if m.Body.Kind == parser.ExprWrite {
			arg = vectorMarker(x, elem)
		}
		hx, err := h.call([]sym{arg})
		if err != nil {
			return fmt.Errorf("update %s: %w", name, err)
		}
		fxhx, err := f.call([]sym{x, hx})
		if err != nil {
			return fmt.Errorf("update %s: %w", name, err)
		}
		decls = append(decls, p.extraDecls...)
		g := goal{name: "inflationary", vars: decls, lhs: fxhx, rhs: hx, assume: p.extraAssume}
		if err := checkGoal(g); err != nil {
			return fmt.Errorf("update %s is not inflationary under merge: %w", name, err)
		}
	}
	return nil
}

// bindUpdateParams maps each update param to an internal SMT var (p0, p1, …),
// appending its declaration. A numeric param is Real-sorted from its numtype; a
// string param (Type=="string") is String-sorted (reusing the String sort added
// for string leaves), so an obligation whose update body reads a string param
// still lowers. Shared by the local and write update obligations.
func bindUpdateParams(params []parser.ParamSpec, decls *[]varDecl) (map[string]sym, error) {
	env := make(map[string]sym, len(params))
	for i, prm := range params {
		internal := fmt.Sprintf("p%d", i)
		if prm.Type == "string" {
			env[prm.Name] = strVr(internal)
			*decls = append(*decls, varDecl{name: internal, str: true})
			continue
		}
		nt, ok := numtype.Parse(prm.Type)
		if !ok {
			return nil, fmt.Errorf("unknown param type %q", prm.Type)
		}
		env[prm.Name] = vr(internal)
		*decls = append(*decls, varDecl{name: internal, typ: nt})
	}
	return env, nil
}

func sortedKeys(m map[string]crdt.Method) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
