package parser

type Stream struct {
	Items []rune
	Pos   int
}

func NewStream(items []rune) Stream {
	return Stream{Items: items, Pos: 0}
}

func (s Stream) Head() (rune, bool) {
	if s.Pos >= len(s.Items) {
		return 0, false
	}
	return s.Items[s.Pos], true
}

func (s Stream) Advance() Stream {
	return Stream{Items: s.Items, Pos: s.Pos + 1}
}

func (s Stream) IsEmpty() bool {
	return s.Pos >= len(s.Items)
}

func runePos(runes []rune, pos int) (line, col int) {
	line = 1
	col = 1
	for i := 0; i < pos && i < len(runes); i++ {
		if runes[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}
