package sandbox

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"gospr/crdt"
)

// A string slot leaf renders as its string value in sandbox state — including one
// nested in a struct — not the empty string the numeric fallthrough would yield.
func TestSlotWireToAny_stringLeaf(t *testing.T) {
	hi := "hi"
	assert.Equal(t, "hi", slotWireToAny(crdt.SlotWire{Str: &hi}))

	empty := ""
	assert.Equal(t, "", slotWireToAny(crdt.SlotWire{Str: &empty}))

	// A numeric leaf still renders as its rational string.
	assert.Equal(t, "5", slotWireToAny(crdt.SlotWire{Num: "5"}))

	// An LWW-shaped struct slot: { version (num), value (string) }.
	v := "world"
	got := slotWireToAny(crdt.SlotWire{Struct: map[string]crdt.SlotWire{
		"version": {Num: "3"},
		"value":   {Str: &v},
	}})
	assert.Equal(t, map[string]any{"version": "3", "value": "world"}, got)
}
