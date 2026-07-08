package builder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"gospr/crdt"
	"gospr/numtype"
	"gospr/parser"
	"gospr/prover"
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
	Elem    crdt.ElemT             // resolved element descriptor (scalar numeric or a struct)
	Merge   parser.Expr            // a Zip expr (Fn resolved)
	Queries map[string]crdt.Method // Reduce methods (bodies resolved)
	Updates map[string]crdt.Method // Local methods (bodies resolved)
	Funcs   map[string]crdt.Function
}

func (m *Model) New(nodeID string) crdt.CRDT {
	return crdt.NewVector(nodeID, m.Elem, m.Merge, m.Queries, m.Updates, m.Funcs)
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
	// Fingerprint identifies "same deployed DSL" across nodes: a hash of a
	// canonical encoding of the input parser.Plan. Two nodes that received the
	// same deploy share it, so a linearized op's peer can ack only when it runs
	// the same plan (a same-name/different-semantics collection is rejected).
	Fingerprint string
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
	vkStruct
	vkFunc
)

// vtype is the builder's internal type: a value type OR (kind vkFunc) a function.
// For vkNum the carried NumType says which numeric subtype; for vkStruct, fields
// lists the ordered field types (and name is the nominal struct name, for
// diagnostics); for vkFunc, fn carries the (possibly partially applied) signature.
// Functions are first-class in the type model — typeOf returns one vtype for both
// values and functions — but not as runtime values: a vkFunc in an argument /
// struct-field / element position is a type error (see the isFunc guards).
type vtype struct {
	kind   vkind
	num    numtype.NumType
	name   string   // vkStruct nominal name (diagnostics only)
	fields []vfield // vkStruct, in declaration order
	fn     *fnType  // vkFunc signature (nil otherwise)
}

// isFunc reports whether t is a function type (a vkFunc carrying a signature).
func isFunc(t vtype) bool { return t.kind == vkFunc }

// mkFunc wraps a signature as a first-class function vtype.
func mkFunc(f fnType) vtype { return vtype{kind: vkFunc, fn: &f} }

// vfield is one field of a struct vtype.
type vfield struct {
	name string
	typ  vtype
}

// vElem lifts a resolved element descriptor into a checker vtype (scalar or
// struct), so combinator/reduce boundary checks can treat both uniformly.
func vElem(t crdt.ElemT) vtype {
	if t.Struct {
		fields := make([]vfield, len(t.Fields))
		for i, f := range t.Fields {
			fields[i] = vfield{name: f.Name, typ: vElem(f.Type)}
		}
		return vtype{kind: vkStruct, name: t.Name, fields: fields}
	}
	return vNum(t.Num)
}

// elemTOf lowers a struct vtype back to a crdt.ElemT descriptor (used to capture
// a struct-valued query result). Only meaningful for vkStruct / vkNum — callers
// gate on isFunc first, so a vkFunc must never reach here (it would lower to a
// garbage scalar ElemT).
func elemTOf(t vtype) crdt.ElemT {
	if t.kind == vkStruct {
		fields := make([]crdt.FieldT, len(t.fields))
		for i, f := range t.fields {
			fields[i] = crdt.FieldT{Name: f.name, Type: elemTOf(f.typ)}
		}
		return crdt.ElemT{Struct: true, Name: t.name, Fields: fields}
	}
	return crdt.ElemT{Num: t.num}
}

// findField returns the type of a struct field by name.
func (t vtype) findField(name string) (vtype, bool) {
	for _, f := range t.fields {
		if f.name == name {
			return f.typ, true
		}
	}
	return vtype{}, false
}

