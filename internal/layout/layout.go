// Package layout turns a parsed copybook into a byte-accurate field map:
// offset, length, and decode kind for every item.
package layout

import (
	"fmt"

	"github.com/Tannex/cq/internal/copybook"
)

// Kind tells the decoder how to interpret a field's bytes.
type Kind string

const (
	KindGroup  Kind = "group"
	KindText   Kind = "text"   // PIC X/A, USAGE DISPLAY
	KindZoned  Kind = "zoned"  // PIC 9, USAGE DISPLAY
	KindEdited Kind = "edited" // numeric-edited picture, decoded as text
	KindPacked Kind = "packed" // COMP-3
	KindBinary Kind = "binary" // COMP / BINARY
	KindFloat  Kind = "float"  // COMP-1 / COMP-2
)

// Field is one resolved copybook item.
type Field struct {
	Name         string      `json:"name"`
	Level        int         `json:"level"`
	Offset       int         `json:"offset"`
	Length       int         `json:"length"` // one element; total storage = Length*max(Occurs,1)
	Kind         Kind        `json:"kind"`
	Picture      string      `json:"picture,omitempty"`
	Digits       int         `json:"digits,omitempty"`
	Scale        int         `json:"scale,omitempty"`
	Signed       bool        `json:"signed,omitempty"`
	SignSeparate bool        `json:"signSeparate,omitempty"`
	SignLeading  bool        `json:"signLeading,omitempty"`
	Occurs       int         `json:"occurs,omitempty"`
	OccursMin    int         `json:"occursMin,omitempty"`
	DependingOn  string      `json:"dependingOn,omitempty"`
	Redefines    string      `json:"redefines,omitempty"`
	Filler       bool        `json:"filler,omitempty"`
	Conditions   []Condition `json:"conditions,omitempty"` // level-88 entries
	Children     []*Field    `json:"children,omitempty"`

	align  int // SYNC boundary (0/1 = none)
	picMin int // minimum group length from a PICTURE on the group itself
}

// Condition is a level-88 condition name with its VALUE literals.
type Condition struct {
	Name   string  `json:"name"`
	Values []Value `json:"values"`
}

// Value is one condition literal; To is set for THRU ranges. Figurative
// constants (ZERO, SPACES, ...) appear as their keyword.
type Value struct {
	From string `json:"value"`
	To   string `json:"thru,omitempty"`
}

// Record is a top-level (01) layout.
type Record struct {
	*Field
	MinLength int // shortest possible record (ODO at minimum)
	MaxLength int // Field.Length; differs from MinLength when variable
}

// Variable reports whether the record length depends on the data.
func (r *Record) Variable() bool { return r.MinLength != r.MaxLength }

