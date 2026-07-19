package cqt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Tannex/cq/internal/copybook"
	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/dsncopy"
	"github.com/Tannex/cq/internal/layout"
	"github.com/Tannex/cq/internal/record"
	"github.com/Tannex/cq/internal/zosmf"
)

// CopybookSource describes an optional display overlay. Exactly one of Local
// and DSN must be set when the source is applied.
type CopybookSource struct {
	Local  string
	DSN    string
	Format string
	Record string
}

func (s CopybookSource) empty() bool {
	return strings.TrimSpace(s.Local) == "" && strings.TrimSpace(s.DSN) == ""
}

func (s CopybookSource) label() string {
	if local := strings.TrimSpace(s.Local); local != "" {
		return local
	}
	return strings.ToUpper(strings.TrimSpace(s.DSN))
}

func (s CopybookSource) validate() (CopybookSource, copybook.Format, error) {
	s.Local = strings.TrimSpace(s.Local)
	s.DSN = strings.ToUpper(strings.TrimSpace(s.DSN))
	s.Record = strings.ToUpper(strings.TrimSpace(s.Record))
	s.Format = strings.ToLower(strings.TrimSpace(s.Format))
	if s.Format == "" {
		s.Format = "auto"
	}
	if (s.Local == "") == (s.DSN == "") {
		return CopybookSource{}, copybook.FormatAuto, errors.New("set exactly one copybook source: local file or DSN")
	}
	var format copybook.Format
	switch s.Format {
	case "auto":
		format = copybook.FormatAuto
	case "fixed":
		format = copybook.FormatFixed
	case "free":
		format = copybook.FormatFree
	default:
		return CopybookSource{}, copybook.FormatAuto, fmt.Errorf("copybook format %q is invalid; use auto, fixed, or free", s.Format)
	}
	return s, format, nil
}

type fieldColumn struct {
	Path  string
	Parts []string
	Width int
	Kind  layout.Kind
}

type overlay struct {
	Source  CopybookSource
	Record  *layout.Record
	Decoder *record.Decoder
	Columns []fieldColumn
}

type fileLoader func(context.Context, string) ([]byte, error)

