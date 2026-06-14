package builder

import (
	"fmt"

	"gospr/crdt"
	"gospr/numtype"
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
	ElemNum numtype.NumType        // the element's numeric type (parsed from Elem.Scalar)
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

// vkind tags the builder's internal value type. vkUnknown is a sentinel used
// only during return-type inference for an as-yet-undetermined (recursive)
// function; it unifies with any concrete type and must not survive to a built
// artifact.
type vkind int

const (
	vkUnknown vkind = iota
	vkNum
	vkBool
	vkString
)

// vtype is the builder's internal value type. For vkNum the carried NumType says
// which numeric subtype; for the others NumType is unused.
type vtype struct {
	kind vkind
	num  numtype.NumType
}

var (
	vUnknown = vtype{kind: vkUnknown}
	vBool    = vtype{kind: vkBool}
	vString  = vtype{kind: vkString}
	// numTop is the widest numeric type (`real`); a primitive accepts any
	// numeric operand, expressed as "operand must be <: numTop".
	numTop = vNum(numtype.NumType{Domain: numtype.DReal, Sign: numtype.SAny})
)

func vNum(nt numtype.NumType) vtype { return vtype{kind: vkNum, num: nt} }

func (t vtype) String() string {
	switch t.kind {
	case vkNum:
		return t.num.String()
	case vkBool:
		return "bool"
	case vkString:
		return "string"
	default:
		return "unknown"
	}
}

func toValType(t vtype) parser.ValType {
	switch t.kind {
	case vkBool:
		return parser.TypeBool
	case vkString:
		return parser.TypeString
	default:
		return parser.TypeReal
	}
}

// toResultNum returns the numeric type of a value result (meaningful only when
// toValType(t) == TypeReal); for non-numeric results it returns the top type.
func toResultNum(t vtype) numtype.NumType {
	if t.kind == vkNum {
		return t.num
	}
	return numtype.NumType{}
}

// subVtype reports whether a is assignable where b is wanted. vkUnknown (a
// recursive call mid-inference) defers — it is treated as assignable to/from
// anything, so anchored recursion keeps type-checking.
func subVtype(a, b vtype) bool {
	if a.kind == vkUnknown || b.kind == vkUnknown {
		return true
	}
	if a.kind == vkNum && b.kind == vkNum {
		return numtype.Sub(a.num, b.num)
	}
	return a.kind == b.kind
}

// sig is a (possibly partially applied) function signature: the param types it
// still expects plus the type it yields once saturated. For a primitive the
// result depends on its operands, so op names the operator and bound records the
// operand types already supplied; result is then computed when saturated. For a
// user function / value, op == "" and result is fixed.
type sig struct {
	params []vtype
	result vtype
	op     string  // primitive operator driving the result rule; "" otherwise
	bound  []vtype // operand types already supplied (for partial primitive apps)
}

// primitiveArity is the table of built-in operators and their arities. All
// current primitives are binary. Result types come from the per-operator rules
// (resultOf), not from this table.
var primitiveArity = map[string]int{
	"+": 2, "*": 2, "-": 2, "max": 2, "min": 2,
	">": 2, "<": 2, ">=": 2, "<=": 2, "==": 2, "/=": 2,
}

// cmpOps are the comparison primitives: any-numeric operands, bool result.
var cmpOps = map[string]bool{">": true, "<": true, ">=": true, "<=": true, "==": true, "/=": true}

// ---- per-operator result rules -------------------------------------

// negateSign flips a sign (used by subtraction: a - b == a + (-b)).
func negateSign(s numtype.Sign) numtype.Sign {
	switch s {
	case numtype.SNonNeg:
		return numtype.SNonPos
	case numtype.SNonPos:
		return numtype.SNonNeg
	default: // Zero, Any
		return s
	}
}

func addSign(a, b numtype.Sign) numtype.Sign {
	switch {
	case a == numtype.SZero:
		return b
	case b == numtype.SZero:
		return a
	case a == numtype.SNonNeg && b == numtype.SNonNeg:
		return numtype.SNonNeg
	case a == numtype.SNonPos && b == numtype.SNonPos:
		return numtype.SNonPos
	default:
		return numtype.SAny
	}
}

func mulSign(a, b numtype.Sign) numtype.Sign {
	switch {
	case a == numtype.SZero || b == numtype.SZero:
		return numtype.SZero
	case a == b: // NonNeg*NonNeg or NonPos*NonPos
		return numtype.SNonNeg
	case a == numtype.SAny || b == numtype.SAny:
		return numtype.SAny
	default: // opposite definite signs
		return numtype.SNonPos
	}
}

func maxSign(a, b numtype.Sign) numtype.Sign {
	switch {
	case a == numtype.SZero && b == numtype.SZero:
		return numtype.SZero
	case a == numtype.SNonNeg || b == numtype.SNonNeg || a == numtype.SZero || b == numtype.SZero:
		// max with a >=0 operand (or 0) is itself >= 0.
		return numtype.SNonNeg
	case a == numtype.SNonPos && b == numtype.SNonPos:
		return numtype.SNonPos
	default:
		return numtype.SAny
	}
}

func minSign(a, b numtype.Sign) numtype.Sign {
	switch {
	case a == numtype.SZero && b == numtype.SZero:
		return numtype.SZero
	case a == numtype.SNonPos || b == numtype.SNonPos || a == numtype.SZero || b == numtype.SZero:
		// min with a <=0 operand (or 0) is itself <= 0.
		return numtype.SNonPos
	case a == numtype.SNonNeg && b == numtype.SNonNeg:
		return numtype.SNonNeg
	default:
		return numtype.SAny
	}
}

// numBin computes the result numeric type of a binary arithmetic operator over
// two numeric operands. Domain widens to Real if either operand is Real; the
// sign follows the operator's sound rule.
func numBin(op string, a, b numtype.NumType) numtype.NumType {
	d := numtype.DInt
	if a.Domain == numtype.DReal || b.Domain == numtype.DReal {
		d = numtype.DReal
	}
	var s numtype.Sign
	switch op {
	case "+":
		s = addSign(a.Sign, b.Sign)
	case "-":
		s = addSign(a.Sign, negateSign(b.Sign))
	case "*":
		s = mulSign(a.Sign, b.Sign)
	case "max":
		s = maxSign(a.Sign, b.Sign)
	case "min":
		s = minSign(a.Sign, b.Sign)
	}
	return numtype.NumType{Domain: d, Sign: s}
}

func Build(plan parser.Plan) (BuiltPlan, error) {
	models := make(map[string]*Model, len(plan.Types))
	for _, td := range plan.Types {
		if _, dup := models[td.Name]; dup {
			return BuiltPlan{}, fmt.Errorf("type %s declared twice", td.Name)
		}
		if td.Elem.Kind != parser.KindReal {
			return BuiltPlan{}, fmt.Errorf("type %s: only a scalar `vector <numeric>` is supported", td.Name)
		}
		elemNum, ok := numtype.Parse(td.Elem.Scalar)
		if !ok {
			return BuiltPlan{}, fmt.Errorf("type %s: unknown element type %q", td.Name, td.Elem.Scalar)
		}
		models[td.Name] = &Model{
			Name:    td.Name,
			Elem:    td.Elem,
			ElemNum: elemNum,
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
		if err := chk.checkCombinatorFn(merge, nil, m.ElemNum); err != nil {
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
		result, err := chk.checkValue(body, m.ElemNum)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("query %s.%s: %w", qd.TypeName, qd.MethodName, err)
		}
		m.Queries[qd.MethodName] = crdt.Method{Params: qd.Params, Body: body, Result: toValType(result), ResultNum: toResultNum(result)}
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
		if err := chk.checkCombinatorFn(body, paramScope(ud.Params), m.ElemNum); err != nil {
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

	// elem/hasElem give the current query's vector element type to the reduce
	// case of typeOf. They are set only for the duration of a checkValue call;
	// reduce is forbidden outside queries (so this is unset everywhere else).
	elem    numtype.NumType
	hasElem bool
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

// register records a resolved function body and its param scope. Each param is
// bound to its declared numeric type.
func (c *checker) register(name string, params []parser.ParamSpec, body parser.Expr) {
	pts := make([]vtype, len(params))
	scope := make(map[string]vtype, len(params))
	for i, p := range params {
		nt, _ := numtype.Parse(p.Type) // validated by validateParams
		pts[i] = vNum(nt)
		scope[p.Name] = vNum(nt)
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
	if s.result.kind == vkUnknown {
		return vUnknown, fmt.Errorf("cannot infer return type (unanchored recursion)")
	}
	c.result[name] = s.result
	return s.result, nil
}

// checkValue type-checks a term that must yield a value (not a function) and
// returns its type. Used for query bodies; elem is the vector element type, made
// available to any `reduce` sub-expression in the body.
func (c *checker) checkValue(e parser.Expr, elem numtype.NumType) (vtype, error) {
	c.elem, c.hasElem = elem, true
	defer func() { c.hasElem = false }()
	s, err := c.typeOf(e, nil)
	if err != nil {
		return vUnknown, err
	}
	if len(s.params) != 0 {
		return vUnknown, fmt.Errorf("expected a value, got a function missing %d argument(s)", len(s.params))
	}
	if s.result.kind == vkUnknown {
		return vUnknown, fmt.Errorf("cannot determine result type")
	}
	return s.result, nil
}

// checkCombinatorFn type-checks the function carried by a resolved zip/local
// node against the element type E: applying the function to E-typed slot(s) must
// yield a result assignable to E (Sub(result, E)). This is where non-negativity
// is enforced — e.g. `local (- k)` on a `vector real0+` loses the sign and is
// rejected. scope carries the method's params (a local update fn may reference
// them, e.g. `local (+ k)`).
func (c *checker) checkCombinatorFn(comb parser.Expr, scope map[string]vtype, elem numtype.NumType) error {
	if comb.Fn == nil {
		return fmt.Errorf("combinator has no function")
	}
	s, err := c.typeOf(*comb.Fn, scope)
	if err != nil {
		return err
	}
	if len(s.params) == 0 {
		return fmt.Errorf("combinator function takes no arguments")
	}
	args := make([]vtype, len(s.params))
	for i := range args {
		args[i] = vNum(elem)
	}
	sat, err := c.applyArgs(s, args)
	if err != nil {
		return err
	}
	if sat.result.kind == vkUnknown {
		return nil // unanchored recursion: result type can't be verified, allowed
	}
	if !subVtype(sat.result, vNum(elem)) {
		return fmt.Errorf("combinator result %s is not assignable to element type %s", sat.result, elem)
	}
	return nil
}

// typeOf computes the (possibly partial) signature of a resolved term.
func (c *checker) typeOf(e parser.Expr, scope map[string]vtype) (sig, error) {
	switch e.Kind {
	case parser.ExprNumLit:
		return sig{result: literalType(e.Num)}, nil
	case parser.ExprStrLit:
		return sig{result: vString}, nil
	case parser.ExprVar:
		t, ok := scope[e.Name]
		if !ok {
			return sig{}, fmt.Errorf("unbound variable %q", e.Name)
		}
		return sig{result: t}, nil
	case parser.ExprRef:
		if e.Ref == parser.RefPrimitive {
			ar, ok := primitiveArity[e.Name]
			if !ok {
				return sig{}, fmt.Errorf("unknown primitive %q", e.Name)
			}
			params := make([]vtype, ar)
			for i := range params {
				params[i] = numTop // any numeric operand
			}
			return sig{params: params, op: e.Name}, nil
		}
		params, ok := c.fnParams[e.Name]
		if !ok {
			return sig{}, fmt.Errorf("unknown function %q", e.Name)
		}
		res, err := c.inferReturn(e.Name)
		if err != nil {
			return sig{}, err
		}
		return sig{params: append([]vtype(nil), params...), result: res}, nil
	case parser.ExprApp:
		if e.Head == nil {
			return sig{}, fmt.Errorf("application has no head")
		}
		hs, err := c.typeOf(*e.Head, scope)
		if err != nil {
			return sig{}, err
		}
		args := make([]vtype, len(e.Args))
		for i, a := range e.Args {
			as, err := c.typeOf(*a, scope)
			if err != nil {
				return sig{}, err
			}
			if len(as.params) != 0 {
				return sig{}, fmt.Errorf("cannot pass a function as an argument")
			}
			args[i] = as.result
		}
		return c.applyArgs(hs, args)
	case parser.ExprGuards:
		return c.typeOfGuards(e, scope)
	case parser.ExprReduce:
		return c.typeOfReduce(e, scope)
	default:
		return sig{}, fmt.Errorf("cannot type-check expression of kind %d", e.Kind)
	}
}

// literalType types a numeric literal. All literals are non-negative; the
// literal 0 gets the internal Zero sign so it is assignable to any numeric type
// (non-negative AND non-positive targets). Integer-valued literals are int;
// others are real.
func literalType(n float64) vtype {
	switch {
	case n == 0:
		return vNum(numtype.NumType{Domain: numtype.DInt, Sign: numtype.SZero})
	case n == float64(int64(n)):
		return vNum(numtype.NumType{Domain: numtype.DInt, Sign: numtype.SNonNeg})
	default:
		return vNum(numtype.NumType{Domain: numtype.DReal, Sign: numtype.SNonNeg})
	}
}

// applyArgs applies args to a (possibly partial) signature. Each arg must be
// assignable to the corresponding param (subVtype, with vUnknown deferring).
// When the result saturates, the result type is computed via resultOf; otherwise
// a new partial sig carrying the remaining params (and accumulated bound
// operands) is returned.
func (c *checker) applyArgs(s sig, args []vtype) (sig, error) {
	if len(args) > len(s.params) {
		return sig{}, fmt.Errorf("too many arguments: expected %d, got %d", len(s.params), len(args))
	}
	for i, a := range args {
		if !subVtype(a, s.params[i]) {
			return sig{}, fmt.Errorf("argument %d: expected %s, got %s", i+1, s.params[i], a)
		}
	}
	bound := append(append([]vtype(nil), s.bound...), args...)
	remaining := s.params[len(args):]
	if len(remaining) == 0 {
		return sig{result: resultOf(s.op, s.result, bound)}, nil
	}
	return sig{params: remaining, result: s.result, op: s.op, bound: bound}, nil
}

// resultOf computes a saturated application's result type. For a non-primitive
// (op == "") it is the fixed fallback (the function's inferred return / value).
// For a primitive, an unknown operand defers to vUnknown; comparisons yield
// bool; arithmetic applies the per-operator numeric rule.
func resultOf(op string, fallback vtype, operands []vtype) vtype {
	if op == "" {
		return fallback
	}
	for _, o := range operands {
		if o.kind == vkUnknown {
			return vUnknown
		}
	}
	if cmpOps[op] {
		return vBool
	}
	return vNum(numBin(op, operands[0].num, operands[1].num))
}

// typeOfReduce types a `reduce <binary fn> <init>` over the current query's
// element type. The result type is the least fixpoint of folding the function
// over (accumulator, element), starting from the init literal's type — bounded
// because the lattice has finite height.
func (c *checker) typeOfReduce(e parser.Expr, scope map[string]vtype) (sig, error) {
	if e.Fn == nil || e.Init == nil {
		return sig{}, fmt.Errorf("malformed reduce")
	}
	if !c.hasElem {
		return sig{}, fmt.Errorf("reduce may only appear in a query body")
	}
	fs, err := c.typeOf(*e.Fn, scope)
	if err != nil {
		return sig{}, err
	}
	if len(fs.params) != 2 {
		return sig{}, fmt.Errorf("reduce needs a binary function")
	}
	initS, err := c.typeOf(*e.Init, scope)
	if err != nil {
		return sig{}, err
	}
	if initS.result.kind != vkNum {
		return sig{}, fmt.Errorf("reduce init must be numeric")
	}
	elemV := vNum(c.elem)
	acc := initS.result.num
	// Iterate to a fixpoint; the lattice height bounds the iteration count.
	for i := 0; i < 16; i++ {
		sat, err := c.applyArgs(fs, []vtype{vNum(acc), elemV})
		if err != nil {
			return sig{}, err
		}
		if sat.result.kind == vkUnknown {
			break // recursive reduce fn mid-inference: settle on the accumulator so far
		}
		if sat.result.kind != vkNum {
			return sig{}, fmt.Errorf("reduce function must produce a numeric value")
		}
		next := numtype.Join(acc, sat.result.num)
		if next == acc {
			break
		}
		acc = next
	}
	return sig{result: vNum(acc)}, nil
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
			if cs.result.kind != vkBool && cs.result.kind != vkUnknown {
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
	return sig{result: result}, nil
}

// unify widens two guard-result types to a common type: vUnknown defers, two
// numeric types join to their least upper bound, and otherwise the kinds must
// match.
func unify(a, b vtype) (vtype, error) {
	switch {
	case a.kind == vkUnknown:
		return b, nil
	case b.kind == vkUnknown:
		return a, nil
	case a.kind == vkNum && b.kind == vkNum:
		return vNum(numtype.Join(a.num, b.num)), nil
	case a.kind != b.kind:
		return vUnknown, fmt.Errorf("%s vs %s", a, b)
	default:
		return a, nil
	}
}

// ---- validators ----------------------------------------------------

func validateParams(ps []parser.ParamSpec) error {
	seen := make(map[string]bool, len(ps))
	for _, p := range ps {
		if _, ok := numtype.Parse(p.Type); !ok {
			return fmt.Errorf("param %s: unknown type %q", p.Name, p.Type)
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

// paramScope builds a type-checking scope binding each param to its declared
// numeric type.
func paramScope(ps []parser.ParamSpec) map[string]vtype {
	s := make(map[string]vtype, len(ps))
	for _, p := range ps {
		nt, _ := numtype.Parse(p.Type) // validated by validateParams
		s[p.Name] = vNum(nt)
	}
	return s
}
