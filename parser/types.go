package parser

import "fmt"

type CollectionSpec struct {
	Name string
	Type string
	Args []string
}

type ParamSpec struct {
	Name string
	Type string
}

type FieldSpec struct {
	Name     string
	CRDTType string
	Args     []string
}

type TypeDef struct {
	Name   string
	Params []ParamSpec
	Fields []FieldSpec
}

type MethodCall struct {
	Field  string
	Method string
	Args   []string
}

type QueryDef struct {
	TypeName   string
	MethodName string
	Body       MethodCall
}

type FieldUpdate struct {
	Field string
	Call  MethodCall
}

type UpdateDef struct {
	TypeName   string
	MethodName string
	Body       []FieldUpdate
}

type Plan struct {
	Collections []CollectionSpec
	Types       []TypeDef
	Queries     []QueryDef
	Updates     []UpdateDef
}

type ParseError struct {
	Pos     int
	Line    int
	Col     int
	Message string
}

func (e ParseError) Error() string {
	return fmt.Sprintf("parse error at line %d col %d: %s", e.Line, e.Col, e.Message)
}
