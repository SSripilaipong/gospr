package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gospr/builder"
	"gospr/parser"
)

const src = `type T = vector real

fn lub a::real b::real = max a b

merge T = zip lub

query T.Value = reduce + 0

update T.Add k::real = local (+ k)
`

// End-to-end: code string -> parse -> build -> model instance -> behaviors.
func TestE2E_vectorModel(t *testing.T) {
	plan, err := parser.Parse(src)
	require.NoError(t, err)

	built, err := builder.Build(plan)
	require.NoError(t, err)

	require.Contains(t, built.Models, "T")
	model := built.Models["T"]

	a := model.New("nodeA")
	b := model.New("nodeB")

	// Empty vector reduces to the init (0).
	got, err := a.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, 0.0, got)

	// `local (+ k)` updates only the calling node's slot.
	require.NoError(t, a.Apply("Add", []any{3.0}))
	require.NoError(t, b.Apply("Add", []any{5.0}))

	got, err = a.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, 3.0, got)

	got, err = b.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, 5.0, got)

	// `zip max` merge unions the slots elementwise; reduce + sums them.
	require.NoError(t, a.Merge(b.Snapshot()))
	require.NoError(t, b.Merge(a.Snapshot()))

	got, err = a.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, 8.0, got)

	got, err = b.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, 8.0, got)

	// Merge takes per-slot max: a stale lower value must not lower a slot.
	a.Apply("Add", []any{10.0}) // nodeA slot -> 13
	stale := map[string]float64{"nodeA": 1.0}
	require.NoError(t, a.Merge(stale))

	got, err = a.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, 18.0, got) // 13 (nodeA) + 5 (nodeB)
}

// Partial application binds the LEFT operand first, so `local (- k)` means
// \x -> k - x (k minus the current slot), NOT a right-section x - k. This is
// intentional: the language has one uniform application rule, no special
// section semantics. Only non-commutative ops (here `-`) are observably
// affected. To get `current - k`, define a fn with the slot as the left
// operand, e.g. `fn rsub k::real x::real = - x k` then `local (rsub k)`.
func TestE2E_nonCommutativePartialApplication(t *testing.T) {
	src := `type C = vector real
merge C = zip max
query C.Value = reduce + 0
update C.Sub k::real = local (- k)
`
	plan, err := parser.Parse(src)
	require.NoError(t, err)
	built, err := builder.Build(plan)
	require.NoError(t, err)
	c := built.Models["C"].New("nodeA")

	// slot starts at 0: (- 10) applied to 0 -> 10 - 0 = 10
	require.NoError(t, c.Apply("Sub", []any{10.0}))
	got, err := c.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, 10.0, got)

	// (- 3) applied to 10 -> 3 - 10 = -7
	require.NoError(t, c.Apply("Sub", []any{3.0}))
	got, err = c.Query("Value", nil)
	require.NoError(t, err)
	assert.Equal(t, -7.0, got)
}
