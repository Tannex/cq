package record

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/layout"
)

// QueryView decodes records for the jq console, one member at a time.
//
// It produces exactly the values DecodeDisplay does — malformed scalars
// become the same visible placeholder strings — but as the plain containers
// a query engine works on (map[string]any, []any, string, json.Number), and
// without the diagnostics and the copybook ordering a query never observes.
// Because members are addressed individually, at any depth, a query that
// reads two fields of a 200-field record decodes two fields, and one that
// reads one leaf of one occurrence of an OCCURS table decodes that leaf.
//
// A view snapshots the decoder's layout and filler setting; build a new one
// after changing either.
type QueryView struct {
	d    *Decoder
	root *Shape

	// fixedEnd is the end of the last visible scalar outside the ODO table.
	fixedEnd int
}

// Shape is a member of the record layout as a query sees it: a scalar or a
// group, possibly repeated as an OCCURS table. A member is addressed in a
// record by its base, the offset of its first occurrence; occurrence i of a
// table starts Field.Length*i bytes later.
type Shape struct {
	Field *layout.Field

	rel   int  // base relative to the enclosing group's base
	odo   bool // the OCCURS DEPENDING ON table, whose length is in the record
	fixed bool // its value depends only on its own bytes: no ODO table inside
	kind  scalarOrder

	// lo and hi bound the bytes of one occurrence that visible scalars
	// occupy, relative to its base. Bytes outside them cannot change the
	// decoded value, so equal bytes inside them mean equal values.
	lo, hi int

	// Group members: visible children with duplicate names resolved to the
	// last one, as in a JSON object, sorted by name.
	names   []string
	members []*Shape
	byName  map[string]*Shape
}

// Table reports whether the member is an OCCURS table, which decodes to an
// array of its occurrences.
func (s *Shape) Table() bool { return s.Field.Occurs > 0 }

// Group reports whether one occurrence of the member is a group, which
// decodes to an object.
func (s *Shape) Group() bool { return s.Field.Kind == layout.KindGroup }

// Names returns a group's member names, sorted and without duplicates.
func (s *Shape) Names() []string { return s.names }

// Members returns the shapes behind Names, in the same order.
func (s *Shape) Members() []*Shape { return s.members }

// Member returns the group member a name resolves to, or nil.
func (s *Shape) Member(name string) *Shape { return s.byName[name] }

// Base returns the base of a group member given the base of the occurrence
// of the group that holds it.
func (s *Shape) Base(groupBase int) int { return groupBase + s.rel }

// Fixed reports whether the member's value depends only on the bytes Span
// reports. The ODO table, and any group holding it, also depend on the
// counter.
func (s *Shape) Fixed() bool { return s.fixed }

// ElementFixed reports whether one occurrence's value depends only on the
// bytes ElementSpan reports. That holds for an occurrence of the ODO table,
// which cannot hold another.
func (s *Shape) ElementFixed() bool { return s.fixed || s.odo }

// Span returns the byte range whose contents decide the whole member at
// base: every occurrence of a table.
func (s *Shape) Span(base int) (start, end int) {
	if s.lo == s.hi {
		return base, base
	}
	return base + s.lo, base + (max(s.Field.Occurs, 1)-1)*s.Field.Length + s.hi
}

// ElementSpan returns the byte range whose contents decide one occurrence
// at base.
func (s *Shape) ElementSpan(base int) (start, end int) { return base + s.lo, base + s.hi }

// QueryView prepares the decoder's record layout for querying.
func (d *Decoder) QueryView() *QueryView {
	v := &QueryView{d: d}
	v.root = d.queryShape(d.Rec.Field, 0)
	v.fixedEnd = d.fixedEnd(d.Rec.Field, 0)
	return v
}

func (d *Decoder) queryShape(f *layout.Field, rel int) *Shape {
	s := &Shape{Field: f, rel: rel, odo: f == d.odo, fixed: f != d.odo}
	if f.Kind != layout.KindGroup {
		s.kind = scalarOrderOf(f)
		s.hi = f.Length
		return s
	}
	s.byName = make(map[string]*Shape)
	first := true
	for _, child := range f.Children {
		if child.Filler && !d.IncludeFillers {
			continue
		}
		c := d.queryShape(child, child.Offset-f.Offset)
		s.byName[child.Name] = c
		// A shadowed duplicate is never decoded, but its bytes are still
		// covered: the extent only has to be large enough.
		if c.lo != c.hi {
			lo, hi := c.Span(c.rel)
			if first {
				s.lo, s.hi, first = lo, hi, false
			} else {
				s.lo, s.hi = min(s.lo, lo), max(s.hi, hi)
			}
		}
		s.fixed = s.fixed && c.fixed
	}
	for name := range s.byName {
		s.names = append(s.names, name)
	}
	sort.Strings(s.names)
	for _, name := range s.names {
		s.members = append(s.members, s.byName[name])
	}
	return s
}

// fixedEnd returns the largest end offset of any visible scalar below g that
// is not part of the ODO table, with base shifting g as for array elements.
func (d *Decoder) fixedEnd(g *layout.Field, base int) int {
	end := 0
	for _, child := range g.Children {
		if child.Filler && !d.IncludeFillers {
			continue
		}
		if child == d.odo {
			continue
		}
		last := base + (child.Offset - g.Offset) + (max(child.Occurs, 1)-1)*child.Length
		if child.Kind == layout.KindGroup {
			end = max(end, d.fixedEnd(child, last))
		} else {
			end = max(end, last+child.Length)
		}
	}
	return end
}

