package record

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type jsonObject map[string]any

// fillerName is the name the copybook parser gives FILLER (and unnamed)
// items and the only key -fillers decoding repeats within one object.
const fillerName = "FILLER"

// fillerValues collects the values of repeated FILLER keys in document
// order. It is a distinct type so one FILLER holding a JSON array (an
// OCCURS filler) is not confused with several FILLER keys.
type fillerValues []any

// EncodeJSON reads one object or an array of objects and writes binary records.
func EncodeJSON(w io.Writer, e *Encoder, r io.Reader) error {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("JSON input is empty")
		}
		return fmt.Errorf("reading JSON input: %w", err)
	}

	n := 0
	switch tok {
	case json.Delim('{'):
		obj, err := readObject(dec)
		if err != nil {
			return fmt.Errorf("record 1: %w", err)
		}
		n = 1
		if err := encodeJSONRecord(w, e, obj, n); err != nil {
			return err
		}
	case json.Delim('['):
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return fmt.Errorf("record %d: %w", n+1, err)
			}
			if tok != json.Delim('{') {
				return fmt.Errorf("record %d: want JSON object, got %s", n+1, tokenType(tok))
			}
			obj, err := readObject(dec)
			if err != nil {
				return fmt.Errorf("record %d: %w", n+1, err)
			}
			n++
			if err := encodeJSONRecord(w, e, obj, n); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return fmt.Errorf("closing JSON array: %w", err)
		}
	default:
		return fmt.Errorf("JSON input must be an object or array of objects, got %s", tokenType(tok))
	}

	if tok, err := dec.Token(); err == nil {
		return fmt.Errorf("unexpected JSON value after record input: %v", tok)
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("reading JSON input: %w", err)
	}
	return nil
}

func encodeJSONRecord(w io.Writer, e *Encoder, obj jsonObject, n int) error {
	b, err := e.Encode(obj)
	if err != nil {
		return fmt.Errorf("record %d: %w", n, err)
	}
	nw, err := w.Write(b)
	if err != nil {
		return fmt.Errorf("record %d: writing binary output: %w", n, err)
	}
	if nw != len(b) {
		return fmt.Errorf("record %d: writing binary output: %w", n, io.ErrShortWrite)
	}
	return nil
}

func readObject(dec *json.Decoder) (jsonObject, error) {
	obj := make(jsonObject)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		name, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("object key is not a string")
		}
		if _, exists := obj[name]; exists && name != fillerName {
			return nil, fmt.Errorf("duplicate JSON field %q", name)
		}
		value, err := readValue(dec)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", name, err)
		}
		if name == fillerName {
			values, _ := obj[name].(fillerValues)
			obj[name] = append(values, value)
		} else {
			obj[name] = value
		}
	}
	if tok, err := dec.Token(); err != nil {
		return nil, err
	} else if tok != json.Delim('}') {
		return nil, fmt.Errorf("want object close, got %v", tok)
	}
	return obj, nil
}

func readValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'):
		return readObject(dec)
	case json.Delim('['):
		var values []any
		for dec.More() {
			v, err := readValue(dec)
			if err != nil {
				return nil, err
			}
			values = append(values, v)
		}
		if tok, err := dec.Token(); err != nil {
			return nil, err
		} else if tok != json.Delim(']') {
			return nil, fmt.Errorf("want array close, got %v", tok)
		}
		return values, nil
	default:
		return tok, nil
	}
}

func tokenType(tok any) string {
	if tok == nil {
		return "null"
	}
	switch tok.(type) {
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	case json.Delim:
		return fmt.Sprintf("%q", tok)
	default:
		return fmt.Sprintf("%T", tok)
	}
}
