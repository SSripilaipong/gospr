package builder

import (
	"gospr/parser"
	"testing"
)

func gcounterPlan(initial string) parser.Plan {
	return parser.Plan{
		Collections: []parser.CollectionSpec{
			{Name: "MyCounter", Type: "GCounter", Args: []string{initial}},
		},
	}
}

func compositePlan() parser.Plan {
	return parser.Plan{
		Types: []parser.TypeDef{
			{
				Name:   "MyType",
				Params: []parser.ParamSpec{{Name: "x", Type: "int"}},
				Fields: []parser.FieldSpec{{Name: "counter", CRDTType: "GCounter", Args: []string{"x"}}},
			},
		},
		Queries: []parser.QueryDef{
			{TypeName: "MyType", MethodName: "MyValue", Body: parser.MethodCall{Field: "counter", Method: "Value"}},
		},
		Updates: []parser.UpdateDef{
			{TypeName: "MyType", MethodName: "AddOne", Body: []parser.FieldUpdate{
				{Field: "counter", Call: parser.MethodCall{Method: "Add", Args: []string{"1"}}},
			}},
		},
		Collections: []parser.CollectionSpec{
			{Name: "MyCounter", Type: "MyType", Args: []string{"0"}},
		},
	}
}

func TestBuild_gcounter(t *testing.T) {
	built, err := Build(gcounterPlan("42"))
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Collections) != 1 || built.Collections[0].Name != "MyCounter" {
		t.Fatalf("unexpected collections: %v", built.Collections)
	}
	c := built.Collections[0].Spec.New("node1")
	v, err := c.Query("Value", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.(float64) != 42 {
		t.Errorf("expected initial 42, got %v", v)
	}
}

func TestBuild_composite(t *testing.T) {
	built, err := Build(compositePlan())
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Collections) != 1 {
		t.Fatalf("expected 1 collection, got %d", len(built.Collections))
	}
	c := built.Collections[0].Spec.New("node1")
	if err := c.Apply("AddOne", nil); err != nil {
		t.Fatal(err)
	}
	v, err := c.Query("MyValue", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.(float64) != 1 {
		t.Errorf("expected 1, got %v", v)
	}
}

func TestBuild_unknownType(t *testing.T) {
	plan := parser.Plan{
		Collections: []parser.CollectionSpec{
			{Name: "X", Type: "NoSuchType", Args: []string{}},
		},
	}
	_, err := Build(plan)
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestBuild_wrongArgCount(t *testing.T) {
	plan := parser.Plan{
		Types: []parser.TypeDef{
			{
				Name:   "MyType",
				Params: []parser.ParamSpec{{Name: "x", Type: "int"}},
				Fields: []parser.FieldSpec{{Name: "counter", CRDTType: "GCounter", Args: []string{"x"}}},
			},
		},
		Collections: []parser.CollectionSpec{
			{Name: "X", Type: "MyType", Args: []string{}},
		},
	}
	_, err := Build(plan)
	if err == nil {
		t.Fatal("expected error for wrong arg count")
	}
}

func TestBuild_invalidInitialArg(t *testing.T) {
	_, err := Build(gcounterPlan("notanumber"))
	if err == nil {
		t.Fatal("expected error for invalid initial value")
	}
}

func TestBuild_real0PlusParam(t *testing.T) {
	plan := parser.Plan{
		Types: []parser.TypeDef{
			{
				Name:   "MyType",
				Params: []parser.ParamSpec{{Name: "x", Type: "int"}},
				Fields: []parser.FieldSpec{{Name: "counter", CRDTType: "GCounter", Args: []string{"x"}}},
			},
		},
		Queries: []parser.QueryDef{
			{TypeName: "MyType", MethodName: "MyValue", Body: parser.MethodCall{Field: "counter", Method: "Value"}},
		},
		Updates: []parser.UpdateDef{
			{TypeName: "MyType", MethodName: "Up", Params: []parser.ParamSpec{{Name: "a", Type: "real0+"}},
				Body: []parser.FieldUpdate{
					{Field: "counter", Call: parser.MethodCall{Method: "Add", Args: []string{"a"}}},
				}},
		},
		Collections: []parser.CollectionSpec{
			{Name: "MyCounter", Type: "MyType", Args: []string{"0"}},
		},
	}
	built, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	c := built.Collections[0].Spec.New("node1")
	if err := c.Apply("Up", []any{2.5}); err != nil {
		t.Fatal(err)
	}
	v, err := c.Query("MyValue", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.(float64) != 2.5 {
		t.Errorf("expected 2.5, got %v", v)
	}
}

func TestBuild_unknownParamType(t *testing.T) {
	plan := parser.Plan{
		Types: []parser.TypeDef{
			{Name: "MyType", Params: []parser.ParamSpec{{Name: "x", Type: "int"}},
				Fields: []parser.FieldSpec{{Name: "counter", CRDTType: "GCounter", Args: []string{"x"}}}},
		},
		Updates: []parser.UpdateDef{
			{TypeName: "MyType", MethodName: "Up", Params: []parser.ParamSpec{{Name: "a", Type: "badtype"}},
				Body: []parser.FieldUpdate{
					{Field: "counter", Call: parser.MethodCall{Method: "Add", Args: []string{"a"}}},
				}},
		},
		Collections: []parser.CollectionSpec{{Name: "C", Type: "MyType", Args: []string{"0"}}},
	}
	_, err := Build(plan)
	if err == nil {
		t.Fatal("expected error for unknown param type")
	}
}
