package parser

type typeExpr struct {
	name string
	args []string
}

type lineResult struct {
	collection *CollectionSpec
	typeDef    *TypeDef
	queryDef   *QueryDef
	updateDef  *UpdateDef
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

func argValueP() Parser[string] {
	return Or(IdentP(), digitsP())
}

func sepP() Parser[Of3[struct{}, rune, struct{}]] {
	return Sequence3(SpacesP(), RuneP(','), SpacesP())
}

func typeExprP() Parser[typeExpr] {
	return Map(
		Sequence4(IdentP(), RuneP('('), SepBy(argValueP(), sepP()), RuneP(')')),
		func(t Of4[string, rune, []string, rune]) typeExpr {
			return typeExpr{name: t.V1, args: t.V3}
		},
	)
}

func typeNameP() Parser[string] {
	plus := Or(Map(RuneP('+'), func(_ rune) string { return "+" }), Succeed(""))
	return Map(Sequence2(IdentP(), plus), func(t Of2[string, string]) string { return t.V1 + t.V2 })
}

func paramSpecP() Parser[ParamSpec] {
	return Map(
		Sequence3(IdentP(), Spaces1P(), typeNameP()),
		func(t Of3[string, struct{}, string]) ParamSpec {
			return ParamSpec{Name: t.V1, Type: t.V3}
		},
	)
}

func methodParamsP() Parser[[]ParamSpec] {
	return Map(
		Sequence3(RuneP('('), SepBy(paramSpecP(), sepP()), RuneP(')')),
		func(t Of3[rune, []ParamSpec, rune]) []ParamSpec { return t.V2 },
	)
}

func methodCallP() Parser[MethodCall] {
	argsAndClose := Sequence2(SepBy(argValueP(), sepP()), RuneP(')'))
	return Map(
		Sequence5(IdentP(), RuneP('.'), IdentP(), RuneP('('), argsAndClose),
		func(t Of5[string, rune, string, rune, Of2[[]string, rune]]) MethodCall {
			return MethodCall{Field: t.V1, Method: t.V3, Args: t.V5.V1}
		},
	)
}

func fieldSpecP() Parser[FieldSpec] {
	colon := Sequence3(SpacesP(), RuneP(':'), SpacesP())
	return Map(
		Sequence3(IdentP(), colon, typeExprP()),
		func(t Of3[string, Of3[struct{}, rune, struct{}], typeExpr]) FieldSpec {
			return FieldSpec{Name: t.V1, CRDTType: t.V3.name, Args: t.V3.args}
		},
	)
}

func fieldUpdateP() Parser[FieldUpdate] {
	colon := Sequence3(SpacesP(), RuneP(':'), SpacesP())
	return Map(
		Sequence3(IdentP(), colon, methodCallP()),
		func(t Of3[string, Of3[struct{}, rune, struct{}], MethodCall]) FieldUpdate {
			return FieldUpdate{Field: t.V1, Call: t.V3}
		},
	)
}

func braceBodyP[A any](p Parser[A]) Parser[[]A] {
	open := Sequence2(RuneP('{'), SpacesP())
	close := Sequence2(SpacesP(), RuneP('}'))
	return Prefix(open, Suffix(close, SepBy(p, Try(sepP()))))
}

func collectionLineP() Parser[lineResult] {
	prefix := Try(Sequence2(StringP("collection"), Spaces1P()))
	eq := Sequence3(SpacesP(), RuneP('='), SpacesP())
	end := Sequence2(SpacesP(), newlineOrEOF())
	rest := Map(
		Sequence2(IdentP(), Prefix(eq, Suffix(end, typeExprP()))),
		func(t Of2[string, typeExpr]) lineResult {
			return lineResult{collection: &CollectionSpec{Name: t.V1, Type: t.V2.name, Args: t.V2.args}}
		},
	)
	return Prefix(prefix, rest)
}

func typeDefLineP() Parser[lineResult] {
	prefix := Try(Sequence2(StringP("type"), Spaces1P()))
	params := Sequence3(RuneP('('), SepBy(paramSpecP(), sepP()), RuneP(')'))
	eq := Sequence3(SpacesP(), RuneP('='), SpacesP())
	end := Sequence2(SpacesP(), newlineOrEOF())
	rest := Map(
		Sequence3(IdentP(), params, Prefix(eq, Suffix(end, braceBodyP(fieldSpecP())))),
		func(t Of3[string, Of3[rune, []ParamSpec, rune], []FieldSpec]) lineResult {
			return lineResult{typeDef: &TypeDef{Name: t.V1, Params: t.V2.V2, Fields: t.V3}}
		},
	)
	return Prefix(prefix, rest)
}

func queryDefLineP() Parser[lineResult] {
	prefix := Try(Sequence2(StringP("query"), Spaces1P()))
	lhs := Sequence4(IdentP(), RuneP('.'), IdentP(), methodParamsP())
	eq := Sequence3(SpacesP(), RuneP('='), SpacesP())
	end := Sequence2(SpacesP(), newlineOrEOF())
	rest := Map(
		Sequence2(lhs, Prefix(eq, Suffix(end, methodCallP()))),
		func(t Of2[Of4[string, rune, string, []ParamSpec], MethodCall]) lineResult {
			return lineResult{queryDef: &QueryDef{TypeName: t.V1.V1, MethodName: t.V1.V3, Params: t.V1.V4, Body: t.V2}}
		},
	)
	return Prefix(prefix, rest)
}

func updateDefLineP() Parser[lineResult] {
	prefix := Try(Sequence2(StringP("update"), Spaces1P()))
	lhs := Sequence4(IdentP(), RuneP('.'), IdentP(), methodParamsP())
	eq := Sequence3(SpacesP(), RuneP('='), SpacesP())
	end := Sequence2(SpacesP(), newlineOrEOF())
	rest := Map(
		Sequence2(lhs, Prefix(eq, Suffix(end, braceBodyP(fieldUpdateP())))),
		func(t Of2[Of4[string, rune, string, []ParamSpec], []FieldUpdate]) lineResult {
			return lineResult{updateDef: &UpdateDef{TypeName: t.V1.V1, MethodName: t.V1.V3, Params: t.V1.V4, Body: t.V2}}
		},
	)
	return Prefix(prefix, rest)
}

func skipLineP() Parser[lineResult] {
	return Map(Suffix(newlineOrEOF(), TillEOL()), func(_ string) lineResult { return lineResult{} })
}

func lineP() Parser[lineResult] {
	return Or(typeDefLineP(),
		Or(queryDefLineP(),
			Or(updateDefLineP(),
				Or(collectionLineP(),
					skipLineP()))))
}

func dslParser() Parser[Plan] {
	return Map(
		Suffix(EOF(), Many(lineP())),
		func(results []lineResult) Plan {
			var plan Plan
			for _, r := range results {
				switch {
				case r.collection != nil:
					plan.Collections = append(plan.Collections, *r.collection)
				case r.typeDef != nil:
					plan.Types = append(plan.Types, *r.typeDef)
				case r.queryDef != nil:
					plan.Queries = append(plan.Queries, *r.queryDef)
				case r.updateDef != nil:
					plan.Updates = append(plan.Updates, *r.updateDef)
				}
			}
			return plan
		},
	)
}
