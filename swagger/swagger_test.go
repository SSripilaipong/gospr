package swagger

import (
	"encoding/json"
	"gospr/builder"
	"gospr/parser"
	"testing"
)

func makeParamPlan() builder.BuiltPlan {
	min := float64(0)
	queries := map[string]parser.QuerySpec{
		"MyValue": {Body: parser.MethodCall{Field: "counter", Method: "Value"}},
	}
	updates := map[string]parser.UpdateSpec{
		"AddOne": {Body: []parser.FieldUpdate{{Field: "counter", Call: parser.MethodCall{Method: "Add", Args: []string{"1"}}}}},
		"Up": {
			Params: []parser.ParamSpec{{Name: "a", Type: "real0+"}},
			Body:   []parser.FieldUpdate{{Field: "counter", Call: parser.MethodCall{Method: "Add", Args: []string{"a"}}}},
		},
	}
	spec := builder.CompositeSpec{
		Fields:  map[string]builder.CollectionSpec{"counter": builder.GCounterSpec{Initial: 0}},
		Queries: queries,
		Updates: updates,
	}
	_ = min
	return builder.BuiltPlan{
		Collections: []builder.BuiltCollection{
			{Name: "MyCounter", Spec: spec},
		},
	}
}

func TestGenerate_paramSchemas(t *testing.T) {
	data, err := Generate(makeParamPlan())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)

	upPath := paths["/api/collections/MyCounter/Up"].(map[string]any)
	post := upPath["post"].(map[string]any)
	rb := post["requestBody"].(map[string]any)
	content := rb["content"].(map[string]any)
	mt := content["application/json"].(map[string]any)

	example := mt["example"].(map[string]any)
	params := example["params"].([]any)
	if len(params) != 1 || params[0] != float64(1) {
		t.Errorf("expected example params [1], got %v", params)
	}

	schemaObj := mt["schema"].(map[string]any)
	propsObj := schemaObj["properties"].(map[string]any)
	paramsSchema := propsObj["params"].(map[string]any)
	items := paramsSchema["items"].(map[string]any)
	if items["type"] != "number" {
		t.Errorf("expected items.type=number, got %v", items["type"])
	}
	if items["minimum"] != float64(0) {
		t.Errorf("expected items.minimum=0, got %v", items["minimum"])
	}
}

func TestGenerate_zeroParamPost(t *testing.T) {
	data, err := Generate(makeParamPlan())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)

	addOnePath := paths["/api/collections/MyCounter/AddOne"].(map[string]any)
	post := addOnePath["post"].(map[string]any)
	rb := post["requestBody"].(map[string]any)
	if rb["required"] != false {
		t.Error("expected required=false for zero-param update")
	}
	mt := rb["content"].(map[string]any)["application/json"].(map[string]any)
	example := mt["example"].(map[string]any)
	if len(example) != 0 {
		t.Errorf("expected empty example {}, got %v", example)
	}
}

func TestGenerate_getZeroParamQuery(t *testing.T) {
	data, err := Generate(makeParamPlan())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)

	getPath := paths["/api/collections/MyCounter/MyValue"].(map[string]any)
	get := getPath["get"].(map[string]any)
	if _, hasParams := get["parameters"]; hasParams {
		t.Error("expected no parameters for zero-param query")
	}
}

func TestGenerate_getParamQuery(t *testing.T) {
	plan := builder.BuiltPlan{
		Collections: []builder.BuiltCollection{
			{Name: "MyCounter", Spec: builder.CompositeSpec{
				Fields: map[string]builder.CollectionSpec{"counter": builder.GCounterSpec{}},
				Queries: map[string]parser.QuerySpec{
					"Scaled": {
						Params: []parser.ParamSpec{{Name: "factor", Type: "real0+"}},
						Body:   parser.MethodCall{Field: "counter", Method: "Value"},
					},
				},
				Updates: map[string]parser.UpdateSpec{},
			}},
		},
	}
	data, err := Generate(plan)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)
	getPath := paths["/api/collections/MyCounter/Scaled"].(map[string]any)
	get := getPath["get"].(map[string]any)
	params := get["parameters"].([]any)
	if len(params) != 1 {
		t.Fatalf("expected 1 parameter, got %d", len(params))
	}
	p := params[0].(map[string]any)
	if p["name"] != "params" || p["in"] != "query" {
		t.Errorf("unexpected parameter: %v", p)
	}
	if p["example"] != "1" {
		t.Errorf("expected example '1', got %v", p["example"])
	}
}
