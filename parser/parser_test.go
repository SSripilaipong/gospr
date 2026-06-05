package parser

import (
	"testing"
)

func TestParse_normal(t *testing.T) {
	plan, err := Parse("collection MyCounter = GCounter(0)\ncollection OtherCounter = GCounter(0)\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Collections) != 2 {
		t.Fatalf("expected 2 collections, got %d", len(plan.Collections))
	}
	if plan.Collections[0].Name != "MyCounter" || plan.Collections[0].Type != "GCounter" || plan.Collections[0].Args[0] != "0" {
		t.Errorf("unexpected first collection: %+v", plan.Collections[0])
	}
	if plan.Collections[1].Name != "OtherCounter" {
		t.Errorf("unexpected second collection: %+v", plan.Collections[1])
	}
}

func TestParse_skipBlanks(t *testing.T) {
	plan, err := Parse("\n\ncollection X = GCounter(1)\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Collections) != 1 || plan.Collections[0].Name != "X" {
		t.Errorf("unexpected result: %+v", plan.Collections)
	}
}

func TestParse_skipUnknownLines(t *testing.T) {
	plan, err := Parse("# comment\ncollection A = GCounter(0)\nunknown line here\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Collections) != 1 || plan.Collections[0].Name != "A" {
		t.Errorf("unexpected result: %+v", plan.Collections)
	}
}

func TestParse_multiArgs(t *testing.T) {
	plan, err := Parse("collection A = SomeType(foo, bar, 42)\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Collections) != 1 {
		t.Fatalf("expected 1 collection, got %d", len(plan.Collections))
	}
	c := plan.Collections[0]
	if c.Type != "SomeType" || len(c.Args) != 3 || c.Args[0] != "foo" || c.Args[1] != "bar" || c.Args[2] != "42" {
		t.Errorf("unexpected collection: %+v", c)
	}
}

func TestParse_empty(t *testing.T) {
	plan, err := Parse("")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Collections) != 0 {
		t.Errorf("expected 0 collections, got %d", len(plan.Collections))
	}
}

func TestParse_malformedReturnsError(t *testing.T) {
	_, err := Parse("collection Bad = GCounter\n")
	if err == nil {
		t.Fatal("expected error for malformed collection line, got nil")
	}
}

func TestParse_errorHasPosition(t *testing.T) {
	_, err := Parse("collection Bad = GCounter\n")
	pe, ok := err.(ParseError)
	if !ok {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
	if pe.Line == 0 || pe.Col == 0 {
		t.Errorf("expected non-zero line/col, got line=%d col=%d", pe.Line, pe.Col)
	}
}

func TestParse_typeDef(t *testing.T) {
	plan, err := Parse("type MyType(x int) = { counter: GCounter(x) }\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Types) != 1 {
		t.Fatalf("expected 1 type, got %d", len(plan.Types))
	}
	td := plan.Types[0]
	if td.Name != "MyType" {
		t.Errorf("unexpected type name: %s", td.Name)
	}
	if len(td.Params) != 1 || td.Params[0].Name != "x" || td.Params[0].Type != "int" {
		t.Errorf("unexpected params: %+v", td.Params)
	}
	if len(td.Fields) != 1 || td.Fields[0].Name != "counter" || td.Fields[0].CRDTType != "GCounter" || td.Fields[0].Args[0] != "x" {
		t.Errorf("unexpected fields: %+v", td.Fields)
	}
}

func TestParse_queryDef(t *testing.T) {
	plan, err := Parse("query MyType.MyValue() = counter.Value()\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Queries) != 1 {
		t.Fatalf("expected 1 query, got %d", len(plan.Queries))
	}
	qd := plan.Queries[0]
	if qd.TypeName != "MyType" || qd.MethodName != "MyValue" {
		t.Errorf("unexpected query header: %+v", qd)
	}
	if qd.Body.Field != "counter" || qd.Body.Method != "Value" || len(qd.Body.Args) != 0 {
		t.Errorf("unexpected query body: %+v", qd.Body)
	}
}

func TestParse_updateDef(t *testing.T) {
	plan, err := Parse("update MyType.AddOne() = { counter: counter.Add(1) }\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(plan.Updates))
	}
	ud := plan.Updates[0]
	if ud.TypeName != "MyType" || ud.MethodName != "AddOne" {
		t.Errorf("unexpected update header: %+v", ud)
	}
	if len(ud.Body) != 1 || ud.Body[0].Field != "counter" || ud.Body[0].Call.Field != "counter" || ud.Body[0].Call.Method != "Add" || ud.Body[0].Call.Args[0] != "1" {
		t.Errorf("unexpected update body: %+v", ud.Body)
	}
}

func TestParse_mixedPlan(t *testing.T) {
	input := "type MyType(x int) = { counter: GCounter(x) }\n" +
		"query MyType.MyValue() = counter.Value()\n" +
		"update MyType.AddOne() = { counter: counter.Add(1) }\n" +
		"collection MyCounter = MyType(0)\n"
	plan, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Types) != 1 || len(plan.Queries) != 1 || len(plan.Updates) != 1 || len(plan.Collections) != 1 {
		t.Errorf("unexpected plan: types=%d queries=%d updates=%d collections=%d",
			len(plan.Types), len(plan.Queries), len(plan.Updates), len(plan.Collections))
	}
}

func TestParse_updateDefWithParams(t *testing.T) {
	plan, err := Parse("update MyType.Up(a real0+) = { counter: counter.Add(a) }\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(plan.Updates))
	}
	ud := plan.Updates[0]
	if len(ud.Params) != 1 || ud.Params[0].Name != "a" || ud.Params[0].Type != "real0+" {
		t.Errorf("unexpected params: %+v", ud.Params)
	}
	if len(ud.Body) != 1 || ud.Body[0].Call.Args[0] != "a" {
		t.Errorf("unexpected body: %+v", ud.Body)
	}
}

func TestParse_queryDefWithParams(t *testing.T) {
	plan, err := Parse("query MyType.Scaled(factor real0+) = counter.Value()\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Queries) != 1 {
		t.Fatalf("expected 1 query, got %d", len(plan.Queries))
	}
	qd := plan.Queries[0]
	if len(qd.Params) != 1 || qd.Params[0].Name != "factor" || qd.Params[0].Type != "real0+" {
		t.Errorf("unexpected params: %+v", qd.Params)
	}
}

func TestParse_typeAndCollectionCoexist(t *testing.T) {
	input := "# comment\n" +
		"type T(n int) = { c: GCounter(n) }\n" +
		"unknown line\n" +
		"collection X = GCounter(0)\n"
	plan, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Types) != 1 || plan.Types[0].Name != "T" {
		t.Errorf("unexpected types: %+v", plan.Types)
	}
	if len(plan.Collections) != 1 || plan.Collections[0].Name != "X" {
		t.Errorf("unexpected collections: %+v", plan.Collections)
	}
}
