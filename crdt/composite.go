package crdt

import (
	"fmt"
	"gospr/parser"
	"sync"
)

type CompositeCRDT struct {
	queries []parser.QueryDef
	updates []parser.UpdateDef
	fields  map[string]CRDT
	mu      sync.RWMutex
}

func NewComposite(def parser.TypeDef, queries []parser.QueryDef, updates []parser.UpdateDef, args []string, nodeID string) (*CompositeCRDT, error) {
	if len(args) != len(def.Params) {
		return nil, fmt.Errorf("type %s expects %d args, got %d", def.Name, len(def.Params), len(args))
	}
	params := make(map[string]string, len(def.Params))
	for i, p := range def.Params {
		params[p.Name] = args[i]
	}
	fields := make(map[string]CRDT, len(def.Fields))
	for _, f := range def.Fields {
		resolved := make([]string, len(f.Args))
		for i, a := range f.Args {
			if v, ok := params[a]; ok {
				resolved[i] = v
			} else {
				resolved[i] = a
			}
		}
		c, err := New(f.CRDTType, resolved, nodeID)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", f.Name, err)
		}
		fields[f.Name] = c
	}
	return &CompositeCRDT{queries: queries, updates: updates, fields: fields}, nil
}

func (c *CompositeCRDT) Apply(action string, payload []any) error {
	var ud *parser.UpdateDef
	for i := range c.updates {
		if c.updates[i].MethodName == action {
			ud = &c.updates[i]
			break
		}
	}
	if ud == nil {
		return fmt.Errorf("unknown action: %s", action)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, fu := range ud.Body {
		field, ok := c.fields[fu.Field]
		if !ok {
			return fmt.Errorf("unknown field: %s", fu.Field)
		}
		args := make([]any, len(fu.Call.Args))
		for i, a := range fu.Call.Args {
			args[i] = a
		}
		if err := field.Apply(fu.Call.Method, args); err != nil {
			return fmt.Errorf("field %s: %w", fu.Field, err)
		}
	}
	return nil
}

func (c *CompositeCRDT) Query(name string) (any, error) {
	var qd *parser.QueryDef
	for i := range c.queries {
		if c.queries[i].MethodName == name {
			qd = &c.queries[i]
			break
		}
	}
	if qd == nil {
		return nil, fmt.Errorf("unknown query: %s", name)
	}
	c.mu.RLock()
	field, ok := c.fields[qd.Body.Field]
	c.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown field: %s", qd.Body.Field)
	}
	return field.Query(qd.Body.Method)
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
