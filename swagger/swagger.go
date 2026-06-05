package swagger

import (
	"encoding/json"
	"gospr/builder"
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
	Type       string            `json:"type,omitempty"`
	Properties map[string]schema `json:"properties,omitempty"`
	Items      *schema           `json:"items,omitempty"`
}

type response struct {
	Description string `json:"description"`
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
							Schema: schema{Type: "string"},
							Example: "collection MyCounter = MyCounterType(9)\ntype MyCounterType(x int) = { x: GCounter(x) }\nquery MyCounterType.X() = x.Value()\nupdate MyCounterType.Up() = { x: x.Add(1) }",
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
		switch spec := bc.Spec.(type) {
		case builder.GCounterSpec:
			addGetPath(paths, bc.Name, "Value")
			addPostPath(paths, bc.Name, "Add")
		case builder.CompositeSpec:
			for queryName := range spec.QueryIndex {
				addGetPath(paths, bc.Name, queryName)
			}
			for actionName := range spec.UpdateIndex {
				addPostPath(paths, bc.Name, actionName)
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

func addGetPath(paths map[string]pathItem, collection, queryName string) {
	key := "/api/collections/" + collection + "/" + queryName
	paths[key] = pathItem{
		Get: &operation{
			Summary: queryName + " on " + collection,
			Parameters: []parameter{
				{Name: "params", In: "query", Required: false, Schema: schema{Type: "string"}},
			},
			Responses: map[string]response{
				"200": {Description: "Query result"},
				"400": {Description: "Error"},
			},
		},
	}
}

func addPostPath(paths map[string]pathItem, collection, actionName string) {
	key := "/api/collections/" + collection + "/" + actionName
	paths[key] = pathItem{
		Post: &operation{
			Summary: actionName + " on " + collection,
			RequestBody: &requestBody{
				Required: false,
				Content: map[string]mediaType{
					"application/json": {
						Schema: schema{
							Type: "object",
							Properties: map[string]schema{
								"params": {Type: "array", Items: &schema{}},
							},
						},
					},
				},
			},
			Responses: map[string]response{
				"200": {Description: "Action applied"},
				"400": {Description: "Error"},
			},
		},
	}
}