// Check reports whether rec can be decoded, with the error DecodeDisplay
// would return. Every other method requires a record that passed Check.
func (v *QueryView) Check(rec []byte) error {
	if err := v.d.validateDisplayRecord(rec); err != nil {
		return err
	}
	if len(rec) < v.fixedEnd {
		// Only reachable for layouts whose fixed part overlaps the ODO
		// table; DecodeDisplay fails on the first field past the record.
		_, err := v.d.DecodeDisplay(rec)
		if err == nil {
			err = fmt.Errorf("record %s is short: layout requires %d bytes, got %d", v.d.Rec.Name, v.fixedEnd, len(rec))
		}
		return err
	}
	return nil
}

// Root returns the shape of the record itself, a group at base 0.
func (v *QueryView) Root() *Shape { return v.root }

// Names returns the record's member names, sorted and without duplicates.
func (v *QueryView) Names() []string { return v.root.names }

// Members returns the shapes behind Names, in the same order.
func (v *QueryView) Members() []*Shape { return v.root.members }

// Member returns the shape a top-level member name resolves to, or nil.
func (v *QueryView) Member(name string) *Shape { return v.root.byName[name] }

// Span returns the byte range that decides a top-level member in every
// record. fixed is false for the ODO table and any group holding it, whose
// values also depend on the counter and whose extent depends on the record.
func (v *QueryView) Span(s *Shape) (start, end int, fixed bool) {
	start, end = s.Span(s.Base(0))
	return start, end, s.fixed
}

// Field decodes one top-level member of a checked record.
func (v *QueryView) Field(rec []byte, s *Shape) any {
	return v.d.queryMember(s, rec, s.Base(0))
}

// Value decodes a whole checked record.
func (v *QueryView) Value(rec []byte) map[string]any {
	return v.d.queryGroup(v.root, rec, 0)
}

// Count returns the number of occurrences of a table in a checked record.
func (v *QueryView) Count(rec []byte, s *Shape) int {
	if s.odo {
		// Check validated the counter, so this cannot fail.
		count, _ := v.d.occursCount(rec)
		return count
	}
	return s.Field.Occurs
}

// Element decodes one occurrence of a member at base: a scalar, or a group
// as a native object.
func (v *QueryView) Element(rec []byte, s *Shape, base int) any {
	return v.d.queryOne(s, rec, base)
}

func (d *Decoder) queryMember(s *Shape, rec []byte, base int) any {
	if s.Field.Occurs == 0 {
		return d.queryOne(s, rec, base)
	}
	count := s.Field.Occurs
	if s.odo {
		count, _ = d.occursCount(rec)
	}
	values := make([]any, count)
	for i := range values {
		values[i] = d.queryOne(s, rec, base+i*s.Field.Length)
	}
	return values
}

func (d *Decoder) queryOne(s *Shape, rec []byte, base int) any {
	if s.Field.Kind == layout.KindGroup {
		return d.queryGroup(s, rec, base)
	}
	return d.queryScalar(s, rec[base:base+s.Field.Length])
}

// queryGroup decodes the visible members a name resolves to; a shadowed
// duplicate would only be overwritten.
func (d *Decoder) queryGroup(s *Shape, rec []byte, base int) map[string]any {
	value := make(map[string]any, len(s.members))
	for i, member := range s.members {
		value[s.names[i]] = d.queryMember(member, rec, base+member.rel)
	}
	return value
}

func (d *Decoder) queryScalar(s *Shape, raw []byte) any {
	f := s.Field
	if f.Kind == layout.KindText || f.Kind == layout.KindEdited {
		return decode.DisplayString(raw, d.CM)
	}
	if value, ok := d.numberToken(s, raw); ok {
		return value
	}
	value, err := d.scalar(f, raw)
	if err == nil && f.Kind == layout.KindZoned && f.SignSeparate && separateSignByte(f, raw) == 0 {
		return d.invalidScalarDisplay(f, raw)
	}
	if err == nil {
		if number, ok := value.(json.Number); !ok || validJSONNumber(string(number)) {
			return value
		}
	}
	return d.invalidScalarDisplay(f, raw)
}

// validJSONNumber reports whether s is a number token encoding/json accepts
// when marshaling a json.Number, which is the test DecodeDisplay applies.
func validJSONNumber(s string) bool {
	if s == "" {
		return true // marshals as 0
	}
	if s[0] == '-' {
		s = s[1:]
		if s == "" {
			return false
		}
	}
	switch {
	case s[0] == '0':
		s = s[1:]
	case '1' <= s[0] && s[0] <= '9':
		s = s[1:]
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	default:
		return false
	}
	if len(s) >= 2 && s[0] == '.' && '0' <= s[1] && s[1] <= '9' {
		s = s[2:]
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	}
	if len(s) >= 2 && (s[0] == 'e' || s[0] == 'E') {
		s = s[1:]
		if s[0] == '+' || s[0] == '-' {
			s = s[1:]
			if s == "" {
				return false
			}
		}
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	}
	return s == ""
}
