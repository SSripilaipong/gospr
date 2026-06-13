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

// vtype is the builder's internal value type. vUnknown is a sentinel used only
// during return-type inference for an as-yet-undetermined (recursive) function;
// it unifies with any concrete type and must not survive to a built artifact.
type vtype int

const (
	vUnknown vtype = iota
	vReal
	vBool
	vString
)

func (t vtype) String() string {
	switch t {
	case vReal:
		return "real"
	case vBool:
		return "bool"
	case vString:
		return "string"
	default:
		return "unknown"
	}
}

func toValType(t vtype) parser.ValType {
	switch t {
	case vBool:
		return parser.TypeBool
	case vString:
		return parser.TypeString
	default:
		return parser.TypeReal
	}
}

// sig is a (possibly partially applied) function signature: the param types it
// still expects, plus the type it yields once saturated. A value has no params.
type sig struct {
	params []vtype
	result vtype
}

// primitiveSig is the table of built-in operators. Arithmetic is real,real->real;
// the comparisons are real,real->bool. Arity is len(params).
var primitiveSig = map[string]sig{
	"+":   {[]vtype{vReal, vReal}, vReal},
	"*":   {[]vtype{vReal, vReal}, vReal},
	"-":   {[]vtype{vReal, vReal}, vReal},
	"max": {[]vtype{vReal, vReal}, vReal},
	"min": {[]vtype{vReal, vReal}, vReal},
	">":   {[]vtype{vReal, vReal}, vBool},
	"<":   {[]vtype{vReal, vReal}, vBool},
	">=":  {[]vtype{vReal, vReal}, vBool},
	"<=":  {[]vtype{vReal, vReal}, vBool},
	"==":  {[]vtype{vReal, vReal}, vBool},
	"/=":  {[]vtype{vReal, vReal}, vBool},
}

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
		if _, clash := primitiveSig[fd.Name]; clash {
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

	// Resolve each function body against its parameter scope. `reduce` is
	// rejected here (allowReduce=false) so global functions stay pure — only
	// query bodies may fold the vector.
	funcs := make(map[string]crdt.Function, len(plan.Functions))
	chk := newChecker(env)
	for _, fd := range plan.Functions {
		scope := paramSet(fd.Params)
		body, err := env.resolve(fd.Body, scope, false)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("function %s: %w", fd.Name, err)
		}
		if a := arityOf(body); a != 0 {
			return BuiltPlan{}, fmt.Errorf("function %s: body must return a value, but is missing %d argument(s)", fd.Name, a)
		}
		chk.register(fd.Name, fd.Params, body)
		funcs[fd.Name] = crdt.Function{Name: fd.Name, Params: fd.Params, Body: body}
	}

	// Type-check every function body and infer its return type (Option A:
	// unanchored recursion whose return type can't be determined is rejected).
	for _, fd := range plan.Functions {
		if _, err := chk.inferReturn(fd.Name); err != nil {
			return BuiltPlan{}, fmt.Errorf("function %s: %w", fd.Name, err)
		}
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
		if err := chk.checkCombinatorFn(merge, nil); err != nil {
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
		// A query body is a general expression that may fold the vector via
		// `reduce` (allowReduce=true). Its result type (real/bool/string) is
		// recorded for serialization/swagger.
		body, err := env.resolve(qd.Body, nil, true)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("query %s.%s: %w", qd.TypeName, qd.MethodName, err)
		}
		result, err := chk.checkValue(body)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("query %s.%s: %w", qd.TypeName, qd.MethodName, err)
		}
		m.Queries[qd.MethodName] = crdt.Method{Params: qd.Params, Body: body, Result: toValType(result)}
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
		if err := chk.checkCombinatorFn(body, realScope(ud.Params)); err != nil {
			return BuiltPlan{}, fmt.Errorf("update %s.%s: %w", ud.TypeName, ud.MethodName, err)
		}
		m.Updates[ud.MethodName] = crdt.Method{Params: ud.Params, Body: body, Result: parser.TypeReal}
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
// every leaf is an ExprVar or ExprRef. allowReduce gates the `reduce` form: it
// is permitted only in query bodies, keeping global functions pure.
func (e env) resolve(expr parser.Expr, scope map[string]bool, allowReduce bool) (parser.Expr, error) {
	switch expr.Kind {
	case parser.ExprNumLit, parser.ExprStrLit:
		return expr, nil
	case parser.ExprName:
		if scope[expr.Name] {
			return parser.Expr{Kind: parser.ExprVar, Name: expr.Name}, nil
		}
		if s, ok := primitiveSig[expr.Name]; ok {
			return parser.Expr{Kind: parser.ExprRef, Name: expr.Name, Arity: len(s.params), Ref: parser.RefPrimitive}, nil
		}
		if a, ok := e.fnArity[expr.Name]; ok {
			return parser.Expr{Kind: parser.ExprRef, Name: expr.Name, Arity: a, Ref: parser.RefFunction}, nil
		}
		return parser.Expr{}, fmt.Errorf("unknown identifier %q", expr.Name)
	case parser.ExprApp:
		if expr.Head == nil {
			return parser.Expr{}, fmt.Errorf("application has no head")
		}
		head, err := e.resolve(*expr.Head, scope, allowReduce)
		if err != nil {
			return parser.Expr{}, err
		}
		ha := arityOf(head)
		if ha < len(expr.Args) {
			return parser.Expr{}, fmt.Errorf("too many arguments: %q takes %d, got %d", describe(head), ha, len(expr.Args))
		}
		args := make([]*parser.Expr, len(expr.Args))
		for i, a := range expr.Args {
			ra, err := e.resolve(*a, scope, allowReduce)
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
				rc, err := e.resolve(*c.Cond, scope, allowReduce)
				if err != nil {
					return parser.Expr{}, err
				}
				nc.Cond = &rc
			}
			if c.Result == nil {
				return parser.Expr{}, fmt.Errorf("guard case has no result")
			}
			rr, err := e.resolve(*c.Result, scope, allowReduce)
			if err != nil {
				return parser.Expr{}, err
			}
			nc.Result = &rr
			cases[i] = nc
		}
		return parser.Expr{Kind: parser.ExprGuards, Cases: cases}, nil
	case parser.ExprReduce:
		if !allowReduce {
			return parser.Expr{}, fmt.Errorf("`reduce` may only appear in a query body")
		}
		fn, err := e.resolveFn(expr.Fn, scope, 2)
		if err != nil {
			return parser.Expr{}, fmt.Errorf("reduce must be `reduce <binary fn> <init>`: %w", err)
		}
		if expr.Init == nil || expr.Init.Kind != parser.ExprNumLit {
			return parser.Expr{}, fmt.Errorf("reduce init must be a number")
		}
		init := *expr.Init
		return parser.Expr{Kind: parser.ExprReduce, Fn: fn, Init: &init}, nil
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
	default:
		return parser.Expr{}, fmt.Errorf("expected a zip/local combinator")
	}
}

