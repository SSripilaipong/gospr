package builder

import (
	"fmt"
	"gospr/crdt"
	"gospr/parser"
	"strconv"
)

type CollectionSpec interface {
	New(nodeID string) crdt.CRDT
}

type BuiltPlan struct {
	Collections []BuiltCollection
}

type BuiltCollection struct {
	Name string
	Spec CollectionSpec
}

type GCounterSpec struct {
	Initial float64
}

func (s GCounterSpec) New(nodeID string) crdt.CRDT {
	return crdt.NewGCounter(nodeID, s.Initial)
}

type CompositeSpec struct {
	Fields  map[string]CollectionSpec
	Queries map[string]parser.QuerySpec
	Updates map[string]parser.UpdateSpec
}

func (s CompositeSpec) New(nodeID string) crdt.CRDT {
	fieldFactories := make(map[string]func(string) crdt.CRDT, len(s.Fields))
	for name, spec := range s.Fields {
		fieldFactories[name] = spec.New
	}
	return crdt.NewComposite(nodeID, fieldFactories, s.Queries, s.Updates)
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
		var initial float64
		if len(args) > 0 {
			var err error
			initial, err = strconv.ParseFloat(args[0], 64)
			if err != nil {
				return BuiltCollection{}, fmt.Errorf("GCounter %q: invalid initial value %q: %w", name, args[0], err)
			}
		}
		return BuiltCollection{Name: name, Spec: GCounterSpec{Initial: initial}}, nil
	default:
		return BuiltCollection{}, fmt.Errorf("unknown CRDT type: %s", typeName)
	}
}

var knownParamTypes = map[string]bool{"int": true, "real0+": true}

func buildComposite(spec parser.CollectionSpec, def parser.TypeDef, queries []parser.QueryDef, updates []parser.UpdateDef) (BuiltCollection, error) {
	if len(spec.Args) != len(def.Params) {
		return BuiltCollection{}, fmt.Errorf("type %s expects %d args, got %d", def.Name, len(def.Params), len(spec.Args))
	}
	params := make(map[string]string, len(def.Params))
	for i, p := range def.Params {
		params[p.Name] = spec.Args[i]
	}

	fieldSpecs := make(map[string]CollectionSpec, len(def.Fields))
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
		fieldSpecs[f.Name] = bc.Spec
	}

	querySpecs := make(map[string]parser.QuerySpec, len(queries))
	for _, q := range queries {
		for _, p := range q.Params {
			if !knownParamTypes[p.Type] {
				return BuiltCollection{}, fmt.Errorf("query %s.%s: unknown param type %q", q.TypeName, q.MethodName, p.Type)
			}
		}
		paramNames := paramNameSet(q.Params)
		for _, arg := range q.Body.Args {
			if err := validateBodyArg(arg, paramNames, q.TypeName, q.MethodName); err != nil {
				return BuiltCollection{}, err
			}
		}
		querySpecs[q.MethodName] = parser.QuerySpec{Params: q.Params, Body: q.Body}
	}

	updateSpecs := make(map[string]parser.UpdateSpec, len(updates))
	for _, u := range updates {
		for _, p := range u.Params {
			if !knownParamTypes[p.Type] {
				return BuiltCollection{}, fmt.Errorf("update %s.%s: unknown param type %q", u.TypeName, u.MethodName, p.Type)
			}
		}
		paramNames := paramNameSet(u.Params)
		for _, fu := range u.Body {
			for _, arg := range fu.Call.Args {
				if err := validateBodyArg(arg, paramNames, u.TypeName, u.MethodName); err != nil {
					return BuiltCollection{}, err
				}
			}
		}
		updateSpecs[u.MethodName] = parser.UpdateSpec{Params: u.Params, Body: u.Body}
	}

	return BuiltCollection{
		Name: spec.Name,
		Spec: CompositeSpec{
			Fields:  fieldSpecs,
			Queries: querySpecs,
			Updates: updateSpecs,
		},
	}, nil
}

func paramNameSet(params []parser.ParamSpec) map[string]bool {
	set := make(map[string]bool, len(params))
	for _, p := range params {
		set[p.Name] = true
	}
	return set
}

func validateBodyArg(arg string, paramNames map[string]bool, typeName, methodName string) error {
	if paramNames[arg] {
		return nil
	}
	if _, err := strconv.ParseFloat(arg, 64); err == nil {
		return nil
	}
	return fmt.Errorf("%s.%s: body arg %q is neither a declared param nor a numeric literal", typeName, methodName, arg)
}
