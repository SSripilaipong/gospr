package crdt

import (
	"fmt"
	"math/big"
	"strings"
	"sync"

	"gospr/numtype"
	"gospr/parser"
)

// maxEvalDepth bounds user-function recursion so a non-terminating definition
// errors out instead of hanging the process.
const maxEvalDepth = 10000

// Method is a validated query or update: its params plus a resolved expression
// body (a general value expression for queries — which may fold via reduce — or
// a Local for updates). Result is the value type the method yields (always rat
// for updates; rat/bool/string for queries) and drives serialization/swagger.
type Method struct {
	Params []parser.ParamSpec
	Body   parser.Expr
	Result parser.ValType
	// ResultNum is the numeric subtype of a query result (meaningful only when
	// Result == TypeReal); it names the domain/sign in the Swagger schema's
	// description (numeric fields are string-typed, exact rationals on the wire).
	ResultNum numtype.NumType
	// ResultStruct is non-nil when a query returns a whole struct value; it carries
	// the resolved struct descriptor so swagger can render an object schema. Nil
	// for scalar/bool/string results (described by Result/ResultNum).
	ResultStruct *ElemT
}

// Function is a resolved user-defined function: its params plus a resolved
// body term. Arity is len(Params).
type Function struct {
	Name   string
	Params []parser.ParamSpec
	Body   parser.Expr
}

// VectorCRDT is a distributed-systems vector: nodeID -> exact rational value
// (math/big.Rat). The user-defined merge/query/update expressions are evaluated
// against this state at runtime, in the context of the global function
// environment. Rationals are exact, so the runtime obeys the same algebraic laws
// the prover discharges (no float rounding to break convergence).
type VectorCRDT struct {
	nodeID  string
	elem    ElemT // resolved element descriptor (scalar or struct); drives zero-slot defaults and wire decoding
	state   map[string]rtVal
	merge   parser.Expr // a Zip expr (Fn resolved)
	queries map[string]Method
	updates map[string]Method
	funcs   map[string]Function
	mu      sync.Mutex
}

func NewVector(nodeID string, elem ElemT, merge parser.Expr, queries, updates map[string]Method, funcs map[string]Function) *VectorCRDT {
	return &VectorCRDT{
		nodeID:  nodeID,
		elem:    elem,
		state:   make(map[string]rtVal),
		merge:   merge,
		queries: queries,
		updates: updates,
		funcs:   funcs,
	}
}

// cloneRat returns a fresh copy of r. The Snapshot/Merge boundary must deep-copy
// because *big.Rat is mutable: sharing a pointer would let a caller (or a peer's
// snapshot) mutate a value aliased into another CRDT's state and corrupt it.
func cloneRat(r *big.Rat) *big.Rat { return new(big.Rat).Set(r) }

// cloneSlot deep-copies a slot value (scalar or struct). Like cloneRat it is used
// at the Snapshot/Merge/Apply boundaries so no mutable *big.Rat is aliased across
// CRDTs.
func cloneSlot(val rtVal) rtVal {
	if val.kind == kStruct {
		fields := make(map[string]rtVal, len(val.fields))
		for k, f := range val.fields {
			fields[k] = cloneSlot(f)
		}
		return structVal(fields)
	}
	return numVal(cloneRat(val.num))
}

// zeroSlot builds the default value for an absent slot: 0 for a scalar element,
// or a struct with every (nested) leaf set to 0.
func zeroSlot(t ElemT) rtVal {
	if t.Struct {
		fields := make(map[string]rtVal, len(t.Fields))
		for _, f := range t.Fields {
			fields[f.Name] = zeroSlot(f.Type)
		}
		return structVal(fields)
	}
	return numVal(new(big.Rat))
}

