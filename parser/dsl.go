package parser

import "strconv"

type lineResult struct {
	typeDef    *TypeDef
	fnDef      *FnDef
	mergeDef   *MergeDef
	queryDef   *QueryDef
	updateDef  *UpdateDef
	collection *CollectionSpec
}

func newlineOrEOF() Parser[struct{}] {
	return Or(Discard(RuneP('\n')), EOF())
}

func digitsP() Parser[string] {
	return Map(
		Many1(Satisfy(func(r rune) bool { return r >= '0' && r <= '9' }, "digit")),
		func(rs []rune) string { return string(rs) },
	)
}

// numberP parses a non-negative decimal literal (a leading '-' is the '-'
// operator, never a literal sign; negative literals are deferred).
func numberP() Parser[float64] {
	frac := Or(
		Try(Map(Sequence2(RuneP('.'), digitsP()), func(t Of2[rune, string]) string { return "." + t.V2 })),
		Succeed(""),
	)
	return func(s Stream) ParseResult[float64] {
		r := Sequence2(digitsP(), frac)(s)
		if !r.Ok {
			return failure[float64](r.Err, r.Consumed)
		}
		text := r.Value.V1 + r.Value.V2
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			line, col := runePos(s.Items, s.Pos)
			return failure[float64](ParseError{Pos: s.Pos, Line: line, Col: col, Message: "invalid number: " + text}, r.Consumed)
		}
		return success(f, r.Next, r.Consumed)
	}
}

// symOpP parses a punctuation operator token: + * -. The word-shaped
// primitives (max, min) are NOT tokenised here — they parse as ordinary
// identifiers (see nameP) and are recognised as primitives by the builder.
// Tokenising them here would misparse identifiers like `maxValue`, since
// StringP("max") has no word boundary.
func symOpP() Parser[string] {
	lit := func(str string) Parser[string] { return Try(StringP(str)) }
	return Or(lit("+"), Or(lit("*"), lit("-")))
}

// paramP parses `name::type`, e.g. `k::real`.
func paramP() Parser[ParamSpec] {
	dcolon := Sequence2(RuneP(':'), RuneP(':'))
	return Map(
		Sequence3(IdentP(), dcolon, IdentP()),
		func(t Of3[string, Of2[rune, rune], string]) ParamSpec {
			return ParamSpec{Name: t.V1, Type: t.V3}
		},
	)
}

// paramsP parses zero-or-more space-separated params after a method name.
// Each param is Try-wrapped so Many stops cleanly when the next token is the
// `=` header terminator rather than another param.
func paramsP() Parser[[]ParamSpec] {
	return Many(Try(Prefix(Spaces1P(), paramP())))
}

// paramsP1 is paramsP but requires at least one param. Used by `fn`, which
// rejects zero-arg functions (every fn is real^n -> real, n >= 1).
func paramsP1() Parser[[]ParamSpec] {
	return Many1(Try(Prefix(Spaces1P(), paramP())))
}

// nameP parses an applicative leaf: an operator token (+ * -) or an
// identifier (which may be a primitive name like max/min, a user fn, or a
// bound variable — the builder resolves which). Emits an unresolved Name.
func nameP() Parser[Expr] {
	return Map(Or(symOpP(), IdentP()), func(n string) Expr { return Expr{Kind: ExprName, Name: n} })
}

// atomP parses a single argument-position term: a number, a parenthesised
// expression, or a name. Application is built from a head atom + trailing
// atoms (see applicationP).
func atomP() Parser[Expr] {
	num := Map(Try(numberP()), func(f float64) Expr { return Expr{Kind: ExprNumLit, Num: f} })
	paren := Map(
		Sequence3(RuneP('('), Prefix(SpacesP(), exprP()), Sequence2(SpacesP(), RuneP(')'))),
		func(t Of3[rune, Expr, Of2[struct{}, rune]]) Expr { return t.V2 },
	)
	return Or(num, Or(paren, nameP()))
}

// applicationP parses `head arg1 arg2 ...` (prefix application). A bare atom
// with no trailing args is returned as-is; otherwise an ExprApp. Args may
// under-saturate the head (partial application) — checked at build time.
func applicationP() Parser[Expr] {
	return Map(
		Sequence2(atomP(), Many(Try(Prefix(Spaces1P(), atomP())))),
		func(t Of2[Expr, []Expr]) Expr {
			if len(t.V2) == 0 {
				return t.V1
			}
			head := t.V1
			args := make([]*Expr, len(t.V2))
			for i := range t.V2 {
				a := t.V2[i]
				args[i] = &a
			}
			return Expr{Kind: ExprApp, Head: &head, Args: args}
		},
	)
}

// exprP parses a full applicative expression. It is recursive (parenthesised
// atoms contain expressions), so the body is deferred to parse time to break
// the parser-construction cycle.
func exprP() Parser[Expr] {
	return func(s Stream) ParseResult[Expr] {
		return applicationP()(s)
	}
}

// elemTypeP parses the vector element type. Only `vector real` for now.
func elemTypeP() Parser[ElemType] {
	return Map(
		Sequence3(StringP("vector"), Spaces1P(), StringP("real")),
		func(_ Of3[string, struct{}, string]) ElemType { return ElemType{Kind: KindReal} },
	)
}

func eqP() Parser[Of3[struct{}, rune, struct{}]] {
	return Sequence3(SpacesP(), RuneP('='), SpacesP())
}

func endP() Parser[Of2[struct{}, struct{}]] {
	return Sequence2(SpacesP(), newlineOrEOF())
}

