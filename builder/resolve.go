package builder

import (
	"fmt"

	"gospr/crdt"
	"gospr/parser"
)

// ---- name resolution -----------------------------------------------

// env resolves unresolved Name leaves into Var (bound params) or Ref
// (primitive/user-fn symbols) and performs arity checks. It holds the symbol
// tables; the per-expression parameter scope is passed in.
type env struct {
	fnArity map[string]int
}

func newEnv(fnArity map[string]int) env { return env{fnArity: fnArity} }

// resolve turns a parser term (with ExprName leaves) into a built term where
// every leaf is an ExprVar or ExprRef. `reduce` is now a pure fold that carries its
// vector explicitly (reduce fn init vec), so it may appear anywhere — the type
// checker rejects it where no vector value is in scope (e.g. a merge/local fn).
func (e env) resolve(expr parser.Expr, scope map[string]bool) (parser.Expr, error) {
	switch expr.Kind {
	case parser.ExprNumLit, parser.ExprStrLit:
		return expr, nil
	case parser.ExprName:
		if scope[expr.Name] {
			return parser.Expr{Kind: parser.ExprVar, Name: expr.Name}, nil
		}
		if a, ok := primitiveArity[expr.Name]; ok {
			return parser.Expr{Kind: parser.ExprRef, Name: expr.Name, Arity: a, Ref: parser.RefPrimitive}, nil
		}
		if a, ok := e.fnArity[expr.Name]; ok {
			return parser.Expr{Kind: parser.ExprRef, Name: expr.Name, Arity: a, Ref: parser.RefFunction}, nil
		}
		return parser.Expr{}, fmt.Errorf("unknown identifier %q", expr.Name)
	case parser.ExprApp:
		if expr.Head == nil {
			return parser.Expr{}, fmt.Errorf("application has no head")
		}
		head, err := e.resolve(*expr.Head, scope)
		if err != nil {
			return parser.Expr{}, err
		}
		ha := arityOf(head)
		if ha < len(expr.Args) {
			return parser.Expr{}, fmt.Errorf("too many arguments: %q takes %d, got %d", describe(head), ha, len(expr.Args))
		}
		args := make([]*parser.Expr, len(expr.Args))
		for i, a := range expr.Args {
			ra, err := e.resolve(*a, scope)
			if err != nil {
				return parser.Expr{}, err
			}
			if arityOf(ra) != 0 {
				return parser.Expr{}, fmt.Errorf("cannot pass a function as an argument")
			}
			args[i] = &ra
		}
		return parser.Expr{Kind: parser.ExprApp, Head: &head, Args: args}, nil
	case parser.ExprGuards:
		if len(expr.Cases) == 0 {
			return parser.Expr{}, fmt.Errorf("guarded body has no cases")
		}
		cases := make([]parser.GuardCase, len(expr.Cases))
		for i, c := range expr.Cases {
			nc := parser.GuardCase{Otherwise: c.Otherwise}
			if c.Cond != nil {
				rc, err := e.resolve(*c.Cond, scope)
				if err != nil {
					return parser.Expr{}, err
				}
				nc.Cond = &rc
			}
			if c.Result == nil {
				return parser.Expr{}, fmt.Errorf("guard case has no result")
			}
			rr, err := e.resolve(*c.Result, scope)
			if err != nil {
				return parser.Expr{}, err
			}
			nc.Result = &rr
			cases[i] = nc
		}
		return parser.Expr{Kind: parser.ExprGuards, Cases: cases}, nil
	case parser.ExprStructLit:
		fields := make([]parser.StructField, len(expr.StructFields))
		for i, sf := range expr.StructFields {
			if sf.Value == nil {
				return parser.Expr{}, fmt.Errorf("struct field %q has no value", sf.Name)
			}
			rv, err := e.resolve(*sf.Value, scope)
			if err != nil {
				return parser.Expr{}, err
			}
			if arityOf(rv) != 0 {
				return parser.Expr{}, fmt.Errorf("struct field %q is not a value", sf.Name)
			}
			fields[i] = parser.StructField{Name: sf.Name, Value: &rv}
		}
		return parser.Expr{Kind: parser.ExprStructLit, StructFields: fields}, nil
	case parser.ExprField:
		if expr.Target == nil {
			return parser.Expr{}, fmt.Errorf("field access has no target")
		}
		target, err := e.resolve(*expr.Target, scope)
		if err != nil {
			return parser.Expr{}, err
		}
		if arityOf(target) != 0 {
			return parser.Expr{}, fmt.Errorf("cannot access a field of a function")
		}
		return parser.Expr{Kind: parser.ExprField, Target: &target, Field: expr.Field}, nil
	case parser.ExprReduce:
		fn, err := e.resolveFn(expr.Fn, scope, 2)
		if err != nil {
			return parser.Expr{}, fmt.Errorf("reduce must be `reduce <binary fn> <init> <vec>`: %w", err)
		}
		if expr.Init == nil {
			return parser.Expr{}, fmt.Errorf("reduce init is missing")
		}
		// The init is a literal (numeric or struct); resolve it as a pure value.
		init, err := e.resolve(*expr.Init, scope)
		if err != nil {
			return parser.Expr{}, fmt.Errorf("reduce init: %w", err)
		}
		if arityOf(init) != 0 {
			return parser.Expr{}, fmt.Errorf("reduce init must be a value")
		}
		if expr.Vec == nil {
			return parser.Expr{}, fmt.Errorf("reduce vector argument is missing")
		}
		vec, err := e.resolve(*expr.Vec, scope)
		if err != nil {
			return parser.Expr{}, fmt.Errorf("reduce vector: %w", err)
		}
		if arityOf(vec) != 0 {
			return parser.Expr{}, fmt.Errorf("reduce vector must be a value")
		}
		return parser.Expr{Kind: parser.ExprReduce, Fn: fn, Init: &init, Vec: &vec}, nil
	default:
		return parser.Expr{}, fmt.Errorf("unexpected expression in this position")
	}
}

