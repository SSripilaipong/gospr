package crdt

import (
	"fmt"
	"strconv"
	"sync"

	"gospr/parser"
)

// Method is a validated query or update: its params plus an evaluable
// expression body (a Reduce for queries, a Local for updates).
type Method struct {
	Params []parser.ParamSpec
	Body   parser.Expr
}

// VectorCRDT is a distributed-systems vector: nodeID -> real value. The
// user-defined merge/query/update expressions are evaluated against this
// state at runtime.
type VectorCRDT struct {
	nodeID  string
	state   map[string]float64
	merge   parser.Expr // a Zip expr
	queries map[string]Method
	updates map[string]Method
	mu      sync.Mutex
}

func NewVector(nodeID string, merge parser.Expr, queries, updates map[string]Method) *VectorCRDT {
	return &VectorCRDT{
		nodeID:  nodeID,
		state:   make(map[string]float64),
		merge:   merge,
		queries: queries,
		updates: updates,
	}
}

// Apply runs an update's `local (section)` against the local node's slot.
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
	f, err := evalSection(*m.Body.Fn, params)
	if err != nil {
		return fmt.Errorf("action %s: %w", action, err)
	}
	v.mu.Lock()
	v.state[v.nodeID] = f(v.state[v.nodeID]) // absent slot defaults to 0
	v.mu.Unlock()
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
	fn, err := binFn(m.Body.Fn.Op)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", name, err)
	}
	acc := m.Body.Init.Num // empty vector -> returns init
	v.mu.Lock()
	for _, val := range v.state {
		acc = fn(acc, val)
	}
	v.mu.Unlock()
	return acc, nil
}

// Merge applies the `zip fn` over the union of node slots.
func (v *VectorCRDT) Merge(snapshot any) error {
	remote, ok := snapshot.(map[string]float64)
	if !ok {
		return fmt.Errorf("invalid VectorCRDT snapshot type %T", snapshot)
	}
	if v.merge.Kind != parser.ExprZip || v.merge.Fn == nil {
		return fmt.Errorf("merge is not a zip expr")
	}
	fn, err := binFn(v.merge.Fn.Op)
	if err != nil {
		return err
	}
	v.mu.Lock()
	for k, rv := range remote {
		if cur, ok := v.state[k]; ok {
			v.state[k] = fn(cur, rv)
		} else {
			v.state[k] = rv // slot absent locally: adopt remote
		}
	}
	v.mu.Unlock()
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

// evalSection turns a Section `(op arg)` into the unary fn \x -> x op arg.
func evalSection(e parser.Expr, params map[string]float64) (func(x float64) float64, error) {
	if e.Kind != parser.ExprSection || e.Arg == nil {
		return nil, fmt.Errorf("expected a section")
	}
	fn, err := binFn(e.Op)
	if err != nil {
		return nil, err
	}
	arg, err := evalOperand(*e.Arg, params)
	if err != nil {
		return nil, err
	}
	return func(x float64) float64 { return fn(x, arg) }, nil
}

func evalOperand(e parser.Expr, params map[string]float64) (float64, error) {
	switch e.Kind {
	case parser.ExprNumLit:
		return e.Num, nil
	case parser.ExprParamRef:
		v, ok := params[e.Param]
		if !ok {
			return 0, fmt.Errorf("unbound param %q", e.Param)
		}
		return v, nil
	default:
		return 0, fmt.Errorf("operand must be a number or param ref")
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
