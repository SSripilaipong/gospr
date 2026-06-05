package crdt

import (
	"fmt"
	"gospr/parser"
	"sync"
)

type CompositeCRDT struct {
	fields      map[string]CRDT
	queryIndex  map[string]parser.MethodCall
	updateIndex map[string][]parser.FieldUpdate
	mu          sync.RWMutex
}

func NewComposite(
	nodeID string,
	fieldFactories map[string]func(string) CRDT,
	queryIndex map[string]parser.MethodCall,
	updateIndex map[string][]parser.FieldUpdate,
) *CompositeCRDT {
	fields := make(map[string]CRDT, len(fieldFactories))
	for name, factory := range fieldFactories {
		fields[name] = factory(nodeID)
	}
	return &CompositeCRDT{fields: fields, queryIndex: queryIndex, updateIndex: updateIndex}
}

func (c *CompositeCRDT) Apply(action string, payload []any) error {
	body, ok := c.updateIndex[action]
	if !ok {
		return fmt.Errorf("unknown action: %s", action)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, fu := range body {
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

func (c *CompositeCRDT) Query(name string, params []any) (any, error) {
	body, ok := c.queryIndex[name]
	if !ok {
		return nil, fmt.Errorf("unknown query: %s", name)
	}
	c.mu.RLock()
	field, ok := c.fields[body.Field]
	c.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown field: %s", body.Field)
	}
	return field.Query(body.Method, params)
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
