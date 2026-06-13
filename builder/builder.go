package builder

import (
	"fmt"

	"gospr/crdt"
	"gospr/parser"
)

// CollectionSpec produces a runtime CRDT instance for a node.
type CollectionSpec interface {
	New(nodeID string) crdt.CRDT
}

// Model is the validated semantic representation of one vector type — the
// "proper AST". It is pure data (no closures), so later optimization/proof
// passes can walk it. Model implements CollectionSpec.
type Model struct {
	Name    string
	Elem    parser.ElemType
	Merge   parser.Expr            // a Zip expr (Fn resolved)
	Queries map[string]crdt.Method // Reduce methods (bodies resolved)
	Updates map[string]crdt.Method // Local methods (bodies resolved)
	Funcs   map[string]crdt.Function
}

func (m *Model) New(nodeID string) crdt.CRDT {
	return crdt.NewVector(nodeID, m.Merge, m.Queries, m.Updates, m.Funcs)
}

type BuiltCollection struct {
	Name string
	Spec CollectionSpec
}

// BuiltPlan carries per-type models (present even when no collection is
// declared, so a model can be tested directly), the global function
// environment, and instantiated collections.
type BuiltPlan struct {
	Models      map[string]*Model
	Functions   map[string]crdt.Function
	Collections []BuiltCollection
}

// primitiveArity is the table of built-in operators and their arities. Every
// primitive is real^arity -> real.
var primitiveArity = map[string]int{"+": 2, "*": 2, "-": 2, "max": 2, "min": 2}

func Build(plan parser.Plan) (BuiltPlan, error) {
	models := make(map[string]*Model, len(plan.Types))
	for _, td := range plan.Types {
		if _, dup := models[td.Name]; dup {
			return BuiltPlan{}, fmt.Errorf("type %s declared twice", td.Name)
		}
		if td.Elem.Kind != parser.KindReal {
			return BuiltPlan{}, fmt.Errorf("type %s: only `vector real` is supported", td.Name)
		}
		models[td.Name] = &Model{
			Name:    td.Name,
			Elem:    td.Elem,
			Queries: map[string]crdt.Method{},
			Updates: map[string]crdt.Method{},
		}
	}

	// Collect function arities up front so bodies may reference functions
	// defined later or themselves (recursion is allowed).
	fnArity := make(map[string]int, len(plan.Functions))
	for _, fd := range plan.Functions {
		if _, dup := fnArity[fd.Name]; dup {
			return BuiltPlan{}, fmt.Errorf("function %s declared twice", fd.Name)
		}
		if _, clash := primitiveArity[fd.Name]; clash {
			return BuiltPlan{}, fmt.Errorf("function %s shadows a built-in primitive", fd.Name)
		}
		if len(fd.Params) == 0 {
			return BuiltPlan{}, fmt.Errorf("function %s: must take at least one parameter", fd.Name)
		}
		if err := validateParams(fd.Params); err != nil {
			return BuiltPlan{}, fmt.Errorf("function %s: %w", fd.Name, err)
		}
		fnArity[fd.Name] = len(fd.Params)
	}

	env := newEnv(fnArity)

	// Resolve each function body against its parameter scope.
	funcs := make(map[string]crdt.Function, len(plan.Functions))
	for _, fd := range plan.Functions {
		scope := paramSet(fd.Params)
		body, err := env.resolve(fd.Body, scope)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("function %s: %w", fd.Name, err)
		}
		if a := arityOf(body); a != 0 {
			return BuiltPlan{}, fmt.Errorf("function %s: body must return a value, but is missing %d argument(s)", fd.Name, a)
		}
		funcs[fd.Name] = crdt.Function{Name: fd.Name, Params: fd.Params, Body: body}
	}

	mergeSeen := make(map[string]bool)
	for _, md := range plan.Merges {
		m, ok := models[md.TypeName]
		if !ok {
			return BuiltPlan{}, fmt.Errorf("merge for unknown type %s", md.TypeName)
		}
		if mergeSeen[md.TypeName] {
			return BuiltPlan{}, fmt.Errorf("type %s has merge defined twice", md.TypeName)
		}
		merge, err := env.resolveCombinator(md.Body, nil)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("merge %s: %w", md.TypeName, err)
		}
		m.Merge = merge
		mergeSeen[md.TypeName] = true
	}

	for _, qd := range plan.Queries {
		m, ok := models[qd.TypeName]
		if !ok {
			return BuiltPlan{}, fmt.Errorf("query for unknown type %s", qd.TypeName)
		}
		if _, dup := m.Queries[qd.MethodName]; dup {
			return BuiltPlan{}, fmt.Errorf("query %s.%s defined twice", qd.TypeName, qd.MethodName)
		}
		if len(qd.Params) != 0 {
			return BuiltPlan{}, fmt.Errorf("query %s.%s: query params are not yet supported", qd.TypeName, qd.MethodName)
		}
		body, err := env.resolveCombinator(qd.Body, nil)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("query %s.%s: %w", qd.TypeName, qd.MethodName, err)
		}
		m.Queries[qd.MethodName] = crdt.Method{Params: qd.Params, Body: body}
	}

	for _, ud := range plan.Updates {
		m, ok := models[ud.TypeName]
		if !ok {
			return BuiltPlan{}, fmt.Errorf("update for unknown type %s", ud.TypeName)
		}
		if _, dup := m.Updates[ud.MethodName]; dup {
			return BuiltPlan{}, fmt.Errorf("update %s.%s defined twice", ud.TypeName, ud.MethodName)
		}
		if err := validateParams(ud.Params); err != nil {
			return BuiltPlan{}, fmt.Errorf("update %s.%s: %w", ud.TypeName, ud.MethodName, err)
		}
		body, err := env.resolveCombinator(ud.Body, paramSet(ud.Params))
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("update %s.%s: %w", ud.TypeName, ud.MethodName, err)
		}
		m.Updates[ud.MethodName] = crdt.Method{Params: ud.Params, Body: body}
	}

	// Every type must define a merge — a vector without a join isn't a CRDT.
	for name := range models {
		if !mergeSeen[name] {
			return BuiltPlan{}, fmt.Errorf("type %s has no merge defined", name)
		}
	}

	// Attach the shared function environment to every model so the runtime
	// can resolve Ref -> user function.
	for _, m := range models {
		m.Funcs = funcs
	}

	collections := make([]BuiltCollection, 0, len(plan.Collections))
	seenCollection := make(map[string]bool, len(plan.Collections))
	for _, cs := range plan.Collections {
		if seenCollection[cs.Name] {
			return BuiltPlan{}, fmt.Errorf("collection %s declared twice", cs.Name)
		}
		m, ok := models[cs.Type]
		if !ok {
			return BuiltPlan{}, fmt.Errorf("collection %s references unknown type %s", cs.Name, cs.Type)
		}
		seenCollection[cs.Name] = true
		collections = append(collections, BuiltCollection{Name: cs.Name, Spec: m})
	}

	return BuiltPlan{Models: models, Functions: funcs, Collections: collections}, nil
}

