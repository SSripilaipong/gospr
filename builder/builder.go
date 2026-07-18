package builder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"gospr/crdt"
	"gospr/parser"
	"gospr/prover"
)

// CollectionSpec produces a runtime CRDT instance for a node.
type CollectionSpec interface {
	New(nodeID string) crdt.CRDT
}

// Model is the validated semantic representation of one vector type — the
// "proper AST". It is pure data (no closures), so later optimization/proof
// passes can walk it. Model implements CollectionSpec.
type Model struct {
	Name    string
	Elem    crdt.ElemT             // resolved element descriptor (scalar numeric or a struct)
	Merge   parser.Expr            // a Zip expr (Fn resolved)
	Queries map[string]crdt.Method // Reduce methods (bodies resolved)
	Updates map[string]crdt.Method // Local methods (bodies resolved)
	Funcs   map[string]crdt.Function
}

func (m *Model) New(nodeID string) crdt.CRDT {
	return crdt.NewVector(nodeID, m.Elem, m.Merge, m.Queries, m.Updates, m.Funcs)
}

type BuiltCollection struct {
	Name string
	Key  *parser.ParamSpec // nil for singleton; otherwise normalized scalar key metadata
	Spec CollectionSpec
}

// BuiltPlan carries per-type models (present even when no collection is
// declared, so a model can be tested directly), the global function
// environment, and instantiated collections.
type BuiltPlan struct {
	Models      map[string]*Model
	Functions   map[string]crdt.Function
	Collections []BuiltCollection
	// Fingerprint identifies "same deployed DSL" across nodes: a hash of a
	// canonical encoding of the input parser.Plan. Two nodes that received the
	// same deploy share it, so a linearized op's peer can ack only when it runs
	// the same plan (a same-name/different-semantics collection is rejected).
	Fingerprint string
}

