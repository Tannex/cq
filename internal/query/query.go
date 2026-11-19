// Package query wraps the project's gojq fork: it compiles a jq expression
// once and runs it over decoded record values.
package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Tannex/cq/internal/gojq"
)

// Query is a compiled jq expression. It is safe for concurrent runs.
type Query struct {
	code    *gojq.Code
	rowWise bool
	staged  *Staged
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
	return &Query{code: code, rowWise: rowWise(q), staged: staged(expr)}, nil
}

// rowWise recognizes `.[]` and `.[] | f`: expressions that apply f to each
// element of the input array on its own. Nothing in f can observe the array
// or another element (there is no enclosing binding, label, or definition to
// reach through), so the outputs for an array are the outputs for its parts,
// concatenated in order.
func rowWise(q *gojq.Query) bool {
	const iterate = ".[]"
	if q.Meta != nil || len(q.Imports) > 0 || len(q.FuncDefs) > 0 {
		return false
	}
	if q.Term != nil {
		return q.String() == iterate
	}
	return q.Op == gojq.OpPipe && len(q.Patterns) == 0 && q.Left != nil && q.Left.String() == iterate
}

// RowWise reports whether the expression evaluates each element of its
// input array independently, which lets a caller split the array, run the
// parts concurrently, and concatenate their outputs in order.
func (q *Query) RowWise() bool { return q.rowWise }

// Staged is an expression split in two around a stage that applies some f
// to every element of the input array: Each is row-wise and evaluates that
// stage over part of the array, and Then evaluates the whole expression
// given the outputs of Each over the whole array, concatenated in order.
type Staged struct {
	each *gojq.Code
	then *gojq.Code
}

// stagedVariable carries the outputs of Each into Then. A user expression
// cannot reach it: one that refers to it without binding it fails to compile
// before it is staged, and one that binds it shadows it in its own scope.
const stagedVariable = "$__cq_each"

// staged recognizes expressions that begin, at the root, with `map(f)`,
// `[.[] | f]` or `X_by(f)` for X in group, sort, unique, min and max,
// optionally followed by `| rest`, and splits them by the definitions in
// builtin.jq:
//
//	map(f)  = [.[] | f]        Each `.[] | f`,   Then `$each [| rest]`
//	X_by(f) = _X_by(map([f]))  Each `.[] | [f]`, Then `_X_by($each) [| rest]`
//
// The head yields exactly one value or one error, so `head | rest` is rest
// applied to that value. f at the root sees no binding, label or definition
// of the user's, so it means the same compiled on its own. An error (halt
// included) raised by f for some element ends `[.[] | f]` with that error and
// nothing else, which the caller reproduces by reporting the earliest failing
// element and not running Then. Every other shape, including one with
// top-level definitions that could redefine map, is not staged.
//
// The source is parsed again because its parts are spliced into new trees,
// and the compiler does not promise to leave a tree it compiled untouched.
func staged(expr string) *Staged {
	q, err := gojq.Parse(expr)
	if err != nil || q.Meta != nil || len(q.Imports) > 0 || len(q.FuncDefs) > 0 {
		return nil
	}
	head, rest := q, (*gojq.Query)(nil)
	if q.Term == nil {
		if q.Op != gojq.OpPipe || len(q.Patterns) > 0 {
			return nil
		}
		head, rest = q.Left, q.Right
	}
	if head.Term == nil || len(head.FuncDefs) > 0 || len(head.Term.SuffixList) > 0 {
		return nil
	}
	each := &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeFunc, Func: &gojq.Func{Name: stagedVariable}}}
	var f, then *gojq.Query
	switch term := head.Term; term.Type {
	case gojq.TermTypeFunc:
		if len(term.Func.Args) != 1 {
			return nil
		}
		switch name := term.Func.Name; name {
		case "map":
			f, then = term.Func.Args[0], each
		case "group_by", "sort_by", "unique_by", "min_by", "max_by":
			f = &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeArray, Array: &gojq.Array{Query: term.Func.Args[0]}}}
			then = &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeFunc, Func: &gojq.Func{Name: "_" + name, Args: []*gojq.Query{each}}}}
		default:
			return nil
		}
	case gojq.TermTypeArray:
		body := term.Array.Query
		if body == nil || !iteratePipe(body) {
			return nil
		}
		f, then = body.Right, each
	default:
		return nil
	}
	iterate := &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeIdentity, SuffixList: []*gojq.Suffix{{Iter: true}}}}
	eachCode, err := gojq.Compile(&gojq.Query{Left: iterate, Op: gojq.OpPipe, Right: f})
	if err != nil {
		return nil
	}
	if rest != nil {
		then = &gojq.Query{Left: then, Op: gojq.OpPipe, Right: rest}
	}
	thenCode, err := gojq.Compile(then, gojq.WithVariables([]string{stagedVariable}))
	if err != nil {
		return nil
	}
	return &Staged{each: eachCode, then: thenCode}
}

// iteratePipe reports whether q is `.[] | f` with nothing bound on the pipe.
func iteratePipe(q *gojq.Query) bool {
	return len(q.FuncDefs) == 0 && q.Op == gojq.OpPipe && len(q.Patterns) == 0 &&
		q.Left != nil && q.Left.String() == ".[]"
}

// Staged returns the expression split around a per-element stage, or nil
// when it has no shape that splits without changing its result.
func (q *Query) Staged() *Staged { return q.staged }

// Each evaluates the per-element stage over input, a run of elements of the
// array the whole expression is meant for.
func (s *Staged) Each(ctx context.Context, input any, emit func(any) error) error {
	return drain(s.each.RunWithContext(ctx, input), emit)
}

// Then evaluates the expression over input given each, the outputs of Each
// over all of input in order.
func (s *Staged) Then(ctx context.Context, input any, each []any, emit func(any) error) error {
	return drain(s.then.RunWithContext(ctx, input, each), emit)
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
	return q.RunContext(context.Background(), v, emit)
}

// RunContext evaluates like Run but honors ctx, so interactive callers can
// bound a pathological expression instead of freezing.
func (q *Query) RunContext(ctx context.Context, v any, emit func(any) error) error {
	return drain(q.code.RunWithContext(ctx, v), emit)
}

func drain(iter gojq.Iter, emit func(any) error) error {
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

// Marshal renders one query output as compact JSON in jq's own encoding.
func Marshal(v any) string {
	encoded, err := gojq.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(encoded)
}
