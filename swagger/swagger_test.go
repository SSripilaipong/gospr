package swagger

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gospr/builder"
	"gospr/crdt"
	"gospr/numtype"
	"gospr/parser"
)

func makeModelPlan() builder.BuiltPlan {
	plus := parser.Expr{Kind: parser.ExprRef, Name: "+", Arity: 2, Ref: parser.RefPrimitive}
	max := parser.Expr{Kind: parser.ExprRef, Name: "max", Arity: 2, Ref: parser.RefPrimitive}
	zero := parser.Expr{Kind: parser.ExprNumLit, Num: big.NewRat(0, 1)}
	// (+ 1) and (+ a) as resolved partial applications.
	one := parser.Expr{Kind: parser.ExprNumLit, Num: big.NewRat(1, 1)}
	aVar := parser.Expr{Kind: parser.ExprVar, Name: "a"}
	addOneFn := parser.Expr{Kind: parser.ExprApp, Head: &plus, Args: []*parser.Expr{&one}}
	addAFn := parser.Expr{Kind: parser.ExprApp, Head: &plus, Args: []*parser.Expr{&aVar}}

	model := &builder.Model{
		Name:  "MyVec",
		Elem:  crdt.ElemT{Num: numtype.NumType{Domain: numtype.DRat, Sign: numtype.SAny}},
		Merge: parser.Expr{Kind: parser.ExprZip, Fn: &max},
		Queries: map[string]crdt.Method{
			"Value": {Body: parser.Expr{Kind: parser.ExprReduce, Fn: &plus, Init: &zero}},
		},
		Updates: map[string]crdt.Method{
			"AddOne": {Body: parser.Expr{Kind: parser.ExprLocal, Fn: &addOneFn}},
			"Add": {
				Params: []parser.ParamSpec{{Name: "a", Type: "rat"}},
				Body:   parser.Expr{Kind: parser.ExprLocal, Fn: &addAFn},
			},
		},
	}
	return builder.BuiltPlan{
		Collections: []builder.BuiltCollection{{Name: "MyVec", Spec: model}},
	}
}

func TestGenerate_paramSchemas(t *testing.T) {
	data, err := Generate(makeModelPlan())
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	paths := doc["paths"].(map[string]any)
	addPath := paths["/api/collections/MyVec/Add"].(map[string]any)
	post := addPath["post"].(map[string]any)
	rb := post["requestBody"].(map[string]any)
	mt := rb["content"].(map[string]any)["application/json"].(map[string]any)

	example := mt["example"].(map[string]any)
	params := example["params"].([]any)
	assert.Equal(t, []any{"1"}, params) // exact-rational string

	schemaObj := mt["schema"].(map[string]any)
	propsObj := schemaObj["properties"].(map[string]any)
	paramsSchema := propsObj["params"].(map[string]any)
	items := paramsSchema["items"].(map[string]any)
	assert.Equal(t, "string", items["type"]) // numbers cross the boundary as strings
}

func TestGenerate_zeroParamPost(t *testing.T) {
	data, err := Generate(makeModelPlan())
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	paths := doc["paths"].(map[string]any)
	addOnePath := paths["/api/collections/MyVec/AddOne"].(map[string]any)
	post := addOnePath["post"].(map[string]any)
	rb := post["requestBody"].(map[string]any)
	assert.Equal(t, false, rb["required"])
	mt := rb["content"].(map[string]any)["application/json"].(map[string]any)
	example := mt["example"].(map[string]any)
	assert.Empty(t, example)
}

func TestGenerate_queryResponseType(t *testing.T) {
	plan := makeModelPlan()
	model := plan.Collections[0].Spec.(*builder.Model)
	model.Queries["Grade"] = crdt.Method{Body: parser.Expr{Kind: parser.ExprStrLit, Str: "A"}, Result: parser.TypeString}

	data, err := Generate(plan)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	paths := doc["paths"].(map[string]any)
	get := paths["/api/collections/MyVec/Grade"].(map[string]any)["get"].(map[string]any)
	resp := get["responses"].(map[string]any)["200"].(map[string]any)
	mt := resp["content"].(map[string]any)["application/json"].(map[string]any)
	assert.Equal(t, "string", mt["schema"].(map[string]any)["type"])

	// the numeric Value query reports a string (exact-rational wire form)
	getValue := paths["/api/collections/MyVec/Value"].(map[string]any)["get"].(map[string]any)
	respValue := getValue["responses"].(map[string]any)["200"].(map[string]any)
	mtValue := respValue["content"].(map[string]any)["application/json"].(map[string]any)
	assert.Equal(t, "string", mtValue["schema"].(map[string]any)["type"])
}

func TestGenerate_getZeroParamQuery(t *testing.T) {
	data, err := Generate(makeModelPlan())
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	paths := doc["paths"].(map[string]any)
	getPath := paths["/api/collections/MyVec/Value"].(map[string]any)
	get := getPath["get"].(map[string]any)
	_, hasParams := get["parameters"]
	assert.False(t, hasParams)
}

func TestGenerate_getParamQueryRequired(t *testing.T) {
	// A parameterized query documents its `params` as required — the runtime
	// rejects a missing param on the arity check.
	plan := makeModelPlan()
	model := plan.Collections[0].Spec.(*builder.Model)
	model.Queries["Above"] = crdt.Method{
		Params: []parser.ParamSpec{{Name: "m", Type: "rat"}},
		Body:   parser.Expr{Kind: parser.ExprVar, Name: "m"},
		Result: parser.TypeBool,
	}

	data, err := Generate(plan)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	paths := doc["paths"].(map[string]any)
	get := paths["/api/collections/MyVec/Above"].(map[string]any)["get"].(map[string]any)
	params := get["parameters"].([]any)
	require.Len(t, params, 1)
	p := params[0].(map[string]any)
	assert.Equal(t, "params", p["name"])
	assert.Equal(t, true, p["required"])
}
