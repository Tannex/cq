// Package recordset exposes a downloaded data set to the jq engine without
// decoding it first.
//
// A Set is the file of length-prefixed record frames z/OSMF streams in record
// mode, mapped into memory, plus the positions of the records the copybook
// can decode. The query engine sees it as one array of objects, but neither
// the array nor the objects exist as Go values: a record is a pointer into
// the mapping, and a member is decoded when — and only when — an expression
// reads it. Evaluating `.[] | select(.STATUS == "A")` over twenty million
// records therefore decodes twenty million STATUS fields and nothing else,
// instead of building twenty million maps first.
package recordset

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/Tannex/cq/internal/gojq"
	"github.com/Tannex/cq/internal/record"
)

// MaxRecord bounds one record frame, mirroring the transport's bound.
const MaxRecord = 16 << 20

const frameHeader = 4

// Set is the decodable records of one frames file under one copybook.
// It is immutable after Open and safe for concurrent queries until Close.
type Set struct {
	view    *record.QueryView
	data    []byte
	release func() error

	// Records are addressed by the offset of their data. When every frame
	// has the same size and decodes, record i sits at i*stride+frameHeader
	// and offsets stays nil; otherwise offsets lists each decodable record.
	stride  int
	offsets []int64
	count   int

	frames  int // frames read, decodable or not
	skipped int // frames the copybook cannot decode
}

// Open maps the frames file at path and indexes its records. A truncated or
// oversized frame is an error; a frame the copybook cannot decode is skipped
// and counted. The context aborts the index pass.
func Open(ctx context.Context, path string, view *record.QueryView) (*Set, error) {
	data, release, err := mapFile(path)
	if err != nil {
		return nil, err
	}
	set := &Set{view: view, data: data, release: release}
	if err := set.index(ctx); err != nil {
		_ = release()
		return nil, err
	}
	return set, nil
}

func (s *Set) index(ctx context.Context) error {
	data := s.data
	uniform := true // every frame so far has the first frame's size and decodes
	for pos := 0; pos < len(data); {
		if s.frames&0xffff == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		if len(data)-pos < frameHeader {
			return fmt.Errorf("truncated record frame header: %w", io.ErrUnexpectedEOF)
		}
		length := int64(binary.BigEndian.Uint32(data[pos:]))
		if length > MaxRecord {
			return fmt.Errorf("record frame of %d bytes exceeds the %d byte bound", length, MaxRecord)
		}
		start := pos + frameHeader
		if int64(len(data)-start) < length {
			return fmt.Errorf("record frame declares %d bytes but is truncated: %w", length, io.ErrUnexpectedEOF)
		}
		size := frameHeader + int(length)
		decodable := s.view.Check(data[start:start+int(length)]) == nil
		if s.frames == 0 {
			s.stride = size
		}
		if uniform && (!decodable || size != s.stride) {
			// Leave the implicit layout: spell out the records seen so far.
			uniform = false
			s.offsets = make([]int64, s.count, max(s.count+1, len(data)/max(s.stride, 1)))
			for i := range s.offsets {
				s.offsets[i] = int64(i*s.stride + frameHeader)
			}
			s.stride = 0
		}
		if decodable {
			if !uniform {
				s.offsets = append(s.offsets, int64(start))
			}
			s.count++
		} else {
			s.skipped++
		}
		s.frames++
		pos += size
	}
	return nil
}

// Close releases the mapping. No value obtained from the set may be used
// afterwards.
func (s *Set) Close() error {
	release := s.release
	s.release, s.data, s.offsets = nil, nil, nil
	if release == nil {
		return nil
	}
	return release()
}

// Len returns the number of decodable records.
func (s *Set) Len() int { return s.count }

// Frames returns the number of record frames in the file.
func (s *Set) Frames() int { return s.frames }

// Skipped returns the number of frames the copybook cannot decode.
func (s *Set) Skipped() int { return s.skipped }

// Array returns the whole set as the jq engine's input value.
func (s *Set) Array() *Array { return &Array{set: s, start: 0, end: s.count} }

// Range returns records start <= i < end as an array of their own.
func (s *Set) Range(start, end int) *Array { return &Array{set: s, start: start, end: end} }

func (s *Set) offset(i int) int64 {
	if s.offsets == nil {
		return int64(i)*int64(s.stride) + frameHeader
	}
	return s.offsets[i]
}

// Array is a run of records of a Set: a jq array that is never built.
type Array struct {
	set        *Set
	start, end int
}

var _ gojq.LazyArray = (*Array)(nil)

// Len implements gojq.LazyArray.
func (a *Array) Len() int { return a.end - a.start }

// At implements gojq.LazyArray.
func (a *Array) At(i int) any { return &Record{set: a.set, offset: a.set.offset(a.start + i)} }

// Slice implements gojq.LazyArray. The elements stay lazy; they share one
// allocation rather than one each.
func (a *Array) Slice(start, end int) []any {
	records := make([]Record, end-start)
	elements := make([]any, end-start)
	for i := range records {
		records[i] = Record{set: a.set, offset: a.set.offset(a.start + start + i)}
		elements[i] = &records[i]
	}
	return elements
}

// Record is one record of a Set: a jq object whose members are decoded from
// the raw bytes when read.
type Record struct {
	set    *Set
	offset int64
}

var (
	_ gojq.LazyObject   = (*Record)(nil)
	_ gojq.LazyComparer = (*Record)(nil)
)

// Bytes returns the record's raw data. It aliases the mapping.
func (r *Record) Bytes() []byte { return r.set.recordAt(r.offset) }

// recordAt returns the data of the record at offset.
func (s *Set) recordAt(offset int64) []byte {
	length := int64(binary.BigEndian.Uint32(s.data[offset-frameHeader:]))
	return s.data[offset : offset+length]
}

// Get implements gojq.LazyObject: it decodes the one member asked for, or
// for a group or a table, nothing until a member of it is read.
func (r *Record) Get(key string) any {
	m := r.set.view.Member(key)
	if m == nil {
		return nil
	}
	return r.set.member(r.offset, r.Bytes(), m, m.Base(0))
}

// Map implements gojq.LazyObject: it decodes the whole record.
func (r *Record) Map() map[string]any { return r.set.view.Value(r.Bytes()) }

// CompareLazy implements gojq.LazyComparer for two records of the same set.
// jq orders objects by their sorted keys and then by the values in key
// order; records of one set share their keys, so only values matter, and a
// member whose bytes are equal on both sides decodes equal and is skipped.
func (r *Record) CompareLazy(other any) (int, bool) {
	o, ok := other.(*Record)
	if !ok || o.set != r.set {
		return 0, false
	}
	if o.offset == r.offset {
		return 0, true
	}
	left, right := r.Bytes(), o.Bytes()
	if bytes.Equal(left, right) {
		return 0, true
	}
	return r.set.compareMembers(r.set.view.Root(), left, 0, right, 0), true
}
