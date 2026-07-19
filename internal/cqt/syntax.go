package cqt

import (
	"strings"

	"github.com/Tannex/cq/internal/decode"
)

// sourceKind classifies the text content of a data set or member so the raw
// record view can pick a highlighter. Detection is heuristic; anything
// ambiguous stays sourcePlain and renders exactly as before.
type sourceKind int

const (
	sourcePlain sourceKind = iota
	sourceJCL
	sourceCOBOL
)

// syntaxSampleLimit bounds how many non-blank lines detection reads. Content
// type never changes mid-file, so a prefix sample is enough.
const syntaxSampleLimit = 120

// COBOL fixed-format layout as 0-based offsets: columns 1-6 hold the numeric
// sequence area, column 7 the indicator character.
const (
	cobolSequenceLen  = 6
	cobolIndicatorCol = 6
)

var cobolDivisionHeaders = []string{
	"IDENTIFICATION DIVISION", "ENVIRONMENT DIVISION", "DATA DIVISION", "PROCEDURE DIVISION",
}

// detectSourceKind classifies decoded display lines as JCL, COBOL, or plain.
// JCL is a prefix-ratio test; COBOL scores independent signals so partial
// listings (a copybook without divisions, a program without sequence numbers)
// still clear the bar while flat data files do not.
func detectSourceKind(lines []string) sourceKind {
	sampled, jcl := 0, 0
	cobolScore, sequenced := 0, 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if sampled == syntaxSampleLimit {
			break
		}
		sampled++
		if isJCLPrefixed(line) {
			jcl++
		}
		upper := strings.ToUpper(line)
		if containsCOBOLDivision(upper) {
			cobolScore += 4
		}
		if cobolCommentLine(line) {
			cobolScore++
		}
		if containsPICClause(upper) {
			cobolScore++
		}
		if sequenceArea(line) {
			sequenced++
		}
	}
	if sampled == 0 {
		return sourcePlain
	}
	if jcl >= 2 && jcl*10 >= sampled*6 {
		return sourceJCL
	}
	// A numeric sequence area on nearly every line supports COBOL but cannot
	// carry the verdict alone: flat data files often start with record ids.
	if sequenced*10 >= sampled*8 {
		cobolScore += 2
	}
	if cobolScore >= 4 {
		return sourceCOBOL
	}
	return sourcePlain
}

// isJCLPrefixed matches JCL statements and comments (`//`) plus delimiters and
// JES control (`/*`).
func isJCLPrefixed(line string) bool {
	return strings.HasPrefix(line, "//") || strings.HasPrefix(line, "/*")
}

func containsCOBOLDivision(upper string) bool {
	for _, header := range cobolDivisionHeaders {
		if strings.Contains(upper, header) {
			return true
		}
	}
	return false
}

func containsPICClause(upper string) bool {
	return strings.Contains(upper, " PIC ") || strings.Contains(upper, " PICTURE ")
}

// sequenceArea reports whether the sequence columns hold a classic numeric
// sequence field: digits, possibly blank-padded, at least one digit.
func sequenceArea(line string) bool {
	if len(line) < cobolSequenceLen {
		return false
	}
	digits := 0
	for _, c := range line[:cobolSequenceLen] {
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == ' ':
		default:
			return false
		}
	}
	return digits > 0
}

// cobolCommentLine reports a `*` or `/` indicator behind a blank-or-digit
// sequence area.
func cobolCommentLine(line string) bool {
	if len(line) <= cobolIndicatorCol {
		return false
	}
	if c := line[cobolIndicatorCol]; c != '*' && c != '/' {
		return false
	}
	return blankOrDigits(line[:cobolSequenceLen])
}

