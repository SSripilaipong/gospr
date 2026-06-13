package parser

import "fmt"

// ---- Element types -------------------------------------------------

// ElemKind enumerates the element type of a vector. Only KindReal is
// produced now; KindStruct is reserved so `vector { x real }` can be
// added later without a Plan-shape change.
type ElemKind int

const (
	KindReal   ElemKind = iota
	KindStruct          // reserved, NOT parsed yet
)

// ElemType describes what each vector slot holds. For `vector real`,
// Kind == KindReal and Fields is nil. Struct support later fills Fields.
type ElemType struct {
	Kind   ElemKind
	Fields []FieldSpec // nil for KindReal; reserved for KindStruct
}

// FieldSpec is retained ONLY for the deferred struct case. It is unused
// by the current grammar but keeps the struct door open.
type FieldSpec struct {
	Name string
	Type ElemType
}

// ---- Expression AST ------------------------------------------------

// ExprKind tags the Expr closed-sum union. The language is a small
// applicative core: literals, variables, function references, and
// application, plus the three CRDT combinator nodes (reduce/zip/local)
// that carry a function-valued term.
type ExprKind int

const (
	ExprNumLit ExprKind = iota // numeric literal: 0, 1, 2.5
	ExprStrLit                 // string literal: "hello"
	ExprName                   // PARSER-ONLY: unresolved identifier or operator token
	ExprVar                    // BUILT-ONLY: a bound parameter reference
	ExprRef                    // BUILT-ONLY: a function symbol (primitive or user fn)
	ExprApp                    // application f a b ... (possibly partial)
	ExprGuards                 // guarded body: | cond = result ... | otherwise = result
	ExprReduce                 // reduce <fn> <init>
	ExprZip                    // zip <fn>
	ExprLocal                  // local <fn>
)

// ValType is a value's type. The language has three: a real number, a boolean
// (produced only by comparison operators), and a string (produced only by
// string literals). Params are real-only; bool/string arise from comparisons,
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
		return "real"
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

	// ExprNumLit
	Num float64

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
	// (a Ref or a partial App). Reduce additionally uses Init (a NumLit).
	Fn   *Expr
	Init *Expr

	// ExprGuards: ordered guard cases, the last of which must be `otherwise`.
	Cases []GuardCase
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

// ParamSpec is `name::type`. Type is "real" for now.
type ParamSpec struct {
	Name string
	Type string
}

// ---- Flat parser-level defs ---------------------------------------

type TypeDef struct {
	Name string
	Elem ElemType
}

// FnDef is a top-level user-defined function `fn name p1::real .. = body`.
// Functions are global (not attached to a type). Body is an unresolved
// applicative term until Build resolves it.
type FnDef struct {
	Name   string
	Params []ParamSpec
	Body   Expr
}

type MergeDef struct {
	TypeName string
	Body     Expr // a Zip expr
}

type QueryDef struct {
	TypeName   string
	MethodName string
	Params     []ParamSpec // empty now; AST allows future query params
	Body       Expr        // a Reduce expr
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
