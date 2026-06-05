package crdt

import (
	"gospr/parser"
	"testing"
)

func makeTestComposite(nodeID string) *CompositeCRDT {
	fieldFactories := map[string]func(string) CRDT{
		"counter": func(id string) CRDT { return NewGCounter(id, 0) },
	}
	queryIndex := map[string]parser.MethodCall{
		"MyValue": {Field: "counter", Method: "Value"},
	}
	updateIndex := map[string][]parser.FieldUpdate{
		"AddOne": {{Field: "counter", Call: parser.MethodCall{Method: "Add", Args: []string{"1"}}}},
	}
	return NewComposite(nodeID, fieldFactories, queryIndex, updateIndex)
}

func TestComposite_applyAndQuery(t *testing.T) {
	c := makeTestComposite("node1")
	if err := c.Apply("AddOne", nil); err != nil {
		t.Fatal(err)
	}
	v, err := c.Query("MyValue", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.(int64) != 1 {
		t.Errorf("expected 1, got %v", v)
	}
}

func TestComposite_mergeAndSnapshot(t *testing.T) {
	c1 := makeTestComposite("node1")
	c2 := makeTestComposite("node2")
	c1.Apply("AddOne", nil)
	c2.Apply("AddOne", nil)
	if err := c1.Merge(c2.Snapshot()); err != nil {
		t.Fatal(err)
	}
	v, err := c1.Query("MyValue", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.(int64) != 2 {
		t.Errorf("expected 2, got %v", v)
	}
}

func TestComposite_unknownAction(t *testing.T) {
	c := makeTestComposite("node1")
	if err := c.Apply("NoSuchAction", nil); err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestComposite_unknownQuery(t *testing.T) {
	c := makeTestComposite("node1")
	if _, err := c.Query("NoSuchQuery", nil); err == nil {
		t.Fatal("expected error for unknown query")
	}
}