// Apply runs an update's `local <unary fn>` against the local node's slot.
func (v *VectorCRDT) Apply(action string, payload []any) error {
	m, ok := v.updates[action]
	if !ok {
		return fmt.Errorf("unknown action: %s", action)
	}
	if len(payload) != len(m.Params) {
		return fmt.Errorf("action %s expects %d params, got %d", action, len(m.Params), len(payload))
	}
	env, err := bindParams(m.Params, payload)
	if err != nil {
		return fmt.Errorf("action %s: %w", action, err)
	}
	if m.Body.Kind != parser.ExprLocal || m.Body.Fn == nil {
		return fmt.Errorf("action %s: body is not a local expr", action)
	}
	f, err := v.evalFuncVal(*m.Body.Fn, env, 1, 0)
	if err != nil {
		return fmt.Errorf("action %s: %w", action, err)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	cur, ok := v.state[v.nodeID]
	if !ok {
		cur = zeroSlot(v.elem) // absent slot defaults to 0 (scalar) or a zero struct
	}
	next, err := apply(f, []rtVal{cur})
	if err != nil {
		return fmt.Errorf("action %s: %w", action, err)
	}
	v.state[v.nodeID] = cloneSlot(next) // detach from any aliased operand
	return nil
}

// Query evaluates a query's body — a general value expression that may fold the
// vector via `reduce` — and returns the result (rat, bool, or string). The
// lock is held across eval because a `reduce` sub-expression reads v.state.
func (v *VectorCRDT) Query(name string, params []any) (any, error) {
	if err := v.ValidateQuery(name, params); err != nil {
		return nil, err
	}
	m := v.queries[name]
	// ValidateQuery already checked arity and each param's domain, so binding
	// here cannot fail; the resulting env is threaded into the body so params
	// are in scope during eval.
	env, err := bindParams(m.Params, params)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", name, err)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	val, err := v.eval(m.Body, env, 0)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", name, err)
	}
	return rtToAny(val)
}

// ValidateQuery is the non-evaluating prefix of Query: it checks the query
// exists, its param arity matches, and each param binds to its declared numeric
// type — but it does NOT evaluate the body. A linearized read calls this as a
// fast preflight so a malformed query is a 400 before any quorum work, and so a
// valid query is never spuriously failed against stale pre-sync local state
// (body evaluation is state-dependent: recursion is bounded by maxEvalDepth).
func (v *VectorCRDT) ValidateQuery(name string, params []any) error {
	m, ok := v.queries[name]
	if !ok {
		return fmt.Errorf("unknown query: %s", name)
	}
	if len(params) != len(m.Params) {
		return fmt.Errorf("query %s expects %d params, got %d", name, len(m.Params), len(params))
	}
	if _, err := bindParams(m.Params, params); err != nil {
		return fmt.Errorf("query %s: %w", name, err)
	}
	return nil
}

// Merge applies the `zip fn` over the union of node slots. The merged result
// is built in a copy and swapped in only after every slot succeeds, so a
// failing user-defined merge fn leaves state untouched.
func (v *VectorCRDT) Merge(snapshot any) error {
	remote, ok := snapshot.(map[string]rtVal)
	if !ok {
		return fmt.Errorf("invalid VectorCRDT snapshot type %T", snapshot)
	}
	return v.mergeRemote(remote)
}

// mergeRemote applies the `zip fn` over the union of local and remote slots and
// swaps the result in atomically (a failing user merge leaves state untouched).
// Shared by Merge (in-process gossip) and MergeWire (the sync protocol). Slot
// values are runtime values (scalar or struct); the zip fn is (E,E)->E.
func (v *VectorCRDT) mergeRemote(remote map[string]rtVal) error {
	if v.merge.Kind != parser.ExprZip || v.merge.Fn == nil {
		return fmt.Errorf("merge is not a zip expr")
	}
	fn, err := v.evalFuncVal(*v.merge.Fn, nil, 2, 0)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	// Deep-clone every value into the working copy: slot values hold mutable
	// *big.Rat, so sharing them would alias local state with the (possibly
	// still-live) remote snapshot or with merge-fn operands.
	merged := make(map[string]rtVal, len(v.state))
	for k, val := range v.state {
		merged[k] = cloneSlot(val)
	}
	for k, rv := range remote {
		if cur, ok := merged[k]; ok {
			nv, err := apply(fn, []rtVal{cur, cloneSlot(rv)})
			if err != nil {
				return err // state left untouched
			}
			merged[k] = cloneSlot(nv)
		} else {
			merged[k] = cloneSlot(rv) // slot absent locally: adopt a copy of remote
		}
	}
	v.state = merged
	return nil
}

