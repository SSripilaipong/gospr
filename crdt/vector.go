package crdt

import (
	"fmt"
	"strconv"
	"sync"

	"gospr/parser"
)

// maxEvalDepth bounds user-function recursion so a non-terminating definition
// errors out instead of hanging the process.
const maxEvalDepth = 10000

// Method is a validated query or update: its params plus a resolved expression
// body (a general value expression for queries — which may fold via reduce — or
// a Local for updates). Result is the value type the method yields (always real
// for updates; real/bool/string for queries) and drives serialization/swagger.
type Method struct {
	Params []parser.ParamSpec
	Body   parser.Expr
	Result parser.ValType
}

// Function is a resolved user-defined function: its params plus a resolved
// body term. Arity is len(Params).
type Function struct {
	Name   string
	Params []parser.ParamSpec
	Body   parser.Expr
}

// VectorCRDT is a distributed-systems vector: nodeID -> real value. The
// user-defined merge/query/update expressions are evaluated against this
// state at runtime, in the context of the global function environment.
type VectorCRDT struct {
	nodeID  string
	state   map[string]float64
	merge   parser.Expr // a Zip expr (Fn resolved)
	queries map[string]Method
	updates map[string]Method
	funcs   map[string]Function
	mu      sync.Mutex
}

func NewVector(nodeID string, merge parser.Expr, queries, updates map[string]Method, funcs map[string]Function) *VectorCRDT {
	return &VectorCRDT{
		nodeID:  nodeID,
		state:   make(map[string]float64),
		merge:   merge,
		queries: queries,
		updates: updates,
		funcs:   funcs,
	}
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
	f, err := v.evalFn(*m.Body.Fn, env, 1)
	if err != nil {
		return fmt.Errorf("action %s: %w", action, err)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	next, err := f([]float64{v.state[v.nodeID]}) // absent slot defaults to 0
	if err != nil {
		return fmt.Errorf("action %s: %w", action, err)
	}
	v.state[v.nodeID] = next
	return nil
}

// Query evaluates a query's body — a general value expression that may fold the
// vector via `reduce` — and returns the result (real, bool, or string). The
// lock is held across eval because a `reduce` sub-expression reads v.state.
func (v *VectorCRDT) Query(name string, params []any) (any, error) {
	m, ok := v.queries[name]
	if !ok {
		return nil, fmt.Errorf("unknown query: %s", name)
	}
	if len(params) != len(m.Params) {
		return nil, fmt.Errorf("query %s expects %d params, got %d", name, len(m.Params), len(params))
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	val, err := v.eval(m.Body, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", name, err)
	}
	return rtToAny(val)
}

// Merge applies the `zip fn` over the union of node slots. The merged result
// is built in a copy and swapped in only after every slot succeeds, so a
// failing user-defined merge fn leaves state untouched.
func (v *VectorCRDT) Merge(snapshot any) error {
	remote, ok := snapshot.(map[string]float64)
	if !ok {
		return fmt.Errorf("invalid VectorCRDT snapshot type %T", snapshot)
	}
	if v.merge.Kind != parser.ExprZip || v.merge.Fn == nil {
		return fmt.Errorf("merge is not a zip expr")
	}
	fn, err := v.evalFn(*v.merge.Fn, nil, 2)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	merged := make(map[string]float64, len(v.state))
	for k, val := range v.state {
		merged[k] = val
	}
	for k, rv := range remote {
		if cur, ok := merged[k]; ok {
			nv, err := fn([]float64{cur, rv})
			if err != nil {
				return err // state left untouched
			}
			merged[k] = nv
		} else {
			merged[k] = rv // slot absent locally: adopt remote
		}
	}
	v.state = merged
	return nil
}

func (v *VectorCRDT) Snapshot() any {
	v.mu.Lock()
	cp := make(map[string]float64, len(v.state))
	for k, val := range v.state {
		cp[k] = val
	}
	v.mu.Unlock()
	return cp
}

// ---- expression evaluation -----------------------------------------

// rtKind tags a runtime value: a real, a string, a bool, or a function value
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
	num   float64
	str   string
	b     bool
	arity int
	call  func(args []rtVal) (rtVal, error)
}

func numVal(f float64) rtVal { return rtVal{kind: kNum, num: f} }
func strVal(s string) rtVal  { return rtVal{kind: kStr, str: s} }
func boolVal(x bool) rtVal   { return rtVal{kind: kBool, b: x} }

func (r rtVal) asNum() (float64, error) {
	if r.kind != kNum {
		return 0, fmt.Errorf("expected a real value")
	}
	return r.num, nil
}

// rtToAny converts a fully-evaluated (non-function) value to the JSON-encodable
// value a query returns.
func rtToAny(r rtVal) (any, error) {
	switch r.kind {
	case kNum:
		return r.num, nil
	case kStr:
		return r.str, nil
	case kBool:
		return r.b, nil
	default:
		return nil, fmt.Errorf("query did not produce a value")
	}
}

// evalFn evaluates a function-valued term into a Go closure of the wanted arity
// over reals. Used for the zip (merge), local (update), and reduce slots, all of
// which are real^want -> real.
func (v *VectorCRDT) evalFn(e parser.Expr, env map[string]rtVal, want int) (func([]float64) (float64, error), error) {
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
	return func(args []float64) (float64, error) {
		rv := make([]rtVal, len(args))
		for i, a := range args {
			rv[i] = numVal(a)
		}
		r, err := val.call(rv)
		if err != nil {
			return 0, err
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
		acc := e.Init.Num // empty vector -> returns init
		for _, val := range v.state {
			acc, err = fn([]float64{acc, val})
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

// primOp returns the binary applier for a primitive. Arithmetic yields a real;
// comparisons yield a bool.
func primOp(op string) (func(a, b rtVal) (rtVal, error), error) {
	switch op {
	case "+":
		return arith(func(a, b float64) float64 { return a + b }), nil
	case "*":
		return arith(func(a, b float64) float64 { return a * b }), nil
	case "-":
		return arith(func(a, b float64) float64 { return a - b }), nil
	case "max":
		return arith(func(a, b float64) float64 {
			if a > b {
				return a
			}
			return b
		}), nil
	case "min":
		return arith(func(a, b float64) float64 {
			if a < b {
				return a
			}
			return b
		}), nil
	case ">":
		return cmp(func(a, b float64) bool { return a > b }), nil
	case "<":
		return cmp(func(a, b float64) bool { return a < b }), nil
	case ">=":
		return cmp(func(a, b float64) bool { return a >= b }), nil
	case "<=":
		return cmp(func(a, b float64) bool { return a <= b }), nil
	case "==":
		return cmp(func(a, b float64) bool { return a == b }), nil
	case "/=":
		return cmp(func(a, b float64) bool { return a != b }), nil
	default:
		return nil, fmt.Errorf("unknown primitive %q", op)
	}
}

func arith(f func(a, b float64) float64) func(a, b rtVal) (rtVal, error) {
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

func cmp(f func(a, b float64) bool) func(a, b rtVal) (rtVal, error) {
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

// bindParams binds method params (real-only) to runtime values.
func bindParams(specs []parser.ParamSpec, vals []any) (map[string]rtVal, error) {
	m := make(map[string]rtVal, len(specs))
	for i, p := range specs {
		f, err := toFloat64(vals[i])
		if err != nil {
			return nil, fmt.Errorf("param %s: %w", p.Name, err)
		}
		m[p.Name] = numVal(f)
	}
	return m, nil
}

func toFloat64(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int64:
		return float64(x), nil
	case int:
		return float64(x), nil
	case string:
		return strconv.ParseFloat(x, 64)
	default:
		return 0, fmt.Errorf("cannot convert %T to float64", v)
	}
}
