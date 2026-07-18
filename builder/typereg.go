package builder

import (
	"fmt"
	"strings"

	"gospr/crdt"
	"gospr/numtype"
	"gospr/parser"
)

// ---- type registry -------------------------------------------------

// typeReg holds the resolved type universe: the vector Models (by type name), the
// resolved struct descriptors (by struct type name), and element-ref aliases
// (`type X = V.Elem`, by alias name). resolveToken classifies a type-position token
// as a numeric type, a named struct/alias type, or a dotted `V.Elem` reference.
type typeReg struct {
	models      map[string]*Model
	structs     map[string]crdt.ElemT
	aliases     map[string]crdt.ElemT
	// Orders preserve source declaration order. Maps above are lookup-only when
	// selecting which validation/proof error is reported first.
	typeOrder   []string
	structOrder []string
	aliasOrder  []string
	vectorOrder []string
}

// isVector reports whether a bare (non-dotted) type token names a resolved vector
// type. A dotted `V.Elem` is not a vector — it is that vector's element — so it is
// excluded here.
func (r typeReg) isVector(tok string) bool {
	if _, _, ok := splitDotted(tok); ok {
		return false
	}
	_, ok := r.models[tok]
	return ok
}

// splitDotted splits a `Base.Member` type token at its first `.`; ok is false for a
// non-dotted token. typeNameP admits at most one `.Member`, so a malformed member
// (a further dot) simply fails the `Elem` check downstream.
func splitDotted(tok string) (base, member string, ok bool) {
	i := strings.IndexByte(tok, '.')
	if i < 0 {
		return "", "", false
	}
	return tok[:i], tok[i+1:], true
}

// resolveToken maps a type-position token to a resolved element descriptor: a
// dotted `V.Elem` reference (the vector's element), a numeric type (numtype.Parse),
// a named struct type, or an element-ref alias. This is the post-resolveTypes form:
// every type is already memoized in the reg, so a `V.Elem` reads Model.Elem
// directly. A (non-dotted) vector type name is rejected — a vector cannot be a
// struct field or a param type.
func (r typeReg) resolveToken(tok string) (crdt.ElemT, error) {
	if base, member, ok := splitDotted(tok); ok {
		if member != "Elem" {
			return crdt.ElemT{}, fmt.Errorf("unknown type member %q in %q (only .Elem is supported)", member, tok)
		}
		m, ok := r.models[base]
		if !ok {
			return crdt.ElemT{}, fmt.Errorf("type %q is not a vector; cannot take `.Elem`", base)
		}
		return m.Elem, nil
	}
	if tok == "string" {
		return crdt.ElemT{Str: true}, nil
	}
	if nt, ok := numtype.Parse(tok); ok {
		return crdt.ElemT{Num: nt}, nil
	}
	if s, ok := r.structs[tok]; ok {
		return s, nil
	}
	if a, ok := r.aliases[tok]; ok {
		return a, nil
	}
	if _, ok := r.models[tok]; ok {
		return crdt.ElemT{}, fmt.Errorf("type %q is a vector and cannot be used as a field or param type", tok)
	}
	return crdt.ElemT{}, fmt.Errorf("unknown type %q", tok)
}

