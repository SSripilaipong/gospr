package crdt

import (
	"fmt"
	"strconv"
)

type CRDT interface {
	Apply(action string, payload []any) error
	Query(name string) (any, error)
	Merge(snapshot any) error
	Snapshot() any
}

func New(typeName string, args []string, nodeID string) (CRDT, error) {
	switch typeName {
	case "GCounter":
		var initial int64
		if len(args) > 0 {
			var err error
			initial, err = strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("GCounter: invalid initial value %q: %w", args[0], err)
			}
		}
		return newGCounter(nodeID, initial), nil
	default:
		return nil, fmt.Errorf("unknown CRDT type: %s", typeName)
	}
}
