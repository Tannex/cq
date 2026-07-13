package copybook

import (
	"fmt"
	"strconv"
	"strings"
)

// Parse parses copybook source into the top-level items (usually the 01
// records). Level-88 entries attach to their data item; level-66 RENAMES
// entries are skipped.
func Parse(src string, f Format) ([]*Item, error) {
	toks := lex(src, f)
	p := &parser{toks: toks}
	var roots []*Item
	var stack []*Item // open group items, innermost last

	for !p.eof() {
		it, err := p.entry()
		if err != nil {
			return nil, err
		}
		if it == nil {
			continue // skipped entry (66, or 88 already attached)
		}
		if it.Level == 88 {
			// attach to the most recent item
			var target *Item
			if len(stack) > 0 {
				target = stack[len(stack)-1]
			}
			if target == nil {
				return nil, &ParseError{it.Line, "level 88 with no preceding data item"}
			}
			target.Conditions = append(target.Conditions, Condition{Name: it.Name, Values: it.condValues})
			continue
		}
		// pop stack to the parent level
		for len(stack) > 0 && stack[len(stack)-1].Level >= it.Level {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			roots = append(roots, it)
		} else {
			// Note: a parent with a PICTURE clause is invalid COBOL but
			// occurs in the wild; the children then overlay its storage.
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, it)
		}
		stack = append(stack, it)
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("no data-description entries found in copybook")
	}
	for _, r := range roots {
		if err := validate(r); err != nil {
			return nil, err
		}
	}
	return roots, nil
}

func validate(it *Item) error {
	if it.Pic == nil && len(it.Children) == 0 && it.Usage != UsageFloat4 && it.Usage != UsageFloat8 {
		return &ParseError{it.Line, fmt.Sprintf("%s (level %02d) has neither a PICTURE nor subordinate items", it.Name, it.Level)}
	}
	if it.Pic != nil && it.Pic.Category != CatNumeric && it.Usage != UsageDisplay {
		return &ParseError{it.Line, fmt.Sprintf("%s: USAGE %s requires a numeric PICTURE", it.Name, it.Usage)}
	}
	for _, c := range it.Children {
		if err := validate(c); err != nil {
			return err
		}
	}
	return nil
}

type parser struct {
	toks []token
	pos  int
}

func (p *parser) eof() bool { return p.pos >= len(p.toks) }
func (p *parser) peek() token {
	if p.eof() {
		return token{}
	}
	return p.toks[p.pos]
}
func (p *parser) next() token { t := p.peek(); p.pos++; return t }

func (p *parser) errf(t token, format string, args ...any) error {
	return &ParseError{t.line, fmt.Sprintf(format, args...)}
}

// keyword set that cannot be a data name following the level number.
var clauseKeywords = map[string]bool{
	"REDEFINES": true, "PIC": true, "PICTURE": true, "USAGE": true,
	"OCCURS": true, "SIGN": true, "SYNC": true, "SYNCHRONIZED": true,
	"JUSTIFIED": true, "JUST": true, "BLANK": true, "VALUE": true,
	"VALUES": true, "RENAMES": true, "GLOBAL": true, "EXTERNAL": true,
	"COMP": true, "COMPUTATIONAL": true, "COMP-1": true, "COMP-2": true,
	"COMP-3": true, "COMP-4": true, "COMP-5": true, "COMPUTATIONAL-1": true,
	"COMPUTATIONAL-2": true, "COMPUTATIONAL-3": true, "COMPUTATIONAL-4": true,
	"COMPUTATIONAL-5": true, "BINARY": true, "PACKED-DECIMAL": true,
	"DISPLAY": true, "POINTER": true, "INDEX": true,
}

var usages = map[string]Usage{
	"COMP": UsageBinary, "COMPUTATIONAL": UsageBinary,
	"COMP-4": UsageBinary, "COMPUTATIONAL-4": UsageBinary,
	"COMP-5": UsageBinary, "COMPUTATIONAL-5": UsageBinary,
	"BINARY": UsageBinary,
	"COMP-3": UsagePacked, "COMPUTATIONAL-3": UsagePacked, "PACKED-DECIMAL": UsagePacked,
	"COMP-1": UsageFloat4, "COMPUTATIONAL-1": UsageFloat4,
	"COMP-2": UsageFloat8, "COMPUTATIONAL-2": UsageFloat8,
	"DISPLAY": UsageDisplay,
}