// resolveFn resolves a function-valued term and asserts its arity. The term is a
// combinator/reduce slot, which is pure, so reduce is disallowed within it.
func (e env) resolveFn(fn *parser.Expr, scope map[string]bool, want int) (*parser.Expr, error) {
	if fn == nil {
		return nil, fmt.Errorf("missing function")
	}
	resolved, err := e.resolve(*fn, scope, false)
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

// ---- type checking -------------------------------------------------

// checker type-checks resolved terms and infers function return types. Param
// types are real-only and known up front; return types are inferred lazily via
// memoized DFS so functions may reference one another and recurse. A recursive
// call caught mid-inference yields vUnknown, which unifies with any concrete
// type — so anchored recursion resolves, while wholly unanchored recursion
// (Option A) leaves vUnknown and is rejected.
type checker struct {
	fnParams   map[string][]vtype
	fnScope    map[string]map[string]vtype
	fnBody     map[string]parser.Expr
	result     map[string]vtype
	inProgress map[string]bool
}

func newChecker(_ env) *checker {
	return &checker{
		fnParams:   map[string][]vtype{},
		fnScope:    map[string]map[string]vtype{},
		fnBody:     map[string]parser.Expr{},
		result:     map[string]vtype{},
		inProgress: map[string]bool{},
	}
}

// register records a resolved function body and its (real-only) param scope.
func (c *checker) register(name string, params []parser.ParamSpec, body parser.Expr) {
	pts := make([]vtype, len(params))
	scope := make(map[string]vtype, len(params))
	for i, p := range params {
		pts[i] = vReal
		scope[p.Name] = vReal
	}
	c.fnParams[name] = pts
	c.fnScope[name] = scope
	c.fnBody[name] = body
}

// inferReturn returns a function's result type, type-checking its body on first
// visit. A function caught mid-inference (recursion) reports vUnknown.
func (c *checker) inferReturn(name string) (vtype, error) {
	if r, ok := c.result[name]; ok {
		return r, nil
	}
	if c.inProgress[name] {
		return vUnknown, nil
	}
	c.inProgress[name] = true
	s, err := c.typeOf(c.fnBody[name], c.fnScope[name])
	c.inProgress[name] = false
	if err != nil {
		return vUnknown, err
	}
	if len(s.params) != 0 {
		return vUnknown, fmt.Errorf("body must return a value, but is missing %d argument(s)", len(s.params))
	}
	if s.result == vUnknown {
		return vUnknown, fmt.Errorf("cannot infer return type (unanchored recursion)")
	}
	c.result[name] = s.result
	return s.result, nil
}

// checkValue type-checks a term that must yield a value (not a function) and
// returns its type. Used for query bodies.
func (c *checker) checkValue(e parser.Expr) (vtype, error) {
	s, err := c.typeOf(e, nil)
	if err != nil {
		return vUnknown, err
	}
	if len(s.params) != 0 {
		return vUnknown, fmt.Errorf("expected a value, got a function missing %d argument(s)", len(s.params))
	}
	if s.result == vUnknown {
		return vUnknown, fmt.Errorf("cannot determine result type")
	}
	return s.result, nil
}

// checkCombinatorFn type-checks the function carried by a resolved zip/local
// node: it must operate on reals and return a real. scope carries the method's
// params (a local update fn may reference them, e.g. `local (+ k)`).
func (c *checker) checkCombinatorFn(comb parser.Expr, scope map[string]vtype) error {
	if comb.Fn == nil {
		return fmt.Errorf("combinator has no function")
	}
	s, err := c.typeOf(*comb.Fn, scope)
	if err != nil {
		return err
	}
	for _, p := range s.params {
		if p != vReal {
			return fmt.Errorf("combinator function must operate on reals")
		}
	}
	if s.result != vReal && s.result != vUnknown {
		return fmt.Errorf("combinator function must return real, returns %s", s.result)
	}
	return nil
}

// typeOf computes the (possibly partial) signature of a resolved term.
func (c *checker) typeOf(e parser.Expr, scope map[string]vtype) (sig, error) {
	switch e.Kind {
	case parser.ExprNumLit:
		return sig{nil, vReal}, nil
	case parser.ExprStrLit:
		return sig{nil, vString}, nil
	case parser.ExprVar:
		t, ok := scope[e.Name]
		if !ok {
			return sig{}, fmt.Errorf("unbound variable %q", e.Name)
		}
		return sig{nil, t}, nil
	case parser.ExprRef:
		if e.Ref == parser.RefPrimitive {
			ps, ok := primitiveSig[e.Name]
			if !ok {
				return sig{}, fmt.Errorf("unknown primitive %q", e.Name)
			}
			return sig{append([]vtype(nil), ps.params...), ps.result}, nil
		}
		params, ok := c.fnParams[e.Name]
		if !ok {
			return sig{}, fmt.Errorf("unknown function %q", e.Name)
		}
		res, err := c.inferReturn(e.Name)
		if err != nil {
			return sig{}, err
		}
		return sig{append([]vtype(nil), params...), res}, nil
	case parser.ExprApp:
		if e.Head == nil {
			return sig{}, fmt.Errorf("application has no head")
		}
		hs, err := c.typeOf(*e.Head, scope)
		if err != nil {
			return sig{}, err
		}
		if len(e.Args) > len(hs.params) {
			return sig{}, fmt.Errorf("too many arguments: expected %d, got %d", len(hs.params), len(e.Args))
		}
		for i, a := range e.Args {
			as, err := c.typeOf(*a, scope)
			if err != nil {
				return sig{}, err
			}
			if len(as.params) != 0 {
				return sig{}, fmt.Errorf("cannot pass a function as an argument")
			}
			if want := hs.params[i]; as.result != vUnknown && as.result != want {
				return sig{}, fmt.Errorf("argument %d: expected %s, got %s", i+1, want, as.result)
			}
		}
		return sig{hs.params[len(e.Args):], hs.result}, nil
	case parser.ExprGuards:
		return c.typeOfGuards(e, scope)
	case parser.ExprReduce:
		if e.Fn == nil {
			return sig{}, fmt.Errorf("reduce has no function")
		}
		fs, err := c.typeOf(*e.Fn, scope)
		if err != nil {
			return sig{}, err
		}
		if len(fs.params) != 2 || fs.params[0] != vReal || fs.params[1] != vReal || (fs.result != vReal && fs.result != vUnknown) {
			return sig{}, fmt.Errorf("reduce needs a real,real->real function")
		}
		return sig{nil, vReal}, nil
	default:
		return sig{}, fmt.Errorf("cannot type-check expression of kind %d", e.Kind)
	}
}

func (c *checker) typeOfGuards(e parser.Expr, scope map[string]vtype) (sig, error) {
	n := len(e.Cases)
	if n == 0 {
		return sig{}, fmt.Errorf("guarded body has no cases")
	}
	result := vUnknown
	for i, gc := range e.Cases {
		isLast := i == n-1
		if gc.Otherwise && !isLast {
			return sig{}, fmt.Errorf("`otherwise` must be the last guard case")
		}
		if !gc.Otherwise && isLast {
			return sig{}, fmt.Errorf("a guarded function must end with an `otherwise` case")
		}
		if !gc.Otherwise {
			if gc.Cond == nil {
				return sig{}, fmt.Errorf("guard case has no condition")
			}
			cs, err := c.typeOf(*gc.Cond, scope)
			if err != nil {
				return sig{}, err
			}
			if len(cs.params) != 0 {
				return sig{}, fmt.Errorf("guard condition must be a value, not a function")
			}
			if cs.result != vBool && cs.result != vUnknown {
				return sig{}, fmt.Errorf("guard condition must be bool, got %s", cs.result)
			}
		}
		if gc.Result == nil {
			return sig{}, fmt.Errorf("guard case has no result")
		}
		rs, err := c.typeOf(*gc.Result, scope)
		if err != nil {
			return sig{}, err
		}
		if len(rs.params) != 0 {
			return sig{}, fmt.Errorf("guard result must be a value, not a function")
		}
		unified, err := unify(result, rs.result)
		if err != nil {
			return sig{}, fmt.Errorf("guard results must all have the same type: %w", err)
		}
		result = unified
	}
	return sig{nil, result}, nil
}

func unify(a, b vtype) (vtype, error) {
	switch {
	case a == vUnknown:
		return b, nil
	case b == vUnknown:
		return a, nil
	case a != b:
		return vUnknown, fmt.Errorf("%s vs %s", a, b)
	default:
		return a, nil
	}
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

// realScope builds a type-checking scope binding each (real-only) param to vReal.
func realScope(ps []parser.ParamSpec) map[string]vtype {
	s := make(map[string]vtype, len(ps))
	for _, p := range ps {
		s[p.Name] = vReal
	}
	return s
}