func (v *VectorCRDT) Snapshot() any {
	v.mu.Lock()
	cp := make(map[string]rtVal, len(v.state))
	for k, val := range v.state {
		cp[k] = cloneSlot(val) // hand out copies so a caller cannot mutate our state
	}
	v.mu.Unlock()
	return cp
}

// SnapshotWire is the transport-safe analogue of Snapshot: it emits each slot as
// an exact-rational string (RatString), so nothing is lost and no *big.Rat is
// aliased across the wire (strings are immutable copies). Used by the sync
// (quorum) protocol; gossip keeps using Snapshot.
func (v *VectorCRDT) SnapshotWire() WireSnapshot {
	v.mu.Lock()
	slots := make(map[string]SlotWire, len(v.state))
	for k, val := range v.state {
		slots[k] = slotToWire(val)
	}
	v.mu.Unlock()
	return WireSnapshot{Slots: slots}
}

// slotToWire recursively encodes a runtime slot value as a transport SlotWire:
// scalars become exact-rational strings, structs become nested objects.
func slotToWire(val rtVal) SlotWire {
	if val.kind == kStruct {
		m := make(map[string]SlotWire, len(val.fields))
		for k, f := range val.fields {
			m[k] = slotToWire(f)
		}
		return SlotWire{Struct: m}
	}
	return SlotWire{Num: val.num.RatString()}
}

// MergeWire decodes each WireSnapshot slot against the element descriptor
// (rejecting a malformed or off-domain slot) and runs the same zip-merge path as
// Merge, so merge semantics stay identical to gossip. Used by the sync protocol.
func (v *VectorCRDT) MergeWire(snap WireSnapshot) error {
	remote := make(map[string]rtVal, len(snap.Slots))
	for k, s := range snap.Slots {
		rv, err := wireToSlot(s, v.elem)
		if err != nil {
			return fmt.Errorf("invalid wire snapshot slot %q: %w", k, err)
		}
		remote[k] = rv
	}
	return v.mergeRemote(remote)
}

// wireToSlot decodes a SlotWire into a runtime value against the expected element
// descriptor: exact field-set match for structs, numeric leaves parsed and
// validated against their leaf NumType. A shape or domain mismatch is rejected.
func wireToSlot(s SlotWire, t ElemT) (rtVal, error) {
	if t.Struct {
		if s.Struct == nil {
			return rtVal{}, fmt.Errorf("expected a struct value")
		}
		if len(s.Struct) != len(t.Fields) {
			return rtVal{}, fmt.Errorf("struct has %d fields, expected %d", len(s.Struct), len(t.Fields))
		}
		fields := make(map[string]rtVal, len(t.Fields))
		for _, f := range t.Fields {
			fs, ok := s.Struct[f.Name]
			if !ok {
				return rtVal{}, fmt.Errorf("missing field %q", f.Name)
			}
			fv, err := wireToSlot(fs, f.Type)
			if err != nil {
				return rtVal{}, fmt.Errorf("field %q: %w", f.Name, err)
			}
			fields[f.Name] = fv
		}
		return structVal(fields), nil
	}
	if s.Num == "" {
		return rtVal{}, fmt.Errorf("expected a scalar value")
	}
	q, ok := new(big.Rat).SetString(s.Num)
	if !ok {
		return rtVal{}, fmt.Errorf("invalid number %q", s.Num)
	}
	if !numtype.Allows(t.Num, q) {
		return rtVal{}, fmt.Errorf("value %s is not a valid %s", q.RatString(), t.Num)
	}
	return numVal(q), nil
}

// ---- expression evaluation -----------------------------------------

// rtKind tags a runtime value: a number, a string, a bool, or a function value
// awaiting `arity` more arguments (partial application makes this uniform).
type rtKind int

const (
	kNum rtKind = iota
	kStr
	kBool
	kStruct
	kFunc
)

// rtVal is a runtime value. Only the field(s) relevant to kind are set.
type rtVal struct {
	kind   rtKind
	num    *big.Rat
	str    string
	b      bool
	fields map[string]rtVal // kStruct
	arity  int
	call   func(args []rtVal) (rtVal, error)
}

