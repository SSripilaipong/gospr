package swagger

import (
	"encoding/json"
	"testing"

	"gospr/builder"
	"gospr/crdt"
	"gospr/parser"
)

func makeModelPlan() builder.BuiltPlan {
	plusFn := parser.Expr{Kind: parser.ExprFuncRef, Op: "+"}
	zero := parser.Expr{Kind: parser.ExprNumLit, Num: 0}
	one := parser.Expr{Kind: parser.ExprNumLit, Num: 1}
	aRef := parser.Expr{Kind: parser.ExprParamRef, Param: "a"}
	maxFn := parser.Expr{Kind: parser.ExprFuncRef, Op: "max"}

	model := &builder.Model{
		Name:  "MyVec",
		Elem:  parser.ElemType{Kind: parser.KindReal},
		Merge: parser.Expr{Kind: parser.ExprZip, Fn: &maxFn},
		Queries: map[string]crdt.Method{
			"Value": {Body: parser.Expr{Kind: parser.ExprReduce, Fn: &plusFn, Init: &zero}},
		},
		Updates: map[string]crdt.Method{
			"AddOne": {Body: parser.Expr{Kind: parser.ExprLocal,
				Fn: &parser.Expr{Kind: parser.ExprSection, Op: "+", Arg: &one}}},
			"Add": {
				Params: []parser.ParamSpec{{Name: "a", Type: "real"}},
				Body: parser.Expr{Kind: parser.ExprLocal,
					Fn: &parser.Expr{Kind: parser.ExprSection, Op: "+", Arg: &aRef}},
			},
		},
	}
	return builder.BuiltPlan{
		Collections: []builder.BuiltCollection{{Name: "MyVec", Spec: model}},
	}
}

func TestGenerate_paramSchemas(t *testing.T) {
	data, err := Generate(makeModelPlan())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)

	addPath := paths["/api/collections/MyVec/Add"].(map[string]any)
	post := addPath["post"].(map[string]any)
	rb := post["requestBody"].(map[string]any)
	mt := rb["content"].(map[string]any)["application/json"].(map[string]any)

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
}

func TestGenerate_zeroParamPost(t *testing.T) {
	data, err := Generate(makeModelPlan())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)

	addOnePath := paths["/api/collections/MyVec/AddOne"].(map[string]any)
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
	data, err := Generate(makeModelPlan())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)

	getPath := paths["/api/collections/MyVec/Value"].(map[string]any)
	get := getPath["get"].(map[string]any)
	if _, hasParams := get["parameters"]; hasParams {
		t.Error("expected no parameters for zero-param query")
	}
}
