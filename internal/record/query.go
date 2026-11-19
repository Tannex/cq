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
// Because members are addressed individually, a query that reads two fields
// of a 200-field record decodes two fields.
//
// A view snapshots the decoder's layout and filler setting; build a new one
// after changing either.
type QueryView struct {
	d *Decoder

	members []*layout.Field          // visible top-level members, copybook order
	byName  map[string]*layout.Field // last duplicate wins, as in a JSON object
	names   []string                 // unique member names, sorted
	sorted  []*layout.Field          // byName values in names order

	// fixedEnd is the end of the last visible scalar outside the ODO table.
	fixedEnd int
}

// QueryView prepares the decoder's record layout for querying.
func (d *Decoder) QueryView() *QueryView {
	v := &QueryView{d: d, byName: make(map[string]*layout.Field)}
	for _, child := range d.Rec.Field.Children {
		if child.Filler && !d.IncludeFillers {
			continue
		}
		v.members = append(v.members, child)
		v.byName[child.Name] = child
	}
	for name := range v.byName {
		v.names = append(v.names, name)
	}
	sort.Strings(v.names)
	for _, name := range v.names {
		v.sorted = append(v.sorted, v.byName[name])
	}
	v.fixedEnd = d.fixedEnd(d.Rec.Field, 0)
	return v
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

// Names returns the record's member names, sorted and without duplicates.
func (v *QueryView) Names() []string { return v.names }

// Members returns the layout fields behind Names, in the same order.
func (v *QueryView) Members() []*layout.Field { return v.sorted }

// Member returns the field a member name resolves to, or nil.
func (v *QueryView) Member(name string) *layout.Field { return v.byName[name] }

// Span returns the byte range a member occupies in every record. fixed is
// false for the ODO table, whose extent depends on the record.
func (v *QueryView) Span(f *layout.Field) (start, end int, fixed bool) {
	if f == v.d.odo {
		return 0, 0, false
	}
	return f.Offset, f.Offset + f.Length*max(f.Occurs, 1), true
}

// Field decodes one member of a checked record.
func (v *QueryView) Field(rec []byte, f *layout.Field) any {
	return v.d.queryField(f, rec, f.Offset)
}

// Value decodes a whole checked record.
func (v *QueryView) Value(rec []byte) map[string]any {
	value := make(map[string]any, len(v.names))
	for _, member := range v.members {
		value[member.Name] = v.d.queryField(member, rec, member.Offset)
	}
	return value
}

func (d *Decoder) queryField(f *layout.Field, rec []byte, base int) any {
	if f.Occurs == 0 {
		return d.queryOne(f, rec, base)
	}
	count := f.Occurs
	if f == d.odo {
		// Check validated the counter, so this cannot fail.
		count, _ = d.occursCount(rec)
	}
	values := make([]any, count)
	for i := range values {
		values[i] = d.queryOne(f, rec, base+i*f.Length)
	}
	return values
}

func (d *Decoder) queryOne(f *layout.Field, rec []byte, base int) any {
	if f.Kind == layout.KindGroup {
		value := make(map[string]any, len(f.Children))
		for _, child := range f.Children {
			if child.Filler && !d.IncludeFillers {
				continue
			}
			value[child.Name] = d.queryField(child, rec, base+(child.Offset-f.Offset))
		}
		return value
	}
	raw := rec[base : base+f.Length]
	if f.Kind == layout.KindText || f.Kind == layout.KindEdited {
		return decode.DisplayString(raw, d.CM)
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
