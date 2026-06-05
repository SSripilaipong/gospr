package parser

import "unicode"

// Primitives

func Succeed[A any](value A) Parser[A] {
	return func(s Stream) ParseResult[A] {
		return success(value, s, false)
	}
}

func Fail[A any](msg string) Parser[A] {
	return func(s Stream) ParseResult[A] {
		line, col := runePos(s.Items, s.Pos)
		return failure[A](ParseError{Pos: s.Pos, Line: line, Col: col, Message: msg}, false)
	}
}

func Satisfy(pred func(rune) bool, label string) Parser[rune] {
	return func(s Stream) ParseResult[rune] {
		r, ok := s.Head()
		if !ok {
			line, col := runePos(s.Items, s.Pos)
			return failure[rune](ParseError{Pos: s.Pos, Line: line, Col: col, Message: "unexpected end of input, expected " + label}, false)
		}
		if !pred(r) {
			line, col := runePos(s.Items, s.Pos)
			return failure[rune](ParseError{Pos: s.Pos, Line: line, Col: col, Message: "unexpected " + string(r) + ", expected " + label}, false)
		}
		return success(r, s.Advance(), true)
	}
}

func EOF() Parser[struct{}] {
	return func(s Stream) ParseResult[struct{}] {
		if s.IsEmpty() {
			return success(struct{}{}, s, false)
		}
		r, _ := s.Head()
		line, col := runePos(s.Items, s.Pos)
		return failure[struct{}](ParseError{Pos: s.Pos, Line: line, Col: col, Message: "unexpected " + string(r) + ", expected end of input"}, false)
	}
}

// Map

func Discard[A any](p Parser[A]) Parser[struct{}] {
	return Map(p, func(_ A) struct{} { return struct{}{} })
}

func Map[A, B any](p Parser[A], f func(A) B) Parser[B] {
	return func(s Stream) ParseResult[B] {
		r := p(s)
		if !r.Ok {
			return failure[B](r.Err, r.Consumed)
		}
		return success(f(r.Value), r.Next, r.Consumed)
	}
}

// Sequencing

func Sequence2[A, B any](p1 Parser[A], p2 Parser[B]) Parser[Of2[A, B]] {
	return func(s Stream) ParseResult[Of2[A, B]] {
		r1 := p1(s)
		if !r1.Ok {
			return failure[Of2[A, B]](r1.Err, r1.Consumed)
		}
		r2 := p2(r1.Next)
		if !r2.Ok {
			return failure[Of2[A, B]](r2.Err, r1.Consumed || r2.Consumed)
		}
		return success(Of2[A, B]{r1.Value, r2.Value}, r2.Next, r1.Consumed || r2.Consumed)
	}
}

func Sequence3[A, B, C any](p1 Parser[A], p2 Parser[B], p3 Parser[C]) Parser[Of3[A, B, C]] {
	return func(s Stream) ParseResult[Of3[A, B, C]] {
		r1 := p1(s)
		if !r1.Ok {
			return failure[Of3[A, B, C]](r1.Err, r1.Consumed)
		}
		r2 := p2(r1.Next)
		if !r2.Ok {
			return failure[Of3[A, B, C]](r2.Err, r1.Consumed || r2.Consumed)
		}
		r3 := p3(r2.Next)
		if !r3.Ok {
			return failure[Of3[A, B, C]](r3.Err, r1.Consumed || r2.Consumed || r3.Consumed)
		}
		return success(Of3[A, B, C]{r1.Value, r2.Value, r3.Value}, r3.Next, r1.Consumed || r2.Consumed || r3.Consumed)
	}
}

func Sequence4[A, B, C, D any](p1 Parser[A], p2 Parser[B], p3 Parser[C], p4 Parser[D]) Parser[Of4[A, B, C, D]] {
	return func(s Stream) ParseResult[Of4[A, B, C, D]] {
		r1 := p1(s)
		if !r1.Ok {
			return failure[Of4[A, B, C, D]](r1.Err, r1.Consumed)
		}
		r2 := p2(r1.Next)
		if !r2.Ok {
			return failure[Of4[A, B, C, D]](r2.Err, r1.Consumed || r2.Consumed)
		}
		r3 := p3(r2.Next)
		if !r3.Ok {
			return failure[Of4[A, B, C, D]](r3.Err, r1.Consumed || r2.Consumed || r3.Consumed)
		}
		r4 := p4(r3.Next)
		if !r4.Ok {
			return failure[Of4[A, B, C, D]](r4.Err, r1.Consumed || r2.Consumed || r3.Consumed || r4.Consumed)
		}
		return success(Of4[A, B, C, D]{r1.Value, r2.Value, r3.Value, r4.Value}, r4.Next, r1.Consumed || r2.Consumed || r3.Consumed || r4.Consumed)
	}
}

