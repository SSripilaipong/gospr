package parser

import (
	"testing"
)

func TestParse_normal(t *testing.T) {
	plan, err := Parse("collection MyCounter = GCounter(0)\ncollection OtherCounter = GCounter(0)\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Collections) != 2 {
		t.Fatalf("expected 2 collections, got %d", len(plan.Collections))
	}
	if plan.Collections[0].Name != "MyCounter" || plan.Collections[0].Type != "GCounter" || plan.Collections[0].Args[0] != "0" {
		t.Errorf("unexpected first collection: %+v", plan.Collections[0])
	}
	if plan.Collections[1].Name != "OtherCounter" {
		t.Errorf("unexpected second collection: %+v", plan.Collections[1])
	}
}

func TestParse_skipBlanks(t *testing.T) {
	plan, err := Parse("\n\ncollection X = GCounter(1)\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Collections) != 1 || plan.Collections[0].Name != "X" {
		t.Errorf("unexpected result: %+v", plan.Collections)
	}
}

func TestParse_skipUnknownLines(t *testing.T) {
	plan, err := Parse("# comment\ncollection A = GCounter(0)\nunknown line here\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Collections) != 1 || plan.Collections[0].Name != "A" {
		t.Errorf("unexpected result: %+v", plan.Collections)
	}
}

func TestParse_multiArgs(t *testing.T) {
	plan, err := Parse("collection A = SomeType(foo, bar, 42)\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Collections) != 1 {
		t.Fatalf("expected 1 collection, got %d", len(plan.Collections))
	}
	c := plan.Collections[0]
	if c.Type != "SomeType" || len(c.Args) != 3 || c.Args[0] != "foo" || c.Args[1] != "bar" || c.Args[2] != "42" {
		t.Errorf("unexpected collection: %+v", c)
	}
}

func TestParse_empty(t *testing.T) {
	plan, err := Parse("")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Collections) != 0 {
		t.Errorf("expected 0 collections, got %d", len(plan.Collections))
	}
}

func TestParse_malformedReturnsError(t *testing.T) {
	_, err := Parse("collection Bad = GCounter\n")
	if err == nil {
		t.Fatal("expected error for malformed collection line, got nil")
	}
}

func TestParse_errorHasPosition(t *testing.T) {
	_, err := Parse("collection Bad = GCounter\n")
	pe, ok := err.(ParseError)
	if !ok {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
	if pe.Line == 0 || pe.Col == 0 {
		t.Errorf("expected non-zero line/col, got line=%d col=%d", pe.Line, pe.Col)
	}
}