// resolveTypes folds the flat TypeDefs into a typeReg: struct type descriptors,
// vector Models (element = a token or an inline struct body), and element-ref
// aliases (`type X = V.Elem`). All three are resolved on demand and memoized,
// sharing one cycle-detection set — so an alias may be used as a struct field type
// or a vector element (it resolves *during* type resolution), and any cycle
// (recursive struct, `type V = vector V.Elem`, or an alias↔vector loop) is caught.
// A user type name may not shadow a numeric type name or the `vector` keyword, so
// every type-position token resolves unambiguously.
func resolveTypes(tds []parser.TypeDef) (typeReg, error) {
	reg := typeReg{
		models:  map[string]*Model{},
		structs: map[string]crdt.ElemT{},
		aliases: map[string]crdt.ElemT{},
	}
	structDefs := map[string]parser.ElemType{}
	vectorDefs := map[string]parser.ElemType{}
	aliasDefs := map[string]parser.ElemType{}
	seen := map[string]bool{}
	for _, td := range tds {
		if seen[td.Name] {
			return reg, fmt.Errorf("type %s declared twice", td.Name)
		}
		if _, isNum := numtype.Parse(td.Name); isNum || td.Name == "vector" || td.Name == "bool" || td.Name == "string" {
			return reg, fmt.Errorf("type name %q is reserved", td.Name)
		}
		seen[td.Name] = true
		reg.typeOrder = append(reg.typeOrder, td.Name)
		switch td.Elem.Kind {
		case parser.KindStruct:
			structDefs[td.Name] = td.Elem
			reg.structOrder = append(reg.structOrder, td.Name)
		case parser.KindVector:
			vectorDefs[td.Name] = td.Elem
			reg.vectorOrder = append(reg.vectorOrder, td.Name)
		case parser.KindElemRef:
			aliasDefs[td.Name] = td.Elem
			reg.aliasOrder = append(reg.aliasOrder, td.Name)
		default:
			return reg, fmt.Errorf("type %s: unsupported definition", td.Name)
		}
	}

	// The three resolvers share `resolving` for cross-category cycle detection.
	// vectorElem memoizes each vector's resolved element (also stored on Model.Elem).
	resolving := map[string]bool{}
	vectorElem := map[string]crdt.ElemT{}
	var (
		resolveToken      func(tok string) (crdt.ElemT, error)
		resolveFields     func(fs []parser.FieldSpec) ([]crdt.FieldT, error)
		resolveStruct     func(name string) (crdt.ElemT, error)
		resolveVectorElem func(name string) (crdt.ElemT, error)
		resolveAlias      func(name string) (crdt.ElemT, error)
	)

	resolveToken = func(tok string) (crdt.ElemT, error) {
		if base, member, ok := splitDotted(tok); ok {
			if member != "Elem" {
				return crdt.ElemT{}, fmt.Errorf("unknown type member %q in %q (only .Elem is supported)", member, tok)
			}
			return resolveVectorElem(base)
		}
		if tok == "string" {
			return crdt.ElemT{Str: true}, nil
		}
		if nt, ok := numtype.Parse(tok); ok {
			return crdt.ElemT{Num: nt}, nil
		}
		if _, ok := structDefs[tok]; ok {
			return resolveStruct(tok)
		}
		if _, ok := aliasDefs[tok]; ok {
			return resolveAlias(tok)
		}
		if _, ok := vectorDefs[tok]; ok {
			return crdt.ElemT{}, fmt.Errorf("type %q is a vector and cannot be used as a field or element type", tok)
		}
		return crdt.ElemT{}, fmt.Errorf("unknown type %q", tok)
	}

	// resolveFields resolves a struct field list (numtype, struct/alias, or a
	// `V.Elem` field type), with dup-field detection.
	resolveFields = func(fs []parser.FieldSpec) ([]crdt.FieldT, error) {
		fseen := map[string]bool{}
		fields := make([]crdt.FieldT, 0, len(fs))
		for _, f := range fs {
			if fseen[f.Name] {
				return nil, fmt.Errorf("duplicate field %q", f.Name)
			}
			fseen[f.Name] = true
			ft, err := resolveToken(f.Type)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", f.Name, err)
			}
			fields = append(fields, crdt.FieldT{Name: f.Name, Type: ft})
		}
		return fields, nil
	}

	resolveStruct = func(name string) (crdt.ElemT, error) {
		if s, ok := reg.structs[name]; ok {
			return s, nil
		}
		if resolving[name] {
			return crdt.ElemT{}, fmt.Errorf("recursive struct type %q", name)
		}
		resolving[name] = true
		fields, err := resolveFields(structDefs[name].Fields)
		resolving[name] = false
		if err != nil {
			return crdt.ElemT{}, fmt.Errorf("struct %s: %w", name, err)
		}
		st := crdt.ElemT{Struct: true, Name: name, Fields: fields}
		reg.structs[name] = st
		return st, nil
	}

	// resolveVectorElem resolves the element of vector `name`: an inline struct body
	// (`vector { ... }`, nominal Name = the vector's) or an element token.
	resolveVectorElem = func(name string) (crdt.ElemT, error) {
		if e, ok := vectorElem[name]; ok {
			return e, nil
		}
		vd, ok := vectorDefs[name]
		if !ok {
			if _, isStruct := structDefs[name]; isStruct {
				return crdt.ElemT{}, fmt.Errorf("type %q is a struct, not a vector; `.Elem` requires a vector", name)
			}
			return crdt.ElemT{}, fmt.Errorf("type %q is not a vector; cannot take `.Elem`", name)
		}
		if resolving[name] {
			return crdt.ElemT{}, fmt.Errorf("recursive type %q", name)
		}
		resolving[name] = true
		var elem crdt.ElemT
		var err error
		if vd.Inner != nil {
			var fields []crdt.FieldT
			fields, err = resolveFields(vd.Inner.Fields)
			if err == nil {
				elem = crdt.ElemT{Struct: true, Name: name, Fields: fields}
			}
		} else {
			elem, err = resolveToken(vd.Elem)
		}
		resolving[name] = false
		if err != nil {
			return crdt.ElemT{}, fmt.Errorf("type %s: %w", name, err)
		}
		vectorElem[name] = elem
		return elem, nil
	}

	// resolveAlias resolves `type name = <ref>.Elem`. A struct element is also
	// exposed as a named struct type (reg.structs), renamed to the alias, so `a::name`
	// and swagger treat it nominally; a numeric element lives only in reg.aliases.
	resolveAlias = func(name string) (crdt.ElemT, error) {
		if a, ok := reg.aliases[name]; ok {
			return a, nil
		}
		if resolving[name] {
			return crdt.ElemT{}, fmt.Errorf("recursive type %q", name)
		}
		resolving[name] = true
		elem, err := resolveToken(aliasDefs[name].Elem)
		resolving[name] = false
		if err != nil {
			return crdt.ElemT{}, fmt.Errorf("type %s: %w", name, err)
		}
		if elem.Struct {
			elem.Name = name
			reg.structs[name] = elem
		}
		reg.aliases[name] = elem
		return elem, nil
	}

	for _, name := range reg.typeOrder {
		if _, ok := structDefs[name]; ok {
			if _, err := resolveStruct(name); err != nil {
				return reg, err
			}
			continue
		}
		if _, ok := aliasDefs[name]; ok {
			if _, err := resolveAlias(name); err != nil {
				return reg, err
			}
			continue
		}
		if _, ok := vectorDefs[name]; ok {
			elem, err := resolveVectorElem(name)
			if err != nil {
				return reg, err
			}
			reg.models[name] = &Model{
				Name:    name,
				Elem:    elem,
				Queries: map[string]crdt.Method{},
				Updates: map[string]crdt.Method{},
			}
		}
	}
	return reg, nil
}