func numVal(f *big.Rat) rtVal            { return rtVal{kind: kNum, num: f} }
func strVal(s string) rtVal              { return rtVal{kind: kStr, str: s} }
func boolVal(x bool) rtVal               { return rtVal{kind: kBool, b: x} }
func structVal(f map[string]rtVal) rtVal { return rtVal{kind: kStruct, fields: f} }

func (r rtVal) asNum() (*big.Rat, error) {
	if r.kind != kNum {
		return nil, fmt.Errorf("expected a numeric value")
	}
	return r.num, nil
}

// rtToAny converts a fully-evaluated (non-function) value to the JSON-encodable
// value a query returns. Numbers are emitted as their exact rational string
// ("5", "1/2") so no precision is lost at the JSON boundary.
func rtToAny(r rtVal) (any, error) {
	switch r.kind {
	case kNum:
		return r.num.RatString(), nil
	case kStr:
		return r.str, nil
	case kBool:
		return r.b, nil
	case kStruct:
		obj := make(map[string]any, len(r.fields))
		for k, f := range r.fields {
			fv, err := rtToAny(f)
			if err != nil {
				return nil, err
			}
			obj[k] = fv
		}
		return obj, nil
	default:
		return nil, fmt.Errorf("query did not produce a value")
	}
}

// evalFuncVal evaluates a function-valued term into an rtVal function of the
// wanted arity. Used for the zip (merge), local (update), and reduce slots, whose
// element operands may be scalar OR struct — so the function is applied via the
// generic rtVal calling convention rather than a scalar closure.
func (v *VectorCRDT) evalFuncVal(e parser.Expr, env map[string]rtVal, want, depth int) (rtVal, error) {
	val, err := v.eval(e, env, depth)
	if err != nil {
		return rtVal{}, err
	}
	if val.kind != kFunc || val.arity != want {
		got := 0
		if val.kind == kFunc {
			got = val.arity
		}
		return rtVal{}, fmt.Errorf("expected a function of %d argument(s), got one of %d", want, got)
	}
	return val, nil
}

// eval evaluates a resolved term. Literals produce values; Refs and partial
// applications produce function values carrying their remaining arity. A
// `reduce` node folds the vector and so requires the caller to hold v.mu.
func (v *VectorCRDT) eval(e parser.Expr, env map[string]rtVal, depth int) (rtVal, error) {
	if depth > maxEvalDepth {
		return rtVal{}, fmt.Errorf("evaluation depth exceeded (possible non-terminating recursion)")
	}
	switch e.Kind {
	case parser.ExprNumLit:
		return numVal(e.Num), nil
	case parser.ExprStrLit:
		return strVal(e.Str), nil
	case parser.ExprVar:
		val, ok := env[e.Name]
		if !ok {
			return rtVal{}, fmt.Errorf("unbound variable %q", e.Name)
		}
		return val, nil
	case parser.ExprRef:
		return v.refVal(e, depth)
	case parser.ExprApp:
		if e.Head == nil {
			return rtVal{}, fmt.Errorf("application has no head")
		}
		head, err := v.eval(*e.Head, env, depth)
		if err != nil {
			return rtVal{}, err
		}
		args := make([]rtVal, len(e.Args))
		for i, a := range e.Args {
			av, err := v.eval(*a, env, depth)
			if err != nil {
				return rtVal{}, err
			}
			if av.kind == kFunc {
				return rtVal{}, fmt.Errorf("cannot pass a function as an argument")
			}
			args[i] = av
		}
		return apply(head, args)
	case parser.ExprGuards:
		for _, gc := range e.Cases {
			if gc.Otherwise {
				return v.eval(*gc.Result, env, depth)
			}
			cv, err := v.eval(*gc.Cond, env, depth)
			if err != nil {
				return rtVal{}, err
			}
			if cv.kind != kBool {
				return rtVal{}, fmt.Errorf("guard condition is not a bool")
			}
			if cv.b {
				return v.eval(*gc.Result, env, depth)
			}
		}
		return rtVal{}, fmt.Errorf("non-exhaustive guards") // unreachable: build requires otherwise
	case parser.ExprStructLit:
		fields := make(map[string]rtVal, len(e.StructFields))
		for _, sf := range e.StructFields {
			if sf.Value == nil {
				return rtVal{}, fmt.Errorf("struct field %q has no value", sf.Name)
			}
			fv, err := v.eval(*sf.Value, env, depth)
			if err != nil {
				return rtVal{}, err
			}
			if fv.kind == kFunc {
				return rtVal{}, fmt.Errorf("struct field %q is not a value", sf.Name)
			}
			fields[sf.Name] = fv
		}
		return structVal(fields), nil
	case parser.ExprField:
		if e.Target == nil {
			return rtVal{}, fmt.Errorf("field access has no target")
		}
		tv, err := v.eval(*e.Target, env, depth)
		if err != nil {
			return rtVal{}, err
		}
		if tv.kind != kStruct {
			return rtVal{}, fmt.Errorf("cannot access field %q of a non-struct value", e.Field)
		}
		fv, ok := tv.fields[e.Field]
		if !ok {
			return rtVal{}, fmt.Errorf("struct has no field %q", e.Field)
		}
		return fv, nil
	case parser.ExprReduce:
		if e.Fn == nil || e.Init == nil {
			return rtVal{}, fmt.Errorf("malformed reduce")
		}
		fn, err := v.evalFuncVal(*e.Fn, env, 2, depth)
		if err != nil {
			return rtVal{}, err
		}
		acc, err := v.eval(*e.Init, env, depth) // empty vector -> returns the init value
		if err != nil {
			return rtVal{}, err
		}
		for _, val := range v.state {
			acc, err = apply(fn, []rtVal{acc, cloneSlot(val)})
			if err != nil {
				return rtVal{}, err
			}
		}
		return acc, nil
	default:
		return rtVal{}, fmt.Errorf("cannot evaluate expression of kind %d", e.Kind)
	}
}

