package parser

import "testing"

// Mandated integration test: parse the canonical snippet into the AST.
func TestParse_integration(t *testing.T) {
	src := `type T = vector real

merge T = zip max

query T.Value = reduce + 0

update T.Add k::real = local (+ k)
`
	plan, err := Parse(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// --- types ---
	if len(plan.Types) != 1 {
		t.Fatalf("want 1 type, got %d", len(plan.Types))
	}
	td := plan.Types[0]
	if td.Name != "T" || td.Elem.Kind != KindReal {
		t.Fatalf("type = %+v, want {T vector real}", td)
	}

	// --- merge ---
	if len(plan.Merges) != 1 {
		t.Fatalf("want 1 merge, got %d", len(plan.Merges))
	}
	md := plan.Merges[0]
	if md.TypeName != "T" {
		t.Fatalf("merge type = %q, want T", md.TypeName)
	}
	if md.Body.Kind != ExprZip || md.Body.Fn == nil || md.Body.Fn.Kind != ExprFuncRef || md.Body.Fn.Op != "max" {
		t.Fatalf("merge body = %+v, want zip max", md.Body)
	}

	// --- query ---
	if len(plan.Queries) != 1 {
		t.Fatalf("want 1 query, got %d", len(plan.Queries))
	}
	qd := plan.Queries[0]
	if qd.TypeName != "T" || qd.MethodName != "Value" || len(qd.Params) != 0 {
		t.Fatalf("query lhs = %+v, want T.Value no params", qd)
	}
	if qd.Body.Kind != ExprReduce || qd.Body.Fn == nil || qd.Body.Fn.Op != "+" ||
		qd.Body.Init == nil || qd.Body.Init.Kind != ExprNumLit || qd.Body.Init.Num != 0 {
		t.Fatalf("query body = %+v, want reduce + 0", qd.Body)
	}

	// --- update ---
	if len(plan.Updates) != 1 {
		t.Fatalf("want 1 update, got %d", len(plan.Updates))
	}
	ud := plan.Updates[0]
	if ud.TypeName != "T" || ud.MethodName != "Add" {
		t.Fatalf("update lhs = %+v, want T.Add", ud)
	}
	if len(ud.Params) != 1 || ud.Params[0].Name != "k" || ud.Params[0].Type != "real" {
		t.Fatalf("update params = %+v, want [k::real]", ud.Params)
	}
	if ud.Body.Kind != ExprLocal || ud.Body.Fn == nil {
		t.Fatalf("update body = %+v, want local <section>", ud.Body)
	}
	sec := ud.Body.Fn
	if sec.Kind != ExprSection || sec.Op != "+" || sec.Arg == nil ||
		sec.Arg.Kind != ExprParamRef || sec.Arg.Param != "k" {
		t.Fatalf("update section = %+v, want (+ k)", sec)
	}

	if len(plan.Collections) != 0 {
		t.Fatalf("want 0 collections, got %d", len(plan.Collections))
	}
}

func TestParse_collection(t *testing.T) {
	plan, err := Parse("collection MyVec = T\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Collections) != 1 {
		t.Fatalf("want 1 collection, got %d", len(plan.Collections))
	}
	c := plan.Collections[0]
	if c.Name != "MyVec" || c.Type != "T" {
		t.Fatalf("collection = %+v, want {MyVec T}", c)
	}
}

func TestParse_sectionNumberLiteral(t *testing.T) {
	plan, err := Parse("update T.Inc = local (+ 1)\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sec := plan.Updates[0].Body.Fn
	if sec.Op != "+" || sec.Arg.Kind != ExprNumLit || sec.Arg.Num != 1 {
		t.Fatalf("section = %+v, want (+ 1)", sec)
	}
}

func TestParse_decimalLiteral(t *testing.T) {
	plan, err := Parse("query T.V = reduce + 2.5\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := plan.Queries[0].Body.Init.Num; got != 2.5 {
		t.Fatalf("init = %v, want 2.5", got)
	}
}

func TestParse_operators(t *testing.T) {
	cases := map[string]string{
		"merge T = zip max\n": "max",
		"merge T = zip min\n": "min",
		"merge T = zip +\n":   "+",
		"merge T = zip *\n":   "*",
		"merge T = zip -\n":   "-",
	}
	for src, want := range cases {
		plan, err := Parse(src)
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", src, err)
		}
		if got := plan.Merges[0].Body.Fn.Op; got != want {
			t.Fatalf("%q: op = %q, want %q", src, got, want)
		}
	}
}

func TestParse_skipBlankAndUnknownLines(t *testing.T) {
	plan, err := Parse("\n# a comment\ntype T = vector real\n\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Types) != 1 {
		t.Fatalf("want 1 type, got %d", len(plan.Types))
	}
}

func TestParse_empty(t *testing.T) {
	plan, err := Parse("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Types) != 0 || len(plan.Collections) != 0 {
		t.Fatalf("want empty plan, got %+v", plan)
	}
}

// A recognized-but-malformed line is a parse error, not silently skipped,
// because Or is committed once the keyword prefix is consumed.
func TestParse_malformedTypeIsError(t *testing.T) {
	if _, err := Parse("type T = vector foo\n"); err == nil {
		t.Fatalf("expected parse error for `vector foo`")
	}
}

func TestParse_malformedMergeIsError(t *testing.T) {
	if _, err := Parse("merge T = zip notAnOp\n"); err == nil {
		t.Fatalf("expected parse error for unknown op token")
	}
}

func TestParse_errorHasPosition(t *testing.T) {
	_, err := Parse("type T = vector foo\n")
	if err == nil {
		t.Fatalf("expected error")
	}
	pe, ok := err.(ParseError)
	if !ok {
		t.Fatalf("error type = %T, want ParseError", err)
	}
	if pe.Line == 0 || pe.Col == 0 {
		t.Fatalf("error has no position: %+v", pe)
	}
}