func Prefix[A, B any](prefix Parser[A], p Parser[B]) Parser[B] {
	return Map(Sequence2(prefix, p), func(t Of2[A, B]) B { return t.V2 })
}

func Suffix[A, B any](suffix Parser[A], p Parser[B]) Parser[B] {
	return Map(Sequence2(p, suffix), func(t Of2[B, A]) B { return t.V1 })
}

// Choice

func Or[A any](left, right Parser[A]) Parser[A] {
	return func(s Stream) ParseResult[A] {
		r := left(s)
		if r.Ok || r.Consumed {
			return r
		}
		return right(s)
	}
}

func Try[A any](p Parser[A]) Parser[A] {
	return func(s Stream) ParseResult[A] {
		r := p(s)
		if !r.Ok {
			r.Consumed = false
		}
		return r
	}
}

// Repetition

func Many[A any](p Parser[A]) Parser[[]A] {
	return func(s Stream) ParseResult[[]A] {
		var items []A
		consumed := false
		cur := s
		for {
			r := p(cur)
			if !r.Ok {
				if r.Consumed {
					return failure[[]A](r.Err, true)
				}
				return success(items, cur, consumed)
			}
			items = append(items, r.Value)
			consumed = consumed || r.Consumed
			if r.Next.Pos == cur.Pos {
				return success(items, r.Next, consumed)
			}
			cur = r.Next
		}
	}
}

func Many1[A any](p Parser[A]) Parser[[]A] {
	return func(s Stream) ParseResult[[]A] {
		r := p(s)
		if !r.Ok {
			return failure[[]A](r.Err, r.Consumed)
		}
		rest := Many(p)(r.Next)
		if !rest.Ok {
			return failure[[]A](rest.Err, r.Consumed || rest.Consumed)
		}
		return success(append([]A{r.Value}, rest.Value...), rest.Next, r.Consumed || rest.Consumed)
	}
}

func SepBy[A, B any](p Parser[A], sep Parser[B]) Parser[[]A] {
	return func(s Stream) ParseResult[[]A] {
		first := p(s)
		if !first.Ok {
			if first.Consumed {
				return failure[[]A](first.Err, true)
			}
			return success([]A{}, s, false)
		}
		items := []A{first.Value}
		consumed := first.Consumed
		cur := first.Next
		for {
			rs := sep(cur)
			if !rs.Ok {
				if rs.Consumed {
					return failure[[]A](rs.Err, true)
				}
				return success(items, cur, consumed)
			}
			rp := p(rs.Next)
			if !rp.Ok {
				return failure[[]A](rp.Err, consumed || rs.Consumed || rp.Consumed)
			}
			items = append(items, rp.Value)
			consumed = consumed || rs.Consumed || rp.Consumed
			cur = rp.Next
		}
	}
}

// Rune helpers

func RuneP(r rune) Parser[rune] {
	return Satisfy(func(c rune) bool { return c == r }, string(r))
}

func StringP(str string) Parser[string] {
	runes := []rune(str)
	return func(s Stream) ParseResult[string] {
		consumed := false
		cur := s
		for _, r := range runes {
			res := RuneP(r)(cur)
			if !res.Ok {
				return failure[string](res.Err, consumed || res.Consumed)
			}
			consumed = true
			cur = res.Next
		}
		return success(str, cur, consumed)
	}
}

func SpacesP() Parser[struct{}] {
	return Map(Many(Satisfy(func(r rune) bool { return r == ' ' || r == '\t' }, "space")), func(_ []rune) struct{} { return struct{}{} })
}

func Spaces1P() Parser[struct{}] {
	return Map(Many1(Satisfy(func(r rune) bool { return r == ' ' || r == '\t' }, "space")), func(_ []rune) struct{} { return struct{}{} })
}

func IdentP() Parser[string] {
	head := Satisfy(func(r rune) bool { return r == '_' || unicode.IsLetter(r) }, "identifier")
	tail := Many(Satisfy(func(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }, "identifier char"))
	return Map(Sequence2(head, tail), func(t Of2[rune, []rune]) string {
		return string(append([]rune{t.V1}, t.V2...))
	})
}

func TillEOL() Parser[string] {
	return Map(Many(Satisfy(func(r rune) bool { return r != '\n' }, "non-newline")), func(rs []rune) string { return string(rs) })
}