var (
	vUnknown = vtype{kind: vkUnknown}
	vBool    = vtype{kind: vkBool}
	vString  = vtype{kind: vkString}
	// numTop is the widest numeric type (`rat`); a primitive accepts any
	// numeric operand, expressed as "operand must be <: numTop".
	numTop = vNum(numtype.NumType{Domain: numtype.DRat, Sign: numtype.SAny})
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
	case vkStruct:
		if t.name != "" {
			return t.name
		}
		parts := make([]string, len(t.fields))
		for i, f := range t.fields {
			parts[i] = f.name + " " + f.typ.String()
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case vkFunc:
		if t.fn == nil {
			return "function"
		}
		parts := make([]string, len(t.fn.params))
		for i, p := range t.fn.params {
			parts[i] = p.String()
		}
		return "(" + strings.Join(parts, ", ") + ") -> " + t.fn.result.String()
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

// containsUnknown reports whether t is vkUnknown or (recursively) has a struct
// field that is. The vUnknown sentinel must not survive into a built artifact, and
// a top-level `kind == vkUnknown` test misses one buried inside a struct field —
// where subVtype's unknown-defers rule would otherwise silently accept it. Used to
// gate every point that finalizes a value type (inferReturn, checkValue).
func containsUnknown(t vtype) bool {
	if t.kind == vkUnknown {
		return true
	}
	if t.kind == vkStruct {
		for _, f := range t.fields {
			if containsUnknown(f.typ) {
				return true
			}
		}
	}
	if t.kind == vkFunc && t.fn != nil {
		for _, p := range t.fn.params {
			if containsUnknown(p) {
				return true
			}
		}
		return containsUnknown(t.fn.result)
	}
	return false
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
	if a.kind == vkStruct && b.kind == vkStruct {
		// Structural, nominal-agnostic: exact field-set match, each field a subtype.
		if len(a.fields) != len(b.fields) {
			return false
		}
		for _, bf := range b.fields {
			af, ok := a.findField(bf.name)
			if !ok || !subVtype(af, bf.typ) {
				return false
			}
		}
		return true
	}
	if a.kind == vkFunc && b.kind == vkFunc {
		return fnSubtype(a.fn, b.fn)
	}
	return a.kind == b.kind
}

// fnSubtype reports structural assignability of two function signatures (same
// arity, params and result pairwise assignable). Defensive: functions never reach
// a value-assignability check today, but the explicit case stops subVtype's
// kind-equality fall-through from declaring any two functions mutually assignable.
func fnSubtype(a, b *fnType) bool {
	if a == nil || b == nil || len(a.params) != len(b.params) {
		return false
	}
	for i := range a.params {
		if !subVtype(a.params[i], b.params[i]) {
			return false
		}
	}
	return subVtype(a.result, b.result)
}

// fnEqual reports structural equality of two function signatures (arity + pairwise
// vtypeEqual on params and result). Companion to fnSubtype for unify/vtypeEqual.
func fnEqual(a, b *fnType) bool {
	if a == nil || b == nil || len(a.params) != len(b.params) {
		return false
	}
	for i := range a.params {
		if !vtypeEqual(a.params[i], b.params[i]) {
			return false
		}
	}
	return vtypeEqual(a.result, b.result)
}

// fnType is a (possibly partially applied) function signature, nested inside a
// vkFunc vtype: the param types it still expects plus the type it yields once
// saturated. For a primitive the result depends on its operands, so op names the
// operator and bound records the operand types already supplied; result is then
// computed when saturated. For a user function, op == "" and result is fixed.
type fnType struct {
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
// two numeric operands. Domain widens to Rat if either operand is Rat; the
// sign follows the operator's sound rule.
func numBin(op string, a, b numtype.NumType) numtype.NumType {
	d := numtype.DInt
	if a.Domain == numtype.DRat || b.Domain == numtype.DRat {
		d = numtype.DRat
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
	// Resolve every `type` definition into either a struct descriptor or a vector
	// Model. The token resolver classifies a type-position name as a numeric type
	// (numtype.Parse) or a user struct type, and rejects the numeric names /
	// `vector` keyword as user type names so a type position is unambiguous.
	types, err := resolveTypes(plan.Types)
	if err != nil {
		return BuiltPlan{}, err
	}
	models := types.models

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
		// A `fn` param may be numeric OR struct-typed (fns receive struct slots
		// from combinators). It must resolve to a known type.
		if err := validateFnParams(fd.Params, types); err != nil {
			return BuiltPlan{}, fmt.Errorf("function %s: %w", fd.Name, err)
		}
		fnArity[fd.Name] = len(fd.Params)
	}

	env := newEnv(fnArity)

	// Resolve each function body against its parameter scope. `reduce` is
	// rejected here (allowReduce=false) so global functions stay pure — only
	// query bodies may fold the vector.
	funcs := make(map[string]crdt.Function, len(plan.Functions))
	chk := newChecker(types)
	for _, fd := range plan.Functions {
		scope := paramSet(fd.Params)
		body, err := env.resolve(fd.Body, scope, false)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("function %s: %w", fd.Name, err)
		}
		if a := arityOf(body); a != 0 {
			return BuiltPlan{}, fmt.Errorf("function %s: body must return a value, but is missing %d argument(s)", fd.Name, a)
		}
		if err := chk.register(fd.Name, fd.Params, body, fd.RetType); err != nil {
			return BuiltPlan{}, fmt.Errorf("function %s: %w", fd.Name, err)
		}
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
		if err := chk.checkCombinatorFn(merge, nil, m.Elem); err != nil {
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
		// Query params cross the HTTP boundary (bindParams), which handles only
		// scalar numeric values — so a struct-typed query param is rejected, same
		// as an update param. Tokens (incl. `V.Elem`/aliases) are normalized to
		// concrete numtype names for the downstream wire/prover/swagger consumers.
		qparams, err := resolveScalarParams(qd.Params, types)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("query %s.%s: %w", qd.TypeName, qd.MethodName, err)
		}
		// A query body is a general expression that may fold the vector via
		// `reduce` (allowReduce=true) and may reference the query's params. Its
		// result type (rat/bool/string) is recorded for serialization/swagger.
		body, err := env.resolve(qd.Body, paramSet(qparams), true)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("query %s.%s: %w", qd.TypeName, qd.MethodName, err)
		}
		result, err := chk.checkValue(body, chk.paramScope(qparams), m.Elem)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("query %s.%s: %w", qd.TypeName, qd.MethodName, err)
		}
		method := crdt.Method{Params: qparams, Body: body}
		if result.kind == vkStruct {
			et := elemTOf(result)
			method.ResultStruct = &et
			method.Result = parser.TypeReal // unused for a struct result; ResultStruct drives swagger
		} else {
			method.Result = toValType(result)
			method.ResultNum = toResultNum(result)
		}
		m.Queries[qd.MethodName] = method
	}

	for _, ud := range plan.Updates {
		m, ok := models[ud.TypeName]
		if !ok {
			return BuiltPlan{}, fmt.Errorf("update for unknown type %s", ud.TypeName)
		}
		if _, dup := m.Updates[ud.MethodName]; dup {
			return BuiltPlan{}, fmt.Errorf("update %s.%s defined twice", ud.TypeName, ud.MethodName)
		}
		// Update params cross the HTTP boundary (bindParams), which handles only
		// scalar numeric values — so a struct-typed update param is rejected;
		// tokens (incl. `V.Elem`/aliases) are normalized to concrete numtype names.
		uparams, err := resolveScalarParams(ud.Params, types)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("update %s.%s: %w", ud.TypeName, ud.MethodName, err)
		}
		body, err := env.resolveCombinator(ud.Body, paramSet(uparams))
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("update %s.%s: %w", ud.TypeName, ud.MethodName, err)
		}
		if err := chk.checkCombinatorFn(body, chk.paramScope(uparams), m.Elem); err != nil {
			return BuiltPlan{}, fmt.Errorf("update %s.%s: %w", ud.TypeName, ud.MethodName, err)
		}
		m.Updates[ud.MethodName] = crdt.Method{Params: uparams, Body: body, Result: parser.TypeReal}
	}

	// Every type must define a merge — a vector without a join isn't a CRDT.
	for name := range models {
		if !mergeSeen[name] {
			return BuiltPlan{}, fmt.Errorf("type %s has no merge defined", name)
		}
	}

	// Prove convergence: the merge must be a join-semilattice and every update
	// inflationary in its induced order. Unprovable types are rejected (the
	// gateway surfaces this as a 400). Requires z3 on PATH.
	for _, m := range models {
		if err := prover.Prove(m.Elem, m.Merge, m.Updates, funcs); err != nil {
			return BuiltPlan{}, fmt.Errorf("convergence proof failed for type %s: %w", m.Name, err)
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

	return BuiltPlan{
		Models:      models,
		Functions:   funcs,
		Collections: collections,
		Fingerprint: fingerprint(plan),
	}, nil
}

// fingerprint hashes a canonical encoding of the input Plan. Plan is flat slices
// of defs (no maps), so json.Marshal is order-stable and yields the same digest
// on every node that received the same deploy. Expr.Num (*big.Rat) marshals via
// its TextMarshaler, so numeric literals are captured exactly.
func fingerprint(plan parser.Plan) string {
	data, err := json.Marshal(plan)
	if err != nil {
		// Plan is plain data with no un-marshalable fields; treat an unexpected
		// failure as an empty (always-mismatching) fingerprint rather than panicking.
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
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
	case parser.ExprStructLit:
		fields := make([]parser.StructField, len(expr.StructFields))
		for i, sf := range expr.StructFields {
			if sf.Value == nil {
				return parser.Expr{}, fmt.Errorf("struct field %q has no value", sf.Name)
			}
			rv, err := e.resolve(*sf.Value, scope, allowReduce)
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
		target, err := e.resolve(*expr.Target, scope, allowReduce)
		if err != nil {
			return parser.Expr{}, err
		}
		if arityOf(target) != 0 {
			return parser.Expr{}, fmt.Errorf("cannot access a field of a function")
		}
		return parser.Expr{Kind: parser.ExprField, Target: &target, Field: expr.Field}, nil
	case parser.ExprReduce:
		if !allowReduce {
			return parser.Expr{}, fmt.Errorf("`reduce` may only appear in a query body")
		}
		fn, err := e.resolveFn(expr.Fn, scope, 2)
		if err != nil {
			return parser.Expr{}, fmt.Errorf("reduce must be `reduce <binary fn> <init>`: %w", err)
		}
		if expr.Init == nil {
			return parser.Expr{}, fmt.Errorf("reduce init is missing")
		}
		// The init is a literal (numeric or struct); resolve it as a pure value.
		init, err := e.resolve(*expr.Init, scope, false)
		if err != nil {
			return parser.Expr{}, fmt.Errorf("reduce init: %w", err)
		}
		if arityOf(init) != 0 {
			return parser.Expr{}, fmt.Errorf("reduce init must be a value")
		}
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
// types are numeric-only and known up front; return types are inferred lazily via
// memoized DFS so functions may reference one another and recurse. A recursive
// call caught mid-inference yields vUnknown, which unifies with any concrete
// type — so anchored recursion resolves, while wholly unanchored recursion
// (Option A) leaves vUnknown and is rejected.
type checker struct {
	reg         typeReg
	fnParams    map[string][]vtype
	fnScope     map[string]map[string]vtype
	fnBody      map[string]parser.Expr
	annotations map[string]vtype // declared `-> type` return types (absent when un-annotated)
	result      map[string]vtype
	inProgress  map[string]bool

	// elem/hasElem give the current query's vector element type to the reduce
	// case of typeOf. They are set only for the duration of a checkValue call;
	// reduce is forbidden outside queries (so this is unset everywhere else).
	elem    crdt.ElemT
	hasElem bool
}

func newChecker(reg typeReg) *checker {
	return &checker{
		reg:         reg,
		fnParams:    map[string][]vtype{},
		fnScope:     map[string]map[string]vtype{},
		fnBody:      map[string]parser.Expr{},
		annotations: map[string]vtype{},
		result:      map[string]vtype{},
		inProgress:  map[string]bool{},
	}
}

// paramVtype resolves a param's declared type token to a checker vtype (scalar or
// struct). The token was validated at registration, so a failure defaults to the
// top numeric type rather than erroring here.
func (c *checker) paramVtype(tok string) vtype {
	et, err := c.reg.resolveToken(tok)
	if err != nil {
		return numTop
	}
	return vElem(et)
}

// resolveResultType resolves a `-> type` return-annotation token to a vtype.
// Unlike paramVtype, it errors on an unknown token (a bad annotation is a build
// error). bool/string are recognized here — a return type may be non-numeric —
// whereas param types (numeric/struct only) never are; `type bool`/`type string`
// are reserved (see resolveTypes) so these keywords are unambiguous.
func (c *checker) resolveResultType(tok string) (vtype, error) {
	switch tok {
	case "bool":
		return vBool, nil
	case "string":
		return vString, nil
	}
	et, err := c.reg.resolveToken(tok)
	if err != nil {
		return vUnknown, err
	}
	return vElem(et), nil
}

// register records a resolved function body, its param scope, and its optional
// resolved return annotation. Each param is bound to its declared type (numeric
// or struct).
func (c *checker) register(name string, params []parser.ParamSpec, body parser.Expr, retType string) error {
	pts := make([]vtype, len(params))
	scope := make(map[string]vtype, len(params))
	for i, p := range params {
		t := c.paramVtype(p.Type)
		pts[i] = t
		scope[p.Name] = t
	}
	c.fnParams[name] = pts
	c.fnScope[name] = scope
	c.fnBody[name] = body
	if retType != "" {
		rt, err := c.resolveResultType(retType)
		if err != nil {
			return fmt.Errorf("return type: %w", err)
		}
		c.annotations[name] = rt
	}
	return nil
}

// paramScope builds a type-checking scope binding each param to its declared type
// (numeric or struct).
func (c *checker) paramScope(ps []parser.ParamSpec) map[string]vtype {
	s := make(map[string]vtype, len(ps))
	for _, p := range ps {
		s[p.Name] = c.paramVtype(p.Type)
	}
	return s
}

// inferReturn returns a function's result type, type-checking its body on first
// visit.
//
// With a declared `-> type` annotation, the declared type is SEEDED into the memo
// before the body is visited, so a recursive call resolves to it rather than to
// vUnknown — this is what lets otherwise-unanchored recursion build. The body is
// then strictly validated: its type must be CONCRETE (never vUnknown) and a
// subtype of the declared type, so subVtype's unknown-defers rule can't launder an
// unresolved recursive body into a passing annotation.
//
// Without an annotation, a function caught mid-inference (recursion) reports
// vUnknown, which unifies with any concrete type — so anchored recursion resolves,
// while wholly-unanchored recursion (Option A) is rejected.
func (c *checker) inferReturn(name string) (vtype, error) {
	if r, ok := c.result[name]; ok {
		return r, nil
	}
	if ann, ok := c.annotations[name]; ok {
		c.result[name] = ann // seed before visiting body: recursive calls see `ann`
		bodyT, err := c.typeOf(c.fnBody[name], c.fnScope[name])
		if err != nil {
			return vUnknown, err
		}
		if isFunc(bodyT) {
			return vUnknown, fmt.Errorf("body must return a value, but is missing %d argument(s)", len(bodyT.fn.params))
		}
		if containsUnknown(bodyT) {
			return vUnknown, fmt.Errorf("cannot verify body against declared return type %s (unanchored recursion)", ann)
		}
		if !subVtype(bodyT, ann) {
			return vUnknown, fmt.Errorf("declared return type %s but body has type %s", ann, bodyT)
		}
		return ann, nil
	}
	if c.inProgress[name] {
		return vUnknown, nil
	}
	c.inProgress[name] = true
	bodyT, err := c.typeOf(c.fnBody[name], c.fnScope[name])
	c.inProgress[name] = false
	if err != nil {
		return vUnknown, err
	}
	if isFunc(bodyT) {
		return vUnknown, fmt.Errorf("body must return a value, but is missing %d argument(s)", len(bodyT.fn.params))
	}
	if containsUnknown(bodyT) {
		return vUnknown, fmt.Errorf("cannot infer return type; add a -> annotation (unanchored recursion)")
	}
	c.result[name] = bodyT
	return bodyT, nil
}

// checkValue type-checks a term that must yield a value (not a function) and
// returns its type. Used for query bodies; scope carries the query's params (the
// body may reference them), and elem is the vector element type, made available
// to any `reduce` sub-expression in the body.
func (c *checker) checkValue(e parser.Expr, scope map[string]vtype, elem crdt.ElemT) (vtype, error) {
	c.elem, c.hasElem = elem, true
	defer func() { c.hasElem = false }()
	t, err := c.typeOf(e, scope)
	if err != nil {
		return vUnknown, err
	}
	if isFunc(t) {
		return vUnknown, fmt.Errorf("expected a value, got a function missing %d argument(s)", len(t.fn.params))
	}
	if containsUnknown(t) {
		return vUnknown, fmt.Errorf("cannot determine result type")
	}
	return t, nil
}

// checkCombinatorFn type-checks the function carried by a resolved zip/local
// node against the element type E: applying the function to E-typed slot(s) must
// yield a result assignable to E (Sub(result, E)). This is where non-negativity
// is enforced — e.g. `local (- k)` on a `vector rat0+` loses the sign and is
// rejected. scope carries the method's params (a local update fn may reference
// them, e.g. `local (+ k)`).
func (c *checker) checkCombinatorFn(comb parser.Expr, scope map[string]vtype, elem crdt.ElemT) error {
	if comb.Fn == nil {
		return fmt.Errorf("combinator has no function")
	}
	elemV := vElem(elem)
	t, err := c.typeOf(*comb.Fn, scope)
	if err != nil {
		return err
	}
	if !isFunc(t) {
		return fmt.Errorf("combinator function takes no arguments")
	}
	args := make([]vtype, len(t.fn.params))
	for i := range args {
		args[i] = elemV
	}
	sat, err := c.applyArgs(*t.fn, args)
	if err != nil {
		return err
	}
	if containsUnknown(sat) {
		return nil // unanchored recursion: result type can't be verified, allowed
	}
	if !subVtype(sat, elemV) {
		return fmt.Errorf("combinator result %s is not assignable to element type %s", sat, elemV)
	}
	return nil
}

// typeOf computes the type of a resolved term: a value vtype, or (for an
// unsaturated reference/application) a vkFunc carrying the remaining signature.
func (c *checker) typeOf(e parser.Expr, scope map[string]vtype) (vtype, error) {
	switch e.Kind {
	case parser.ExprNumLit:
		return literalType(e.Num), nil
	case parser.ExprStrLit:
		return vString, nil
	case parser.ExprVar:
		t, ok := scope[e.Name]
		if !ok {
			return vtype{}, fmt.Errorf("unbound variable %q", e.Name)
		}
		return t, nil
	case parser.ExprRef:
		if e.Ref == parser.RefPrimitive {
			ar, ok := primitiveArity[e.Name]
			if !ok {
				return vtype{}, fmt.Errorf("unknown primitive %q", e.Name)
			}
			params := make([]vtype, ar)
			for i := range params {
				params[i] = numTop // any numeric operand
			}
			return mkFunc(fnType{params: params, op: e.Name}), nil
		}
		params, ok := c.fnParams[e.Name]
		if !ok {
			return vtype{}, fmt.Errorf("unknown function %q", e.Name)
		}
		res, err := c.inferReturn(e.Name)
		if err != nil {
			return vtype{}, err
		}
		return mkFunc(fnType{params: append([]vtype(nil), params...), result: res}), nil
	case parser.ExprApp:
		if e.Head == nil {
			return vtype{}, fmt.Errorf("application has no head")
		}
		hs, err := c.typeOf(*e.Head, scope)
		if err != nil {
			return vtype{}, err
		}
		if !isFunc(hs) {
			return vtype{}, fmt.Errorf("cannot apply arguments to a value of type %s", hs)
		}
		args := make([]vtype, len(e.Args))
		for i, a := range e.Args {
			as, err := c.typeOf(*a, scope)
			if err != nil {
				return vtype{}, err
			}
			if isFunc(as) {
				return vtype{}, fmt.Errorf("cannot pass a function as an argument")
			}
			args[i] = as
		}
		return c.applyArgs(*hs.fn, args)
	case parser.ExprGuards:
		return c.typeOfGuards(e, scope)
	case parser.ExprStructLit:
		fields := make([]vfield, len(e.StructFields))
		seen := make(map[string]bool, len(e.StructFields))
		for i, sf := range e.StructFields {
			if seen[sf.Name] {
				return vtype{}, fmt.Errorf("duplicate struct field %q", sf.Name)
			}
			seen[sf.Name] = true
			fs, err := c.typeOf(*sf.Value, scope)
			if err != nil {
				return vtype{}, err
			}
			if isFunc(fs) {
				return vtype{}, fmt.Errorf("struct field %q is not a value", sf.Name)
			}
			fields[i] = vfield{name: sf.Name, typ: fs}
		}
		return vtype{kind: vkStruct, fields: fields}, nil
	case parser.ExprField:
		ts, err := c.typeOf(*e.Target, scope)
		if err != nil {
			return vtype{}, err
		}
		if isFunc(ts) {
			return vtype{}, fmt.Errorf("cannot access a field of a function")
		}
		if ts.kind != vkStruct {
			return vtype{}, fmt.Errorf("cannot access field %q of non-struct type %s", e.Field, ts)
		}
		ft, ok := ts.findField(e.Field)
		if !ok {
			return vtype{}, fmt.Errorf("type %s has no field %q", ts, e.Field)
		}
		return ft, nil
	case parser.ExprReduce:
		return c.typeOfReduce(e, scope)
	default:
		return vtype{}, fmt.Errorf("cannot type-check expression of kind %d", e.Kind)
	}
}

// literalType types a numeric literal. The literal 0 gets the internal Zero sign
// so it is assignable to any numeric type (non-negative AND non-positive targets);
// a positive literal is non-negative (SNonNeg), a negative one is non-positive
// (SNonPos). Integer-valued literals are int; others are rat.
func literalType(n *big.Rat) vtype {
	if n.Sign() == 0 {
		return vNum(numtype.NumType{Domain: numtype.DInt, Sign: numtype.SZero})
	}
	sign := numtype.SNonNeg
	if n.Sign() < 0 {
		sign = numtype.SNonPos
	}
	domain := numtype.DRat
	if n.IsInt() {
		domain = numtype.DInt
	}
	return vNum(numtype.NumType{Domain: domain, Sign: sign})
}

// applyArgs applies args to a (possibly partial) signature. Each arg must be
// assignable to the corresponding param (subVtype, with vUnknown deferring).
// When the result saturates, the result type is computed via resultOf; otherwise
// a new partial sig carrying the remaining params (and accumulated bound
// operands) is returned.
func (c *checker) applyArgs(f fnType, args []vtype) (vtype, error) {
	if len(args) > len(f.params) {
		return vtype{}, fmt.Errorf("too many arguments: expected %d, got %d", len(f.params), len(args))
	}
	for i, a := range args {
		if !subVtype(a, f.params[i]) {
			return vtype{}, fmt.Errorf("argument %d: expected %s, got %s", i+1, f.params[i], a)
		}
	}
	bound := append(append([]vtype(nil), f.bound...), args...)
	remaining := f.params[len(args):]
	if len(remaining) == 0 {
		return resultOf(f.op, f.result, bound), nil
	}
	return mkFunc(fnType{params: remaining, result: f.result, op: f.op, bound: bound}), nil
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
func (c *checker) typeOfReduce(e parser.Expr, scope map[string]vtype) (vtype, error) {
	if e.Fn == nil || e.Init == nil {
		return vtype{}, fmt.Errorf("malformed reduce")
	}
	if !c.hasElem {
		return vtype{}, fmt.Errorf("reduce may only appear in a query body")
	}
	fs, err := c.typeOf(*e.Fn, scope)
	if err != nil {
		return vtype{}, err
	}
	if !isFunc(fs) || len(fs.fn.params) != 2 {
		return vtype{}, fmt.Errorf("reduce needs a binary function")
	}
	initT, err := c.typeOf(*e.Init, scope)
	if err != nil {
		return vtype{}, err
	}
	if isFunc(initT) {
		return vtype{}, fmt.Errorf("reduce init must be a value")
	}
	elemV := vElem(c.elem) // fold element type: scalar or struct
	acc := initT
	// Iterate to a fixpoint; the lattice has finite height, so this terminates.
	for i := 0; i < 16; i++ {
		sat, err := c.applyArgs(*fs.fn, []vtype{acc, elemV})
		if err != nil {
			return vtype{}, err
		}
		if sat.kind == vkUnknown {
			break // recursive reduce fn mid-inference: settle on the accumulator so far
		}
		next, err := unify(acc, sat)
		if err != nil {
			return vtype{}, fmt.Errorf("reduce function result type is inconsistent: %w", err)
		}
		if vtypeEqual(next, acc) {
			break
		}
		acc = next
	}
	return acc, nil
}

// vtypeEqual reports structural equality of two value types (used to detect the
// reduce fixpoint). Numeric types compare by NumType; structs by matching field
// sets recursively.
func vtypeEqual(a, b vtype) bool {
	if a.kind != b.kind {
		return false
	}
	switch a.kind {
	case vkNum:
		return a.num == b.num
	case vkStruct:
		if len(a.fields) != len(b.fields) {
			return false
		}
		for _, af := range a.fields {
			bf, ok := b.findField(af.name)
			if !ok || !vtypeEqual(af.typ, bf) {
				return false
			}
		}
		return true
	case vkFunc:
		return fnEqual(a.fn, b.fn)
	default:
		return true
	}
}

func (c *checker) typeOfGuards(e parser.Expr, scope map[string]vtype) (vtype, error) {
	n := len(e.Cases)
	if n == 0 {
		return vtype{}, fmt.Errorf("guarded body has no cases")
	}
	result := vUnknown
	for i, gc := range e.Cases {
		isLast := i == n-1
		if gc.Otherwise && !isLast {
			return vtype{}, fmt.Errorf("`otherwise` must be the last guard case")
		}
		if !gc.Otherwise && isLast {
			return vtype{}, fmt.Errorf("a guarded function must end with an `otherwise` case")
		}
		if !gc.Otherwise {
			if gc.Cond == nil {
				return vtype{}, fmt.Errorf("guard case has no condition")
			}
			cs, err := c.typeOf(*gc.Cond, scope)
			if err != nil {
				return vtype{}, err
			}
			if isFunc(cs) {
				return vtype{}, fmt.Errorf("guard condition must be a value, not a function")
			}
			if cs.kind != vkBool && cs.kind != vkUnknown {
				return vtype{}, fmt.Errorf("guard condition must be bool, got %s", cs)
			}
		}
		if gc.Result == nil {
			return vtype{}, fmt.Errorf("guard case has no result")
		}
		rs, err := c.typeOf(*gc.Result, scope)
		if err != nil {
			return vtype{}, err
		}
		if isFunc(rs) {
			return vtype{}, fmt.Errorf("guard result must be a value, not a function")
		}
		unified, err := unify(result, rs)
		if err != nil {
			return vtype{}, fmt.Errorf("guard results must all have the same type: %w", err)
		}
		result = unified
	}
	return result, nil
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
	case a.kind == vkStruct && b.kind == vkStruct:
		if len(a.fields) != len(b.fields) {
			return vUnknown, fmt.Errorf("%s vs %s", a, b)
		}
		fields := make([]vfield, len(a.fields))
		for i, af := range a.fields {
			bf, ok := b.findField(af.name)
			if !ok {
				return vUnknown, fmt.Errorf("%s vs %s", a, b)
			}
			u, err := unify(af.typ, bf)
			if err != nil {
				return vUnknown, err
			}
			fields[i] = vfield{name: af.name, typ: u}
		}
		return vtype{kind: vkStruct, name: a.name, fields: fields}, nil
	case a.kind == vkFunc && b.kind == vkFunc:
		if fnEqual(a.fn, b.fn) {
			return a, nil
		}
		return vUnknown, fmt.Errorf("cannot unify functions %s vs %s", a, b)
	case a.kind != b.kind:
		return vUnknown, fmt.Errorf("%s vs %s", a, b)
	default:
		return a, nil
	}
}

// ---- type registry -------------------------------------------------

// typeReg holds the resolved type universe: the vector Models (by type name), the
// resolved struct descriptors (by struct type name), and element-ref aliases
// (`type X = V.Elem`, by alias name). resolveToken classifies a type-position token
// as a numeric type, a named struct/alias type, or a dotted `V.Elem` reference.
type typeReg struct {
	models  map[string]*Model
	structs map[string]crdt.ElemT
	aliases map[string]crdt.ElemT
}

// splitDotted splits a `Base.Member` type token at its first `.`; ok is false for a
// non-dotted token. typeNameP admits at most one `.Member`, so a malformed member
// (a further dot) simply fails the `Elem` check downstream.
func splitDotted(tok string) (base, member string, ok bool) {
	i := strings.IndexByte(tok, '.')
	if i < 0 {
		return "", "", false
	}
	return tok[:i], tok[i+1:], true
}

// resolveToken maps a type-position token to a resolved element descriptor: a
// dotted `V.Elem` reference (the vector's element), a numeric type (numtype.Parse),
// a named struct type, or an element-ref alias. This is the post-resolveTypes form:
// every type is already memoized in the reg, so a `V.Elem` reads Model.Elem
// directly. A (non-dotted) vector type name is rejected — a vector cannot be a
// struct field or a param type.
func (r typeReg) resolveToken(tok string) (crdt.ElemT, error) {
	if base, member, ok := splitDotted(tok); ok {
		if member != "Elem" {
			return crdt.ElemT{}, fmt.Errorf("unknown type member %q in %q (only .Elem is supported)", member, tok)
		}
		m, ok := r.models[base]
		if !ok {
			return crdt.ElemT{}, fmt.Errorf("type %q is not a vector; cannot take `.Elem`", base)
		}
		return m.Elem, nil
	}
	if nt, ok := numtype.Parse(tok); ok {
		return crdt.ElemT{Num: nt}, nil
	}
	if s, ok := r.structs[tok]; ok {
		return s, nil
	}
	if a, ok := r.aliases[tok]; ok {
		return a, nil
	}
	if _, ok := r.models[tok]; ok {
		return crdt.ElemT{}, fmt.Errorf("type %q is a vector and cannot be used as a field or param type", tok)
	}
	return crdt.ElemT{}, fmt.Errorf("unknown type %q", tok)
}

// resolveTypes folds the flat TypeDefs into a typeReg: struct type descriptors,
// vector Models (element = a token or an inline struct body), and element-ref
// aliases (`type X = V.Elem`). All three are resolved on demand and memoized,
// sharing one cycle-detection set — so an alias may be used as a struct field type
// or a vector element (it resolves *during* type resolution), and any cycle
// (recursive struct, `type V = vector V.Elem`, or an alias↔vector loop) is caught.
// A user type name may not shadow a numeric type name or the `vector` keyword, so
// every type-position token resolves unambiguously.
func resolveTypes(tds []parser.TypeDef) (typeReg, error) {
	reg := typeReg{models: map[string]*Model{}, structs: map[string]crdt.ElemT{}, aliases: map[string]crdt.ElemT{}}
	structDefs := map[string]parser.ElemType{}
	vectorDefs := map[string]parser.ElemType{}
	aliasDefs := map[string]parser.ElemType{}
	seen := map[string]bool{}
	for _, td := range tds {
		if seen[td.Name] {
			return reg, fmt.Errorf("type %s declared twice", td.Name)
		}
		if _, isNum := numtype.Parse(td.Name); isNum || td.Name == "vector" || td.Name == "bool" || td.Name == "string" {
			return reg, fmt.Errorf("type name %q is reserved", td.Name)
		}
		seen[td.Name] = true
		switch td.Elem.Kind {
		case parser.KindStruct:
			structDefs[td.Name] = td.Elem
		case parser.KindVector:
			vectorDefs[td.Name] = td.Elem
		case parser.KindElemRef:
			aliasDefs[td.Name] = td.Elem
		default:
			return reg, fmt.Errorf("type %s: unsupported definition", td.Name)
		}
	}

	// The three resolvers share `resolving` for cross-category cycle detection.
	// vectorElem memoizes each vector's resolved element (also stored on Model.Elem).
	resolving := map[string]bool{}
	vectorElem := map[string]crdt.ElemT{}
	var (
		resolveToken      func(tok string) (crdt.ElemT, error)
		resolveFields     func(fs []parser.FieldSpec) ([]crdt.FieldT, error)
		resolveStruct     func(name string) (crdt.ElemT, error)
		resolveVectorElem func(name string) (crdt.ElemT, error)
		resolveAlias      func(name string) (crdt.ElemT, error)
	)

	resolveToken = func(tok string) (crdt.ElemT, error) {
		if base, member, ok := splitDotted(tok); ok {
			if member != "Elem" {
				return crdt.ElemT{}, fmt.Errorf("unknown type member %q in %q (only .Elem is supported)", member, tok)
			}
			return resolveVectorElem(base)
		}
		if nt, ok := numtype.Parse(tok); ok {
			return crdt.ElemT{Num: nt}, nil
		}
		if _, ok := structDefs[tok]; ok {
			return resolveStruct(tok)
		}
		if _, ok := aliasDefs[tok]; ok {
			return resolveAlias(tok)
		}
		if _, ok := vectorDefs[tok]; ok {
			return crdt.ElemT{}, fmt.Errorf("type %q is a vector and cannot be used as a field or element type", tok)
		}
		return crdt.ElemT{}, fmt.Errorf("unknown type %q", tok)
	}

	// resolveFields resolves a struct field list (numtype, struct/alias, or a
	// `V.Elem` field type), with dup-field detection.
	resolveFields = func(fs []parser.FieldSpec) ([]crdt.FieldT, error) {
		fseen := map[string]bool{}
		fields := make([]crdt.FieldT, 0, len(fs))
		for _, f := range fs {
			if fseen[f.Name] {
				return nil, fmt.Errorf("duplicate field %q", f.Name)
			}
			fseen[f.Name] = true
			ft, err := resolveToken(f.Type)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", f.Name, err)
			}
			fields = append(fields, crdt.FieldT{Name: f.Name, Type: ft})
		}
		return fields, nil
	}

	resolveStruct = func(name string) (crdt.ElemT, error) {
		if s, ok := reg.structs[name]; ok {
			return s, nil
		}
		if resolving[name] {
			return crdt.ElemT{}, fmt.Errorf("recursive struct type %q", name)
		}
		resolving[name] = true
		fields, err := resolveFields(structDefs[name].Fields)
		resolving[name] = false
		if err != nil {
			return crdt.ElemT{}, fmt.Errorf("struct %s: %w", name, err)
		}
		st := crdt.ElemT{Struct: true, Name: name, Fields: fields}
		reg.structs[name] = st
		return st, nil
	}

	// resolveVectorElem resolves the element of vector `name`: an inline struct body
	// (`vector { ... }`, nominal Name = the vector's) or an element token.
	resolveVectorElem = func(name string) (crdt.ElemT, error) {
		if e, ok := vectorElem[name]; ok {
			return e, nil
		}
		vd, ok := vectorDefs[name]
		if !ok {
			if _, isStruct := structDefs[name]; isStruct {
				return crdt.ElemT{}, fmt.Errorf("type %q is a struct, not a vector; `.Elem` requires a vector", name)
			}
			return crdt.ElemT{}, fmt.Errorf("type %q is not a vector; cannot take `.Elem`", name)
		}
		if resolving[name] {
			return crdt.ElemT{}, fmt.Errorf("recursive type %q", name)
		}
		resolving[name] = true
		var elem crdt.ElemT
		var err error
		if vd.Inner != nil {
			var fields []crdt.FieldT
			fields, err = resolveFields(vd.Inner.Fields)
			if err == nil {
				elem = crdt.ElemT{Struct: true, Name: name, Fields: fields}
			}
		} else {
			elem, err = resolveToken(vd.Elem)
		}
		resolving[name] = false
		if err != nil {
			return crdt.ElemT{}, fmt.Errorf("type %s: %w", name, err)
		}
		vectorElem[name] = elem
		return elem, nil
	}

	// resolveAlias resolves `type name = <ref>.Elem`. A struct element is also
	// exposed as a named struct type (reg.structs), renamed to the alias, so `a::name`
	// and swagger treat it nominally; a numeric element lives only in reg.aliases.
	resolveAlias = func(name string) (crdt.ElemT, error) {
		if a, ok := reg.aliases[name]; ok {
			return a, nil
		}
		if resolving[name] {
			return crdt.ElemT{}, fmt.Errorf("recursive type %q", name)
		}
		resolving[name] = true
		elem, err := resolveToken(aliasDefs[name].Elem)
		resolving[name] = false
		if err != nil {
			return crdt.ElemT{}, fmt.Errorf("type %s: %w", name, err)
		}
		if elem.Struct {
			elem.Name = name
			reg.structs[name] = elem
		}
		reg.aliases[name] = elem
		return elem, nil
	}

	for name := range structDefs {
		if _, err := resolveStruct(name); err != nil {
			return reg, err
		}
	}
	for name := range aliasDefs {
		if _, err := resolveAlias(name); err != nil {
			return reg, err
		}
	}
	for name := range vectorDefs {
		elem, err := resolveVectorElem(name)
		if err != nil {
			return reg, err
		}
		reg.models[name] = &Model{
			Name:    name,
			Elem:    elem,
			Queries: map[string]crdt.Method{},
			Updates: map[string]crdt.Method{},
		}
	}
	return reg, nil
}

// ---- validators ----------------------------------------------------

// validateFnParams validates a function's params: names unique and each type a
// known type (numeric or struct). A `fn` may take struct-typed params.
func validateFnParams(ps []parser.ParamSpec, reg typeReg) error {
	seen := make(map[string]bool, len(ps))
	for _, p := range ps {
		if _, err := reg.resolveToken(p.Type); err != nil {
			return fmt.Errorf("param %s: %w", p.Name, err)
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate param %s", p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

// resolveScalarParams validates and NORMALIZES update/query params: names unique
// and each type resolving to a numeric type (not a struct). Update/query params
// cross the wire via bindParams (scalar-only), and downstream (bindParams, prover,
// swagger) parses the stored token via numtype.Parse — so a `V.Elem`/alias token
// must be rewritten to its concrete numtype name here. The returned slice preserves
// order and names with Type set to the resolved numtype's canonical name.
func resolveScalarParams(ps []parser.ParamSpec, reg typeReg) ([]parser.ParamSpec, error) {
	seen := make(map[string]bool, len(ps))
	out := make([]parser.ParamSpec, len(ps))
	for i, p := range ps {
		et, err := reg.resolveToken(p.Type)
		if err != nil {
			return nil, fmt.Errorf("param %s: %w", p.Name, err)
		}
		if et.Struct {
			return nil, fmt.Errorf("param %s: type %q must be a numeric type (struct-typed params are not supported here)", p.Name, p.Type)
		}
		if seen[p.Name] {
			return nil, fmt.Errorf("duplicate param %s", p.Name)
		}
		seen[p.Name] = true
		out[i] = parser.ParamSpec{Name: p.Name, Type: et.Num.String()}
	}
	return out, nil
}

func paramSet(ps []parser.ParamSpec) map[string]bool {
	s := make(map[string]bool, len(ps))
	for _, p := range ps {
		s[p.Name] = true
	}
	return s
}
