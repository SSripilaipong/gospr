package crdt

import (
	"fmt"
	"gospr/parser"
	"sync"
)

type CompositeCRDT struct {
	fields  map[string]CRDT
	queries map[string]parser.QuerySpec
	updates map[string]parser.UpdateSpec
	mu      sync.RWMutex
}

func NewComposite(
	nodeID string,
	fieldFactories map[string]func(string) CRDT,
	queries map[string]parser.QuerySpec,
	updates map[string]parser.UpdateSpec,
) *CompositeCRDT {
	fields := make(map[string]CRDT, len(fieldFactories))
	for name, factory := range fieldFactories {
		fields[name] = factory(nodeID)
	}
	return &CompositeCRDT{fields: fields, queries: queries, updates: updates}
}

func (c *CompositeCRDT) Apply(action string, payload []any) error {
	spec, ok := c.updates[action]
	if !ok {
		return fmt.Errorf("unknown action: %s", action)
	}
	if len(payload) != len(spec.Params) {
		return fmt.Errorf("action %s expects %d params, got %d", action, len(spec.Params), len(payload))
	}
	paramMap, err := buildParamMap(spec.Params, payload)
	if err != nil {
		return fmt.Errorf("action %s: %w", action, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, fu := range spec.Body {
		field, ok := c.fields[fu.Field]
		if !ok {
			return fmt.Errorf("unknown field: %s", fu.Field)
		}
		args := resolveArgs(fu.Call.Args, paramMap)
		if err := field.Apply(fu.Call.Method, args); err != nil {
			return fmt.Errorf("field %s: %w", fu.Field, err)
		}
	}
	return nil
}

func (c *CompositeCRDT) Query(name string, params []any) (any, error) {
	spec, ok := c.queries[name]
	if !ok {
		return nil, fmt.Errorf("unknown query: %s", name)
	}
	if len(params) != len(spec.Params) {
		return nil, fmt.Errorf("query %s expects %d params, got %d", name, len(spec.Params), len(params))
	}
	paramMap, err := buildParamMap(spec.Params, params)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", name, err)
	}
	c.mu.RLock()
	field, ok := c.fields[spec.Body.Field]
	c.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown field: %s", spec.Body.Field)
	}
	return field.Query(spec.Body.Method, resolveArgs(spec.Body.Args, paramMap))
}

func (c *CompositeCRDT) Snapshot() any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snap := make(map[string]any, len(c.fields))
	for name, f := range c.fields {
		snap[name] = f.Snapshot()
	}
	return snap
}

func (c *CompositeCRDT) Merge(snapshot any) error {
	snap, ok := snapshot.(map[string]any)
	if !ok {
		return fmt.Errorf("invalid CompositeCRDT snapshot type")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, f := range c.fields {
		s, ok := snap[name]
		if !ok {
			continue
		}
		if err := f.Merge(s); err != nil {
			return fmt.Errorf("field %s: %w", name, err)
		}
	}
	return nil
}

func buildParamMap(specs []parser.ParamSpec, values []any) (map[string]any, error) {
	m := make(map[string]any, len(specs))
	for i, p := range specs {
		v, err := validateParam(values[i], p.Type)
		if err != nil {
			return nil, fmt.Errorf("param %s: %w", p.Name, err)
		}
		m[p.Name] = v
	}
	return m, nil
}

func validateParam(v any, typeName string) (any, error) {
	switch typeName {
	case "real0+":
		f, err := toFloat64(v)
		if err != nil {
			return nil, fmt.Errorf("expected real0+ (non-negative real), got %v: %w", v, err)
		}
		if f < 0 {
			return nil, fmt.Errorf("real0+ requires value >= 0, got %v", f)
		}
		return f, nil
	default:
		return v, nil
	}
}

func resolveArgs(args []string, paramMap map[string]any) []any {
	resolved := make([]any, len(args))
	for i, a := range args {
		if val, ok := paramMap[a]; ok {
			resolved[i] = val
		} else {
			resolved[i] = a
		}
	}
	return resolved
}
