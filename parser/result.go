package parser

type ParseResult[A any] struct {
	Value    A
	Next     Stream
	Ok       bool
	Err      ParseError
	Consumed bool
}

type Parser[A any] func(Stream) ParseResult[A]

func success[A any](v A, next Stream, consumed bool) ParseResult[A] {
	return ParseResult[A]{Value: v, Next: next, Ok: true, Consumed: consumed}
}

func failure[A any](err ParseError, consumed bool) ParseResult[A] {
	return ParseResult[A]{Ok: false, Err: err, Consumed: consumed}
}

type Of2[A, B any]       struct{ V1 A; V2 B }
type Of3[A, B, C any]    struct{ V1 A; V2 B; V3 C }
type Of4[A, B, C, D any] struct{ V1 A; V2 B; V3 C; V4 D }