// ---- name resolution -----------------------------------------------

// env resolves unresolved Name leaves into Var (bound params) or Ref
// (primitive/user-fn symbols) and performs arity checks. It holds the symbol
// tables; the per-expression parameter scope is passed in.
type env struct {
	fnArity map[string]int
}

func newEnv(fnArity map[string]int) env { return env{fnArity: fnArity} }

// resolve turns a parser term (with ExprName leaves) into a built term where
// every leaf is an ExprVar or ExprRef.
func (e env) resolve(expr parser.Expr, scope map[string]bool) (parser.Expr, error) {
	switch expr.Kind {
	case parser.ExprNumLit:
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
	default:
		return parser.Expr{}, fmt.Errorf("unexpected expression in this position")
	}
}

// resolveCombinator resolves a merge/query/update body (a Zip/Reduce/Local
// node carrying a function-valued term) and checks the function's arity.
func (e env) resolveCombinator(expr parser.Expr, scope map[string]bool) (parser.Expr, error) {
	switch expr.Kind {
	case parser.ExprZip:
		fn, err := e.resolveFn(expr.Fn, scope, 2)
		if err != nil {
			return parser.Expr{}, fmt.Errorf("merge must be `zip <binary fn>`: %w", err)
		}
		return parser.Expr{Kind: parser.ExprZip, Fn: fn}, nil
	case parser.ExprReduce:
		fn, err := e.resolveFn(expr.Fn, scope, 2)
		if err != nil {
			return parser.Expr{}, fmt.Errorf("query must be `reduce <binary fn> <init>`: %w", err)
		}
		if expr.Init == nil || expr.Init.Kind != parser.ExprNumLit {
			return parser.Expr{}, fmt.Errorf("reduce init must be a number")
		}
		init := *expr.Init
		return parser.Expr{Kind: parser.ExprReduce, Fn: fn, Init: &init}, nil
	case parser.ExprLocal:
		fn, err := e.resolveFn(expr.Fn, scope, 1)
		if err != nil {
			return parser.Expr{}, fmt.Errorf("update must be `local <unary fn>`: %w", err)
		}
		return parser.Expr{Kind: parser.ExprLocal, Fn: fn}, nil
	default:
		return parser.Expr{}, fmt.Errorf("expected a zip/reduce/local combinator")
	}
}

// resolveFn resolves a function-valued term and asserts its arity.
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

// ---- validators ----------------------------------------------------

func validateParams(ps []parser.ParamSpec) error {
	seen := make(map[string]bool, len(ps))
	for _, p := range ps {
		if p.Type != "real" {
			return fmt.Errorf("param %s: unknown type %q (only real)", p.Name, p.Type)
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate param %s", p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

func paramSet(ps []parser.ParamSpec) map[string]bool {
	s := make(map[string]bool, len(ps))
	for _, p := range ps {
		s[p.Name] = true
	}
	return s
}
