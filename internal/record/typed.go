package record

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/layout"
)

// Value is one decoded value. Decoder-produced values are Object, Array,
// string, or json.Number.
type Value any

// Member is one named object value. Objects are slices, rather than maps, so
// copybook order and duplicate names are retained.
type Member struct {
	Name  string
	Value Value
}

// Object is an ordered collection of named values.
type Object []Member

// Lookup returns the first member with name.
func (o Object) Lookup(name string) (Value, bool) {
	for _, member := range o {
		if member.Name == name {
			return member.Value, true
		}
	}
	return nil, false
}

// MarshalJSON writes members in their copybook order.
func (o Object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, member := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, err := json.Marshal(member.Name)
		if err != nil {
			return nil, err
		}
		buf.Write(name)
		buf.WriteByte(':')
		value, err := marshalValue(member.Value)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", member.Name, err)
		}
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Array is an ordered collection of decoded values.
type Array []Value

// MarshalJSON preserves exact json.Number tokens in array elements.
func (a Array) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, value := range a {
		if i > 0 {
			buf.WriteByte(',')
		}
		encoded, err := marshalValue(value)
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", i, err)
		}
		buf.Write(encoded)
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}

func marshalValue(value Value) ([]byte, error) {
	switch value := value.(type) {
	case Object:
		return value.MarshalJSON()
	case Array:
		return value.MarshalJSON()
	case string:
		return json.Marshal(value)
	case json.Number:
		return json.Marshal(value)
	default:
		return nil, fmt.Errorf("unsupported decoded value type %T", value)
	}
}

// Diagnostic describes one scalar value localized by TUI-lenient decoding.
type Diagnostic struct {
	FieldPath string
	Offset    int
	Length    int
	RawHex    string
	Err       error
}

func (d Diagnostic) Error() string {
	return fmt.Sprintf("field %s at offset %d length %d (raw %s): %v", d.FieldPath, d.Offset, d.Length, d.RawHex, d.Err)
}

// Unwrap exposes the original scalar decoding error.
func (d Diagnostic) Unwrap() error { return d.Err }

// ErrLowValue identifies a text or edited field whose 0x00 bytes were made
// visible as U+00B7 MIDDLE DOT.
var ErrLowValue = errors.New("LOW-VALUE substituted with U+00B7")

// DecodedRecord holds an ordered decoded value and any localized diagnostics.
type DecodedRecord struct {
	Value       Object
	Diagnostics []Diagnostic
}

// MarshalJSON marshals only the record value. Diagnostics are presentation
// metadata and are intentionally not mixed into the user's data.
func (r DecodedRecord) MarshalJSON() ([]byte, error) { return r.Value.MarshalJSON() }

// JSON returns compact, ordered JSON for the record value.
func (r DecodedRecord) JSON() ([]byte, error) { return r.MarshalJSON() }

// DecodeDisplay decodes a record for the TUI. Malformed independent scalar
// fields become visible strings with diagnostics; structural errors remain
// fatal.
func (d *Decoder) DecodeDisplay(rec []byte) (DecodedRecord, error) {
	if err := d.validateDisplayRecord(rec); err != nil {
		return DecodedRecord{}, err
	}

	var diagnostics []Diagnostic
	value, err := d.decodeDisplayGroup(d.Rec.Field, rec, 0, "", &diagnostics)
	if err != nil {
		return DecodedRecord{}, err
	}
	return DecodedRecord{Value: value, Diagnostics: diagnostics}, nil
}

func (d *Decoder) validateDisplayRecord(rec []byte) error {
	want := d.Rec.MaxLength
	if d.odo != nil {
		counterEnd := d.counter.Offset + d.counter.Length
		if counterEnd > len(rec) {
			return fmt.Errorf("field %s at offset %d:%d exceeds record length %d", d.counter.Name, d.counter.Offset, counterEnd, len(rec))
		}
		count, err := d.occursCount(rec)
		if err != nil {
			return err
		}
		if count < d.odo.OccursMin {
			return fmt.Errorf("DEPENDING ON field %s: count %d outside %d..%d", d.counter.Name, count, d.odo.OccursMin, d.odo.Occurs)
		}
		want = d.odo.Offset + count*d.odo.Length
	}
	if len(rec) < want {
		return fmt.Errorf("record %s is short: layout requires %d bytes, got %d", d.Rec.Name, want, len(rec))
	}
	return nil
}

