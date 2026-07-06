package parser

import "math/big"

type lineResult struct {
	typeDef    *TypeDef
	fnDef      *FnDef
	mergeDef   *MergeDef
	queryDef   *QueryDef
	updateDef  *UpdateDef
	collection *CollectionSpec
}

// commentP consumes a `#` and the rest of the physical line (up to but not
// including the newline). A comment is treated as insignificant whitespace
// everywhere a line may end (via newlineOrEOF) and, inside `{ ... }` bodies,
// wherever whitespace separates tokens (via wsP/ws1P). `#` is unused elsewhere in
// the grammar, and stringLitP consumes its own chars, so there is no collision.
func commentP() Parser[struct{}] {
	return Discard(Sequence2(RuneP('#'), TillEOL()))
}

func newlineOrEOF() Parser[struct{}] {
	return Prefix(Or(commentP(), Succeed(struct{}{})), Or(Discard(RuneP('\n')), EOF()))
}

// blankOrCommentP matches a line that is only optional spaces/tabs followed by an
// optional trailing comment, then the newline/EOF. It is the single definition of
// "a line with no statement on it" — reused by blankOrCommentLineP (top level) and
// as the inter-case separator inside guarded functions.
func blankOrCommentP() Parser[struct{}] {
	return Discard(Sequence2(SpacesP(), newlineOrEOF()))
}

func digitsP() Parser[string] {
	return Map(
		Many1(Satisfy(func(r rune) bool { return r >= '0' && r <= '9' }, "digit")),
		func(rs []rune) string { return string(rs) },
	)
}

