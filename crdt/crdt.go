package crdt

import "gospr/numtype"

// SlotWire is the transport-safe form of a single node slot: either a scalar
// (Num, an exact-rational string) or a struct (Struct, field -> nested SlotWire),
// nestable. Exactly one of Num/Struct is populated. A scalar slot's Num is always
// a non-empty RatString (even 0 -> "0"), so Num=="" unambiguously marks a struct.
type SlotWire struct {
	Num    string              `json:"num,omitempty"`    // scalar: exact-rational string
	Struct map[string]SlotWire `json:"struct,omitempty"` // struct: field -> nested slot
}

// WireSnapshot is the transport-safe form of a collection's state: nodeID -> a
// recursive SlotWire (scalar or struct). It is a named struct, not a bare map, so
// the slot encoding can carry structs (struct vectors) without changing every
// sync DTO signature.
type WireSnapshot struct {
	Slots map[string]SlotWire `json:"slots"` // nodeID -> slot value
}

// ---- resolved element descriptors ----------------------------------

// ElemT is a resolved element-type descriptor produced by the builder and
// consumed by the runtime (zero-struct defaults, wire decoding/validation), the
// prover (leaf flattening), and swagger (object schemas). A slot holds either a
// scalar numeric value (Num, when !Struct) or an ordered struct of named fields
// (Fields, when Struct), and structs nest.
type ElemT struct {
	Struct bool
	Num    numtype.NumType // when !Struct
	Name   string          // struct nominal type name (when Struct)
	Fields []FieldT        // when Struct, in declaration order
}

// FieldT is one resolved struct field: a name and its element type (itself an
// ElemT, so fields may be nested structs).
type FieldT struct {
	Name string
	Type ElemT
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
