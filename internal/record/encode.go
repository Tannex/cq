package record

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Tannex/cq/internal/decode"
	cobolencode "github.com/Tannex/cq/internal/encode"
	"github.com/Tannex/cq/internal/layout"
)

// Encoder encodes JSON objects as records described by a single layout.
type Encoder struct {
	Rec   *layout.Record
	CM    *decode.Charmap
	Lrecl int

	odo     *layout.Field
	counter *layout.Field
}

// NewEncoder validates that rec can be encoded unambiguously.
func NewEncoder(rec *layout.Record, cm *decode.Charmap) (*Encoder, error) {
	if rec.Kind != layout.KindGroup {
		return nil, fmt.Errorf("record %s: elementary 01-level records are not supported", rec.Name)
	}
	e := &Encoder{Rec: rec, CM: cm}

	var odos []*layout.Field
	layout.Walk(rec.Field, func(f *layout.Field) {
		if f.DependingOn != "" {
			odos = append(odos, f)
		}
	})
	if len(odos) > 1 {
		return nil, fmt.Errorf("record %s: multiple OCCURS DEPENDING ON tables are not supported", rec.Name)
	}
	if len(odos) == 1 {
		f := odos[0]
		if found, usable := primaryFieldPath(rec.Field, f, false, true); !found || !usable {
			return nil, fmt.Errorf("record %s: OCCURS DEPENDING ON table %s must be on the primary non-OCCURS path", rec.Name, f.Name)
		}
		if f.Offset+f.Length*f.Occurs != rec.MaxLength {
			return nil, fmt.Errorf("record %s: OCCURS DEPENDING ON table %s is not at the end of the record; only trailing ODO is supported", rec.Name, f.Name)
		}
		c := layout.FindByName(rec.Field, f.DependingOn)
		if c == nil {
			return nil, fmt.Errorf("record %s: DEPENDING ON field %s not found", rec.Name, f.DependingOn)
		}
		if c.Kind == layout.KindGroup || c.Occurs > 0 {
			return nil, fmt.Errorf("record %s: DEPENDING ON field %s must be an elementary scalar", rec.Name, c.Name)
		}
		if found, usable := primaryFieldPath(rec.Field, c, false, false); !found || !usable {
			return nil, fmt.Errorf("record %s: DEPENDING ON field %s must be on the primary non-OCCURS path", rec.Name, c.Name)
		}
		if c.Offset+c.Length > f.Offset {
			return nil, fmt.Errorf("record %s: DEPENDING ON field %s does not precede table %s", rec.Name, c.Name, f.Name)
		}
		e.odo, e.counter = f, c
	}

	if err := validateJSONNames(rec.Field); err != nil {
		return nil, fmt.Errorf("record %s: %w", rec.Name, err)
	}
	return e, nil
}

func primaryFieldPath(f, target *layout.Field, unavailable, allowTargetOccurs bool) (bool, bool) {
	for _, child := range f.Children {
		occursUnavailable := child.Occurs > 0 && (child != target || !allowTargetOccurs)
		childUnavailable := unavailable || child.Redefines != "" || occursUnavailable
		if child == target {
			return true, !childUnavailable
		}
		if found, usable := primaryFieldPath(child, target, childUnavailable, allowTargetOccurs); found {
			return true, usable
		}
	}
	return false, false
}

func validateJSONNames(g *layout.Field) error {
	if g.Kind != layout.KindGroup {
		return nil
	}
	seen := make(map[string]bool)
	primary := make(map[string]*layout.Field)
	for _, f := range g.Children {
		if !f.Filler {
			if seen[f.Name] {
				return fmt.Errorf("duplicate field name %s at the same group level cannot be represented as JSON", f.Name)
			}
			seen[f.Name] = true
		}
		if f.Redefines == "" {
			if !f.Filler {
				primary[f.Name] = f
			}
		} else {
			target := primary[f.Redefines]
			if target == nil {
				return fmt.Errorf("field %s redefines unavailable primary field %s", f.Name, f.Redefines)
			}
			if fieldStorage(f) > fieldStorage(target) {
				return fmt.Errorf("field %s is longer than primary field %s; primary-view encoding would leave bytes undefined", f.Name, target.Name)
			}
			primary[f.Name] = target
		}
		if err := validateJSONNames(f); err != nil {
			return err
		}
	}
	return nil
}