// refVal turns a resolved Ref into a function value. Primitives wrap primOp;
// user functions bind their args and evaluate the body.
func (v *VectorCRDT) refVal(e parser.Expr, depth int) (rtVal, error) {
	switch e.Ref {
	case parser.RefPrimitive:
		op, err := primOp(e.Name)
		if err != nil {
			return rtVal{}, err
		}
		return rtVal{kind: kFunc, arity: 2, call: func(args []rtVal) (rtVal, error) {
			return op(args[0], args[1])
		}}, nil
	case parser.RefFunction:
		def, ok := v.funcs[e.Name]
		if !ok {
			return rtVal{}, fmt.Errorf("unknown function %q", e.Name)
		}
		return rtVal{kind: kFunc, arity: len(def.Params), call: func(args []rtVal) (rtVal, error) {
			callEnv := make(map[string]rtVal, len(def.Params))
			for i, p := range def.Params {
				callEnv[p.Name] = args[i]
			}
			return v.eval(def.Body, callEnv, depth+1)
		}}, nil
	default:
		return rtVal{}, fmt.Errorf("unknown reference kind for %q", e.Name)
	}
}

// apply applies a function value to args: saturated -> call, fewer ->
// partial application, more -> error.
func apply(f rtVal, args []rtVal) (rtVal, error) {
	if f.kind != kFunc {
		return rtVal{}, fmt.Errorf("cannot apply a non-function")
	}
	switch {
	case len(args) == f.arity:
		return f.call(args)
	case len(args) < f.arity:
		bound := append([]rtVal(nil), args...)
		remaining := f.arity - len(args)
		return rtVal{kind: kFunc, arity: remaining, call: func(rest []rtVal) (rtVal, error) {
			return f.call(append(append([]rtVal(nil), bound...), rest...))
		}}, nil
	default:
		return rtVal{}, fmt.Errorf("too many arguments: expected %d, got %d", f.arity, len(args))
	}
}

