package builder

import (
	"fmt"
	"math/big"
	"strings"

	"gospr/crdt"
	"gospr/numtype"
	"gospr/parser"
)

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
	vkVector // a whole-vector value (folded by `reduce`, supplied by `write`/query)
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
	elem   *vtype   // vkVector element type (nil otherwise)
}

// vVector wraps an element type as a whole-vector value type. A vector is never a
// storable leaf (not a struct field / slot value) — it exists only as a value
// folded by `reduce` and supplied by `write`/query at a combinator boundary.
func vVector(elem vtype) vtype { return vtype{kind: vkVector, elem: &elem} }

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
	if t.Str {
		return vString
	}
	return vNum(t.Num)
}

// elemTOf lowers a storable checker type to the shared element descriptor.
// Unsupported kinds indicate a broken builder invariant and are returned as
// errors instead of being silently converted to a numeric type.
func elemTOf(t vtype) (crdt.ElemT, error) {
	switch t.kind {
	case vkStruct:
		fields := make([]crdt.FieldT, len(t.fields))
		for i, f := range t.fields {
			ft, err := elemTOf(f.typ)
			if err != nil {
				return crdt.ElemT{}, fmt.Errorf("field %s: %w", f.name, err)
			}
			fields[i] = crdt.FieldT{Name: f.name, Type: ft}
		}
		return crdt.ElemT{Struct: true, Name: t.name, Fields: fields}, nil
	case vkString:
		return crdt.ElemT{Str: true}, nil
	case vkNum:
		return crdt.ElemT{Num: t.num}, nil
	default:
		return crdt.ElemT{}, fmt.Errorf("internal: cannot lower %s as a storable element", t)
	}
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
	case vkVector:
		if t.elem == nil {
			return "vector"
		}
		return "vector " + t.elem.String()
	default:
		return "unknown"
	}
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
	if t.kind == vkVector && t.elem != nil {
		return containsUnknown(*t.elem)
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
	if a.kind == vkVector && b.kind == vkVector {
		if a.elem == nil || b.elem == nil {
			return false
		}
		return subVtype(*a.elem, *b.elem)
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

// resolveTokenVtype resolves a param/return type token to a checker vtype. Unlike
// the leaf-only reg.resolveToken (which rejects vector names, since a vector is not
// a storable slot leaf), this recognizes a bare vector type name and yields a
// vkVector value type — so a fn param or `-> type` may be the whole vector `X`
// (folded by `reduce`, supplied by `write`/query). A dotted `V.Elem` still resolves
// to the element via resolveToken. Leaf positions (struct fields, vector elements,
// wire params) keep calling reg.resolveToken directly and stay vector-rejecting.
func (c *checker) resolveTokenVtype(tok string) (vtype, error) {
	if c.reg.isVector(tok) {
		return vVector(vElem(c.reg.models[tok].Elem)), nil
	}
	et, err := c.reg.resolveToken(tok)
	if err != nil {
		return vUnknown, err
	}
	return vElem(et), nil
}

// paramVtype resolves a previously validated param token. A failure means an
// earlier builder phase did not uphold its contract, so preserve it as an
// explicit internal error rather than silently widening the parameter to rat.
func (c *checker) paramVtype(tok string) (vtype, error) {
	t, err := c.resolveTokenVtype(tok)
	if err != nil {
		return vUnknown, fmt.Errorf("internal: parameter type %q failed after validation: %w", tok, err)
	}
	return t, nil
}

// resolveResultType resolves a `-> type` return-annotation token to a vtype.
// Unlike paramVtype, it errors on an unknown token (a bad annotation is a build
// error). bool/string are recognized here — a return type may be non-numeric —
// whereas param types never are; `type bool`/`type string` are reserved (see
// resolveTypes) so these keywords are unambiguous. A vector type name yields a
// vkVector (a fn may return the whole vector).
func (c *checker) resolveResultType(tok string) (vtype, error) {
	switch tok {
	case "bool":
		return vBool, nil
	case "string":
		return vString, nil
	}
	return c.resolveTokenVtype(tok)
}

// register records a resolved function body, its param scope, and its optional
// resolved return annotation. Each param is bound to its declared type (numeric
// or struct).
func (c *checker) register(name string, params []parser.ParamSpec, body parser.Expr, retType string) error {
	pts := make([]vtype, len(params))
	scope := make(map[string]vtype, len(params))
	for i, p := range params {
		t, err := c.paramVtype(p.Type)
		if err != nil {
			return fmt.Errorf("param %s: %w", p.Name, err)
		}
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
func (c *checker) paramScope(ps []parser.ParamSpec) (map[string]vtype, error) {
	s := make(map[string]vtype, len(ps))
	for _, p := range ps {
		t, err := c.paramVtype(p.Type)
		if err != nil {
			return nil, fmt.Errorf("param %s: %w", p.Name, err)
		}
		s[p.Name] = t
	}
	return s, nil
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

// checkQueryFn type-checks a query body, which is a function of the whole vector.
// After the declared params are bound (in scope), the body must resolve to a
// function still expecting exactly one vector-typed argument (X -> result) — the
// runtime supplies the vector. It returns the applied result type, which must be a
// concrete, serializable value (not a function, not a vector, no unresolved leaf).
func (c *checker) checkQueryFn(e parser.Expr, scope map[string]vtype, elem crdt.ElemT) (vtype, error) {
	t, err := c.typeOf(e, scope)
	if err != nil {
		return vUnknown, err
	}
	if !isFunc(t) {
		return vUnknown, fmt.Errorf("a query body must be a function of the vector (X -> result); got a bare value %s", t)
	}
	if len(t.fn.params) != 1 {
		return vUnknown, fmt.Errorf("a query body must expect exactly one (vector) argument, but expects %d", len(t.fn.params))
	}
	vecArg := vVector(vElem(elem))
	if t.fn.params[0].kind != vkVector {
		return vUnknown, fmt.Errorf("a query body's remaining argument must be the vector %s, got %s", vecArg, t.fn.params[0])
	}
	if !subVtype(vecArg, t.fn.params[0]) {
		return vUnknown, fmt.Errorf("query vector argument mismatch: fold expects %s, vector is %s", t.fn.params[0], vecArg)
	}
	res, err := c.applyArgs(*t.fn, []vtype{vecArg})
	if err != nil {
		return vUnknown, err
	}
	if isFunc(res) {
		return vUnknown, fmt.Errorf("query result is still a function missing %d argument(s)", len(res.fn.params))
	}
	if res.kind == vkVector {
		return vUnknown, fmt.Errorf("a query may not return a whole vector (it is not serializable)")
	}
	if containsVector(res) {
		return vUnknown, fmt.Errorf("a query result may not contain a vector (it is not serializable)")
	}
	if containsUnknown(res) {
		return vUnknown, fmt.Errorf("cannot determine query result type")
	}
	return res, nil
}

// containsVector reports whether t is a vector or (recursively) a struct with a
// vector-typed field — the serialization gate for query results (result lowering
// knows only num/string/bool/struct).
func containsVector(t vtype) bool {
	if t.kind == vkVector {
		return true
	}
	if t.kind == vkStruct {
		for _, f := range t.fields {
			if containsVector(f.typ) {
				return true
			}
		}
	}
	return false
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
	// zip/local apply the fn to element-typed slot(s); write applies it to the WHOLE
	// vector (X -> element). Either way the result must be assignable to E — this is
	// where non-negativity is enforced (an element type is never a vector, so a
	// write result stays a storable leaf).
	args := make([]vtype, len(t.fn.params))
	for i := range args {
		if comb.Kind == parser.ExprWrite {
			args[i] = vVector(elemV)
		} else {
			args[i] = elemV
		}
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
		// A comparison operand may be numeric OR string (its param is typed numTop
		// only as a placeholder); the operand-kind and no-mixed rules live here, not
		// in the fixed signature — resultOf then yields bool. Arithmetic stays
		// strictly numeric via subVtype.
		if cmpOps[f.op] {
			if a.kind != vkNum && a.kind != vkString && a.kind != vkUnknown {
				return vtype{}, fmt.Errorf("comparison operand %d must be numeric or string, got %s", i+1, a)
			}
		} else if !subVtype(a, f.params[i]) {
			return vtype{}, fmt.Errorf("argument %d: expected %s, got %s", i+1, f.params[i], a)
		}
	}
	bound := append(append([]vtype(nil), f.bound...), args...)
	remaining := f.params[len(args):]
	if len(remaining) == 0 {
		if cmpOps[f.op] {
			if err := checkCmpOperands(bound); err != nil {
				return vtype{}, err
			}
		}
		return resultOf(f.op, f.result, bound), nil
	}
	return mkFunc(fnType{params: remaining, result: f.result, op: f.op, bound: bound}), nil
}

// checkCmpOperands rejects a comparison over mismatched operand kinds (numeric vs
// string). Both operands must be the same value kind; an unknown operand (mid-
// inference recursion) defers. This runs only once a comparison saturates, so a
// partial `(> a.value)` is checked when its second operand arrives.
func checkCmpOperands(operands []vtype) error {
	if len(operands) != 2 {
		return nil
	}
	l, r := operands[0], operands[1]
	if l.kind == vkUnknown || r.kind == vkUnknown {
		return nil
	}
	if l.kind != r.kind {
		return fmt.Errorf("comparison operands must have the same type, got %s and %s", l, r)
	}
	return nil
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

// typeOfReduce types a `reduce <binary fn> <init> <vec>`. The fold element type
// comes from the explicit vector argument (not implicit state), and the result is
// the least fixpoint of folding the function over (accumulator, element) from the
// init literal's type — bounded because the lattice has finite height.
func (c *checker) typeOfReduce(e parser.Expr, scope map[string]vtype) (vtype, error) {
	if e.Fn == nil || e.Init == nil || e.Vec == nil {
		return vtype{}, fmt.Errorf("malformed reduce")
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
	vt, err := c.typeOf(*e.Vec, scope)
	if err != nil {
		return vtype{}, err
	}
	if vt.kind != vkVector || vt.elem == nil {
		return vtype{}, fmt.Errorf("reduce needs a vector to fold, got %s", vt)
	}
	elemV := *vt.elem // fold element type comes from the vector argument
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
	case vkVector:
		if a.elem == nil || b.elem == nil {
			return a.elem == b.elem
		}
		return vtypeEqual(*a.elem, *b.elem)
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
