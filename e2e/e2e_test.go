package e2e

import (
	"testing"

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
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	built, err := builder.Build(plan)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	model, ok := built.Models["T"]
	if !ok {
		t.Fatalf("model T not built")
	}

	a := model.New("nodeA")
	b := model.New("nodeB")

	// Empty vector reduces to the init (0).
	if got, _ := a.Query("Value", nil); got != 0.0 {
		t.Fatalf("empty Value = %v, want 0", got)
	}

	// `local (+ k)` updates only the calling node's slot.
	if err := a.Apply("Add", []any{3.0}); err != nil {
		t.Fatalf("a.Add: %v", err)
	}
	if err := b.Apply("Add", []any{5.0}); err != nil {
		t.Fatalf("b.Add: %v", err)
	}
	if got, _ := a.Query("Value", nil); got != 3.0 {
		t.Fatalf("a.Value = %v, want 3", got)
	}
	if got, _ := b.Query("Value", nil); got != 5.0 {
		t.Fatalf("b.Value = %v, want 5", got)
	}

	// `zip max` merge unions the slots elementwise; reduce + sums them.
	if err := a.Merge(b.Snapshot()); err != nil {
		t.Fatalf("a.Merge(b): %v", err)
	}
	if err := b.Merge(a.Snapshot()); err != nil {
		t.Fatalf("b.Merge(a): %v", err)
	}
	if got, _ := a.Query("Value", nil); got != 8.0 {
		t.Fatalf("a.Value after merge = %v, want 8", got)
	}
	if got, _ := b.Query("Value", nil); got != 8.0 {
		t.Fatalf("b.Value after merge = %v, want 8", got)
	}

	// Merge takes per-slot max: a stale lower value must not lower a slot.
	a.Apply("Add", []any{10.0}) // nodeA slot -> 13
	stale := map[string]float64{"nodeA": 1.0}
	if err := a.Merge(stale); err != nil {
		t.Fatalf("a.Merge(stale): %v", err)
	}
	if got, _ := a.Query("Value", nil); got != 18.0 { // 13 (nodeA) + 5 (nodeB)
		t.Fatalf("a.Value after stale merge = %v, want 18", got)
	}
}
