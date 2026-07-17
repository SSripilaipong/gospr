package e2e

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gospr/builder"
	"gospr/parser"
)

// This test is the guarantee behind docs/examples.md: every ```gos program
// published there must still pass `gospr check`. It is deliberately black-box and
// data-driven — it does NOT hard-code any example source. Instead it reads the
// committed doc, extracts each ```gos ... ``` block, and runs the exact same
// parse->build pipeline as main.go's `check` command on it.
//
// Consequence: docs/examples.md is the single source of truth for the examples.
// If you add or edit an example there, fence the complete program with ```gos and
// keep it buildable — this test will catch it if it ever drifts from the grammar
// or stops proving convergence. (Requires z3 on PATH, like the other e2e tests.)
const examplesDoc = "../docs/examples.md"

// minExamples guards against a doc refactor that silently drops the ```gos tags
// (which would otherwise make this test pass vacuously). Bump it when you add
// examples; it is a floor, not an exact count.
const minExamples = 7

var gosBlockRe = regexp.MustCompile("(?s)```gos\\n(.*?)```")

func TestExamplesDoc_allBuild(t *testing.T) {
	data, err := os.ReadFile(examplesDoc)
	require.NoError(t, err, "read %s", examplesDoc)

	blocks := extractGosBlocks(string(data))
	require.GreaterOrEqual(t, len(blocks), minExamples,
		"found %d ```gos blocks in %s, expected at least %d", len(blocks), examplesDoc, minExamples)

	for _, b := range blocks {
		t.Run(b.name, func(t *testing.T) {
			plan, err := parser.Parse(b.code)
			require.NoError(t, err, "parse failed")

			built, err := builder.Build(plan)
			require.NoError(t, err, "build failed (type-check or convergence proof)")

			assert.GreaterOrEqual(t, len(built.Collections), 1,
				"example declares no collection — it would deploy to nothing")
		})
	}
}

type gosBlock struct {
	name string // nearest preceding "## " heading, for a readable subtest name
	code string
}

// extractGosBlocks pulls every ```gos fenced block out of the markdown, pairing
// each with the most recent "## " heading above it.
func extractGosBlocks(md string) []gosBlock {
	var blocks []gosBlock
	for _, m := range gosBlockRe.FindAllStringSubmatchIndex(md, -1) {
		code := md[m[2]:m[3]]
		blocks = append(blocks, gosBlock{
			name: headingBefore(md, m[0]),
			code: code,
		})
	}
	return blocks
}

func headingBefore(md string, pos int) string {
	name := "example"
	for _, line := range strings.Split(md[:pos], "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			name = slug(h)
		}
	}
	return name
}

// slug makes a heading safe for a subtest name (t.Run turns spaces into
// underscores anyway; this keeps names tidy and unique-ish).
func slug(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == ' ', r == '-', r == '_':
			return '_'
		default:
			return -1
		}
	}, s)
	return s
}
