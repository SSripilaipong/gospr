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
// body (a Reduce for queries, a Local for updates).
type Method struct {
	Params []parser.ParamSpec
	Body   parser.Expr
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
	params, err := bindParams(m.Params, payload)
	if err != nil {
		return fmt.Errorf("action %s: %w", action, err)
	}
	if m.Body.Kind != parser.ExprLocal || m.Body.Fn == nil {
		return fmt.Errorf("action %s: body is not a local expr", action)
	}
	f, err := v.evalFn(*m.Body.Fn, params, 1)
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

// Query runs a `reduce fn init` fold over every slot in the vector.
func (v *VectorCRDT) Query(name string, params []any) (any, error) {
	m, ok := v.queries[name]
	if !ok {
		return nil, fmt.Errorf("unknown query: %s", name)
	}
	if len(params) != len(m.Params) {
		return nil, fmt.Errorf("query %s expects %d params, got %d", name, len(m.Params), len(params))
	}
	if m.Body.Kind != parser.ExprReduce || m.Body.Fn == nil || m.Body.Init == nil {
		return nil, fmt.Errorf("query %s: body is not a reduce expr", name)
	}
	fn, err := v.evalFn(*m.Body.Fn, nil, 2)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", name, err)
	}
	acc := m.Body.Init.Num // empty vector -> returns init
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, val := range v.state {
		acc, err = fn([]float64{acc, val})
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", name, err)
		}
	}
	return acc, nil
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

// rtVal is a runtime value: either a number or a function awaiting `arity`
// more arguments (partial application makes this uniform).
type rtVal struct {
	isFunc bool
	num    float64
	arity  int
	call   func(args []float64) (rtVal, error)
}

func numVal(f float64) rtVal { return rtVal{num: f} }

// evalFn evaluates a function-valued term into a Go closure of the wanted
// arity. Used for the zip/reduce/local combinator slots.
func (v *VectorCRDT) evalFn(e parser.Expr, env map[string]float64, want int) (func([]float64) (float64, error), error) {
	val, err := v.eval(e, env, 0)
	if err != nil {
		return nil, err
	}
	if !val.isFunc || val.arity != want {
		got := 0
		if val.isFunc {
			got = val.arity
		}
		return nil, fmt.Errorf("expected a function of %d argument(s), got one of %d", want, got)
	}
	return func(args []float64) (float64, error) {
		r, err := val.call(args)
		if err != nil {
			return 0, err
		}
		if r.isFunc {
			return 0, fmt.Errorf("function did not return a value")
		}
		return r.num, nil
	}, nil
}

// eval evaluates a resolved term. Number-typed leaves produce numVal; Refs and
// partial applications produce function values carrying their remaining arity.
func (v *VectorCRDT) eval(e parser.Expr, env map[string]float64, depth int) (rtVal, error) {
	if depth > maxEvalDepth {
		return rtVal{}, fmt.Errorf("evaluation depth exceeded (possible non-terminating recursion)")
	}
	switch e.Kind {
	case parser.ExprNumLit:
		return numVal(e.Num), nil
	case parser.ExprVar:
		val, ok := env[e.Name]
		if !ok {
			return rtVal{}, fmt.Errorf("unbound variable %q", e.Name)
		}
		return numVal(val), nil
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
		args := make([]float64, len(e.Args))
		for i, a := range e.Args {
			av, err := v.eval(*a, env, depth)
			if err != nil {
				return rtVal{}, err
			}
			if av.isFunc {
				return rtVal{}, fmt.Errorf("cannot pass a function as an argument")
			}
			args[i] = av.num
		}
		return apply(head, args)
	default:
		return rtVal{}, fmt.Errorf("cannot evaluate expression of kind %d", e.Kind)
	}
}

// refVal turns a resolved Ref into a function value. Primitives wrap binFn;
// user functions bind their args and evaluate the body.
func (v *VectorCRDT) refVal(e parser.Expr, depth int) (rtVal, error) {
	switch e.Ref {
	case parser.RefPrimitive:
		fn, err := binFn(e.Name)
		if err != nil {
			return rtVal{}, err
		}
		return rtVal{isFunc: true, arity: 2, call: func(args []float64) (rtVal, error) {
			return numVal(fn(args[0], args[1])), nil
		}}, nil
	case parser.RefFunction:
		def, ok := v.funcs[e.Name]
		if !ok {
			return rtVal{}, fmt.Errorf("unknown function %q", e.Name)
		}
		return rtVal{isFunc: true, arity: len(def.Params), call: func(args []float64) (rtVal, error) {
			callEnv := make(map[string]float64, len(def.Params))
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
func apply(f rtVal, args []float64) (rtVal, error) {
	if !f.isFunc {
		return rtVal{}, fmt.Errorf("cannot apply a non-function")
	}
	switch {
	case len(args) == f.arity:
		return f.call(args)
	case len(args) < f.arity:
		bound := append([]float64(nil), args...)
		remaining := f.arity - len(args)
		return rtVal{isFunc: true, arity: remaining, call: func(rest []float64) (rtVal, error) {
			return f.call(append(append([]float64(nil), bound...), rest...))
		}}, nil
	default:
		return rtVal{}, fmt.Errorf("too many arguments: expected %d, got %d", f.arity, len(args))
	}
}

func binFn(op string) (func(a, b float64) float64, error) {
	switch op {
	case "+":
		return func(a, b float64) float64 { return a + b }, nil
	case "*":
		return func(a, b float64) float64 { return a * b }, nil
	case "-":
		return func(a, b float64) float64 { return a - b }, nil
	case "max":
		return func(a, b float64) float64 {
			if a > b {
				return a
			}
			return b
		}, nil
	case "min":
		return func(a, b float64) float64 {
			if a < b {
				return a
			}
			return b
		}, nil
	default:
		return nil, fmt.Errorf("unknown function %q", op)
	}
}

func bindParams(specs []parser.ParamSpec, vals []any) (map[string]float64, error) {
	m := make(map[string]float64, len(specs))
	for i, p := range specs {
		f, err := toFloat64(vals[i])
		if err != nil {
			return nil, fmt.Errorf("param %s: %w", p.Name, err)
		}
		m[p.Name] = f
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