// entry parses one data-description entry up to and including its period.
// Returns nil for entries that are recognized and skipped (level 66).
func (p *parser) entry() (*Item, error) {
	t := p.next()
	lvl, err := strconv.Atoi(t.text)
	if err != nil || t.lit {
		return nil, p.errf(t, "expected a level number, got %q", t.text)
	}
	if !(lvl >= 1 && lvl <= 49 || lvl == 66 || lvl == 77 || lvl == 88) {
		return nil, p.errf(t, "invalid level number %d", lvl)
	}
	it := &Item{Level: lvl, Line: t.line, Name: "FILLER", Filler: true}

	// Optional data name.
	if !p.eof() && !p.peek().isTerm() && !p.peek().lit {
		w := p.peek().text
		if !clauseKeywords[w] {
			p.next()
			if w != "FILLER" {
				it.Name = w
				it.Filler = false
			}
		}
	}

	if lvl == 66 { // RENAMES: consume to period, skip
		for !p.eof() && !p.next().isTerm() {
		}
		return nil, nil
	}

	for !p.eof() {
		t := p.next()
		if t.isTerm() {
			if lvl == 88 && len(it.condValues) == 0 {
				return nil, p.errf(t, "level 88 %s has no VALUE clause", it.Name)
			}
			return it, nil
		}
		switch t.text {
		case "REDEFINES":
			n := p.next()
			if n.text == "" || n.isTerm() {
				return nil, p.errf(t, "REDEFINES requires a data name")
			}
			it.Redefines = n.text
		case "PIC", "PICTURE":
			if p.peek().text == "IS" {
				p.next()
			}
			n := p.next()
			if n.text == "" || n.isTerm() {
				return nil, p.errf(t, "PICTURE requires a picture string")
			}
			pic, err := parsePic(n.text)
			if err != nil {
				return nil, p.errf(n, "%v", err)
			}
			it.Pic = pic
		case "USAGE":
			if p.peek().text == "IS" {
				p.next()
			}
			n := p.next()
			u, ok := usages[n.text]
			if !ok {
				return nil, p.errf(n, "unsupported USAGE %q", n.text)
			}
			it.Usage, it.UsageSet = u, true
		case "OCCURS":
			if err := p.occurs(it, t); err != nil {
				return nil, err
			}
		case "SIGN":
			if p.peek().text == "IS" {
				p.next()
			}
			n := p.next()
			switch n.text {
			case "LEADING":
				it.SignLeading = true
			case "TRAILING":
			default:
				return nil, p.errf(n, "SIGN must be LEADING or TRAILING, got %q", n.text)
			}
			if p.peek().text == "SEPARATE" {
				p.next()
				it.SignSeparate = true
				if p.peek().text == "CHARACTER" {
					p.next()
				}
			}
		case "SYNC", "SYNCHRONIZED":
			it.Sync = true
			if w := p.peek().text; w == "LEFT" || w == "RIGHT" {
				p.next()
			}
		case "JUSTIFIED", "JUST":
			it.Justified = true
			if p.peek().text == "RIGHT" {
				p.next()
			}
		case "BLANK":
			for w := p.peek().text; w == "WHEN" || w == "ZERO" || w == "ZEROS" || w == "ZEROES"; w = p.peek().text {
				p.next()
			}
			it.BlankZero = true
		case "VALUE", "VALUES":
			if err := p.values(it); err != nil {
				return nil, err
			}
		case "GLOBAL", "EXTERNAL":
			// no storage effect
		default:
			if u, ok := usages[t.text]; ok { // bare usage keyword
				it.Usage, it.UsageSet = u, true
				continue
			}
			if t.text == "POINTER" || t.text == "INDEX" {
				return nil, p.errf(t, "USAGE %s is not supported", t.text)
			}
			return nil, p.errf(t, "unexpected token %q in entry for %s", t.text, it.Name)
		}
	}
	return nil, p.errf(token{line: it.Line}, "entry for %s not terminated by a period", it.Name)
}

// occurs parses OCCURS [n TO] m [TIMES] [DEPENDING ON name] [KEY.../INDEXED BY...].
func (p *parser) occurs(it *Item, at token) error {
	n := p.next()
	c1, err := strconv.Atoi(n.text)
	if err != nil {
		return p.errf(n, "OCCURS requires a count, got %q", n.text)
	}
	it.Occurs = c1
	if p.peek().text == "TO" {
		p.next()
		m := p.next()
		c2, err := strconv.Atoi(m.text)
		if err != nil {
			return p.errf(m, "OCCURS n TO requires a count, got %q", m.text)
		}
		it.OccursMin, it.Occurs = c1, c2
	}
	if p.peek().text == "TIMES" {
		p.next()
	}
	for {
		switch p.peek().text {
		case "DEPENDING":
			p.next()
			if p.peek().text == "ON" {
				p.next()
			}
			d := p.next()
			if d.text == "" || d.isTerm() {
				return p.errf(at, "DEPENDING ON requires a data name")
			}
			it.DependingOn = d.text
		case "ASCENDING", "DESCENDING":
			p.next()
			if p.peek().text == "KEY" {
				p.next()
			}
			if p.peek().text == "IS" {
				p.next()
			}
			p.next() // key name
		case "INDEXED":
			p.next()
			if p.peek().text == "BY" {
				p.next()
			}
			// one or more index names
			for !p.eof() && !p.peek().isTerm() && !clauseKeywords[p.peek().text] &&
				p.peek().text != "DEPENDING" && p.peek().text != "ASCENDING" &&
				p.peek().text != "DESCENDING" && p.peek().text != "INDEXED" {
				p.next()
			}
		default:
			return nil
		}
	}
}

// values consumes a VALUE clause; the literals are only kept for level 88.
func (p *parser) values(it *Item) error {
	if w := p.peek().text; w == "IS" || w == "ARE" {
		p.next()
	}
	for !p.eof() {
		t := p.peek()
		if t.isTerm() {
			break
		}
		if !t.lit && clauseKeywords[t.text] && t.text != "VALUE" && t.text != "VALUES" {
			break
		}
		if t.text == "THRU" || t.text == "THROUGH" {
			p.next()
			continue
		}
		p.next()
		v := t.text
		if t.lit {
			v = strings.Trim(v, `'"`)
		}
		it.condValues = append(it.condValues, v)
	}
	return nil
}