// resolveCombinator resolves a merge or update body (a Zip/Local node carrying
// a function-valued term) and checks the function's arity. Queries no longer go
// through here — their bodies are general expressions (see resolve). Combinator
// functions are pure, so reduce is not permitted inside them.
func (e env) resolveCombinator(expr parser.Expr, scope map[string]bool) (parser.Expr, error) {
	switch expr.Kind {
	case parser.ExprZip:
		fn, err := e.resolveFn(expr.Fn, scope, 2)
		if err != nil {
			return parser.Expr{}, fmt.Errorf("merge must be `zip <binary fn>`: %w", err)
		}
		return parser.Expr{Kind: parser.ExprZip, Fn: fn}, nil
	case parser.ExprLocal:
		fn, err := e.resolveFn(expr.Fn, scope, 1)
		if err != nil {
			return parser.Expr{}, fmt.Errorf("update must be `local <unary fn>`: %w", err)
		}
		return parser.Expr{Kind: parser.ExprLocal, Fn: fn}, nil
	case parser.ExprWrite:
		fn, err := e.resolveFn(expr.Fn, scope, 1)
		if err != nil {
			return parser.Expr{}, fmt.Errorf("update must be `write <unary fn>`: %w", err)
		}
		return parser.Expr{Kind: parser.ExprWrite, Fn: fn}, nil
	default:
		return parser.Expr{}, fmt.Errorf("expected a zip/local/write combinator")
	}
}

// resolveFn resolves a function-valued term and asserts its arity. The term is a
// combinator/reduce slot, which is pure, so reduce is disallowed within it.
func (e env) resolveFn(fn *parser.Expr, scope map[string]bool, want int) (*parser.Expr, error) {
	if fn == nil {
		return nil, fmt.Errorf("missing function")
	}
	resolved, err := e.resolve(*fn, scope)
	if err != nil {
		return nil, err
	}
	if a := arityOf(resolved); a != want {
		return nil, fmt.Errorf("expected a function of %d argument(s), got one of %d", want, a)
	}
	return &resolved, nil
}

// arityOf reports how many more arguments a resolved term needs before it is a
// value. A value (Var/Lit, or a saturated application) has arity 0.
func arityOf(e parser.Expr) int {
	switch e.Kind {
	case parser.ExprRef:
		return e.Arity
	case parser.ExprApp:
		if e.Head == nil {
			return 0
		}
		return arityOf(*e.Head) - len(e.Args)
	default: // ExprVar, ExprNumLit
		return 0
	}
}

func describe(e parser.Expr) string {
	if e.Kind == parser.ExprRef {
		return e.Name
	}
	return "expression"
}

