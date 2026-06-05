package builder

import (
	"fmt"
	"gospr/crdt"
	"gospr/parser"
	"strconv"
)

type BuiltPlan struct {
	Collections []BuiltCollection
}

type BuiltCollection struct {
	Name string
	New  func(nodeID string) crdt.CRDT
}

func Build(plan parser.Plan) (BuiltPlan, error) {
	typeMap := make(map[string]parser.TypeDef, len(plan.Types))
	for _, td := range plan.Types {
		typeMap[td.Name] = td
	}
	queryMap := make(map[string][]parser.QueryDef)
	for _, qd := range plan.Queries {
		queryMap[qd.TypeName] = append(queryMap[qd.TypeName], qd)
	}
	updateMap := make(map[string][]parser.UpdateDef)
	for _, ud := range plan.Updates {
		updateMap[ud.TypeName] = append(updateMap[ud.TypeName], ud)
	}

	collections := make([]BuiltCollection, 0, len(plan.Collections))
	for _, spec := range plan.Collections {
		var bc BuiltCollection
		var err error
		if td, ok := typeMap[spec.Type]; ok {
			bc, err = buildComposite(spec, td, queryMap[spec.Type], updateMap[spec.Type])
		} else {
			bc, err = buildPrimitive(spec.Name, spec.Type, spec.Args)
		}
		if err != nil {
			return BuiltPlan{}, err
		}
		collections = append(collections, bc)
	}
	return BuiltPlan{Collections: collections}, nil
}

func buildPrimitive(name, typeName string, args []string) (BuiltCollection, error) {
	switch typeName {
	case "GCounter":
		var initial int64
		if len(args) > 0 {
			var err error
			initial, err = strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return BuiltCollection{}, fmt.Errorf("GCounter %q: invalid initial value %q: %w", name, args[0], err)
			}
		}
		return BuiltCollection{
			Name: name,
			New:  func(nodeID string) crdt.CRDT { return crdt.NewGCounter(nodeID, initial) },
		}, nil
	default:
		return BuiltCollection{}, fmt.Errorf("unknown CRDT type: %s", typeName)
	}
}

func buildComposite(spec parser.CollectionSpec, def parser.TypeDef, queries []parser.QueryDef, updates []parser.UpdateDef) (BuiltCollection, error) {
	if len(spec.Args) != len(def.Params) {
		return BuiltCollection{}, fmt.Errorf("type %s expects %d args, got %d", def.Name, len(def.Params), len(spec.Args))
	}
	params := make(map[string]string, len(def.Params))
	for i, p := range def.Params {
		params[p.Name] = spec.Args[i]
	}

	fieldFactories := make(map[string]func(string) crdt.CRDT, len(def.Fields))
	for _, f := range def.Fields {
		resolved := make([]string, len(f.Args))
		for i, a := range f.Args {
			if v, ok := params[a]; ok {
				resolved[i] = v
			} else {
				resolved[i] = a
			}
		}
		bc, err := buildPrimitive(f.Name, f.CRDTType, resolved)
		if err != nil {
			return BuiltCollection{}, fmt.Errorf("field %s: %w", f.Name, err)
		}
		factory := bc.New
		fieldFactories[f.Name] = factory
	}

	queryIndex := make(map[string]parser.MethodCall, len(queries))
	for _, q := range queries {
		queryIndex[q.MethodName] = q.Body
	}
	updateIndex := make(map[string][]parser.FieldUpdate, len(updates))
	for _, u := range updates {
		updateIndex[u.MethodName] = u.Body
	}

	return BuiltCollection{
		Name: spec.Name,
		New: func(nodeID string) crdt.CRDT {
			return crdt.NewComposite(nodeID, fieldFactories, queryIndex, updateIndex)
		},
	}, nil
}
