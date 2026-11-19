package recordset

import (
	"bytes"
	"cmp"

	"github.com/Tannex/cq/internal/gojq"
	"github.com/Tannex/cq/internal/record"
)

// Groups and OCCURS tables below a record are lazy too, so `.CUSTOMER.TIER`
// decodes one scalar and `.ITEMS[1].QUANTITY` one scalar of one occurrence
// instead of the whole group or table. Their Map and Slice still decode
// fully, to native values, as Record.Map does: those serve output, keys,
// to_entries and the like, which read every member anyway, and nested lazy
// values there would only add allocations.

// node addresses a member of one record: one occurrence of a group, or a
// whole table, whose first occurrence starts at base.
type node struct {
	set    *Set
	offset int64
	shape  *record.Shape
	base   int32
	count  int32 // occurrences, for a table
}

func (n *node) bytes() []byte { return n.set.recordAt(n.offset) }

// Group is one occurrence of a group: a jq object whose members are decoded
// from the raw bytes when read.
type Group struct{ node }

// Table is an OCCURS table: a jq array whose occurrences are decoded from
// the raw bytes when read.
type Table struct{ node }

var (
	_ gojq.LazyObject   = (*Group)(nil)
	_ gojq.LazyComparer = (*Group)(nil)
	_ gojq.LazyArray    = (*Table)(nil)
	_ gojq.LazyComparer = (*Table)(nil)
)

// member returns the value of member m, whose base is base, of the record
// at offset: a scalar is decoded, a group or a table stays lazy.
func (s *Set) member(offset int64, rec []byte, m *record.Shape, base int) any {
	switch {
	case m.Table():
		return &Table{node{set: s, offset: offset, shape: m, base: int32(base), count: int32(s.view.Count(rec, m))}}
	case m.Group():
		return &Group{node{set: s, offset: offset, shape: m, base: int32(base)}}
	default:
		return s.view.Element(rec, m, base)
	}
}

// Get implements gojq.LazyObject: it decodes the one member asked for.
func (g *Group) Get(key string) any {
	m := g.shape.Member(key)
	if m == nil {
		return nil
	}
	return g.set.member(g.offset, g.bytes(), m, m.Base(int(g.base)))
}

// Map implements gojq.LazyObject: it decodes the whole group.
func (g *Group) Map() map[string]any {
	return g.set.view.Element(g.bytes(), g.shape, int(g.base)).(map[string]any)
}

// CompareLazy implements gojq.LazyComparer for two occurrences of the same
// group of the same set's layout, which share their keys.
func (g *Group) CompareLazy(other any) (int, bool) {
	o, ok := other.(*Group)
	if !ok || o.shape != g.shape {
		return 0, false
	}
	return g.set.compareOne(g.shape, g.bytes(), int(g.base), o.bytes(), int(o.base)), true
}

// Len implements gojq.LazyArray.
func (t *Table) Len() int { return int(t.count) }

// At implements gojq.LazyArray.
func (t *Table) At(i int) any {
	base := int(t.base) + i*t.shape.Field.Length
	if t.shape.Group() {
		return &Group{node{set: t.set, offset: t.offset, shape: t.shape, base: int32(base)}}
	}
	return t.set.view.Element(t.bytes(), t.shape, base)
}

// Slice implements gojq.LazyArray: it decodes the occurrences.
func (t *Table) Slice(start, end int) []any {
	rec, view := t.bytes(), t.set.view
	elements := make([]any, end-start)
	for i := range elements {
		elements[i] = view.Element(rec, t.shape, int(t.base)+(start+i)*t.shape.Field.Length)
	}
	return elements
}

// CompareLazy implements gojq.LazyComparer for two instances of the same
// table of the same set's layout.
func (t *Table) CompareLazy(other any) (int, bool) {
	o, ok := other.(*Table)
	if !ok || o.shape != t.shape {
		return 0, false
	}
	return t.set.compareTable(t.shape, t.bytes(), int(t.base), int(t.count), o.bytes(), int(o.base), int(o.count)), true
}

// The comparisons below order members as gojq.Compare orders their decoded
// values, without decoding what they do not have to. Both sides are the same
// member of one layout, so objects have the same keys, compared by value in
// key order, and arrays compare by occurrence and then by length. Wherever
// the bytes that decide a value are equal on both sides, the values are.

// compareOne orders one occurrence of a member in record a at baseA with
// one in record b at baseB.
func (s *Set) compareOne(m *record.Shape, a []byte, baseA int, b []byte, baseB int) int {
	if m.ElementFixed() {
		startA, endA := m.ElementSpan(baseA)
		startB, endB := m.ElementSpan(baseB)
		if equalSpans(a, startA, endA, b, startB, endB) {
			return 0
		}
	}
	if m.Group() {
		return s.compareMembers(m, a, baseA, b, baseB)
	}
	n := m.Field.Length
	if order, ok := s.view.CompareScalar(m, a[baseA:baseA+n], b[baseB:baseB+n]); ok {
		return order
	}
	return gojq.Compare(s.view.Element(a, m, baseA), s.view.Element(b, m, baseB))
}

// compareMembers orders two occurrences of group g by their members.
func (s *Set) compareMembers(g *record.Shape, a []byte, baseA int, b []byte, baseB int) int {
	for _, m := range g.Members() {
		memberA, memberB := m.Base(baseA), m.Base(baseB)
		var order int
		if m.Table() {
			order = s.compareTable(m, a, memberA, s.view.Count(a, m), b, memberB, s.view.Count(b, m))
		} else {
			order = s.compareOne(m, a, memberA, b, memberB)
		}
		if order != 0 {
			return order
		}
	}
	return 0
}

// compareTable orders two instances of table t, of countA and countB
// occurrences.
func (s *Set) compareTable(t *record.Shape, a []byte, baseA, countA int, b []byte, baseB, countB int) int {
	if t.Fixed() {
		startA, endA := t.Span(baseA)
		startB, endB := t.Span(baseB)
		if equalSpans(a, startA, endA, b, startB, endB) {
			return 0
		}
	}
	for i := range min(countA, countB) {
		offset := i * t.Field.Length
		if order := s.compareOne(t, a, baseA+offset, b, baseB+offset); order != 0 {
			return order
		}
	}
	return cmp.Compare(countA, countB)
}

// equalSpans reports whether two byte ranges are equal. A range past the
// end of its record is never reported equal, so the caller compares the
// members it can decode instead.
func equalSpans(a []byte, startA, endA int, b []byte, startB, endB int) bool {
	return endA <= len(a) && endB <= len(b) && bytes.Equal(a[startA:endA], b[startB:endB])
}
