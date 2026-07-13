// Package query wraps gojq: it compiles a jq expression once and runs it
// over decoded record values.
package query

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/itchyny/gojq"
)

// Query is a compiled jq expression.
type Query struct {
	code *gojq.Code
}

// Compile parses and compiles a jq expression.
func Compile(expr string) (*Query, error) {
	q, err := gojq.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	code, err := gojq.Compile(q)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	return &Query{code: code}, nil
}

// Halt is returned by Run when the expression called halt or halt_error;
// processing of further input should stop.
type Halt struct {
	Value    any // non-nil for halt_error: the error payload
	ExitCode int
}

func (h *Halt) Error() string {
	if h.Value == nil {
		return "halted"
	}
	if s, ok := h.Value.(string); ok {
		return s
	}
	b, _ := gojq.Marshal(h.Value)
	return string(b)
}

// FromJSON prepares raw JSON for querying, keeping numbers as json.Number
// so long packed-decimal values do not lose precision on the way in.
func FromJSON(raw []byte) (any, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// Run evaluates the query against one input value and calls emit for each
// result. Errors raised by the expression (including halt) abort the run.
func (q *Query) Run(v any, emit func(any) error) error {
	iter := q.code.Run(v)
	for {
		out, ok := iter.Next()
		if !ok {
			return nil
		}
		if err, isErr := out.(error); isErr {
			var halt *gojq.HaltError
			if errors.As(err, &halt) {
				return &Halt{Value: halt.Value(), ExitCode: halt.ExitCode()}
			}
			return err
		}
		if err := emit(out); err != nil {
			return err
		}
	}
}
