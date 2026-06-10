package builder

import (
	"fmt"

	"gospr/crdt"
	"gospr/parser"
)

// CollectionSpec produces a runtime CRDT instance for a node.
type CollectionSpec interface {
	New(nodeID string) crdt.CRDT
}

// Model is the validated semantic representation of one vector type — the
// "proper AST". It is pure data (no closures), so later optimization passes
// can walk it. Model implements CollectionSpec.
type Model struct {
	Name    string
	Elem    parser.ElemType
	Merge   parser.Expr            // a Zip expr
	Queries map[string]crdt.Method // Reduce methods
	Updates map[string]crdt.Method // Local methods
}

func (m *Model) New(nodeID string) crdt.CRDT {
	return crdt.NewVector(nodeID, m.Merge, m.Queries, m.Updates)
}

type BuiltCollection struct {
	Name string
	Spec CollectionSpec
}

// BuiltPlan carries per-type models (present even when no collection is
// declared, so a model can be tested directly) plus instantiated collections.
type BuiltPlan struct {
	Models      map[string]*Model
	Collections []BuiltCollection
}

var knownOps = map[string]bool{"+": true, "*": true, "-": true, "max": true, "min": true}

func Build(plan parser.Plan) (BuiltPlan, error) {
	models := make(map[string]*Model, len(plan.Types))
	for _, td := range plan.Types {
		if _, dup := models[td.Name]; dup {
			return BuiltPlan{}, fmt.Errorf("type %s declared twice", td.Name)
		}
		if td.Elem.Kind != parser.KindReal {
			return BuiltPlan{}, fmt.Errorf("type %s: only `vector real` is supported", td.Name)
		}
		models[td.Name] = &Model{
			Name:    td.Name,
			Elem:    td.Elem,
			Queries: map[string]crdt.Method{},
			Updates: map[string]crdt.Method{},
		}
	}

	mergeSeen := make(map[string]bool)
	for _, md := range plan.Merges {
		m, ok := models[md.TypeName]
		if !ok {
			return BuiltPlan{}, fmt.Errorf("merge for unknown type %s", md.TypeName)
		}
		if mergeSeen[md.TypeName] {
			return BuiltPlan{}, fmt.Errorf("type %s has merge defined twice", md.TypeName)
		}
		if err := validateZip(md.Body); err != nil {
			return BuiltPlan{}, fmt.Errorf("merge %s: %w", md.TypeName, err)
		}
		m.Merge = md.Body
		mergeSeen[md.TypeName] = true
	}

	for _, qd := range plan.Queries {
		m, ok := models[qd.TypeName]
		if !ok {
			return BuiltPlan{}, fmt.Errorf("query for unknown type %s", qd.TypeName)
		}
		if _, dup := m.Queries[qd.MethodName]; dup {
			return BuiltPlan{}, fmt.Errorf("query %s.%s defined twice", qd.TypeName, qd.MethodName)
		}
		if len(qd.Params) != 0 {
			return BuiltPlan{}, fmt.Errorf("query %s.%s: query params are not yet supported", qd.TypeName, qd.MethodName)
		}
		if err := validateReduce(qd.Body); err != nil {
			return BuiltPlan{}, fmt.Errorf("query %s.%s: %w", qd.TypeName, qd.MethodName, err)
		}
		m.Queries[qd.MethodName] = crdt.Method{Params: qd.Params, Body: qd.Body}
	}

	for _, ud := range plan.Updates {
		m, ok := models[ud.TypeName]
		if !ok {
			return BuiltPlan{}, fmt.Errorf("update for unknown type %s", ud.TypeName)
		}
		if _, dup := m.Updates[ud.MethodName]; dup {
			return BuiltPlan{}, fmt.Errorf("update %s.%s defined twice", ud.TypeName, ud.MethodName)
		}
		if err := validateParams(ud.Params); err != nil {
			return BuiltPlan{}, fmt.Errorf("update %s.%s: %w", ud.TypeName, ud.MethodName, err)
		}
		if err := validateLocal(ud.Body, paramSet(ud.Params)); err != nil {
			return BuiltPlan{}, fmt.Errorf("update %s.%s: %w", ud.TypeName, ud.MethodName, err)
		}
		m.Updates[ud.MethodName] = crdt.Method{Params: ud.Params, Body: ud.Body}
	}

	// Every type must define a merge — a vector without a join isn't a CRDT.
	for name := range models {
		if !mergeSeen[name] {
			return BuiltPlan{}, fmt.Errorf("type %s has no merge defined", name)
		}
	}

	collections := make([]BuiltCollection, 0, len(plan.Collections))
	seenCollection := make(map[string]bool, len(plan.Collections))
	for _, cs := range plan.Collections {
		if seenCollection[cs.Name] {
			return BuiltPlan{}, fmt.Errorf("collection %s declared twice", cs.Name)
		}
		m, ok := models[cs.Type]
		if !ok {
			return BuiltPlan{}, fmt.Errorf("collection %s references unknown type %s", cs.Name, cs.Type)
		}
		seenCollection[cs.Name] = true
		collections = append(collections, BuiltCollection{Name: cs.Name, Spec: m})
	}

	return BuiltPlan{Models: models, Collections: collections}, nil
}

// ---- validators ----------------------------------------------------

func validateParams(ps []parser.ParamSpec) error {
	for _, p := range ps {
		if p.Type != "real" {
			return fmt.Errorf("param %s: unknown type %q (only real)", p.Name, p.Type)
		}
	}
	return nil
}

func paramSet(ps []parser.ParamSpec) map[string]bool {
	s := make(map[string]bool, len(ps))
	for _, p := range ps {
		s[p.Name] = true
	}
	return s
}

func validateFuncRef(e parser.Expr) error {
	if e.Kind != parser.ExprFuncRef {
		return fmt.Errorf("expected a binary function")
	}
	if !knownOps[e.Op] {
		return fmt.Errorf("unknown function %q", e.Op)
	}
	return nil
}

func validateZip(e parser.Expr) error {
	if e.Kind != parser.ExprZip || e.Fn == nil {
		return fmt.Errorf("merge must be `zip <fn>`")
	}
	return validateFuncRef(*e.Fn)
}

func validateReduce(e parser.Expr) error {
	if e.Kind != parser.ExprReduce || e.Fn == nil || e.Init == nil {
		return fmt.Errorf("query must be `reduce <fn> <init>`")
	}
	if err := validateFuncRef(*e.Fn); err != nil {
		return err
	}
	if e.Init.Kind != parser.ExprNumLit {
		return fmt.Errorf("reduce init must be a number")
	}
	return nil
}

func validateLocal(e parser.Expr, params map[string]bool) error {
	if e.Kind != parser.ExprLocal || e.Fn == nil {
		return fmt.Errorf("update must be `local <fn>`")
	}
	return validateSection(*e.Fn, params)
}

func validateSection(e parser.Expr, params map[string]bool) error {
	if e.Kind != parser.ExprSection || e.Arg == nil {
		return fmt.Errorf("expected a section like (+ k)")
	}
	if !knownOps[e.Op] {
		return fmt.Errorf("unknown operator %q in section", e.Op)
	}
	switch e.Arg.Kind {
	case parser.ExprNumLit:
		return nil
	case parser.ExprParamRef:
		if !params[e.Arg.Param] {
			return fmt.Errorf("section references unknown param %q", e.Arg.Param)
		}
		return nil
	default:
		return fmt.Errorf("section argument must be a number or param ref")
	}
}
