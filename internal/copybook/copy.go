package copybook

import (
	"fmt"
	"strings"
)

const maxCopyDepth = 64

// CopyResolver returns the source of a named copybook member.
type CopyResolver func(name string) (string, error)

// ParseWithCopies expands plain COPY member statements recursively before
// parsing the resulting data-description entries.
func ParseWithCopies(src string, f Format, resolve CopyResolver) ([]*Item, error) {
	toks, err := expandCopies(lex(src, f), resolve, nil)
	if err != nil {
		return nil, err
	}
	return parseTokens(toks)
}

func expandCopies(toks []token, resolve CopyResolver, stack []string) ([]token, error) {
	var out []token
	statementStart := true
	for i := 0; i < len(toks); {
		tok := toks[i]
		if !statementStart || tok.lit || tok.text != "COPY" {
			out = append(out, tok)
			statementStart = tok.isTerm()
			i++
			continue
		}

		if resolve == nil {
			return nil, fmt.Errorf("line %d: COPY requires a resolver", tok.line)
		}
		if i+1 >= len(toks) || toks[i+1].lit || toks[i+1].isTerm() {
			return nil, fmt.Errorf("line %d: COPY requires a member name", tok.line)
		}
		member := toks[i+1].text
		if i+2 >= len(toks) || !toks[i+2].isTerm() {
			return nil, fmt.Errorf("line %d: COPY %s uses unsupported clauses; only COPY member. is supported", tok.line, member)
		}
		if len(stack) >= maxCopyDepth {
			return nil, fmt.Errorf("COPY nesting exceeds %d members: %s", maxCopyDepth, strings.Join(stack, " -> "))
		}
		for _, ancestor := range stack {
			if ancestor == member {
				chain := append(append([]string{}, stack...), member)
				return nil, fmt.Errorf("COPY cycle: %s", strings.Join(chain, " -> "))
			}
		}

		src, err := resolve(member)
		if err != nil {
			chain := append(append([]string{}, stack...), member)
			return nil, fmt.Errorf("resolve COPY %s: %w", strings.Join(chain, " -> "), err)
		}
		expanded, err := expandCopies(lex(src, FormatAuto), resolve, append(stack, member))
		if err != nil {
			return nil, err
		}
		out = append(out, expanded...)
		statementStart = true
		i += 3
	}
	return out, nil
}
