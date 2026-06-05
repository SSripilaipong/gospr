package crdt

import (
	"fmt"
	"sync"
)

type GCounter struct {
	nodeID string
	counts map[string]int64
	mu     sync.Mutex
}

func newGCounter(nodeID string) *GCounter {
	return &GCounter{
		nodeID: nodeID,
		counts: make(map[string]int64),
	}
}

func (g *GCounter) Apply(action string, payload []any) error {
	if action != "Add" {
		return fmt.Errorf("unknown action: %s", action)
	}
	if len(payload) < 1 {
		return fmt.Errorf("Add requires one argument")
	}
	n, err := toInt64(payload[0])
	if err != nil {
		return err
	}
	g.mu.Lock()
	g.counts[g.nodeID] += n
	g.mu.Unlock()
	return nil
}

func (g *GCounter) Query(name string) (any, error) {
	if name != "Value" {
		return nil, fmt.Errorf("unknown query: %s", name)
	}
	g.mu.Lock()
	var sum int64
	for _, v := range g.counts {
		sum += v
	}
	g.mu.Unlock()
	return sum, nil
}

func (g *GCounter) Snapshot() any {
	g.mu.Lock()
	copy := make(map[string]int64, len(g.counts))
	for k, v := range g.counts {
		copy[k] = v
	}
	g.mu.Unlock()
	return copy
}

func (g *GCounter) Merge(snapshot any) error {
	remote, ok := snapshot.(map[string]int64)
	if !ok {
		return fmt.Errorf("invalid GCounter snapshot type")
	}
	g.mu.Lock()
	for k, v := range remote {
		if v > g.counts[k] {
			g.counts[k] = v
		}
	}
	g.mu.Unlock()
	return nil
}

func toInt64(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case float64:
		return int64(x), nil
	case int:
		return int64(x), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int64", v)
	}
}
