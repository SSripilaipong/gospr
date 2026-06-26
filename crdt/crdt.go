package crdt

// WireSnapshot is the transport-safe form of a collection's state: nodeID -> an
// exact-rational string (the same canonical form used at the HTTP boundary). It
// is a named struct, not a bare map, so a richer slot encoding or a Kind/Version
// tag can be added later (struct vectors are a planned feature) without changing
// every sync DTO signature that carries it.
type WireSnapshot struct {
	Slots map[string]string `json:"slots"` // nodeID -> exact-rational string
}

type CRDT interface {
	Apply(action string, payload []any) error
	Query(name string, params []any) (any, error)
	Merge(snapshot any) error
	Snapshot() any
	// ValidateQuery performs the non-evaluating prefix of Query (lookup +
	// param arity/binding) and returns before evaluating the body, so a caller
	// can reject a malformed query fast without depending on current state.
	ValidateQuery(name string, params []any) error
	// SnapshotWire / MergeWire are the transport-safe analogues of Snapshot /
	// Merge: they exchange WireSnapshot (exact-rational strings) for the
	// synchronous (potentially cross-machine) quorum protocol, while gossip
	// keeps using the in-process Snapshot / Merge.
	SnapshotWire() WireSnapshot
	MergeWire(snap WireSnapshot) error
}