func (d *Decoder) decodeDisplayField(f *layout.Field, rec []byte, base int, path string, diagnostics *[]Diagnostic) (Value, error) {
	if f.Occurs > 0 {
		count := f.Occurs
		if f == d.odo {
			var err error
			count, err = d.occursCount(rec)
			if err != nil {
				return nil, err
			}
		}
		values := make(Array, 0, count)
		for i := 0; i < count; i++ {
			value, err := d.decodeDisplayOne(f, rec, base+i*f.Length, fmt.Sprintf("%s[%d]", path, i+1), diagnostics)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	}
	return d.decodeDisplayOne(f, rec, base, path, diagnostics)
}

func (d *Decoder) decodeDisplayOne(f *layout.Field, rec []byte, base int, path string, diagnostics *[]Diagnostic) (Value, error) {
	if f.Kind == layout.KindGroup {
		return d.decodeDisplayGroup(f, rec, base, path, diagnostics)
	}
	end := base + f.Length
	if end > len(rec) {
		return nil, fmt.Errorf("field %s at offset %d:%d exceeds record length %d", f.Name, base, end, len(rec))
	}

	raw := rec[base:end]
	value, err := d.displayScalar(f, raw)
	if err != nil {
		*diagnostics = append(*diagnostics, Diagnostic{
			FieldPath: path,
			Offset:    base,
			Length:    f.Length,
			RawHex:    boundedRawHex(raw),
			Err:       err,
		})
	}
	return value, nil
}

func (d *Decoder) decodeDisplayGroup(g *layout.Field, rec []byte, base int, path string, diagnostics *[]Diagnostic) (Object, error) {
	value := make(Object, 0, len(g.Children))
	for _, child := range g.Children {
		if child.Filler && !d.IncludeFillers {
			continue
		}
		childPath := child.Name
		if path != "" {
			childPath = path + "." + child.Name
		}
		childValue, err := d.decodeDisplayField(child, rec, base+(child.Offset-g.Offset), childPath, diagnostics)
		if err != nil {
			return nil, err
		}
		value = append(value, Member{Name: child.Name, Value: childValue})
	}
	return value, nil
}

func (d *Decoder) displayScalar(f *layout.Field, raw []byte) (Value, error) {
	if f.Kind == layout.KindText || f.Kind == layout.KindEdited {
		value := decode.DisplayString(raw, d.CM)
		if count := bytes.Count(raw, []byte{0}); count > 0 {
			return value, fmt.Errorf("%w (%d byte(s))", ErrLowValue, count)
		}
		return value, nil
	}

	value, err := d.scalar(f, raw)
	if f.Kind == layout.KindZoned && f.SignSeparate && separateSignByte(f, raw) == 0 {
		err = fmt.Errorf("invalid separate sign character: LOW-VALUE")
	}
	if err == nil {
		if number, ok := value.(json.Number); ok {
			if _, marshalErr := json.Marshal(number); marshalErr != nil {
				err = marshalErr
			}
		}
	}
	if err == nil {
		return value, nil
	}
	return d.invalidScalarDisplay(f, raw), err
}

func separateSignByte(f *layout.Field, raw []byte) byte {
	if f.SignLeading {
		return raw[0]
	}
	return raw[len(raw)-1]
}

func (d *Decoder) invalidScalarDisplay(f *layout.Field, raw []byte) string {
	switch f.Kind {
	case layout.KindZoned:
		if f.SignSeparate {
			return d.invalidSeparateDisplay(f, raw)
		}
		return invalidZonedDisplay(f, raw)
	case layout.KindPacked:
		return invalidPackedDisplay(f, raw)
	default:
		return "�"
	}
}

func invalidZonedDisplay(f *layout.Field, raw []byte) string {
	digits := make([]rune, len(raw))
	negative := false
	for i, b := range raw {
		zone, digit := b>>4, b&0x0f
		last := i == len(raw)-1
		validZone := zone == 0xF || zone == 0x3 || (last && (zone == 0xA || zone == 0xC || zone == 0xD || zone == 0xE))
		if validZone && digit <= 9 {
			digits[i] = rune('0' + digit)
		} else if b == 0 {
			digits[i] = '·'
		} else {
			digits[i] = '�'
		}
		if last && zone == 0xD && f.Signed {
			negative = true
		}
	}
	value := displayDecimal(digits, f.Scale)
	if negative {
		value = "-" + value
	}
	return value
}

func (d *Decoder) invalidSeparateDisplay(f *layout.Field, raw []byte) string {
	var sign byte
	var digits []byte
	if f.SignLeading {
		sign, digits = raw[0], raw[1:]
	} else {
		sign, digits = raw[len(raw)-1], raw[:len(raw)-1]
	}

	digitField := *f
	digitField.SignSeparate = false
	digitField.SignLeading = false
	digitField.Signed = false
	value := invalidZonedDisplay(&digitField, digits)

	signText := decode.String([]byte{sign}, d.CM)
	switch signText {
	case "-":
		return "-" + value
	case "+", "":
		if sign == 0 {
			if f.SignLeading {
				return "·" + value
			}
			return value + "·"
		}
		return value
	default:
		if f.SignLeading {
			return "�" + value
		}
		return value + "�"
	}
}

func invalidPackedDisplay(f *layout.Field, raw []byte) string {
	digits := make([]rune, 0, len(raw)*2-1)
	for _, b := range raw[:len(raw)-1] {
		digits = append(digits, displayNibble(b>>4), displayNibble(b&0x0f))
	}
	last := raw[len(raw)-1]
	digits = append(digits, displayNibble(last>>4))

	if extra := len(digits) - f.Digits; extra > 0 {
		padding := true
		for _, digit := range digits[:extra] {
			if digit != '0' {
				padding = false
				break
			}
		}
		if padding {
			digits = digits[extra:]
		}
	}

	value := displayDecimal(digits, f.Scale)
	switch last & 0x0f {
	case 0xB, 0xD:
		value = "-" + value
	case 0xA, 0xC, 0xE, 0xF:
	default:
		value += "�"
	}
	return value
}

func displayNibble(nibble byte) rune {
	if nibble <= 9 {
		return rune('0' + nibble)
	}
	return '�'
}

func displayDecimal(digits []rune, scale int) string {
	if scale <= 0 {
		return string(digits)
	}
	if scale >= len(digits) {
		return "0." + strings.Repeat("0", scale-len(digits)) + string(digits)
	}
	split := len(digits) - scale
	return string(digits[:split]) + "." + string(digits[split:])
}

const diagnosticRawByteLimit = 32

func boundedRawHex(raw []byte) string {
	truncated := len(raw) > diagnosticRawByteLimit
	if truncated {
		raw = raw[:diagnosticRawByteLimit]
	}
	value := strings.ToUpper(hex.EncodeToString(raw))
	if truncated {
		value += "..."
	}
	return value
}
