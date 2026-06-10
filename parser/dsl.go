package parser

import "strconv"

type lineResult struct {
	typeDef    *TypeDef
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

// opP parses a known binary operator token. Each alternative is Try-wrapped
// so a partial StringP match (e.g. "max" against "min") resets Consumed and
// Or falls through to the next alternative.
func opP() Parser[string] {
	lit := func(str string) Parser[string] { return Try(StringP(str)) }
	return Or(lit("max"), Or(lit("min"), Or(lit("+"), Or(lit("*"), lit("-")))))
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

// operandP parses a section's bound argument: a number literal or a param ref.
func operandP() Parser[Expr] {
	num := Map(Try(numberP()), func(f float64) Expr { return Expr{Kind: ExprNumLit, Num: f} })
	ref := Map(IdentP(), func(n string) Expr { return Expr{Kind: ExprParamRef, Param: n} })
	return Or(num, ref)
}

// sectionP parses `( op operand )`, e.g. `(+ k)` or `(* m)`.
func sectionP() Parser[Expr] {
	return Map(
		Sequence5(RuneP('('), Prefix(SpacesP(), opP()), Prefix(SpacesP(), operandP()), SpacesP(), RuneP(')')),
		func(t Of5[rune, string, Expr, struct{}, rune]) Expr {
			arg := t.V3
			return Expr{Kind: ExprSection, Op: t.V2, Arg: &arg}
		},
	)
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

func mergeLineP() Parser[lineResult] {
	prefix := Try(Sequence2(StringP("merge"), Spaces1P()))
	zipExpr := Map(
		Prefix(Sequence2(StringP("zip"), Spaces1P()), opP()),
		func(op string) Expr {
			fn := Expr{Kind: ExprFuncRef, Op: op}
			return Expr{Kind: ExprZip, Fn: &fn}
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
	reduceExpr := Map(
		Sequence3(Prefix(Sequence2(StringP("reduce"), Spaces1P()), opP()), Spaces1P(), numberP()),
		func(t Of3[string, struct{}, float64]) Expr {
			fn := Expr{Kind: ExprFuncRef, Op: t.V1}
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
	localExpr := Map(
		Prefix(Sequence2(StringP("local"), Spaces1P()), sectionP()),
		func(sec Expr) Expr {
			fn := sec
			return Expr{Kind: ExprLocal, Fn: &fn}
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
		Or(mergeLineP(),
			Or(queryLineP(),
				Or(updateLineP(),
					Or(collectionLineP(),
						skipLineP())))))
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