// numberP parses an exact finite decimal literal with an optional leading '-'
// sign. The syntax is decimal-only (digits with an optional fractional part) —
// there is no '/' rational literal form in source. This is a deliberate design
// choice, not a loss of exactness: a decimal like 0.1 becomes *exactly* 1/10 with
// no float rounding (so the runtime and the convergence proof reason over the same
// value). The p/q rational form (e.g. "1/3") exists only as a wire-boundary
// convenience — keeping '/' out of source keeps ℚ closed with no division operator.
// A '-' must be immediately followed by digits, so `-5` is the literal negative
// five while `- 5` (with a space) is the '-' operator applied; the sole caller wraps
// numberP in Try, so consuming `-` and then failing on the space cleanly backtracks
// to the operator.
func numberP() Parser[*big.Rat] {
	sign := Or(Try(Map(RuneP('-'), func(rune) string { return "-" })), Succeed(""))
	frac := Or(
		Try(Map(Sequence2(RuneP('.'), digitsP()), func(t Of2[rune, string]) string { return "." + t.V2 })),
		Succeed(""),
	)
	return func(s Stream) ParseResult[*big.Rat] {
		r := Sequence3(sign, digitsP(), frac)(s)
		if !r.Ok {
			return failure[*big.Rat](r.Err, r.Consumed)
		}
		text := r.Value.V1 + r.Value.V2 + r.Value.V3
		q, ok := new(big.Rat).SetString(text)
		if !ok {
			line, col := runePos(s.Items, s.Pos)
			return failure[*big.Rat](ParseError{Pos: s.Pos, Line: line, Col: col, Message: "invalid number: " + text}, r.Consumed)
		}
		return success(q, r.Next, r.Consumed)
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

// typeNameP parses a type token in a type position: an identifier optionally
// followed by EITHER a `.Member` suffix (a `V.Elem` element reference) OR a single
// `+`/`-` numtype sign suffix. This covers the numeric type names (`rat0+`, `int`,
// …), user-defined struct/alias type names (`X`, `Inner`), and vector element
// references (`V.Elem`) in one word-bounded token, so `intThing` reads as the whole
// identifier rather than the numtype prefix `int`. The builder classifies the token
// (numtype vs named type vs dotted element ref) and validates that a dotted member
// is `Elem`. A dotted token never carries a sign, and a sign is only meaningful for
// numtype names; type positions never sit next to an expression, so eagerly
// consuming the trailing suffix here is unambiguous.
func typeNameP() Parser[string] {
	member := Try(Map(Sequence2(RuneP('.'), IdentP()), func(t Of2[rune, string]) string { return "." + t.V2 }))
	sign := Or(
		Try(Map(RuneP('+'), func(rune) string { return "+" })),
		Or(Try(Map(RuneP('-'), func(rune) string { return "-" })), Succeed("")),
	)
	return Map(Sequence2(IdentP(), Or(member, sign)), func(t Of2[string, string]) string { return t.V1 + t.V2 })
}

// paramP parses `name::type`, e.g. `k::rat0+` or `a::X`. The type is a numtype
// name or a user struct type name; the builder validates it (and, per position,
// whether a struct-typed param is allowed).
func paramP() Parser[ParamSpec] {
	dcolon := Sequence2(RuneP(':'), RuneP(':'))
	return Map(
		Sequence3(IdentP(), dcolon, typeNameP()),
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
// rejects zero-arg functions (every fn takes n >= 1 numeric params).
func paramsP1() Parser[[]ParamSpec] {
	return Many1(Try(Prefix(Spaces1P(), paramP())))
}

// nameP parses an applicative leaf: an operator token (+ * -) or an
// identifier (which may be a primitive name like max/min, a user fn, or a
// bound variable — the builder resolves which). Emits an unresolved Name.
func nameP() Parser[Expr] {
	return Map(Or(symOpP(), IdentP()), func(n string) Expr { return Expr{Kind: ExprName, Name: n} })
}

func oneWSCharP() Parser[rune] {
	return Satisfy(func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }, "whitespace")
}

// wsP consumes spaces, tabs, newlines, and `#` comments. It is used inside `{ ... }`
// braces (struct type defs and struct literals), which may span multiple physical
// lines, so newlines and comments there are insignificant whitespace rather than
// construct delimiters.
func wsP() Parser[struct{}] {
	return Discard(Many(Or(Discard(oneWSCharP()), commentP())))
}

// ws1P is wsP but requires at least one whitespace/newline/comment unit. Used to
// separate struct-type field lines (`Pos rat0+` then a newline then `Neg rat0+`).
func ws1P() Parser[struct{}] {
	return Discard(Many1(Or(Discard(oneWSCharP()), commentP())))
}

// structLitP parses a struct construction literal `{ Name: expr, Name: expr }`.
// Fields are comma-separated with an optional trailing comma, and whitespace
// (including newlines) is allowed throughout, so a literal may span lines. At
// least one field is required. The trailing reference to exprP is deferred to
// parse time to break the construction cycle.
func structLitP() Parser[Expr] {
	fieldP := Map(
		Sequence5(IdentP(), Prefix(wsP(), RuneP(':')), wsP(), exprP(), Succeed(struct{}{})),
		func(t Of5[string, rune, struct{}, Expr, struct{}]) StructField {
			v := t.V4
			return StructField{Name: t.V1, Value: &v}
		},
	)
	comma := Sequence2(wsP(), RuneP(','))
	more := Many(Try(Prefix(comma, Prefix(wsP(), fieldP))))
	trailingComma := Or(Try(Discard(comma)), Succeed(struct{}{}))
	return Map(
		Sequence5(
			RuneP('{'),
			Prefix(wsP(), fieldP),
			more,
			Prefix(Sequence2(trailingComma, wsP()), RuneP('}')),
			Succeed(struct{}{}),
		),
		func(t Of5[rune, StructField, []StructField, rune, struct{}]) Expr {
			fields := append([]StructField{t.V2}, t.V3...)
			return Expr{Kind: ExprStructLit, StructFields: fields}
		},
	)
}

// reduceFormP parses `reduce <atom> <init-atom>`, e.g. `reduce max 0` or
// `reduce M { Pos: 0, Neg: 0 }`. It is a value-producing primary (it folds the
// vector), so it may appear anywhere an atom may — but the builder restricts it
// to query bodies. The init is a literal atom (numeric or struct literal); the
// builder type-checks it against the fold type. The trailing reference to atomP
// is deferred to parse time (atomP itself lists reduceFormP).
func reduceFormP() Parser[Expr] {
	return Map(
		Sequence3(Prefix(Sequence2(StringP("reduce"), Spaces1P()), atomP()), Spaces1P(), atomP()),
		func(t Of3[Expr, struct{}, Expr]) Expr {
			fn := t.V1
			init := t.V3
			return Expr{Kind: ExprReduce, Fn: &fn, Init: &init}
		},
	)
}

// baseAtomP parses a single argument-position term without trailing field
// access: a number, a string literal, a `reduce` form, a struct literal, a
// parenthesised expression, or a name. `reduce` is tried before nameP so the
// keyword is not read as an identifier (Try lets a name like `reducer` still
// parse).
func baseAtomP() Parser[Expr] {
	num := Map(Try(numberP()), func(f *big.Rat) Expr { return Expr{Kind: ExprNumLit, Num: f} })
	str := stringLitP()
	reduceAtom := Try(func(s Stream) ParseResult[Expr] { return reduceFormP()(s) })
	structLit := Try(func(s Stream) ParseResult[Expr] { return structLitP()(s) })
	paren := Map(
		Sequence3(RuneP('('), Prefix(SpacesP(), exprP()), Sequence2(SpacesP(), RuneP(')'))),
		func(t Of3[rune, Expr, Of2[struct{}, rune]]) Expr { return t.V2 },
	)
	return Or(num, Or(str, Or(reduceAtom, Or(structLit, Or(paren, nameP())))))
}

// atomP parses a base atom followed by zero or more `.Field` accesses, so
// `a.Pos` and `(reduce M {…}).Net` are single atoms. A numeric literal's own `.`
// (e.g. `2.5`) is consumed by numberP first, and `.Field` requires an identifier,
// so the two never collide.
func atomP() Parser[Expr] {
	return Map(
		Sequence2(baseAtomP(), Many(Try(Prefix(RuneP('.'), IdentP())))),
		func(t Of2[Expr, []string]) Expr {
			e := t.V1
			for _, f := range t.V2 {
				target := e
				e = Expr{Kind: ExprField, Target: &target, Field: f}
			}
			return e
		},
	)
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

// structTypeP parses a struct type body `{ Name Type ... }`. Fields are
// `Name Type` (space-separated), separated from one another by whitespace
// (typically newlines), with at least one field. Type is a token (numtype name
// or a user struct type name), resolved by the builder.
func structTypeP() Parser[ElemType] {
	fieldSpec := Map(
		Sequence2(IdentP(), Prefix(Spaces1P(), typeNameP())),
		func(t Of2[string, string]) FieldSpec { return FieldSpec{Name: t.V1, Type: t.V2} },
	)
	more := Many(Try(Prefix(ws1P(), fieldSpec)))
	return Map(
		Sequence4(
			RuneP('{'),
			Prefix(wsP(), fieldSpec),
			more,
			Prefix(wsP(), RuneP('}')),
		),
		func(t Of4[rune, FieldSpec, []FieldSpec, rune]) ElemType {
			return ElemType{Kind: KindStruct, Fields: append([]FieldSpec{t.V2}, t.V3...)}
		},
	)
}

// elemTypeP parses a type definition's right-hand side, one of three forms:
//   - a struct body `{ ... }` (structTypeP);
//   - a vector `vector <element>` whose element is an inline struct body
//     `{ ... }` or a token (numtype name, named struct/alias type, or a `V.Elem`
//     reference);
//   - a bare element reference `Base.Elem` (`type X = V.Elem`), which names a
//     vector's element type.
//
// The vector alternative is Try-wrapped: `Or` commits once input is consumed, and
// `StringP("vector")` matches the prefix of an identifier like `vectorClock`, so
// without Try a `type X = vectorClock.Elem` would fail the following Spaces1P and
// commit, starving the element-ref form. Try lets that partial match backtrack so
// the element-ref alternative runs. A non-dotted bare token (`type X = Y`) matches
// none of the three and remains a parse error, so plain aliases stay unsupported.
func elemTypeP() Parser[ElemType] {
	structElem := Map(structTypeP(), func(inner ElemType) ElemType {
		i := inner
		return ElemType{Kind: KindVector, Inner: &i}
	})
	tokenElem := Map(typeNameP(), func(tok string) ElemType { return ElemType{Kind: KindVector, Elem: tok} })
	vec := Prefix(Sequence2(StringP("vector"), Spaces1P()), Or(structElem, tokenElem))
	elemRef := Map(
		Sequence3(IdentP(), RuneP('.'), IdentP()),
		func(t Of3[string, rune, string]) ElemType { return ElemType{Kind: KindElemRef, Elem: t.V1 + "." + t.V3} },
	)
	return Or(structTypeP(), Or(Try(vec), elemRef))
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
//   - single-line:  `fn name p1::rat .. = <expr>`
//   - guarded:      `fn name p1::rat ..` then one-or-more `| cond = result`
//     lines ending in `| otherwise = result`.
//
// The header consumes its own end-of-line in the guarded form, and each
// guardLineP consumes its own, so one fnLineP invocation legitimately spans
// several physical lines. At least one param is required.
func fnLineP() Parser[lineResult] {
	prefix := Try(Sequence2(StringP("fn"), Spaces1P()))
	header := Sequence2(IdentP(), paramsP1())
	singleBody := Prefix(eqP(), Suffix(endP(), exprP()))
	// A guard case may be preceded by blank/comment-only lines. guardSep is
	// Try-wrapped so an indented `| ...` line (whose leading spaces guardSep would
	// otherwise eat) is not mistaken for a blank line, and each Many1 iteration is
	// Try-wrapped so trailing blank/comment lines after the last case are restored
	// for the top-level line loop.
	guardSep := Discard(Many(Try(blankOrCommentP())))
	guardedBody := Map(
		Prefix(endP(), Many1(Try(Prefix(guardSep, guardLineP())))),
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

// blankOrCommentLineP matches a blank line or a whole-line/indented comment and
// produces an empty result (no statement). Try-wrapped so a line carrying real
// content backtracks and reaches unknownLineP.
func blankOrCommentLineP() Parser[lineResult] {
	return Map(Try(blankOrCommentP()), func(struct{}) lineResult { return lineResult{} })
}

// unknownLineP is the last alternative in lineP: any line that matched no keyword,
// no stray-`|`, and is not blank/comment is an unrecognized statement. It commits a
// parse error (rather than silently skipping) so a typo like `udpate T.Add ...`
// fails loudly instead of vanishing. The committed failure propagates out of
// Many(lineP()) as the overall parse error.
func unknownLineP() Parser[lineResult] {
	return func(s Stream) ParseResult[lineResult] {
		line, col := runePos(s.Items, s.Pos)
		return failure[lineResult](ParseError{Pos: s.Pos, Line: line, Col: col, Message: "unrecognized statement"}, true)
	}
}

// barLineP rejects a stray `| ...` line. A guard line belongs to guarded-function
// syntax and is consumed by fnLineP; any `|` reaching the top level (e.g. a
// malformed guard after `otherwise`, or a `|` with no `fn` header) is a syntax
// error, not a blank/unknown line to skip. Detection is Try-wrapped so a line
// that merely has leading spaces but no `|` falls through to blankOrCommentLineP;
// once a `|` is seen the failure is committed.
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
								Or(blankOrCommentLineP(),
									unknownLineP()))))))))
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
