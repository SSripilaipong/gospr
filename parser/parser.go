package parser

import (
	"bufio"
	"fmt"
	"strings"
)

func Parse(input string) (Plan, error) {
	var plan Plan
	scanner := bufio.NewScanner(strings.NewReader(input))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "collection ") {
			continue
		}
		spec, err := parseLine(line)
		if err != nil {
			return Plan{}, err
		}
		plan.Collections = append(plan.Collections, spec)
	}
	return plan, nil
}

func parseLine(line string) (CollectionSpec, error) {
	// "collection <Name> = <Type>(<args>)"
	rest := strings.TrimPrefix(line, "collection ")
	eqIdx := strings.Index(rest, " = ")
	if eqIdx < 0 {
		return CollectionSpec{}, fmt.Errorf("malformed line: %q", line)
	}
	name := strings.TrimSpace(rest[:eqIdx])
	rhs := strings.TrimSpace(rest[eqIdx+3:])

	parenOpen := strings.Index(rhs, "(")
	parenClose := strings.LastIndex(rhs, ")")
	if parenOpen < 0 || parenClose < parenOpen {
		return CollectionSpec{}, fmt.Errorf("malformed type expression: %q", rhs)
	}
	typeName := strings.TrimSpace(rhs[:parenOpen])
	argsStr := strings.TrimSpace(rhs[parenOpen+1 : parenClose])

	var args []string
	if argsStr != "" {
		for _, a := range strings.Split(argsStr, ",") {
			args = append(args, strings.TrimSpace(a))
		}
	}

	return CollectionSpec{Name: name, Type: typeName, Args: args}, nil
}