func Build(plan parser.Plan) (BuiltPlan, error) {
	// Resolve every `type` definition into either a struct descriptor or a vector
	// Model. The token resolver classifies a type-position name as a numeric type
	// (numtype.Parse) or a user struct type, and rejects the numeric names /
	// `vector` keyword as user type names so a type position is unambiguous.
	types, err := resolveTypes(plan.Types)
	if err != nil {
		return BuiltPlan{}, err
	}
	models := types.models

	// Collect function arities up front so bodies may reference functions
	// defined later or themselves (recursion is allowed).
	fnArity := make(map[string]int, len(plan.Functions))
	for _, fd := range plan.Functions {
		if _, dup := fnArity[fd.Name]; dup {
			return BuiltPlan{}, fmt.Errorf("function %s declared twice", fd.Name)
		}
		if _, clash := primitiveArity[fd.Name]; clash {
			return BuiltPlan{}, fmt.Errorf("function %s shadows a built-in primitive", fd.Name)
		}
		if len(fd.Params) == 0 {
			return BuiltPlan{}, fmt.Errorf("function %s: must take at least one parameter", fd.Name)
		}
		// A `fn` param may be numeric OR struct-typed (fns receive struct slots
		// from combinators). It must resolve to a known type.
		if err := validateFnParams(fd.Params, types); err != nil {
			return BuiltPlan{}, fmt.Errorf("function %s: %w", fd.Name, err)
		}
		fnArity[fd.Name] = len(fd.Params)
	}

	env := newEnv(fnArity)

	// Resolve each function body against its parameter scope. `reduce` is a pure
	// fold that takes its vector explicitly, so it may appear in a fn body that has
	// a vector-typed param; the type checker rejects it where no vector is in scope.
	funcs := make(map[string]crdt.Function, len(plan.Functions))
	chk := newChecker(types)
	for _, fd := range plan.Functions {
		scope := paramSet(fd.Params)
		body, err := env.resolve(fd.Body, scope)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("function %s: %w", fd.Name, err)
		}
		if a := arityOf(body); a != 0 {
			return BuiltPlan{}, fmt.Errorf("function %s: body must return a value, but is missing %d argument(s)", fd.Name, a)
		}
		if err := chk.register(fd.Name, fd.Params, body, fd.RetType); err != nil {
			return BuiltPlan{}, fmt.Errorf("function %s: %w", fd.Name, err)
		}
		funcs[fd.Name] = crdt.Function{Name: fd.Name, Params: fd.Params, Body: body}
	}

	// Type-check every function body and infer its return type (Option A:
	// unanchored recursion whose return type can't be determined is rejected).
	for _, fd := range plan.Functions {
		if _, err := chk.inferReturn(fd.Name); err != nil {
			return BuiltPlan{}, fmt.Errorf("function %s: %w", fd.Name, err)
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
		merge, err := env.resolveCombinator(md.Body, nil)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("merge %s: %w", md.TypeName, err)
		}
		if err := chk.checkCombinatorFn(merge, nil, m.Elem); err != nil {
			return BuiltPlan{}, fmt.Errorf("merge %s: %w", md.TypeName, err)
		}
		m.Merge = merge
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
		// Query params cross the HTTP boundary (bindParams), which handles only
		// scalar numeric values — so a struct-typed query param is rejected, same
		// as an update param. Tokens (incl. `V.Elem`/aliases) are normalized to
		// concrete numtype names for the downstream wire/prover/swagger consumers.
		qparams, err := resolveScalarParams(qd.Params, types, false)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("query %s.%s: %w", qd.TypeName, qd.MethodName, err)
		}
		// A query body is a function of the whole vector: after its declared params
		// are bound (leftmost), it must still expect exactly one vector-typed arg
		// (X -> result), which the runtime supplies. `reduce` folds that vector
		// inside a helper fn. The result type (rat/bool/string/struct) is recorded
		// for serialization/swagger; a vector result is rejected (not serializable).
		body, err := env.resolve(qd.Body, paramSet(qparams))
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("query %s.%s: %w", qd.TypeName, qd.MethodName, err)
		}
		qscope, err := chk.paramScope(qparams)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("query %s.%s: %w", qd.TypeName, qd.MethodName, err)
		}
		result, err := chk.checkQueryFn(body, qscope, m.Elem)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("query %s.%s: %w", qd.TypeName, qd.MethodName, err)
		}
		method := crdt.Method{Params: qparams, Body: body}
		switch result.kind {
		case vkStruct:
			et, err := elemTOf(result)
			if err != nil {
				return BuiltPlan{}, fmt.Errorf("query %s.%s result: %w", qd.TypeName, qd.MethodName, err)
			}
			method.ResultStruct = &et
			method.Result = parser.TypeReal // unused for a struct result; ResultStruct drives swagger
		case vkNum:
			method.Result = parser.TypeReal
			method.ResultNum = result.num
		case vkBool:
			method.Result = parser.TypeBool
		case vkString:
			method.Result = parser.TypeString
		default:
			return BuiltPlan{}, fmt.Errorf("query %s.%s result: internal: unsupported result type %s", qd.TypeName, qd.MethodName, result)
		}
		m.Queries[qd.MethodName] = method
	}

	for _, ud := range plan.Updates {
		m, ok := models[ud.TypeName]
		if !ok {
			return BuiltPlan{}, fmt.Errorf("update for unknown type %s", ud.TypeName)
		}
		if _, dup := m.Updates[ud.MethodName]; dup {
			return BuiltPlan{}, fmt.Errorf("update %s.%s defined twice", ud.TypeName, ud.MethodName)
		}
		// Update params cross the HTTP boundary (bindParams), which handles only
		// scalar numeric values — so a struct-typed update param is rejected;
		// tokens (incl. `V.Elem`/aliases) are normalized to concrete numtype names.
		uparams, err := resolveScalarParams(ud.Params, types, true)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("update %s.%s: %w", ud.TypeName, ud.MethodName, err)
		}
		body, err := env.resolveCombinator(ud.Body, paramSet(uparams))
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("update %s.%s: %w", ud.TypeName, ud.MethodName, err)
		}
		uscope, err := chk.paramScope(uparams)
		if err != nil {
			return BuiltPlan{}, fmt.Errorf("update %s.%s: %w", ud.TypeName, ud.MethodName, err)
		}
		if err := chk.checkCombinatorFn(body, uscope, m.Elem); err != nil {
			return BuiltPlan{}, fmt.Errorf("update %s.%s: %w", ud.TypeName, ud.MethodName, err)
		}
		m.Updates[ud.MethodName] = crdt.Method{Params: uparams, Body: body, Result: parser.TypeReal}
	}

	// Every type must define a merge — a vector without a join isn't a CRDT.
	for _, name := range types.vectorOrder {
		if !mergeSeen[name] {
			return BuiltPlan{}, fmt.Errorf("type %s has no merge defined", name)
		}
	}
	if err := validateResolvedProgram(plan, models, funcs); err != nil {
		return BuiltPlan{}, err
	}

	// Prove convergence: the merge must be a join-semilattice and every update
	// inflationary in its induced order. Unprovable types are rejected (the
	// gateway surfaces this as a 400). Requires z3 on PATH.
	for _, name := range types.vectorOrder {
		m := models[name]
		if err := prover.Prove(m.Elem, m.Merge, m.Updates, funcs); err != nil {
			return BuiltPlan{}, fmt.Errorf("convergence proof failed for type %s: %w", m.Name, err)
		}
	}

	// Attach the shared function environment to every model so the runtime
	// can resolve Ref -> user function.
	for _, name := range types.vectorOrder {
		models[name].Funcs = funcs
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
		var key *parser.ParamSpec
		if cs.Key != nil {
			keys, err := resolveScalarParams([]parser.ParamSpec{*cs.Key}, types, true)
			if err != nil {
				return BuiltPlan{}, fmt.Errorf("collection %s key: %w", cs.Name, err)
			}
			key = &keys[0]
		}
		seenCollection[cs.Name] = true
		collections = append(collections, BuiltCollection{Name: cs.Name, Key: key, Spec: m})
	}

	return BuiltPlan{
		Models:      models,
		Functions:   funcs,
		Collections: collections,
		Fingerprint: fingerprint(plan),
	}, nil
}