func blankOrDigits(s string) bool {
	for _, c := range s {
		if c != ' ' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// highlightSourceLine tints one decoded display line for the detected kind.
// The output strips back to the input exactly, so ANSI-aware truncation and
// horizontal panning keep working; callers skip the cursor row so the
// selected-row style renders uniformly.
func highlightSourceLine(line string, kind sourceKind) string {
	switch kind {
	case sourceJCL:
		return highlightJCL(line)
	case sourceCOBOL:
		return highlightCOBOL(line)
	default:
		return line
	}
}

// jclOperations are the operation-field words worth calling out; anything else
// in that position (continuation parameters, operands) stays plain.
var jclOperations = map[string]bool{
	"JOB": true, "EXEC": true, "DD": true, "PROC": true, "PEND": true,
	"SET": true, "IF": true, "THEN": true, "ELSE": true, "ENDIF": true,
	"INCLUDE": true, "JCLLIB": true, "OUTPUT": true, "EXPORT": true,
	"CNTL": true, "ENDCNTL": true, "COMMAND": true, "XMIT": true,
}

func highlightJCL(line string) string {
	switch {
	case strings.HasPrefix(line, "//*"), strings.HasPrefix(line, "/*"):
		return consolePalette.muted.Render(line)
	case !strings.HasPrefix(line, "//"):
		return line // in-stream data between a `DD *` and its delimiter
	}
	name, rest := splitAtSpace(line[2:])
	var b strings.Builder
	b.WriteString(consolePalette.cyan.Render("//" + name))
	gap := len(rest) - len(strings.TrimLeft(rest, " "))
	b.WriteString(rest[:gap])
	rest = rest[gap:]
	if op, tail := splitAtSpace(rest); jclOperations[strings.ToUpper(op)] {
		b.WriteString(consolePalette.amber.Render(op))
		rest = tail
	}
	b.WriteString(highlightJCLParameters(rest))
	return b.String()
}

// highlightJCLParameters tints `KEYWORD=` parameter keys and quoted values in
// the operand field, leaving everything else untouched.
func highlightJCLParameters(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		switch c := text[i]; {
		case c == '\'':
			literal := takeQuoted(text[i:], '\'')
			b.WriteString(consolePalette.green.Render(literal))
			i += len(literal)
		case isJCLWordByte(c):
			end := wordEnd(text, i, isJCLWordByte)
			word := text[i:end]
			if end < len(text) && text[end] == '=' {
				b.WriteString(consolePalette.green.Render(word))
			} else {
				b.WriteString(word)
			}
			i = end
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func isJCLWordByte(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
		c == '&' || c == '@' || c == '#' || c == '$' || c == '.'
}

// cobolKeywords covers divisions, common clauses, and the verbs that dominate
// everyday listings; a full reserved-word list would add noise, not signal.
var cobolKeywords = map[string]bool{
	"IDENTIFICATION": true, "ENVIRONMENT": true, "DATA": true, "PROCEDURE": true,
	"DIVISION": true, "SECTION": true, "PROGRAM-ID": true, "AUTHOR": true,
	"WORKING-STORAGE": true, "LINKAGE": true, "FILE": true, "FILE-CONTROL": true,
	"SELECT": true, "ASSIGN": true, "FD": true, "COPY": true, "REPLACING": true,
	"VALUE": true, "OCCURS": true, "REDEFINES": true, "FILLER": true,
	"COMP": true, "COMP-3": true, "BINARY": true, "PACKED-DECIMAL": true, "USAGE": true,
	"MOVE": true, "TO": true, "FROM": true, "GIVING": true, "USING": true,
	"PERFORM": true, "UNTIL": true, "VARYING": true, "THRU": true, "TIMES": true,
	"IF": true, "ELSE": true, "END-IF": true, "EVALUATE": true, "WHEN": true, "END-EVALUATE": true,
	"COMPUTE": true, "ADD": true, "SUBTRACT": true, "MULTIPLY": true, "DIVIDE": true,
	"DISPLAY": true, "ACCEPT": true, "CALL": true, "GOBACK": true, "STOP": true, "RUN": true,
	"OPEN": true, "CLOSE": true, "READ": true, "WRITE": true, "REWRITE": true,
	"INPUT": true, "OUTPUT": true, "I-O": true, "END-PERFORM": true, "END-READ": true,
	"INITIALIZE": true, "INSPECT": true, "STRING": true, "UNSTRING": true, "EXIT": true,
	"NOT": true, "AND": true, "OR": true, "ZERO": true, "ZEROS": true, "SPACES": true, "SPACE": true,
}

func highlightCOBOL(line string) string {
	var b strings.Builder
	body := line
	if sequenceArea(line) {
		b.WriteString(consolePalette.muted.Render(line[:cobolSequenceLen]))
		body = line[cobolSequenceLen:]
		if len(body) > 0 {
			switch body[0] {
			case '*', '/':
				b.WriteString(consolePalette.muted.Render(body))
				return b.String()
			case '-':
				b.WriteString(consolePalette.amber.Render("-"))
				body = body[1:]
			}
		}
	}
	b.WriteString(highlightCOBOLBody(body))
	return b.String()
}

func highlightCOBOLBody(text string) string {
	var b strings.Builder
	pictureNext := false
	for i := 0; i < len(text); {
		switch c := text[i]; {
		case c == '\'' || c == '"':
			literal := takeQuoted(text[i:], c)
			b.WriteString(consolePalette.green.Render(literal))
			i += len(literal)
			pictureNext = false
		case pictureNext && c != ' ':
			end := wordEnd(text, i, func(c byte) bool { return c != ' ' })
			b.WriteString(consolePalette.amber.Render(text[i:end]))
			i = end
			pictureNext = false
		case isCOBOLWordByte(c):
			end := wordEnd(text, i, isCOBOLWordByte)
			word := text[i:end]
			upper := strings.ToUpper(word)
			switch {
			case upper == "PIC" || upper == "PICTURE":
				b.WriteString(consolePalette.amber.Render(word))
				pictureNext = true
			case cobolKeywords[upper]:
				b.WriteString(consolePalette.cyan.Render(word))
			default:
				b.WriteString(word)
			}
			i = end
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func isCOBOLWordByte(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-'
}

// takeQuoted returns the literal starting at text[0] (the opening quote)
// through the closing quote, or the rest of the text when unterminated.
func takeQuoted(text string, quote byte) string {
	if end := strings.IndexByte(text[1:], quote); end >= 0 {
		return text[:end+2]
	}
	return text
}

// splitAtSpace cuts text at the first space, keeping the space in rest.
func splitAtSpace(text string) (field, rest string) {
	if i := strings.IndexByte(text, ' '); i >= 0 {
		return text[:i], text[i:]
	}
	return text, ""
}

func wordEnd(text string, start int, isWordByte func(byte) bool) int {
	end := start
	for end < len(text) && isWordByte(text[end]) {
		end++
	}
	return end
}

// recordSyntax returns the cached content classification for the loaded
// records, re-detecting only when the cache size changes — never per keystroke
// or per render frame.
func (m *Model) recordSyntax() sourceKind {
	if len(m.records) == 0 {
		return sourcePlain
	}
	if m.syntaxSampled == len(m.records) {
		return m.syntaxKind
	}
	limit := min(len(m.records), syntaxSampleLimit)
	lines := make([]string, limit)
	for i := range lines {
		lines[i] = decode.DisplayBytes(m.records[i].Record.Data, m.charmap)
	}
	m.syntaxKind = detectSourceKind(lines)
	m.syntaxSampled = len(m.records)
	return m.syntaxKind
}
