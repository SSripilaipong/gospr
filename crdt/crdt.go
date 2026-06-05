package crdt

import "fmt"

type CRDT interface {
	Apply(action string, payload []any) error
	Query(name string) (any, error)
	Merge(snapshot any) error
	Snapshot() any
}

func New(typeName string, args []string, nodeID string) (CRDT, error) {
	switch typeName {
	case "GCounter":
		return newGCounter(nodeID), nil
	default:
		return nil, fmt.Errorf("unknown CRDT type: %s", typeName)
	}
}
