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

// A struct-result query with a mix of numeric and string leaves documents each
// field with the right OpenAPI schema: a string leaf is a plain `string`, a
// numeric leaf a string-typed exact-rational (with its numtype in the description).
func TestGenerate_structResultWithStringField(t *testing.T) {
	plan := makeModelPlan()
	model := plan.Collections[0].Spec.(*builder.Model)
	elem := crdt.ElemT{Struct: true, Name: "R", Fields: []crdt.FieldT{
		{Name: "version", Type: crdt.ElemT{Num: numtype.NumType{Domain: numtype.DInt, Sign: numtype.SNonNeg}}},
		{Name: "value", Type: crdt.ElemT{Str: true}},
	}}
	model.Queries["Latest"] = crdt.Method{Body: parser.Expr{Kind: parser.ExprStrLit, Str: "x"}, ResultStruct: &elem}

	data, err := Generate(plan)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	paths := doc["paths"].(map[string]any)
	get := paths["/api/collections/MyVec/Latest"].(map[string]any)["get"].(map[string]any)
	resp := get["responses"].(map[string]any)["200"].(map[string]any)
	sch := resp["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	assert.Equal(t, "object", sch["type"])
	props := sch["properties"].(map[string]any)

	valueSch := props["value"].(map[string]any) // string leaf -> plain string, no numeric description
	assert.Equal(t, "string", valueSch["type"])
	assert.Nil(t, valueSch["description"])

	versionSch := props["version"].(map[string]any) // numeric leaf -> exact-rational string
	assert.Equal(t, "string", versionSch["type"])
	assert.Contains(t, versionSch["description"], "exact rational")
}

func TestGenerate_getZeroParamQuery(t *testing.T) {
	data, err := Generate(makeModelPlan())
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	paths := doc["paths"].(map[string]any)
	getPath := paths["/api/collections/MyVec/Value"].(map[string]any)
	get := getPath["get"].(map[string]any)
	// Even a zero-param query carries exactly one parameter: the opt-in sync
	// header (no `params` query parameter).
	params := get["parameters"].([]any)
	require.Len(t, params, 1)
	assertSyncHeaderParam(t, params[0].(map[string]any))
	// The synchronous failure mode is documented.
	_, has503 := get["responses"].(map[string]any)["503"]
	assert.True(t, has503, "GET should document a 503 for an unmet sync quorum")
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
	// Two parameters now: the sync header plus the `params` query parameter.
	params := get["parameters"].([]any)
	require.Len(t, params, 2)
	byName := map[string]map[string]any{}
	for _, p := range params {
		pm := p.(map[string]any)
		byName[pm["name"].(string)] = pm
	}
	require.Contains(t, byName, "params")
	assert.Equal(t, true, byName["params"]["required"])
	require.Contains(t, byName, "X-Gospr-Sync-Ratio")
	assertSyncHeaderParam(t, byName["X-Gospr-Sync-Ratio"])
}

// assertSyncHeaderParam checks the shared opt-in consistency header parameter.
func assertSyncHeaderParam(t *testing.T, p map[string]any) {
	t.Helper()
	assert.Equal(t, "X-Gospr-Sync-Ratio", p["name"])
	assert.Equal(t, "header", p["in"])
	assert.Equal(t, false, p["required"])
}

func TestGenerate_postDocumentsSyncHeaderAnd503(t *testing.T) {
	data, err := Generate(makeModelPlan())
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	paths := doc["paths"].(map[string]any)
	post := paths["/api/collections/MyVec/Add"].(map[string]any)["post"].(map[string]any)
	params := post["parameters"].([]any)
	require.Len(t, params, 1)
	assertSyncHeaderParam(t, params[0].(map[string]any))
	_, has503 := post["responses"].(map[string]any)["503"]
	assert.True(t, has503, "POST should document a 503 for an unmet sync quorum")
}
