package crdt

import (
	"gospr/parser"
	"testing"
)

func makeTestDef() (parser.TypeDef, []parser.QueryDef, []parser.UpdateDef) {
	def := parser.TypeDef{
		Name:   "MyType",
		Params: []parser.ParamSpec{{Name: "x", Type: "int"}},
		Fields: []parser.FieldSpec{{Name: "counter", CRDTType: "GCounter", Args: []string{"x"}}},
	}
	queries := []parser.QueryDef{
		{TypeName: "MyType", MethodName: "MyValue", Body: parser.MethodCall{Field: "counter", Method: "Value"}},
	}
	updates := []parser.UpdateDef{
		{TypeName: "MyType", MethodName: "AddOne", Body: []parser.FieldUpdate{
			{Field: "counter", Call: parser.MethodCall{Field: "counter", Method: "Add", Args: []string{"1"}}},
		}},
	}
	return def, queries, updates
}

func TestComposite_applyAndQuery(t *testing.T) {
	def, queries, updates := makeTestDef()
	c, err := NewComposite(def, queries, updates, []string{"0"}, "node1")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Apply("AddOne", nil); err != nil {
		t.Fatal(err)
	}
	v, err := c.Query("MyValue")
	if err != nil {
		t.Fatal(err)
	}
	if v.(int64) != 1 {
		t.Errorf("expected 1, got %v", v)
	}
}

func TestComposite_mergeAndSnapshot(t *testing.T) {
	def, queries, updates := makeTestDef()
	c1, _ := NewComposite(def, queries, updates, []string{"0"}, "node1")
	c2, _ := NewComposite(def, queries, updates, []string{"0"}, "node2")
	c1.Apply("AddOne", nil)
	c2.Apply("AddOne", nil)
	if err := c1.Merge(c2.Snapshot()); err != nil {
		t.Fatal(err)
	}
	v, err := c1.Query("MyValue")
	if err != nil {
		t.Fatal(err)
	}
	if v.(int64) != 2 {
		t.Errorf("expected 2, got %v", v)
	}
}

func TestComposite_unknownAction(t *testing.T) {
	def, queries, updates := makeTestDef()
	c, _ := NewComposite(def, queries, updates, []string{"0"}, "node1")
	if err := c.Apply("NoSuchAction", nil); err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestComposite_unknownQuery(t *testing.T) {
	def, queries, updates := makeTestDef()
	c, _ := NewComposite(def, queries, updates, []string{"0"}, "node1")
	if _, err := c.Query("NoSuchQuery"); err == nil {
		t.Fatal("expected error for unknown query")
	}
}

func TestComposite_wrongArgCount(t *testing.T) {
	def, queries, updates := makeTestDef()
	_, err := NewComposite(def, queries, updates, []string{}, "node1")
	if err == nil {
		t.Fatal("expected error for wrong arg count")
	}
}
