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

// stringLitP parses a double-quoted string literal with `\" \\ \n \t` escapes,
// e.g. "You got a A". On a non-quote first char it fails without consuming (so
// it backtracks in an Or); once the opening quote is consumed an unterminated
// or badly-escaped literal is a committed parse error.
func stringLitP() Parser[Expr] {
	return func(s Stream) ParseResult[Expr] {
		open := RuneP('"')(s)
		if !open.Ok {
			return failure[Expr](open.Err, open.Consumed)
		}
		var sb []rune
		cur := open.Next
		for {
			r, ok := cur.Head()
			if !ok {
				line, col := runePos(cur.Items, cur.Pos)
				return failure[Expr](ParseError{Pos: cur.Pos, Line: line, Col: col, Message: "unterminated string literal"}, true)
			}
			if r == '"' {
				return success(Expr{Kind: ExprStrLit, Str: string(sb)}, cur.Advance(), true)
			}
			if r == '\\' {
				nxt := cur.Advance()
				e, ok := nxt.Head()
				if !ok {
					line, col := runePos(nxt.Items, nxt.Pos)
					return failure[Expr](ParseError{Pos: nxt.Pos, Line: line, Col: col, Message: "unterminated escape in string literal"}, true)
				}
				switch e {
				case '"':
					sb = append(sb, '"')
				case '\\':
					sb = append(sb, '\\')
				case 'n':
					sb = append(sb, '\n')
				case 't':
					sb = append(sb, '\t')
				default:
					line, col := runePos(nxt.Items, nxt.Pos)
					return failure[Expr](ParseError{Pos: nxt.Pos, Line: line, Col: col, Message: "invalid escape \\" + string(e)}, true)
				}
				cur = nxt.Advance()
				continue
			}
			sb = append(sb, r)
			cur = cur.Advance()
		}
	}
}

