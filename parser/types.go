package parser

import "fmt"

// ---- Element types -------------------------------------------------

// ElemKind enumerates the element type of a vector. Only KindReal is
// produced now; KindStruct is reserved so `vector { x real }` can be
// added later without a Plan-shape change.
type ElemKind int

const (
	KindReal ElemKind = iota
	KindStruct // reserved, NOT parsed yet
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

// ExprKind tags the Expr closed-sum union.
type ExprKind int

const (
	ExprFuncRef  ExprKind = iota // bare binary fn: + * max - min
	ExprNumLit                   // numeric literal: 0, 1, 2.5
	ExprParamRef                 // reference to a method param: k, m
	ExprSection                  // partial application: (+ k), (* m)
	ExprReduce                   // reduce <fn> <init>
	ExprZip                      // zip <fn>
	ExprLocal                    // local <fn>
	ExprCompose                  // FUTURE: f . g (never produced now)
)

// Expr is a closed sum type. Only the fields relevant to Kind are set.
// No closures — fully serializable, so later optimization passes can
// walk it.
type Expr struct {
	Kind ExprKind

	// ExprFuncRef / ExprSection: Op is one of "+","*","max","-","min".
	Op string

	// ExprNumLit
	Num float64

	// ExprParamRef
	Param string

	// ExprSection: Arg is the bound right operand (a NumLit or ParamRef).
	// Section means \x -> x Op Arg.
	Arg *Expr

	// ExprReduce / ExprZip: Fn is the binary fn (a FuncRef).
	// ExprLocal: Fn is the unary fn (a Section).
	Fn *Expr

	// ExprReduce only.
	Init *Expr

	// ExprCompose (FUTURE): Left . Right. Unused now.
	Left  *Expr
	Right *Expr
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