func buildOverlay(ctx context.Context, source CopybookSource, codepage string, browser zosmf.Browser, loadFile fileLoader, searchPaths []string) (*overlay, error) {
	source, format, err := source.validate()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var raw []byte
	if source.Local != "" {
		if loadFile == nil {
			return nil, errors.New("local copybook loading is unavailable")
		}
		raw, err = loadFile(ctx, source.Local)
	} else {
		if browser == nil {
			return nil, errors.New("z/OSMF session is not ready")
		}
		raw, err = browser.FetchText(ctx, source.DSN)
	}
	if err != nil {
		return nil, fmt.Errorf("load copybook %s: %w", source.label(), err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	resolver := dsncopy.New(searchPaths, browser, nil)
	resolve := func(member string) (string, error) {
		if browser == nil {
			return "", fmt.Errorf("COPY %s requires a z/OSMF session and dsnSearchPath", member)
		}
		return resolver.ResolveContext(ctx, member)
	}
	items, err := copybook.ParseWithCopies(string(raw), format, resolve)
	if err != nil {
		return nil, fmt.Errorf("parse copybook %s: %w", source.label(), err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := layout.Build(items)
	if err != nil {
		return nil, fmt.Errorf("build copybook layout %s: %w", source.label(), err)
	}
	selected, err := selectOverlayRecord(records, source.Record)
	if err != nil {
		return nil, err
	}
	cm, err := decode.Codepage(codepage)
	if err != nil {
		return nil, err
	}
	decoder, err := record.NewDecoder(selected, cm)
	if err != nil {
		return nil, err
	}
	return &overlay{
		Source: source, Record: selected, Decoder: decoder, Columns: overlayColumns(selected),
	}, nil
}

func selectOverlayRecord(records []*layout.Record, name string) (*layout.Record, error) {
	if len(records) == 0 {
		return nil, errors.New("copybook has no records")
	}
	if name == "" {
		return records[0], nil
	}
	for _, candidate := range records {
		if candidate.Name == name {
			return candidate, nil
		}
	}
	names := make([]string, 0, len(records))
	for _, candidate := range records {
		names = append(names, candidate.Name)
	}
	return nil, fmt.Errorf("copybook has no record %q; available records: %s", name, strings.Join(names, ", "))
}

func overlayColumns(rec *layout.Record) []fieldColumn {
	var columns []fieldColumn
	var walk func(*layout.Field, string)
	walk = func(field *layout.Field, prefix string) {
		for _, child := range field.Children {
			if child.Filler {
				continue
			}
			path := child.Name
			if prefix != "" {
				path = prefix + "." + child.Name
			}
			if child.Occurs > 0 || child.Kind != layout.KindGroup {
				kind := child.Kind
				if child.Occurs > 0 {
					// Arrays render as compact JSON regardless of element
					// kind, so style them like groups, not numbers.
					kind = layout.KindGroup
				}
				columns = append(columns, fieldColumn{Path: path, Parts: strings.Split(path, "."), Width: columnWidth(path, child), Kind: kind})
				continue
			}
			walk(child, path)
		}
	}
	walk(rec.Field, "")
	return columns
}

const (
	minColumnWidth  = 10
	maxHeaderWidth  = 28
	jsonColumnWidth = 40
	floatValueWidth = 14
)

// columnWidth sizes a column to fit the widest value the field can render, so
// table cells never silently truncate data. The header path contributes up to
// maxHeaderWidth, matching the pre-existing header clamp.
func columnWidth(path string, field *layout.Field) int {
	width := min(len(path), maxHeaderWidth)
	if data := fieldDisplayWidth(field); data > width {
		width = data
	}
	return max(width, minColumnWidth)
}

// fieldDisplayWidth estimates the widest rendering of a field's decoded value.
func fieldDisplayWidth(field *layout.Field) int {
	if field.Occurs > 0 || field.Kind == layout.KindGroup {
		// Arrays and groups render as compact JSON with no fixed bound; give
		// them a generous column and leave full inspection to the JSON view.
		return jsonColumnWidth
	}
	switch field.Kind {
	case layout.KindText, layout.KindEdited:
		return field.Length
	case layout.KindZoned, layout.KindPacked, layout.KindBinary:
		width := field.Digits
		if field.Signed {
			width++
		}
		if field.Scale > 0 {
			width += 2 // decimal point plus a possible leading zero
		}
		return width
	case layout.KindFloat:
		return floatValueWidth
	default:
		return minColumnWidth
	}
}

func valueAtPath(value record.Object, parts []string) (record.Value, bool) {
	var current record.Value = value
	for _, part := range parts {
		object, ok := current.(record.Object)
		if !ok {
			return nil, false
		}
		current, ok = object.Lookup(part)
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func compactValue(value record.Value) string {
	switch value := value.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	case record.Object:
		encoded, err := value.MarshalJSON()
		if err == nil {
			return string(encoded)
		}
	case record.Array:
		encoded, err := value.MarshalJSON()
		if err == nil {
			return string(encoded)
		}
	}
	return "�"
}

func diagnosticForColumn(diagnostics []record.Diagnostic, path string) (record.Diagnostic, bool) {
	for _, diagnostic := range diagnostics {
		if diagnostic.FieldPath == path || strings.HasPrefix(diagnostic.FieldPath, path+".") || strings.HasPrefix(diagnostic.FieldPath, path+"[") {
			return diagnostic, true
		}
	}
	return record.Diagnostic{}, false
}

func prettyRecordJSON(decoded record.DecodedRecord) string {
	encoded, err := decoded.JSON()
	if err != nil {
		return "ERROR: " + err.Error()
	}
	var out bytes.Buffer
	if err := json.Indent(&out, encoded, "", "  "); err != nil {
		return string(encoded)
	}
	return out.String()
}
