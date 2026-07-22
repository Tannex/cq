package copybook

import (
	"fmt"
	"strings"
)

// Format selects how source lines are interpreted.
type Format int

const (
	FormatAuto  Format = iota // fixed unless a line proves otherwise
	FormatFixed               // cols 1-6 sequence, col 7 indicator, 8-72 code
	FormatFree                // whole line is code, *> comments
)

// ParseFormat maps a user-supplied format name to a Format. An empty value
// means auto; matching is case-insensitive.
func ParseFormat(value string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return FormatAuto, nil
	case "fixed":
		return FormatFixed, nil
	case "free":
		return FormatFree, nil
	}
	return FormatAuto, fmt.Errorf("unknown copybook format %q (want auto, fixed, or free)", value)
}

type token struct {
	text string // uppercased except literals, which keep quotes and case
	line int
	lit  bool // quoted literal
}

// isTerm reports whether tok is the statement terminator.
func (t token) isTerm() bool { return !t.lit && t.text == "." }

// detectFormat guesses fixed vs free format. A line is only possible in
// free format if columns 1-6 contain something other than spaces/digits,
// or if it is shorter than 7 columns yet carries code.
func detectFormat(lines []string) Format {
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		head := ln
		if len(head) > 6 {
			head = head[:6]
		}
		for _, c := range head {
			if c != ' ' && (c < '0' || c > '9') {
				return FormatFree
			}
		}
	}
	return FormatFixed
}

// codeLines strips sequence areas and comments, returning code text per
// original line number (1-based).
func codeLines(src string, f Format) []struct {
	text string
	line int
} {
	raw := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	if f == FormatAuto {
		f = detectFormat(raw)
	}
	var out []struct {
		text string
		line int
	}
	for i, ln := range raw {
		ln = strings.ReplaceAll(ln, "\t", " ")
		var code string
		if f == FormatFixed {
			if len(ln) < 8 {
				continue
			}
			ind := ln[6]
			if ind == '*' || ind == '/' || ind == '$' {
				continue // comment or directive line
			}
			code = ln[7:min(len(ln), 72)]
			if ind == '-' {
				// Continuation: rare in copybooks; join to previous line.
				if n := len(out); n > 0 {
					out[n-1].text += " " + strings.TrimSpace(code)
					continue
				}
			}
		} else {
			code = ln
			if idx := strings.Index(code, "*>"); idx >= 0 {
				code = code[:idx]
			}
			if t := strings.TrimSpace(code); strings.HasPrefix(t, "*") {
				continue
			}
		}
		if strings.TrimSpace(code) == "" {
			continue
		}
		out = append(out, struct {
			text string
			line int
		}{code, i + 1})
	}
	return out
}

// lex tokenizes copybook source. Commas and semicolons are separators —
// except inside a PICTURE string (PIC --,--9.99), where they are symbols.
// A period is a terminator only when followed by a space or end of line,
// so PIC strings like ZZ9.99 survive. Quoted literals keep their quotes.
func lex(src string, f Format) []token {
	var toks []token
	picNext := false // the next word is a PICTURE string
	for _, cl := range codeLines(src, f) {
		s := cl.text
		i := 0
		for i < len(s) {
			c := s[i]
			switch {
			case c == ' ' || (c == ',' || c == ';') && !picNext:
				i++
			case c == '\'' || c == '"':
				q := c
				j := i + 1
				for j < len(s) {
					if s[j] == q {
						if j+1 < len(s) && s[j+1] == q { // doubled quote
							j += 2
							continue
						}
						break
					}
					j++
				}
				if j < len(s) {
					j++ // include closing quote
				}
				toks = append(toks, token{text: s[i:j], line: cl.line, lit: true})
				i = j
			default:
				j := i
				for j < len(s) && s[j] != ' ' && s[j] != '\'' && s[j] != '"' &&
					(picNext || s[j] != ',' && s[j] != ';') {
					j++
				}
				w := s[i:j]
				// Trailing period is a terminator when at word end followed
				// by separator/EOL (always true here since we cut at spaces).
				term := false
				if len(w) > 1 && strings.HasSuffix(w, ".") {
					w = w[:len(w)-1]
					term = true
				}
				if w != "" {
					up := strings.ToUpper(w)
					toks = append(toks, token{text: up, line: cl.line})
					picNext = up == "PIC" || up == "PICTURE" || picNext && up == "IS"
				}
				if term || w == "" && s[i] == '.' {
					toks = append(toks, token{text: ".", line: cl.line})
				}
				i = j
			}
		}
	}
	return toks
}
