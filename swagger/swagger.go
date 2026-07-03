package swagger

import (
	"encoding/json"
	"fmt"
	"gospr/builder"
	"gospr/crdt"
	"gospr/numtype"
	"gospr/parser"
	"strings"
)

type openAPI struct {
	OpenAPI string              `json:"openapi"`
	Info    info                `json:"info"`
	Paths   map[string]pathItem `json:"paths"`
}

type info struct {
	Title   string `json:"title"`
	Version string `json:"version"`
}

type pathItem struct {
	Get  *operation `json:"get,omitempty"`
	Post *operation `json:"post,omitempty"`
}

type operation struct {
	Summary     string              `json:"summary"`
	Parameters  []parameter         `json:"parameters,omitempty"`
	RequestBody *requestBody        `json:"requestBody,omitempty"`
	Responses   map[string]response `json:"responses"`
}

type parameter struct {
	Name     string `json:"name"`
	In       string `json:"in"`
	Required bool   `json:"required"`
	Schema   schema `json:"schema"`
	Example  any    `json:"example,omitempty"`
}

type requestBody struct {
	Required bool                 `json:"required"`
	Content  map[string]mediaType `json:"content"`
}

type mediaType struct {
	Schema  schema `json:"schema"`
	Example any    `json:"example,omitempty"`
}

type schema struct {
	Type        string            `json:"type,omitempty"`
	Description string            `json:"description,omitempty"`
	Properties  map[string]schema `json:"properties,omitempty"`
	Items       *schema           `json:"items,omitempty"`
}

type response struct {
	Description string               `json:"description"`
	Content     map[string]mediaType `json:"content,omitempty"`
}

func Generate(plan builder.BuiltPlan) ([]byte, error) {
	paths := map[string]pathItem{
		"/api/cluster/deploy": {
			Post: &operation{
				Summary: "Deploy a DSL plan to the cluster",
				RequestBody: &requestBody{
					Required: true,
					Content: map[string]mediaType{
						"text/plain": {
							Schema:  schema{Type: "string"},
							Example: "type T = vector rat0+\nmerge T = zip max\nquery T.Value = reduce + 0\nupdate T.Add k::rat0+ = local (+ k)\ncollection MyVec = T",
						},
					},
				},
				Responses: map[string]response{
					"200": {Description: "Deployed successfully"},
					"400": {Description: "Parse or build error"},
				},
			},
		},
	}

	for _, bc := range plan.Collections {
		if m, ok := bc.Spec.(*builder.Model); ok {
			for queryName, qs := range m.Queries {
				addGetPath(paths, bc.Name, queryName, qs.Params, qs.Result, qs.ResultNum, qs.ResultStruct)
			}
			for actionName, us := range m.Updates {
				addPostPath(paths, bc.Name, actionName, us.Params)
			}
		}
	}

	spec := openAPI{
		OpenAPI: "3.0.0",
		Info:    info{Title: "gospr API", Version: "1.0.0"},
		Paths:   paths,
	}
	return json.MarshalIndent(spec, "", "  ")
}

func addGetPath(paths map[string]pathItem, collection, queryName string, params []parser.ParamSpec, result parser.ValType, resultNum numtype.NumType, resultStruct *crdt.ElemT) {
	key := "/api/collections/" + collection + "/" + queryName
	resultSchema := valTypeSchema(result, resultNum)
	if resultStruct != nil {
		resultSchema = structSchema(*resultStruct)
	}
	op := &operation{
		Summary: queryName + " on " + collection,
		Responses: map[string]response{
			"200": {
				Description: "Query result",
				Content:     map[string]mediaType{"application/json": {Schema: resultSchema}},
			},
			"400": {Description: "Error"},
		},
	}
	if len(params) > 0 {
		parts := make([]string, len(params))
		for i, ps := range params {
			parts[i] = ps.Name + " (" + ps.Type + ")"
		}
		examples := make([]string, len(params))
		for i, ps := range params {
			examples[i] = fmt.Sprintf("%v", paramExample(ps))
		}
		op.Parameters = []parameter{{
			Name:     "params",
			In:       "query",
			Required: false,
			Schema:   schema{Type: "string", Description: strings.Join(parts, ", ")},
			Example:  strings.Join(examples, ","),
		}}
	}
	paths[key] = pathItem{Get: op}
}

func addPostPath(paths map[string]pathItem, collection, actionName string, params []parser.ParamSpec) {
	key := "/api/collections/" + collection + "/" + actionName
	op := &operation{
		Summary: actionName + " on " + collection,
		Responses: map[string]response{
			"200": {Description: "Action applied"},
			"400": {Description: "Error"},
		},
	}
	var mt mediaType
	if len(params) == 0 {
		mt = mediaType{
			Schema:  schema{Type: "object"},
			Example: map[string]any{},
		}
	} else {
		itemsSchema := &schema{}
		if len(params) == 1 {
			s := paramToSchema(params[0])
			itemsSchema = &s
		}
		vals := make([]any, len(params))
		for i, p := range params {
			vals[i] = paramExample(p)
		}
		mt = mediaType{
			Schema: schema{
				Type: "object",
				Properties: map[string]schema{
					"params": {Type: "array", Items: itemsSchema},
				},
			},
			Example: map[string]any{"params": vals},
		}
	}
	op.RequestBody = &requestBody{
		Required: len(params) > 0,
		Content:  map[string]mediaType{"application/json": mt},
	}
	paths[key] = pathItem{
		Post: op,
	}
}

// valTypeSchema maps a query's result value type to an OpenAPI schema. For a
// numeric result the carried NumType drives the description of the string-typed
// exact-rational schema.
func valTypeSchema(t parser.ValType, nt numtype.NumType) schema {
	switch t {
	case parser.TypeBool:
		return schema{Type: "boolean"}
	case parser.TypeString:
		return schema{Type: "string"}
	default:
		return numSchema(nt)
	}
}

// structSchema renders a struct element/result type as an OpenAPI object schema,
// each field a nested schema (scalar leaves are string-typed exact rationals),
// recursing for nested structs.
func structSchema(t crdt.ElemT) schema {
	props := make(map[string]schema, len(t.Fields))
	for _, f := range t.Fields {
		if f.Type.Struct {
			props[f.Name] = structSchema(f.Type)
		} else {
			props[f.Name] = numSchema(f.Type.Num)
		}
	}
	return schema{Type: "object", Properties: props}
}

// numSchema renders a numeric type as a JSON schema. Numbers cross the boundary
// as exact-rational strings ("5", "1/2", "-3/4"), so the schema is string-typed;
// the domain/sign constraint that a numeric min/max would express is carried in
// the description instead.
func numSchema(nt numtype.NumType) schema {
	return schema{Type: "string", Description: "exact rational (" + nt.String() + ")"}
}

func paramToSchema(p parser.ParamSpec) schema {
	nt, ok := numtype.Parse(p.Type)
	if !ok {
		return schema{}
	}
	return numSchema(nt)
}

// paramExample returns a representative wire value: an exact-rational string,
// since numbers cross the boundary as strings.
func paramExample(p parser.ParamSpec) any {
	nt, ok := numtype.Parse(p.Type)
	if !ok {
		return nil
	}
	if nt.Sign == numtype.SNonPos {
		return "-1"
	}
	return "1"
}
