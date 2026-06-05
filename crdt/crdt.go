package crdt

type CRDT interface {
	Apply(action string, payload []any) error
	Query(name string, params []any) (any, error)
	Merge(snapshot any) error
	Snapshot() any
}