// Build resolves all copybook items into record layouts. When the copybook
// is a fragment (no 01 levels, several top-level items), the items are
// wrapped in a single synthetic record.
func Build(items []*copybook.Item) ([]*Record, error) {
	min := items[0].Level
	for _, it := range items {
		if it.Level < min {
			min = it.Level
		}
	}
	if min > 1 && len(items) > 1 {
		items = []*copybook.Item{{Level: 1, Name: "RECORD", Children: items, Line: items[0].Line}}
	}
	var recs []*Record
	for _, it := range items {
		f, err := resolve(it, copybook.UsageDisplay, false, false, false)
		if err != nil {
			return nil, err
		}
		if _, err := place(f, 0); err != nil {
			return nil, err
		}
		rec := &Record{Field: f, MaxLength: f.Length, MinLength: f.Length}
		if v := odoSlack(f); v > 0 {
			rec.MinLength -= v
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

// resolve computes kinds and per-element lengths bottom-up. usage, sign
// options inherit from group items.
func resolve(it *copybook.Item, usage copybook.Usage, sep, lead, sync bool) (*Field, error) {
	if it.UsageSet {
		usage = it.Usage
	}
	if it.SignSeparate {
		sep = true
	}
	if it.SignLeading {
		lead = true
	}
	if it.Sync {
		sync = true
	}
	f := &Field{
		Name: it.Name, Level: it.Level, Occurs: it.Occurs, OccursMin: it.OccursMin,
		DependingOn: it.DependingOn, Redefines: it.Redefines, Filler: it.Filler,
	}
	for _, c := range it.Conditions {
		lc := Condition{Name: c.Name}
		for _, v := range c.Values {
			lc.Values = append(lc.Values, Value{From: v.From, To: v.To})
		}
		f.Conditions = append(f.Conditions, lc)
	}
	if len(it.Children) > 0 {
		f.Kind = KindGroup
		if it.Pic != nil {
			// Invalid COBOL that tools tolerate: an item with both a
			// PICTURE and subordinates. Children overlay the PIC storage,
			// and the group is at least as long as the picture.
			f.Picture, f.picMin = it.Pic.Raw, it.Pic.Width
		}
		for _, c := range it.Children {
			cf, err := resolve(c, usage, sep, lead, sync)
			if err != nil {
				return nil, err
			}
			f.Children = append(f.Children, cf)
		}
		return f, nil
	}

	// Elementary item.
	switch usage {
	case copybook.UsageFloat4:
		f.Kind, f.Length = KindFloat, 4
		if sync {
			f.align = 4
		}
		return f, nil
	case copybook.UsageFloat8:
		f.Kind, f.Length = KindFloat, 8
		if sync {
			f.align = 8
		}
		return f, nil
	}
	pic := it.Pic
	if pic == nil {
		return nil, &copybook.ParseError{Line: it.Line, Msg: fmt.Sprintf("%s has no PICTURE clause", it.Name)}
	}
	f.Picture, f.Digits, f.Scale, f.Signed = pic.Raw, pic.Digits, pic.Scale, pic.Signed
	switch {
	case usage == copybook.UsageBinary:
		f.Kind = KindBinary
		switch {
		case pic.Digits <= 4:
			f.Length = 2
		case pic.Digits <= 9:
			f.Length = 4
		case pic.Digits <= 18:
			f.Length = 8
		default:
			return nil, &copybook.ParseError{Line: it.Line, Msg: fmt.Sprintf("%s: more than 18 digits in binary item", it.Name)}
		}
		if sync {
			f.align = f.Length
		}
	case usage == copybook.UsagePacked:
		f.Kind = KindPacked
		f.Length = pic.Digits/2 + 1
	case pic.Category == copybook.CatNumeric:
		f.Kind = KindZoned
		f.Length = pic.Digits
		if pic.Signed && sep {
			f.Length++
			f.SignSeparate = true
			f.SignLeading = lead
		}
	case pic.Category == copybook.CatNumericEdited:
		f.Kind, f.Length = KindEdited, pic.Width
	default:
		f.Kind, f.Length = KindText, pic.Width
	}
	if f.Length <= 0 {
		return nil, &copybook.ParseError{Line: it.Line, Msg: fmt.Sprintf("%s: zero-length field", it.Name)}
	}
	return f, nil
}

// place assigns offsets. Returns the end offset (start + total length).
// Group length is derived from its children, honouring REDEFINES and SYNC.
func place(f *Field, start int) (int, error) {
	f.Offset = start
	if f.Kind != KindGroup {
		return start + f.total(), nil
	}
	cur := start
	end := start
	byName := map[string]*Field{}
	for _, c := range f.Children {
		at := cur
		if c.Redefines != "" {
			t, ok := byName[c.Redefines]
			if !ok {
				return 0, fmt.Errorf("%s REDEFINES %s, but %s is not an earlier item at the same level", c.Name, c.Redefines, c.Redefines)
			}
			at = t.Offset
		} else if a := c.align; a > 1 && at%a != 0 {
			at += a - at%a // SYNC slack bytes
		}
		e, err := place(c, at)
		if err != nil {
			return 0, err
		}
		if !c.Filler {
			byName[c.Name] = c
		}
		if e > end {
			end = e
		}
		if c.Redefines == "" {
			cur = e
		} else if e > cur {
			cur = e // redefinition longer than its target
		}
	}
	f.Length = end - start
	if f.Length < f.picMin {
		f.Length = f.picMin
	}
	return start + f.total(), nil
}

// total is the full storage of the field including all occurrences.
func (f *Field) total() int {
	n := f.Occurs
	if n == 0 {
		n = 1
	}
	return f.Length * n
}

// odoSlack returns the storage between minimum and maximum occurrences of
// the record's OCCURS DEPENDING ON tables.
func odoSlack(f *Field) int {
	v := 0
	Walk(f, func(f *Field) {
		if f.DependingOn != "" {
			v += (f.Occurs - f.OccursMin) * f.Length
		}
	})
	return v
}

// Walk visits every field depth-first, parents before children.
func Walk(f *Field, fn func(*Field)) {
	fn(f)
	for _, c := range f.Children {
		Walk(c, fn)
	}
}

// FindByName returns the first field with the given name (depth-first).
func FindByName(root *Field, name string) *Field {
	if root.Name == name && !root.Filler {
		return root
	}
	for _, child := range root.Children {
		if found := FindByName(child, name); found != nil {
			return found
		}
	}
	return nil
}
