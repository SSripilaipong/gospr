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
	state   map[string]*big.Rat
	merge   parser.Expr // a Zip expr (Fn resolved)
	queries map[string]Method
	updates map[string]Method
	funcs   map[string]Function
	mu      sync.Mutex
}

func NewVector(nodeID string, merge parser.Expr, queries, updates map[string]Method, funcs map[string]Function) *VectorCRDT {
	return &VectorCRDT{
		nodeID:  nodeID,
		state:   make(map[string]*big.Rat),
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
	f, err := v.evalFn(*m.Body.Fn, env, 1)
	if err != nil {
		return fmt.Errorf("action %s: %w", action, err)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	cur := v.state[v.nodeID]
	if cur == nil {
		cur = new(big.Rat) // absent slot defaults to 0
	}
	next, err := f([]*big.Rat{cur})
	if err != nil {
		return fmt.Errorf("action %s: %w", action, err)
	}
	v.state[v.nodeID] = cloneRat(next) // detach from any aliased operand
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
	v.mu.Lock()
	defer v.mu.Unlock()
	val, err := v.eval(m.Body, nil, 0)
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
	remote, ok := snapshot.(map[string]*big.Rat)
	if !ok {
		return fmt.Errorf("invalid VectorCRDT snapshot type %T", snapshot)
	}
	return v.mergeRemote(remote)
}

// mergeRemote applies the `zip fn` over the union of local and remote slots and
// swaps the result in atomically (a failing user merge leaves state untouched).
// Shared by Merge (in-process gossip) and MergeWire (the sync protocol).
func (v *VectorCRDT) mergeRemote(remote map[string]*big.Rat) error {
	if v.merge.Kind != parser.ExprZip || v.merge.Fn == nil {
		return fmt.Errorf("merge is not a zip expr")
	}
	fn, err := v.evalFn(*v.merge.Fn, nil, 2)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	// Deep-clone every value into the working copy: *big.Rat is mutable, so
	// sharing pointers would alias local state with the (possibly still-live)
	// remote snapshot or with merge-fn operands.
	merged := make(map[string]*big.Rat, len(v.state))
	for k, val := range v.state {
		merged[k] = cloneRat(val)
	}
	for k, rv := range remote {
		if cur, ok := merged[k]; ok {
			nv, err := fn([]*big.Rat{cur, rv})
			if err != nil {
				return err // state left untouched
			}
			merged[k] = cloneRat(nv)
		} else {
			merged[k] = cloneRat(rv) // slot absent locally: adopt a copy of remote
		}
	}
	v.state = merged
	return nil
}

func (v *VectorCRDT) Snapshot() any {
	v.mu.Lock()
	cp := make(map[string]*big.Rat, len(v.state))
	for k, val := range v.state {
		cp[k] = cloneRat(val) // hand out copies so a caller cannot mutate our state
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
	slots := make(map[string]string, len(v.state))
	for k, val := range v.state {
		slots[k] = val.RatString()
	}
	v.mu.Unlock()
	return WireSnapshot{Slots: slots}
}

// MergeWire parses each WireSnapshot slot into a fresh *big.Rat (rejecting
// malformed) and runs the same zip-merge path as Merge, so merge semantics stay
// identical to gossip. Used by the sync (quorum) protocol.
func (v *VectorCRDT) MergeWire(snap WireSnapshot) error {
	remote := make(map[string]*big.Rat, len(snap.Slots))
	for k, s := range snap.Slots {
		q, ok := new(big.Rat).SetString(s)
		if !ok {
			return fmt.Errorf("invalid wire snapshot slot %q: %q", k, s)
		}
		remote[k] = q
	}
	return v.mergeRemote(remote)
}

// ---- expression evaluation -----------------------------------------

// rtKind tags a runtime value: a number, a string, a bool, or a function value
// awaiting `arity` more arguments (partial application makes this uniform).
type rtKind int

const (
	kNum rtKind = iota
	kStr
	kBool
	kFunc
)

// rtVal is a runtime value. Only the field(s) relevant to kind are set.
type rtVal struct {
	kind  rtKind
	num   *big.Rat
	str   string
	b     bool
	arity int
	call  func(args []rtVal) (rtVal, error)
}

func numVal(f *big.Rat) rtVal { return rtVal{kind: kNum, num: f} }
func strVal(s string) rtVal   { return rtVal{kind: kStr, str: s} }
func boolVal(x bool) rtVal    { return rtVal{kind: kBool, b: x} }

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
	default:
		return nil, fmt.Errorf("query did not produce a value")
	}
}

// evalFn evaluates a function-valued term into a Go closure of the wanted arity
// over rationals. Used for the zip (merge), local (update), and reduce slots, all
// of which are rat^want -> rat.
func (v *VectorCRDT) evalFn(e parser.Expr, env map[string]rtVal, want int) (func([]*big.Rat) (*big.Rat, error), error) {
	val, err := v.eval(e, env, 0)
	if err != nil {
		return nil, err
	}
	if val.kind != kFunc || val.arity != want {
		got := 0
		if val.kind == kFunc {
			got = val.arity
		}
		return nil, fmt.Errorf("expected a function of %d argument(s), got one of %d", want, got)
	}
	return func(args []*big.Rat) (*big.Rat, error) {
		rv := make([]rtVal, len(args))
		for i, a := range args {
			rv[i] = numVal(a)
		}
		r, err := val.call(rv)
		if err != nil {
			return nil, err
		}
		return r.asNum()
	}, nil
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
	case parser.ExprReduce:
		if e.Fn == nil || e.Init == nil {
			return rtVal{}, fmt.Errorf("malformed reduce")
		}
		fn, err := v.evalFn(*e.Fn, env, 2)
		if err != nil {
			return rtVal{}, err
		}
		acc := cloneRat(e.Init.Num) // empty vector -> returns init (copy, never mutate the AST literal)
		for _, val := range v.state {
			acc, err = fn([]*big.Rat{acc, val})
			if err != nil {
				return rtVal{}, err
			}
		}
		return numVal(acc), nil
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
