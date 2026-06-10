package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gospr/builder"
	"gospr/parser"
)

const src = `type T = vector real

merge T = zip max

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
