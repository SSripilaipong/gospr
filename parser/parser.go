package parser

func Parse(input string) (Plan, error) {
	runes := []rune(input)
	result := dslParser()(NewStream(runes))
	if !result.Ok {
		return Plan{}, result.Err
	}
	return result.Value, nil
}
