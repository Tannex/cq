package copybook

import (
	"fmt"
	"strings"
	"sync"
)

const maxCopyDepth = 64

// CopyResolver returns the source of a named copybook member. Resolvers must
// be safe for concurrent calls: expansion prefetches the members of a level
// in parallel so slow lookups (such as per-member Zowe requests) overlap.
type CopyResolver func(name string) (string, error)

// ParseWithCopies expands plain COPY member statements recursively before
// parsing the resulting data-description entries.
func ParseWithCopies(src string, f Format, resolve CopyResolver) ([]*Item, error) {
	toks := lex(src, f)
	if err := rejectProgramSource(toks); err != nil {
		return nil, err
	}
	toks, err := expandCopies(toks, resolve, nil)
	if err != nil {
		return nil, err
	}
	return parseTokens(toks)
}

func expandCopies(toks []token, resolve CopyResolver, stack []string) ([]token, error) {
	prefetchCopies(toks, resolve, stack)
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

// prefetchCopies resolves the distinct COPY members of one expansion level
// concurrently to warm the resolver's cache. Failures are ignored here; the
// sequential walk in expandCopies reports them in source order.
func prefetchCopies(toks []token, resolve CopyResolver, stack []string) {
	if resolve == nil || len(stack) >= maxCopyDepth {
		return
	}
	seen := make(map[string]bool, len(stack))
	for _, ancestor := range stack {
		seen[ancestor] = true // the walk stops on cycles before resolving
	}
	var members []string
	statementStart := true
	for i := 0; i < len(toks); i++ {
		tok := toks[i]
		if !statementStart || tok.lit || tok.text != "COPY" {
			statementStart = tok.isTerm()
			continue
		}
		if i+2 >= len(toks) || toks[i+1].lit || toks[i+1].isTerm() || !toks[i+2].isTerm() {
			return // malformed COPY; the walk reports the error
		}
		if member := toks[i+1].text; !seen[member] {
			seen[member] = true
			members = append(members, member)
		}
		statementStart = true
		i += 2
	}
	if len(members) < 2 {
		return
	}
	var wg sync.WaitGroup
	for _, member := range members {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = resolve(member)
		}()
	}
	wg.Wait()
}

func rejectProgramSource(toks []token) error {
	for i := 0; i+1 < len(toks); i++ {
		if !toks[i].lit && toks[i].text == "PROCEDURE" &&
			!toks[i+1].lit && toks[i+1].text == "DIVISION" {
			return fmt.Errorf("Not a copybook: PROCEDURE DIVISION at line %d", toks[i].line)
		}
	}
	return nil
}