func fieldStorage(f *layout.Field) int {
	n := f.Occurs
	if n == 0 {
		n = 1
	}
	return f.Length * n
}

// Encode validates one JSON object and returns its canonical record bytes.
func (e *Encoder) Encode(obj jsonObject) ([]byte, error) {
	count, err := e.odoCount(obj)
	if err != nil {
		return nil, err
	}
	recordLen := e.Rec.MaxLength
	if e.odo != nil {
		recordLen = e.odo.Offset + count*e.odo.Length
	}
	outLen := recordLen
	if e.Lrecl > outLen {
		outLen = e.Lrecl
	}
	pad, err := cobolencode.String("", outLen, e.CM)
	if err != nil {
		return nil, err
	}
	out := pad
	if err := e.fillDefaults(e.Rec.Field, out, 0, count, false); err != nil {
		return nil, err
	}
	if err := e.encodeGroup(e.Rec.Field, obj, out, 0, e.Rec.Name, count, true); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *Encoder) odoCount(obj jsonObject) (int, error) {
	if e.odo == nil {
		return 0, nil
	}
	v, path, ok := findFieldValue(e.Rec.Field, obj, e.counter, e.Rec.Name)
	if !ok {
		return 0, fmt.Errorf("%s: missing DEPENDING ON field %s", path, e.counter.Name)
	}
	num, ok := v.(json.Number)
	if !ok {
		return 0, fmt.Errorf("%s: want JSON number, got %s", path, jsonType(v))
	}
	n, err := strconv.Atoi(string(num))
	if err != nil {
		return 0, fmt.Errorf("%s: DEPENDING ON value %s is not an integer", path, num)
	}
	// Accept the same 0..Occurs range as the decoder so decoded records
	// can always be regenerated, even when the data sits below OccursMin.
	if n < 0 || n > e.odo.Occurs {
		return 0, fmt.Errorf("%s: count %d outside 0..%d", path, n, e.odo.Occurs)
	}
	return n, nil
}

func findFieldValue(g *layout.Field, obj jsonObject, target *layout.Field, path string) (any, string, bool) {
	for _, f := range g.Children {
		if f.Filler || f.Redefines != "" {
			continue
		}
		v, ok := obj[f.Name]
		fieldPath := path + "." + f.Name
		if f == target {
			return v, fieldPath, ok
		}
		if !ok || f.Kind != layout.KindGroup || f.Occurs > 0 {
			continue
		}
		child, ok := v.(jsonObject)
		if !ok {
			continue
		}
		if got, gotPath, found := findFieldValue(f, child, target, fieldPath); found {
			return got, gotPath, true
		}
	}
	return nil, path + "." + target.Name, false
}

// fillDefaults writes default bytes into FILLER storage (including fields
// nested under FILLER groups). Every other scalar is required JSON input
// and is written by encodeGroup.
func (e *Encoder) fillDefaults(f *layout.Field, out []byte, base, odoCount int, underFiller bool) error {
	if f.Redefines != "" {
		return nil
	}
	filler := underFiller || f.Filler
	n := 1
	if f.Occurs > 0 {
		n = f.Occurs
		if f == e.odo {
			n = odoCount
		}
	}
	for i := 0; i < n; i++ {
		at := base + i*f.Length
		if f.Kind == layout.KindGroup {
			for _, child := range f.Children {
				if err := e.fillDefaults(child, out, at+(child.Offset-f.Offset), odoCount, filler); err != nil {
					return err
				}
			}
			continue
		}
		if !filler {
			continue
		}
		b, err := e.encodeScalar(f, zeroValue(f))
		if err != nil {
			return fmt.Errorf("defaulting FILLER %s: %w", f.Name, err)
		}
		if at+len(b) > len(out) {
			return fmt.Errorf("field %s exceeds encoded record length", f.Name)
		}
		copy(out[at:at+len(b)], b)
	}
	return nil
}

func zeroValue(f *layout.Field) any {
	if f.Kind == layout.KindText || f.Kind == layout.KindEdited {
		return ""
	}
	return json.Number("0")
}

