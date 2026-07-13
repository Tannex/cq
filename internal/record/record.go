// Package record converts fixed-length (or tail-ODO variable) records between
// binary data and JSON using a copybook layout.
package record

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/layout"
)

// Decoder decodes records described by a single record layout.
type Decoder struct {
	Rec            *layout.Record
	CM             *decode.Charmap
	IncludeFillers bool
	Lrecl          int // record length override; 0 = derive from layout

	odo     *layout.Field // the single ODO table, when the record is variable
	counter *layout.Field // its DEPENDING ON field
	wheres  []*where      // -where clauses, ANDed
}

// NewDecoder validates that the record is decodable (at most one OCCURS
// DEPENDING ON, and only as the trailing storage of the record).
func NewDecoder(rec *layout.Record, cm *decode.Charmap) (*Decoder, error) {
	d := &Decoder{Rec: rec, CM: cm}
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
		if f.Offset+f.Length*f.Occurs != rec.MaxLength {
			return nil, fmt.Errorf("record %s: OCCURS DEPENDING ON table %s is not at the end of the record; only trailing ODO is supported", rec.Name, f.Name)
		}
		c := layout.FindByName(rec.Field, f.DependingOn)
		if c == nil {
			return nil, fmt.Errorf("record %s: DEPENDING ON field %s not found", rec.Name, f.DependingOn)
		}
		if c.Offset+c.Length > f.Offset {
			return nil, fmt.Errorf("record %s: DEPENDING ON field %s does not precede table %s", rec.Name, c.Name, f.Name)
		}
		d.odo, d.counter = f, c
	}
	return d, nil
}

// Next reads the next record. io.EOF signals a clean end of input.
func (d *Decoder) Next(r io.Reader) ([]byte, error) {
	if d.Lrecl > 0 {
		buf := make([]byte, d.Lrecl)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, eofOr(err, d.Lrecl)
		}
		return buf, nil
	}
	if d.odo == nil {
		buf := make([]byte, d.Rec.MaxLength)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, eofOr(err, d.Rec.MaxLength)
		}
		return buf, nil
	}
	// Variable record: read the fixed prefix, then count*element more.
	prefix := make([]byte, d.odo.Offset)
	if _, err := io.ReadFull(r, prefix); err != nil {
		return nil, eofOr(err, len(prefix))
	}
	n, err := d.occursCount(prefix)
	if err != nil {
		return nil, err
	}
	tail := make([]byte, n*d.odo.Length)
	if len(tail) > 0 {
		if _, err := io.ReadFull(r, tail); err != nil {
			return nil, fmt.Errorf("record truncated: wanted %d table bytes: %w", len(tail), err)
		}
	}
	return append(prefix, tail...), nil
}

func eofOr(err error, want int) error {
	if err == io.EOF {
		return io.EOF
	}
	return fmt.Errorf("record truncated: wanted %d bytes: %w (wrong copybook, codepage, or a non-binary transfer?)", want, err)
}

// occursCount decodes the ODO counter from the fixed prefix.
func (d *Decoder) occursCount(prefix []byte) (int, error) {
	s, err := d.scalar(d.counter, prefix[d.counter.Offset:d.counter.Offset+d.counter.Length])
	if err != nil {
		return 0, fmt.Errorf("decoding DEPENDING ON field %s: %w", d.counter.Name, err)
	}
	raw, _ := s.(json.Number)
	n, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0, fmt.Errorf("DEPENDING ON field %s: value %v is not an integer count", d.counter.Name, s)
	}
	if n < 0 || n > d.odo.Occurs {
		return 0, fmt.Errorf("DEPENDING ON field %s: count %d outside 0..%d", d.counter.Name, n, d.odo.Occurs)
	}
	return n, nil
}

// Decode renders one record as a compact JSON object with fields in
// copybook order.
func (d *Decoder) Decode(rec []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := d.writeGroup(&buf, d.Rec.Field, rec, 0); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (d *Decoder) writeField(buf *bytes.Buffer, f *layout.Field, rec []byte, base int) error {
	if f.Occurs > 0 {
		n := f.Occurs
		if f == d.odo {
			c, err := d.occursCount(rec)
			if err != nil {
				return err
			}
			n = c
		}
		buf.WriteByte('[')
		for i := 0; i < n; i++ {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := d.writeOne(buf, f, rec, base+i*f.Length); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	}
	return d.writeOne(buf, f, rec, base)
}

func (d *Decoder) writeOne(buf *bytes.Buffer, f *layout.Field, rec []byte, base int) error {
	if f.Kind == layout.KindGroup {
		return d.writeGroup(buf, f, rec, base)
	}
	end := base + f.Length
	if end > len(rec) {
		return fmt.Errorf("field %s at offset %d:%d exceeds record length %d", f.Name, base, end, len(rec))
	}
	v, err := d.scalar(f, rec[base:end])
	if err != nil {
		return fmt.Errorf("field %s (offset %d): %w", f.Name, base, err)
	}
	switch t := v.(type) {
	case json.Number:
		buf.WriteString(string(t))
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(b)
	}
	return nil
}

func (d *Decoder) writeGroup(buf *bytes.Buffer, g *layout.Field, rec []byte, base int) error {
	buf.WriteByte('{')
	first := true
	for _, c := range g.Children {
		if c.Filler && !d.IncludeFillers {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		name, _ := json.Marshal(c.Name)
		buf.Write(name)
		buf.WriteByte(':')
		// Child offsets are absolute within the record for the first
		// occurrence; base shifts them for later array elements.
		if err := d.writeField(buf, c, rec, base+(c.Offset-g.Offset)); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// scalar decodes one elementary field's bytes.
func (d *Decoder) scalar(f *layout.Field, b []byte) (any, error) {
	switch f.Kind {
	case layout.KindText, layout.KindEdited:
		return decode.String(b, d.CM), nil
	case layout.KindZoned:
		if f.SignSeparate {
			return d.zonedSeparate(f, b)
		}
		s, err := decode.Zoned(b, f.Scale, f.Signed)
		return json.Number(s), err
	case layout.KindPacked:
		s, err := decode.Packed(b, f.Scale)
		return json.Number(s), err
	case layout.KindBinary:
		s, err := decode.Binary(b, f.Signed, f.Scale)
		return json.Number(s), err
	case layout.KindFloat:
		s, err := decode.Float(b)
		return json.Number(s), err
	default:
		return nil, fmt.Errorf("cannot decode kind %s", f.Kind)
	}
}

// zonedSeparate handles SIGN IS LEADING/TRAILING SEPARATE: one dedicated
// sign character before or after the digits.
func (d *Decoder) zonedSeparate(f *layout.Field, b []byte) (any, error) {
	if len(b) < 2 {
		return nil, fmt.Errorf("separate-sign field shorter than 2 bytes")
	}
	var signB, digits []byte
	if f.SignLeading {
		signB, digits = b[:1], b[1:]
	} else {
		signB, digits = b[len(b)-1:], b[:len(b)-1]
	}
	sign := decode.String(signB, d.CM)
	s, err := decode.Zoned(digits, f.Scale, false)
	if err != nil {
		return nil, err
	}
	switch sign {
	case "-":
		if strings.ContainsAny(s, "123456789") { // never emit -0
			s = "-" + s
		}
	case "+", "":
	default:
		return nil, fmt.Errorf("invalid separate sign character %q", sign)
	}
	return json.Number(s), nil
}