func typeDefLineP() Parser[lineResult] {
	prefix := Try(Sequence2(StringP("type"), Spaces1P()))
	rest := Map(
		Sequence2(IdentP(), Prefix(eqP(), Suffix(endP(), elemTypeP()))),
		func(t Of2[string, ElemType]) lineResult {
			return lineResult{typeDef: &TypeDef{Name: t.V1, Elem: t.V2}}
		},
	)
	return Prefix(prefix, rest)
}

// fnLineP parses `fn name p1::real p2::real = <expr>`. The body is a full
// applicative expression; at least one param is required.
func fnLineP() Parser[lineResult] {
	prefix := Try(Sequence2(StringP("fn"), Spaces1P()))
	rest := Map(
		Sequence2(Sequence2(IdentP(), paramsP1()), Prefix(eqP(), Suffix(endP(), exprP()))),
		func(t Of2[Of2[string, []ParamSpec], Expr]) lineResult {
			return lineResult{fnDef: &FnDef{Name: t.V1.V1, Params: t.V1.V2, Body: t.V2}}
		},
	)
	return Prefix(prefix, rest)
}

func mergeLineP() Parser[lineResult] {
	prefix := Try(Sequence2(StringP("merge"), Spaces1P()))
	// `zip <atom>`: the function is a single atom (a name or a parenthesised
	// term), never a bare application — keeps the line grammar unambiguous.
	zipExpr := Map(
		Prefix(Sequence2(StringP("zip"), Spaces1P()), atomP()),
		func(fn Expr) Expr {
			f := fn
			return Expr{Kind: ExprZip, Fn: &f}
		},
	)
	rest := Map(
		Sequence2(IdentP(), Prefix(eqP(), Suffix(endP(), zipExpr))),
		func(t Of2[string, Expr]) lineResult {
			return lineResult{mergeDef: &MergeDef{TypeName: t.V1, Body: t.V2}}
		},
	)
	return Prefix(prefix, rest)
}

func queryLineP() Parser[lineResult] {
	prefix := Try(Sequence2(StringP("query"), Spaces1P()))
	lhs := Sequence4(IdentP(), RuneP('.'), IdentP(), paramsP())
	// `reduce <atom> <number>`: atom (not full application) for the fn so the
	// trailing init number is not swallowed as an argument.
	reduceExpr := Map(
		Sequence3(Prefix(Sequence2(StringP("reduce"), Spaces1P()), atomP()), Spaces1P(), numberP()),
		func(t Of3[Expr, struct{}, float64]) Expr {
			fn := t.V1
			init := Expr{Kind: ExprNumLit, Num: t.V3}
			return Expr{Kind: ExprReduce, Fn: &fn, Init: &init}
		},
	)
	rest := Map(
		Sequence2(lhs, Prefix(eqP(), Suffix(endP(), reduceExpr))),
		func(t Of2[Of4[string, rune, string, []ParamSpec], Expr]) lineResult {
			return lineResult{queryDef: &QueryDef{TypeName: t.V1.V1, MethodName: t.V1.V3, Params: t.V1.V4, Body: t.V2}}
		},
	)
	return Prefix(prefix, rest)
}

func updateLineP() Parser[lineResult] {
	prefix := Try(Sequence2(StringP("update"), Spaces1P()))
	lhs := Sequence4(IdentP(), RuneP('.'), IdentP(), paramsP())
	// `local <atom>`: e.g. `local (+ k)` (a parenthesised partial application).
	localExpr := Map(
		Prefix(Sequence2(StringP("local"), Spaces1P()), atomP()),
		func(fn Expr) Expr {
			f := fn
			return Expr{Kind: ExprLocal, Fn: &f}
		},
	)
	rest := Map(
		Sequence2(lhs, Prefix(eqP(), Suffix(endP(), localExpr))),
		func(t Of2[Of4[string, rune, string, []ParamSpec], Expr]) lineResult {
			return lineResult{updateDef: &UpdateDef{TypeName: t.V1.V1, MethodName: t.V1.V3, Params: t.V1.V4, Body: t.V2}}
		},
	)
	return Prefix(prefix, rest)
}

func collectionLineP() Parser[lineResult] {
	prefix := Try(Sequence2(StringP("collection"), Spaces1P()))
	rest := Map(
		Sequence2(IdentP(), Prefix(eqP(), Suffix(endP(), IdentP()))),
		func(t Of2[string, string]) lineResult {
			return lineResult{collection: &CollectionSpec{Name: t.V1, Type: t.V2}}
		},
	)
	return Prefix(prefix, rest)
}

func skipLineP() Parser[lineResult] {
	return Map(Suffix(newlineOrEOF(), TillEOL()), func(_ string) lineResult { return lineResult{} })
}

func lineP() Parser[lineResult] {
	return Or(typeDefLineP(),
		Or(fnLineP(),
			Or(mergeLineP(),
				Or(queryLineP(),
					Or(updateLineP(),
						Or(collectionLineP(),
							skipLineP()))))))
}

func dslParser() Parser[Plan] {
	return Map(
		Suffix(EOF(), Many(lineP())),
		func(results []lineResult) Plan {
			var plan Plan
			for _, r := range results {
				switch {
				case r.typeDef != nil:
					plan.Types = append(plan.Types, *r.typeDef)
				case r.fnDef != nil:
					plan.Functions = append(plan.Functions, *r.fnDef)
				case r.mergeDef != nil:
					plan.Merges = append(plan.Merges, *r.mergeDef)
				case r.queryDef != nil:
					plan.Queries = append(plan.Queries, *r.queryDef)
				case r.updateDef != nil:
					plan.Updates = append(plan.Updates, *r.updateDef)
				case r.collection != nil:
					plan.Collections = append(plan.Collections, *r.collection)
				}
			}
			return plan
		},
	)
}
