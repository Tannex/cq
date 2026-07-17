package record

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/Tannex/cq/internal/layout"
)

// where is one compiled -where clause: a level-88 condition, optionally
// negated, resolved to its data item and pre-parsed literals.
type where struct {
	name   string
	negate bool
	field  *layout.Field
	tests  []test
}

// test is one VALUE literal of the condition, pre-parsed for the parent
// field's comparison domain (numeric or text).
type test struct {
	numLo, numHi *big.Rat // numeric bound(s); numHi nil unless THRU
	txtLo, txtHi string   // text bound(s)
	isRange      bool
}

// AddWhere compiles a -where clause. spec is a level-88 condition name,
// optionally negated with a leading "!" or "not ". Clauses AND together.
func (d *Decoder) AddWhere(spec string) error {
	w := &where{name: strings.TrimSpace(spec)}
	if strings.HasPrefix(w.name, "!") {
		w.negate = true
		w.name = strings.TrimSpace(w.name[1:])
	} else if rest, ok := cutFold(w.name, "not "); ok {
		w.negate = true
		w.name = strings.TrimSpace(rest)
	}
	if w.name == "" {
		return fmt.Errorf("-where: empty condition name")
	}

	field, cond, err := findCondition(d.Rec.Field, w.name)
	if err != nil {
		return err
	}
	if field.Kind == layout.KindGroup {
		return fmt.Errorf("-where %s: conditions on group items are not supported yet", cond.Name)
	}
	numeric := field.Kind != layout.KindText && field.Kind != layout.KindEdited
	for _, v := range cond.Values {
		t, err := compileTest(v, numeric)
		if err != nil {
			return fmt.Errorf("-where %s: %w", cond.Name, err)
		}
		w.tests = append(w.tests, t)
	}
	w.field = field
	d.wheres = append(d.wheres, w)
	return nil
}

// findCondition locates the unique level-88 condition with the given name
// (case-insensitive) and returns its parent data item. Items under OCCURS
// are rejected: the clause would need a subscript to be well-defined.
func findCondition(root *layout.Field, name string) (*layout.Field, *layout.Condition, error) {
	var field *layout.Field
	var cond *layout.Condition
	dup := false
	var walk func(f *layout.Field, inArray bool)
	walk = func(f *layout.Field, inArray bool) {
		for i := range f.Conditions {
			if strings.EqualFold(f.Conditions[i].Name, name) {
				if field != nil {
					dup = true
					return
				}
				if inArray || f.Occurs > 0 {
					field = f // remembered only to report the better error below
					cond = nil
					return
				}
				field, cond = f, &f.Conditions[i]
			}
		}
		for _, c := range f.Children {
			walk(c, inArray || f.Occurs > 0)
		}
	}
	walk(root, false)
	switch {
	case dup:
		return nil, nil, fmt.Errorf("-where %s: condition name is ambiguous (defined more than once)", name)
	case field == nil:
		return nil, nil, fmt.Errorf("-where %s: no such level-88 condition in record %s", name, root.Name)
	case cond == nil:
		return nil, nil, fmt.Errorf("-where %s: condition is inside an OCCURS table; not supported yet", name)
	}
	return field, cond, nil
}

// compileTest parses one VALUE literal for the comparison domain.
func compileTest(v layout.Value, numeric bool) (test, error) {
	t := test{isRange: v.To != ""}
	if numeric {
		var err error
		if t.numLo, err = numLiteral(v.From); err != nil {
			return t, err
		}
		if t.isRange {
			if t.numHi, err = numLiteral(v.To); err != nil {
				return t, err
			}
		}
		return t, nil
	}
	var err error
	if t.txtLo, err = txtLiteral(v.From); err != nil {
		return t, err
	}
	if t.isRange {
		if t.txtHi, err = txtLiteral(v.To); err != nil {
			return t, err
		}
	}
	return t, nil
}

func numLiteral(s string) (*big.Rat, error) {
	switch strings.ToUpper(s) {
	case "ZERO", "ZEROS", "ZEROES":
		return new(big.Rat), nil
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("literal %q is not numeric (needed for a numeric field)", s)
	}
	return r, nil
}

func txtLiteral(s string) (string, error) {
	switch strings.ToUpper(s) {
	case "SPACE", "SPACES":
		return "", nil
	case "ZERO", "ZEROS", "ZEROES":
		return "0", nil
	case "QUOTE", "QUOTES":
		return `"`, nil
	case "LOW-VALUE", "LOW-VALUES", "HIGH-VALUE", "HIGH-VALUES", "NULL", "NULLS":
		return "", fmt.Errorf("figurative constant %s is not supported in -where", s)
	}
	return s, nil
}

// Matches evaluates all -where clauses (ANDed) against one raw record.
// With no clauses it is always true.
func (d *Decoder) Matches(rec []byte) (bool, error) {
	for _, w := range d.wheres {
		m, err := w.match(d, rec)
		if err != nil {
			return false, err
		}
		if m == w.negate {
			return false, nil
		}
	}
	return true, nil
}

func (w *where) match(d *Decoder, rec []byte) (bool, error) {
	f := w.field
	if f.Offset+f.Length > len(rec) {
		return false, fmt.Errorf("-where %s: field %s outside record", w.name, f.Name)
	}
	v, err := d.scalar(f, rec[f.Offset:f.Offset+f.Length])
	if err != nil {
		return false, fmt.Errorf("-where %s: %w", w.name, err)
	}
	switch val := v.(type) {
	case json.Number:
		r, err := numLiteral(string(val))
		if err != nil {
			return false, fmt.Errorf("-where %s: cannot compare value %q", w.name, val)
		}
		for _, t := range w.tests {
			if t.isRange {
				if r.Cmp(t.numLo) >= 0 && r.Cmp(t.numHi) <= 0 {
					return true, nil
				}
			} else if r.Cmp(t.numLo) == 0 {
				return true, nil
			}
		}
	case string:
		for _, t := range w.tests {
			if t.isRange {
				// Byte-wise comparison of the decoded UTF-8 text; note this
				// is not EBCDIC collation order.
				if val >= t.txtLo && val <= t.txtHi {
					return true, nil
				}
			} else if val == t.txtLo {
				return true, nil
			}
		}
	}
	return false, nil
}

// cutFold is strings.CutPrefix with ASCII case folding.
func cutFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return "", false
}