// validateResolvedProgram checks the builder's output invariant over every
// emitted expression root before proof/runtime consumers receive the plan.
func validateResolvedProgram(plan parser.Plan, models map[string]*Model, funcs map[string]crdt.Function) error {
	for _, fd := range plan.Functions {
		fn, ok := funcs[fd.Name]
		if !ok {
			return fmt.Errorf("internal: resolved function %q is missing", fd.Name)
		}
		if err := validateResolvedExpr(fn.Body); err != nil {
			return fmt.Errorf("function %s: %w", fd.Name, err)
		}
	}
	for _, md := range plan.Merges {
		m, ok := models[md.TypeName]
		if !ok {
			return fmt.Errorf("internal: resolved merge model %q is missing", md.TypeName)
		}
		if err := validateResolvedExpr(m.Merge); err != nil {
			return fmt.Errorf("merge %s: %w", md.TypeName, err)
		}
	}
	for _, qd := range plan.Queries {
		m, ok := models[qd.TypeName]
		if !ok {
			return fmt.Errorf("internal: resolved query model %q is missing", qd.TypeName)
		}
		method, ok := m.Queries[qd.MethodName]
		if !ok {
			return fmt.Errorf("internal: resolved query %s.%s is missing", qd.TypeName, qd.MethodName)
		}
		if err := validateResolvedExpr(method.Body); err != nil {
			return fmt.Errorf("query %s.%s: %w", qd.TypeName, qd.MethodName, err)
		}
	}
	for _, ud := range plan.Updates {
		m, ok := models[ud.TypeName]
		if !ok {
			return fmt.Errorf("internal: resolved update model %q is missing", ud.TypeName)
		}
		method, ok := m.Updates[ud.MethodName]
		if !ok {
			return fmt.Errorf("internal: resolved update %s.%s is missing", ud.TypeName, ud.MethodName)
		}
		if err := validateResolvedExpr(method.Body); err != nil {
			return fmt.Errorf("update %s.%s: %w", ud.TypeName, ud.MethodName, err)
		}
	}
	return nil
}

// validateResolvedExpr is exhaustive by construction: the default rejects a
// newly added expression kind until this traversal explicitly handles it.
func validateResolvedExpr(e parser.Expr) error {
	child := func(label string, expr *parser.Expr) error {
		if expr == nil {
			return fmt.Errorf("internal: resolved %s is missing", label)
		}
		return validateResolvedExpr(*expr)
	}

	switch e.Kind {
	case parser.ExprNumLit:
		if e.Num == nil {
			return fmt.Errorf("internal: resolved numeric literal has no value")
		}
		return nil
	case parser.ExprStrLit, parser.ExprVar, parser.ExprRef:
		return nil
	case parser.ExprName:
		return fmt.Errorf("internal: unresolved name %q survived Build; resolve must rewrite every ExprName to Var/Ref", e.Name)
	case parser.ExprApp:
		if err := child("application head", e.Head); err != nil {
			return err
		}
		for i, arg := range e.Args {
			if err := child(fmt.Sprintf("application argument %d", i), arg); err != nil {
				return err
			}
		}
		return nil
	case parser.ExprGuards:
		for i, guard := range e.Cases {
			if !guard.Otherwise {
				if err := child(fmt.Sprintf("guard %d condition", i), guard.Cond); err != nil {
					return err
				}
			} else if guard.Cond != nil {
				if err := validateResolvedExpr(*guard.Cond); err != nil {
					return err
				}
			}
			if err := child(fmt.Sprintf("guard %d result", i), guard.Result); err != nil {
				return err
			}
		}
		return nil
	case parser.ExprStructLit:
		for _, field := range e.StructFields {
			if err := child(fmt.Sprintf("struct field %q", field.Name), field.Value); err != nil {
				return err
			}
		}
		return nil
	case parser.ExprField:
		return child("field target", e.Target)
	case parser.ExprReduce:
		if err := child("reduce function", e.Fn); err != nil {
			return err
		}
		if err := child("reduce init", e.Init); err != nil {
			return err
		}
		return child("reduce vector", e.Vec)
	case parser.ExprZip, parser.ExprLocal, parser.ExprWrite:
		return child("combinator function", e.Fn)
	default:
		return fmt.Errorf("internal: resolved expression has unsupported kind %d", e.Kind)
	}
}
