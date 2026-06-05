package parser

type typeExpr struct {
	name string
	args []string
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

func collectionLineP() Parser[*CollectionSpec] {
	prefix := Try(Sequence2(StringP("collection"), Spaces1P()))
	eq := Sequence3(SpacesP(), RuneP('='), SpacesP())
	end := Sequence2(SpacesP(), newlineOrEOF())
	rest := Map(
		Sequence2(IdentP(), Prefix(eq, Suffix(end, typeExprP()))),
		func(t Of2[string, typeExpr]) *CollectionSpec {
			return &CollectionSpec{Name: t.V1, Type: t.V2.name, Args: t.V2.args}
		},
	)
	return Prefix(prefix, rest)
}

func skipLineP() Parser[*CollectionSpec] {
	return Map(Suffix(newlineOrEOF(), TillEOL()), func(_ string) *CollectionSpec { return nil })
}

func lineP() Parser[*CollectionSpec] {
	return Or(collectionLineP(), skipLineP())
}

func dslParser() Parser[Plan] {
	return Map(
		Suffix(EOF(), Many(lineP())),
		func(specs []*CollectionSpec) Plan {
			var plan Plan
			for _, s := range specs {
				if s != nil {
					plan.Collections = append(plan.Collections, *s)
				}
			}
			return plan
		},
	)
}