// fingerprint hashes a canonical encoding of the input Plan. Plan is flat slices
// of defs (no maps), so json.Marshal is order-stable and yields the same digest
// on every node that received the same deploy. Expr.Num (*big.Rat) marshals via
// its TextMarshaler, so numeric literals are captured exactly.
func fingerprint(plan parser.Plan) string {
	data, err := json.Marshal(plan)
	if err != nil {
		// Plan is plain data with no un-marshalable fields; treat an unexpected
		// failure as an empty (always-mismatching) fingerprint rather than panicking.
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ---- validators ----------------------------------------------------

// validateFnParams validates a function's params: names unique and each type a
// known type. A `fn` may take numeric, string, or struct params (it receives slots
// from combinators) AND a whole-vector param (`v::X`, folded by `reduce` / supplied
// by `write`/query) — the latter names a vector type, which reg.resolveToken rejects
// as a leaf, so vectors are admitted here explicitly.
func validateFnParams(ps []parser.ParamSpec, reg typeReg) error {
	seen := make(map[string]bool, len(ps))
	for _, p := range ps {
		if !reg.isVector(p.Type) {
			if _, err := reg.resolveToken(p.Type); err != nil {
				return fmt.Errorf("param %s: %w", p.Name, err)
			}
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate param %s", p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

// resolveScalarParams validates and NORMALIZES method params / collection keys: names unique
// and each type resolving to a numeric type (not a struct). Update/query params
// cross the wire via bindParams (scalar-only), and downstream (bindParams, prover,
// swagger) parses the stored token via numtype.Parse — so a `V.Elem`/alias token
// must be rewritten to its concrete numtype name here. The returned slice preserves
// order and names with Type set to the resolved numtype's canonical name.
func resolveScalarParams(ps []parser.ParamSpec, reg typeReg, allowString bool) ([]parser.ParamSpec, error) {
	seen := make(map[string]bool, len(ps))
	out := make([]parser.ParamSpec, len(ps))
	for i, p := range ps {
		et, err := reg.resolveToken(p.Type)
		if err != nil {
			return nil, fmt.Errorf("param %s: %w", p.Name, err)
		}
		if et.Struct {
			return nil, fmt.Errorf("param %s: type %q must be a numeric type (struct-typed params are not supported here)", p.Name, p.Type)
		}
		if seen[p.Name] {
			return nil, fmt.Errorf("duplicate param %s", p.Name)
		}
		seen[p.Name] = true
		if et.Str {
			// Strings are faithful through POST update params and collection path
			// keys. GET query `?params=` is comma-split, so string query params stay
			// rejected (allowString=false there).
			if !allowString {
				return nil, fmt.Errorf("param %s: string params are not supported here (only update params may be strings)", p.Name)
			}
			out[i] = parser.ParamSpec{Name: p.Name, Type: "string"}
			continue
		}
		out[i] = parser.ParamSpec{Name: p.Name, Type: et.Num.String()}
	}
	return out, nil
}

func paramSet(ps []parser.ParamSpec) map[string]bool {
	s := make(map[string]bool, len(ps))
	for _, p := range ps {
		s[p.Name] = true
	}
	return s
}