// symOpP parses a punctuation operator token: arithmetic `+ * -` and the
// comparison operators `>= <= == /= > <`. The word-shaped primitives (max, min)
// are NOT tokenised here — they parse as ordinary identifiers (see nameP) and
// are recognised as primitives by the builder. Tokenising them here would
// misparse identifiers like `maxValue`, since StringP("max") has no word
// boundary.
//
// Multi-char tokens are tried before their single-char prefixes (`>=` before
// `>`); every alternative is Try-wrapped so a partial match backtracks cleanly.
// `=` alone is never an operator — it is the header/guard separator (eqP).
func symOpP() Parser[string] {
	lit := func(str string) Parser[string] { return Try(StringP(str)) }
	return Or(lit("+"), Or(lit("*"), Or(lit("-"),
		Or(lit(">="), Or(lit("<="), Or(lit("=="), Or(lit("/="),
			Or(lit(">"), lit("<")))))))))
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

// reduceFormP parses `reduce <atom> <number>`, e.g. `reduce max 0`. It is a
// value-producing primary (it folds the vector to a real), so it may appear
// anywhere an atom may — but the builder restricts it to query bodies, keeping
// global functions pure. The trailing reference to atomP is deferred to parse
// time to break the construction cycle (atomP itself lists reduceFormP).
func reduceFormP() Parser[Expr] {
	return Map(
		Sequence3(Prefix(Sequence2(StringP("reduce"), Spaces1P()), atomP()), Spaces1P(), numberP()),
		func(t Of3[Expr, struct{}, float64]) Expr {
			fn := t.V1
			init := Expr{Kind: ExprNumLit, Num: t.V3}
			return Expr{Kind: ExprReduce, Fn: &fn, Init: &init}
		},
	)
}

// atomP parses a single argument-position term: a number, a string literal, a
// `reduce` form, a parenthesised expression, or a name. Application is built
// from a head atom + trailing atoms (see applicationP). `reduce` is tried
// before nameP so the keyword is not read as an identifier (Try lets a name
// like `reducer` still parse).
func atomP() Parser[Expr] {
	num := Map(Try(numberP()), func(f float64) Expr { return Expr{Kind: ExprNumLit, Num: f} })
	str := stringLitP()
	reduceAtom := Try(func(s Stream) ParseResult[Expr] { return reduceFormP()(s) })
	paren := Map(
		Sequence3(RuneP('('), Prefix(SpacesP(), exprP()), Sequence2(SpacesP(), RuneP(')'))),
		func(t Of3[rune, Expr, Of2[struct{}, rune]]) Expr { return t.V2 },
	)
	return Or(num, Or(str, Or(reduceAtom, Or(paren, nameP()))))
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

// guardLineP parses one `| cond = result` line of a guarded body, including its
// trailing end-of-line. The terminal `| otherwise = result` is recognised
// specially and emitted as GuardCase{Otherwise: true} (a marker, not an
// expression) so the builder can enforce that the last — and only the last —
// case is `otherwise`.
func guardLineP() Parser[GuardCase] {
	bar := Sequence2(SpacesP(), RuneP('|'))
	resultPart := Prefix(eqP(), Suffix(endP(), exprP())) // ` = result` + endP
	otherwiseCase := Map(
		Sequence2(StringP("otherwise"), resultPart),
		func(t Of2[string, Expr]) GuardCase {
			r := t.V2
			return GuardCase{Otherwise: true, Result: &r}
		},
	)
	normalCase := Map(
		Sequence2(exprP(), resultPart),
		func(t Of2[Expr, Expr]) GuardCase {
			c := t.V1
			r := t.V2
			return GuardCase{Cond: &c, Result: &r}
		},
	)
	return Prefix(Sequence2(bar, SpacesP()), Or(Try(otherwiseCase), normalCase))
}

// fnLineP parses a user function definition. Two body forms:
//   - single-line:  `fn name p1::real .. = <expr>`
//   - guarded:      `fn name p1::real ..` then one-or-more `| cond = result`
//     lines ending in `| otherwise = result`.
//
// The header consumes its own end-of-line in the guarded form, and each
// guardLineP consumes its own, so one fnLineP invocation legitimately spans
// several physical lines. At least one param is required.
func fnLineP() Parser[lineResult] {
	prefix := Try(Sequence2(StringP("fn"), Spaces1P()))
	header := Sequence2(IdentP(), paramsP1())
	singleBody := Prefix(eqP(), Suffix(endP(), exprP()))
	guardedBody := Map(
		Prefix(endP(), Many1(Try(guardLineP()))),
		func(cases []GuardCase) Expr { return Expr{Kind: ExprGuards, Cases: cases} },
	)
	rest := Map(
		Sequence2(header, Or(Try(singleBody), guardedBody)),
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
	// The body is a general expression that may wrap a `reduce` form, e.g.
	// `reduce + 0` or `myScore (reduce max 0)`. `reduce` parses as an atom (see
	// reduceFormP); the builder restricts it to query bodies.
	rest := Map(
		Sequence2(lhs, Prefix(eqP(), Suffix(endP(), exprP()))),
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

// barLineP rejects a stray `| ...` line. A guard line belongs to guarded-function
// syntax and is consumed by fnLineP; any `|` reaching the top level (e.g. a
// malformed guard after `otherwise`, or a `|` with no `fn` header) is a syntax
// error, not a blank/unknown line to skip. Detection is Try-wrapped so a line
// that merely has leading spaces but no `|` falls through to skipLineP; once a
// `|` is seen the failure is committed.
func barLineP() Parser[lineResult] {
	detect := Try(Sequence2(SpacesP(), RuneP('|')))
	return func(s Stream) ParseResult[lineResult] {
		if r := detect(s); !r.Ok {
			return failure[lineResult](r.Err, false)
		}
		line, col := runePos(s.Items, s.Pos)
		return failure[lineResult](ParseError{Pos: s.Pos, Line: line, Col: col, Message: "unexpected `|`: guard lines must directly follow a `fn` header"}, true)
	}
}

func lineP() Parser[lineResult] {
	return Or(typeDefLineP(),
		Or(fnLineP(),
			Or(mergeLineP(),
				Or(queryLineP(),
					Or(updateLineP(),
						Or(collectionLineP(),
							Or(barLineP(),
								skipLineP())))))))
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