// primOp returns the binary applier for a primitive. Arithmetic yields an exact
// rational; comparisons yield a bool. Every arithmetic result is a fresh
// allocation, so operands are never mutated.
func primOp(op string) (func(a, b rtVal) (rtVal, error), error) {
	switch op {
	case "+":
		return arith(func(a, b *big.Rat) *big.Rat { return new(big.Rat).Add(a, b) }), nil
	case "*":
		return arith(func(a, b *big.Rat) *big.Rat { return new(big.Rat).Mul(a, b) }), nil
	case "-":
		return arith(func(a, b *big.Rat) *big.Rat { return new(big.Rat).Sub(a, b) }), nil
	case "max":
		return arith(func(a, b *big.Rat) *big.Rat {
			if a.Cmp(b) >= 0 {
				return cloneRat(a)
			}
			return cloneRat(b)
		}), nil
	case "min":
		return arith(func(a, b *big.Rat) *big.Rat {
			if a.Cmp(b) <= 0 {
				return cloneRat(a)
			}
			return cloneRat(b)
		}), nil
	case ">":
		return cmp(func(a, b *big.Rat) bool { return a.Cmp(b) > 0 }), nil
	case "<":
		return cmp(func(a, b *big.Rat) bool { return a.Cmp(b) < 0 }), nil
	case ">=":
		return cmp(func(a, b *big.Rat) bool { return a.Cmp(b) >= 0 }), nil
	case "<=":
		return cmp(func(a, b *big.Rat) bool { return a.Cmp(b) <= 0 }), nil
	case "==":
		return cmp(func(a, b *big.Rat) bool { return a.Cmp(b) == 0 }), nil
	case "/=":
		return cmp(func(a, b *big.Rat) bool { return a.Cmp(b) != 0 }), nil
	default:
		return nil, fmt.Errorf("unknown primitive %q", op)
	}
}

func arith(f func(a, b *big.Rat) *big.Rat) func(a, b rtVal) (rtVal, error) {
	return func(a, b rtVal) (rtVal, error) {
		x, err := a.asNum()
		if err != nil {
			return rtVal{}, err
		}
		y, err := b.asNum()
		if err != nil {
			return rtVal{}, err
		}
		return numVal(f(x, y)), nil
	}
}

func cmp(f func(a, b *big.Rat) bool) func(a, b rtVal) (rtVal, error) {
	return func(a, b rtVal) (rtVal, error) {
		x, err := a.asNum()
		if err != nil {
			return rtVal{}, err
		}
		y, err := b.asNum()
		if err != nil {
			return rtVal{}, err
		}
		return boolVal(f(x, y)), nil
	}
}

// bindParams binds method params to runtime values, validating each value
// against its declared numeric type (sign + integrality). An out-of-domain value
// — e.g. a negative increment on a `rat0+` counter — is rejected here, which the
// gateway surfaces as a 4xx.
func bindParams(specs []parser.ParamSpec, vals []any) (map[string]rtVal, error) {
	m := make(map[string]rtVal, len(specs))
	for i, p := range specs {
		f, err := toRat(vals[i])
		if err != nil {
			return nil, fmt.Errorf("param %s: %w", p.Name, err)
		}
		nt, ok := numtype.Parse(p.Type)
		if !ok {
			return nil, fmt.Errorf("param %s: unknown type %q", p.Name, p.Type)
		}
		if !numtype.Allows(nt, f) {
			return nil, fmt.Errorf("param %s: value %s is not a valid %s", p.Name, f.RatString(), p.Type)
		}
		m[p.Name] = numVal(f)
	}
	return m, nil
}

// toRat converts an inbound param value to an exact rational. Strings are the
// canonical wire form (parsed exactly, so "0.1" is 1/10 and "1/3" is exact);
// float64/int are accepted for in-process callers, with float64 being the only
// lossy path (it carries the IEEE value, since the original decimal is gone).
func toRat(v any) (*big.Rat, error) {
	switch x := v.(type) {
	case *big.Rat:
		return x, nil
	case string:
		q, ok := new(big.Rat).SetString(strings.TrimSpace(x))
		if !ok {
			return nil, fmt.Errorf("invalid number %q", x)
		}
		return q, nil
	case float64:
		q := new(big.Rat).SetFloat64(x)
		if q == nil {
			return nil, fmt.Errorf("value %v is not a finite number", x)
		}
		return q, nil
	case int64:
		return new(big.Rat).SetInt64(x), nil
	case int:
		return new(big.Rat).SetInt64(int64(x)), nil
	default:
		return nil, fmt.Errorf("cannot convert %T to a number", v)
	}
}
