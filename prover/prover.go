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
	symConst  symKind = iota // num
	symVar                   // name
	symBin                   // op in + - * max min, operands a,b
	symCmp                   // op in > < >= <= == /=, operands a,b (a boolean term)
	symIte                   // if cond then a else b
	symStruct                // struct value: fields (each a sym)
)

type sym struct {
	kind   symKind
	num    *big.Rat
	name   string
	op     string
	cond   *sym
	a, b   *sym
	fields map[string]sym // symStruct
}

func cst(f *big.Rat) sym { return sym{kind: symConst, num: f} }
func vr(name string) sym { return sym{kind: symVar, name: name} }
func structSym(fields map[string]sym) sym {
	return sym{kind: symStruct, fields: fields}
}
func bin(op string, a, b sym) sym {
	return sym{kind: symBin, op: op, a: &a, b: &b}
}
func cmpSym(op string, a, b sym) sym {
	return sym{kind: symCmp, op: op, a: &a, b: &b}
}
func ite(cond, then, els sym) sym {
	return sym{kind: symIte, cond: &cond, a: &then, b: &els}
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
		return sym{}, nil, fmt.Errorf("string values cannot appear in a merge/update function")
	case parser.ExprReduce, parser.ExprZip, parser.ExprLocal:
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

// ---- obligations ---------------------------------------------------

type varDecl struct {
	name string
	typ  numtype.NumType
}

// goal is one obligation: prove lhs == rhs for all in-domain values of vars.
type goal struct {
	name string
	vars []varDecl
	lhs  sym
	rhs  sym
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
	*decls = append(*decls, varDecl{prefix, t.Num})
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
		{"commutativity", commDecls, fab, fba},
		{"associativity", assocDecls, left, right},
		{"idempotence", idemDecls, faa, ia},
	}
	for _, g := range mergeGoals {
		if err := checkGoal(g); err != nil {
			return fmt.Errorf("merge is not a join-semilattice (%s): %w", g.name, err)
		}
	}

	// Updates: prove inflationary in merge's induced order. The slot is the element
	// type (scalar or struct); each update param is a scalar declared from its own
	// numeric type and mapped to an internal SMT name (p0, p1, …).
	for _, name := range sortedKeys(updates) {
		m := updates[name]
		if m.Body.Kind != parser.ExprLocal || m.Body.Fn == nil {
			return fmt.Errorf("update %s is not a local expr", name)
		}
		var decls []varDecl
		x := symVarOf(slotVar, elem, &decls)
		env := make(map[string]sym, len(m.Params))
		for i, prm := range m.Params {
			nt, ok := numtype.Parse(prm.Type)
			if !ok {
				return fmt.Errorf("update %s: unknown param type %q", name, prm.Type)
			}
			internal := fmt.Sprintf("p%d", i)
			env[prm.Name] = vr(internal)
			decls = append(decls, varDecl{internal, nt})
		}
		h, err := p.evalFn(*m.Body.Fn, env, 1)
		if err != nil {
			return fmt.Errorf("update %s: %w", name, err)
		}
		hx, err := h.call([]sym{x})
		if err != nil {
			return fmt.Errorf("update %s: %w", name, err)
		}
		fxhx, err := f.call([]sym{x, hx})
		if err != nil {
			return fmt.Errorf("update %s: %w", name, err)
		}
		g := goal{"inflationary", decls, fxhx, hx}
		if err := checkGoal(g); err != nil {
			return fmt.Errorf("update %s is not inflationary under merge: %w", name, err)
		}
	}
	return nil
}

func sortedKeys(m map[string]crdt.Method) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
