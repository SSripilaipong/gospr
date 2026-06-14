package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gospr/builder"
	"gospr/parser"
)

const src = `type T = vector real0+

fn lub a::real0+ b::real0+ = max a b

merge T = zip lub

query T.Value = reduce + 0

update T.Add k::real0+ = local (+ k)
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

// End-to-end: a guarded, string-returning function consumed by a query that
// maps the reduced vector to a grade. Exercises guards, comparisons, string
// results, otherwise, and reduce-in-a-query-expression together.
func TestE2E_guardedGradeQuery(t *testing.T) {
	src := `type Scores = vector real0+
fn myScore x::real
| (> x 90) = "You got a A"
| (> x 80) = "You got a B"
| (> x 70) = "You got a C"
| otherwise = "You got a F"
merge Scores = zip max
query Scores.Grade = myScore (reduce max 0)
update Scores.Add k::real0+ = local (+ k)
collection Scores = Scores
`
	plan, err := parser.Parse(src)
	require.NoError(t, err)
	built, err := builder.Build(plan)
	require.NoError(t, err)
	m := built.Models["Scores"]

	// empty vector folds to 0 -> otherwise branch
	got, err := m.New("nodeA").Query("Grade", nil)
	require.NoError(t, err)
	assert.Equal(t, "You got a F", got)

	v := m.New("nodeA")
	require.NoError(t, v.Apply("Add", []any{95.0}))
	got, err = v.Query("Grade", nil)
	require.NoError(t, err)
	assert.Equal(t, "You got a A", got)

	// lower the working set: a fresh node summing to 75 -> grade C
	w := m.New("nodeB")
	require.NoError(t, w.Apply("Add", []any{75.0}))
	got, err = w.Query("Grade", nil)
	require.NoError(t, err)
	assert.Equal(t, "You got a C", got)
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