func (e *Encoder) encodeGroup(g *layout.Field, obj jsonObject, out []byte, base int, path string, odoCount int, write bool) error {
	known := make(map[string]bool)
	fillerCount := 0
	for _, f := range g.Children {
		if f.Filler {
			fillerCount++
		} else {
			known[f.Name] = true
		}
	}
	for name := range obj {
		if !known[name] && !(name == fillerName && fillerCount > 0) {
			return fmt.Errorf("%s: unknown field %q", path, name)
		}
	}
	// FILLER values are optional as a whole: absent fillers keep their
	// defaults, present ones (as -fillers decoding emits them) map to the
	// group's FILLER fields in copybook order and must cover all of them.
	fillers, _ := obj[fillerName].(fillerValues)
	if len(fillers) > 0 && len(fillers) != fillerCount {
		return fmt.Errorf("%s: got %d FILLER values, want %d", path, len(fillers), fillerCount)
	}
	fillerIndex := 0
	for _, f := range g.Children {
		var v any
		if f.Filler {
			if len(fillers) == 0 {
				continue
			}
			v = fillers[fillerIndex]
			fillerIndex++
		} else {
			value, ok := obj[f.Name]
			if !ok {
				return fmt.Errorf("%s.%s: missing field", path, f.Name)
			}
			v = value
		}
		fieldPath := path + "." + f.Name
		fieldWrite := write && f.Redefines == ""
		if err := e.encodeField(f, v, out, base+(f.Offset-g.Offset), fieldPath, odoCount, fieldWrite); err != nil {
			return err
		}
	}
	return nil
}

func (e *Encoder) encodeField(f *layout.Field, value any, out []byte, base int, path string, odoCount int, write bool) error {
	if value == nil {
		return fmt.Errorf("%s: null is not allowed", path)
	}
	if f.Occurs > 0 {
		values, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s: want JSON array, got %s", path, jsonType(value))
		}
		want := f.Occurs
		if f == e.odo {
			want = odoCount
		}
		if len(values) != want {
			return fmt.Errorf("%s: got %d elements, want %d", path, len(values), want)
		}
		for i, v := range values {
			if err := e.encodeOne(f, v, out, base+i*f.Length, fmt.Sprintf("%s[%d]", path, i), odoCount, write); err != nil {
				return err
			}
		}
		return nil
	}
	return e.encodeOne(f, value, out, base, path, odoCount, write)
}

func (e *Encoder) encodeOne(f *layout.Field, value any, out []byte, base int, path string, odoCount int, write bool) error {
	if f.Kind == layout.KindGroup {
		obj, ok := value.(jsonObject)
		if !ok {
			return fmt.Errorf("%s: want JSON object, got %s", path, jsonType(value))
		}
		return e.encodeGroup(f, obj, out, base, path, odoCount, write)
	}
	b, err := e.encodeScalar(f, value)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if write {
		if base+len(b) > len(out) {
			return fmt.Errorf("%s: field exceeds encoded record length", path)
		}
		copy(out[base:base+len(b)], b)
	}
	return nil
}

func (e *Encoder) encodeScalar(f *layout.Field, value any) ([]byte, error) {
	switch f.Kind {
	case layout.KindText, layout.KindEdited:
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("want JSON string, got %s", jsonType(value))
		}
		return cobolencode.String(s, f.Length, e.CM)
	case layout.KindZoned:
		n, ok := value.(json.Number)
		if !ok {
			return nil, fmt.Errorf("want JSON number, got %s", jsonType(value))
		}
		if f.SignSeparate {
			return cobolencode.ZonedSeparate(n, f.Digits, f.Scale, f.Signed, f.SignLeading, e.CM)
		}
		return cobolencode.Zoned(n, f.Digits, f.Scale, f.Signed, e.CM)
	case layout.KindPacked:
		n, ok := value.(json.Number)
		if !ok {
			return nil, fmt.Errorf("want JSON number, got %s", jsonType(value))
		}
		return cobolencode.Packed(n, f.Digits, f.Scale, f.Signed)
	case layout.KindBinary:
		n, ok := value.(json.Number)
		if !ok {
			return nil, fmt.Errorf("want JSON number, got %s", jsonType(value))
		}
		return cobolencode.Binary(n, f.Length, f.Scale, f.Signed)
	case layout.KindFloat:
		n, ok := value.(json.Number)
		if !ok {
			return nil, fmt.Errorf("want JSON number, got %s", jsonType(value))
		}
		return cobolencode.Float(n, f.Length)
	default:
		return nil, fmt.Errorf("cannot encode kind %s", f.Kind)
	}
}

func jsonType(v any) string {
	switch v.(type) {
	case jsonObject:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%T", v)
	}
}
