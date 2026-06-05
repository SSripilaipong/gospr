package parser

import "fmt"

type CollectionSpec struct {
	Name string
	Type string
	Args []string
}

type Plan struct {
	Collections []CollectionSpec
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
