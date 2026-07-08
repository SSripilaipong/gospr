package parser

import (
	"fmt"
	"math/big"
)

// ---- Element types -------------------------------------------------

// ElemKind tags what a `type` definition describes. A struct type is a set of
// named fields (`type X = { Pos rat0+ }`); a vector type is a distributed vector
// whose element is another type (`type VX = vector X`, `type T = vector rat0+`).
type ElemKind int

const (
	KindStruct  ElemKind = iota // struct type: Fields
	KindVector                  // vector type: element in Elem (token) or Inner (inline struct)
	KindElemRef                 // element-ref alias: `type X = V.Elem` — dotted token in Elem
)

// ElemType describes a `type` definition. For KindVector the element is either a
// *token* in Elem — a numeric type name (rat, rat0+, …), a user struct/alias type
// name, or a dotted `V.Elem` reference — or, when the element is written inline as
// a struct body (`vector { ... }`), the nested struct ElemType in Inner (exactly
// one of Elem/Inner is set). For KindStruct, Fields holds the ordered field list.
// For KindElemRef, Elem holds the dotted `Base.Elem` reference token. The builder
// resolves every token (the parser stays semantics-free).
type ElemType struct {
	Kind   ElemKind
	Elem   string      // KindVector (token element) / KindElemRef: type token
	Inner  *ElemType   // KindVector: inline struct element (mutually exclusive with Elem)
	Fields []FieldSpec // KindStruct: ordered fields
}

// FieldSpec is one `Name Type` line of a struct type. Type is a token (a numtype
// name or another struct type name), resolved by the builder — a field may be
// nested (its type is another struct type).
type FieldSpec struct {
	Name string
	Type string
}

// ---- Expression AST ------------------------------------------------

// ExprKind tags the Expr closed-sum union. The language is a small
// applicative core: literals, variables, function references, and
// application, plus the three CRDT combinator nodes (reduce/zip/local)
// that carry a function-valued term.
type ExprKind int

const (
	ExprNumLit    ExprKind = iota // numeric literal: 0, 1, 2.5
	ExprStrLit                    // string literal: "hello"
	ExprName                      // PARSER-ONLY: unresolved identifier or operator token
	ExprVar                       // BUILT-ONLY: a bound parameter reference
	ExprRef                       // BUILT-ONLY: a function symbol (primitive or user fn)
	ExprApp                       // application f a b ... (possibly partial)
	ExprGuards                    // guarded body: | cond = result ... | otherwise = result
	ExprStructLit                 // struct construction: { Name: expr, ... }
	ExprField                     // field access: target.Field
	ExprReduce                    // reduce <fn> <init>
	ExprZip                       // zip <fn>
	ExprLocal                     // local <fn>
)

// ValType is a value's type. The language has three: a rational number, a boolean
// (produced only by comparison operators), and a string (produced only by
// string literals). Params are numeric-only; bool/string arise from comparisons,
// string literals, and inferred function return types.
type ValType int

const (
	TypeReal ValType = iota
	TypeBool
	TypeString
)

func (t ValType) String() string {
	switch t {
	case TypeReal:
		return "rat"
	case TypeBool:
		return "bool"
	case TypeString:
		return "string"
	default:
		return "unknown"
	}
}

// RefKind distinguishes a built-in primitive operator from a user-defined
// function. Only set on a resolved ExprRef.
type RefKind int

const (
	RefPrimitive RefKind = iota
	RefFunction
)

// Expr is a closed sum type. Only the fields relevant to Kind are set.
// No closures — fully serializable, so later optimization/proof passes
// can walk it.
//
// Invariant: ExprName appears ONLY in parser output (pre-Build); ExprVar
// and ExprRef appear ONLY after Build resolves names against scope. A
// built term therefore has no unresolved leaves — every leaf is a Var
// (a bound variable) or a Ref (a function symbol), unambiguously.
type Expr struct {
	Kind ExprKind

	// ExprNumLit — an exact finite decimal, held as an exact rational (so e.g.
	// 0.1 is exactly 1/10, with no float rounding that could desync runtime from
	// the convergence proof).
	Num *big.Rat

	// ExprStrLit
	Str string

	// ExprName / ExprVar / ExprRef: the identifier or operator symbol.
	Name string

	// ExprRef (resolved): the symbol's arity and whether it is a built-in
	// primitive or a user-defined function.
	Arity int
	Ref   RefKind

	// ExprApp: Head applied to Args (currying-friendly; len(Args) may be
	// fewer than the head's arity, i.e. a partial application).
	Head *Expr
	Args []*Expr

	// ExprReduce / ExprZip / ExprLocal: Fn is the function-valued term
	// (a Ref or a partial App). Reduce additionally uses Init (a literal —
	// a NumLit or a StructLit).
	Fn   *Expr
	Init *Expr

	// ExprGuards: ordered guard cases, the last of which must be `otherwise`.
	Cases []GuardCase

	// ExprStructLit: ordered field constructions { Name: Value, ... }.
	StructFields []StructField

	// ExprField: project Field out of the struct value Target.
	Target *Expr
	Field  string
}

// StructField is one `Name: expr` construction in a struct literal. Order is
// preserved so the built Plan (and thus the Fingerprint) is stable.
type StructField struct {
	Name  string
	Value *Expr
}

// GuardCase is one `| cond = result` line of a guarded function body. The
// terminal `| otherwise = result` is represented with Otherwise == true and a
// nil Cond — `otherwise` is a syntactic marker, never an expression, so it
// cannot be forged by an always-true condition.
type GuardCase struct {
	Cond      *Expr // nil when Otherwise
	Result    *Expr
	Otherwise bool
}

// ---- Method params -------------------------------------------------

// ParamSpec is `name::type`. Type is one of the six numeric names (e.g. "rat").
type ParamSpec struct {
	Name string
	Type string
}

// ---- Flat parser-level defs ---------------------------------------

type TypeDef struct {
	Name string
	Elem ElemType
}

// FnDef is a top-level user-defined function `fn name p1::rat .. = body`.
// Functions are global (not attached to a type). Body is an unresolved
// applicative term until Build resolves it. RetType is the optional `-> type`
// return annotation (a raw type token, "" when absent); the builder resolves and
// enforces it.
type FnDef struct {
	Name    string
	Params  []ParamSpec
	Body    Expr
	RetType string `json:",omitempty"`
}

type MergeDef struct {
	TypeName string
	Body     Expr // a Zip expr
}

type QueryDef struct {
	TypeName   string
	MethodName string
	Params     []ParamSpec // scalar-numeric params, bound into the body at query time
	Body       Expr        // a general value expression (may wrap a `reduce`)
}

type UpdateDef struct {
	TypeName   string
	MethodName string
	Params     []ParamSpec
	Body       Expr // a Local expr
}

type CollectionSpec struct {
	Name string
	Type string // user-defined type name, no args
}

type Plan struct {
	Types       []TypeDef
	Functions   []FnDef
	Merges      []MergeDef
	Queries     []QueryDef
	Updates     []UpdateDef
	Collections []CollectionSpec
}

// ---- Errors --------------------------------------------------------

type ParseError struct {
	Pos     int
	Line    int
	Col     int
	Message string
}

func (e ParseError) Error() string {
	return fmt.Sprintf("parse error at line %d col %d: %s", e.Line, e.Col, e.Message)
}
