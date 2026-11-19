package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/itchyny/gojq"
)

func run(t *testing.T, expr, input string) []string {
	t.Helper()
	q, err := Compile(expr)
	if err != nil {
		t.Fatal(err)
	}
	v, err := FromJSON([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	err = q.Run(v, func(r any) error {
		b, err := gojq.Marshal(r)
		if err != nil {
			return err
		}
		out = append(out, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFieldAccess(t *testing.T) {
	got := run(t, `.NAME`, `{"NAME":"ALICE","N":1}`)
	if len(got) != 1 || got[0] != `"ALICE"` {
		t.Errorf("got %v", got)
	}
}

func TestSelectAndArithmetic(t *testing.T) {
	got := run(t, `select(.BAL < 0) | .BAL * 2`, `{"BAL":-123.45}`)
	if len(got) != 1 || got[0] != `-246.9` {
		t.Errorf("got %v", got)
	}
	got = run(t, `select(.BAL < 0)`, `{"BAL":10}`)
	if len(got) != 0 {
		t.Errorf("want no results, got %v", got)
	}
}

func TestPrecisionPreserved(t *testing.T) {
	// 18-digit integers survive FromJSON (json.Number) and identity queries.
	got := run(t, `.N`, `{"N":123456789012345678}`)
	if len(got) != 1 || got[0] != `123456789012345678` {
		t.Errorf("got %v", got)
	}
}

func TestMultipleResults(t *testing.T) {
	got := run(t, `.A[]`, `{"A":[1,2,3]}`)
	if len(got) != 3 || got[0] != `1` || got[2] != `3` {
		t.Errorf("got %v", got)
	}
}

func TestCompileError(t *testing.T) {
	if _, err := Compile(`.foo | `); err == nil {
		t.Fatal("want parse error")
	}
}

func TestRuntimeError(t *testing.T) {
	q, err := Compile(`.A.B`)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := FromJSON([]byte(`{"A":"text"}`))
	err = q.Run(v, func(any) error { return nil })
	if err == nil {
		t.Fatal("want runtime error indexing a string")
	}
	var h *Halt
	if errors.As(err, &h) {
		t.Fatalf("plain error reported as halt: %v", err)
	}
}

func TestHalt(t *testing.T) {
	q, err := Compile(`halt`)
	if err != nil {
		t.Fatal(err)
	}
	err = q.Run(map[string]any{}, func(any) error { return nil })
	var h *Halt
	if !errors.As(err, &h) || h.Value != nil {
		t.Fatalf("want clean halt, got %v", err)
	}

	q, err = Compile(`"stop here" | halt_error`)
	if err != nil {
		t.Fatal(err)
	}
	err = q.Run(map[string]any{}, func(any) error { return nil })
	if !errors.As(err, &h) || h.Error() != "stop here" {
		t.Fatalf("want halt_error payload, got %v", err)
	}
}

// runStaged evaluates a staged query the way a parallel caller does: Each
// over the input cut in two, its outputs concatenated, and Then over them.
func runStaged(s *Staged, input []any) ([]string, error) {
	var each []any
	collect := func(v any) error { each = append(each, v); return nil }
	for _, part := range [][]any{input[:len(input)/2], input[len(input)/2:]} {
		if err := s.Each(context.Background(), part, collect); err != nil {
			return nil, err
		}
	}
	var out []string
	err := s.Then(context.Background(), input, each, func(v any) error {
		out = append(out, Marshal(v))
		return nil
	})
	return out, err
}

func TestStagedMatchesWholeEvaluation(t *testing.T) {
	input, err := FromJSON([]byte(`[{"A":3,"B":"x","C":[1,2]},{"A":1,"B":"y","C":[]},{"A":2,"B":"x","C":[3]},{"A":1,"B":"z","C":[4,5,6]}]`))
	if err != nil {
		t.Fatal(err)
	}
	for expr, staged := range map[string]bool{
		`map(.A)`:                      true,
		`map(.C[])`:                    true,
		`map(select(.A > 1)) | length`: true,
		`[.[] | .B]`:                   true,
		`[.[] | .A, .B] | .[2:]`:       true,
		`group_by(.B)`:                 true,
		`group_by(.C[]) | length`:      true,
		`sort_by(.A)`:                  true,
		`sort_by(.A, .B) | map(.B)`:    true,
		`sort_by(-.A) | .[] | .B`:      true,
		`unique_by(.A) | map(.B)`:      true,
		`min_by(.A)`:                   true,
		`max_by(.C | length) | .B`:     true,
		`min_by(empty)`:                true,
		`map(.B.x)`:                    true,
		`map(if .A == 2 then error("second half") elif .A == 3 then error("first half") else . end)`: true,
		`sort_by(if .A == 2 then halt_error else .A end)`:                                            true,
		`map(.A) | error("rest")`:                true,
		`map(.A) | . as $__cq_each | $__cq_each`: true,
		`map(. as $__cq_each | $__cq_each.A)`:    true,
		`def map(f): [.[] | f, f]; map(.A)`:      false,
		`.[] | .A`:                               false,
		`.[] as $x | [$x.A]`:                     false,
		`[.[] as $x | $x.A]`:                     false,
		`.[1:] | map(.A)`:                        false,
		`{a: map(.A)}`:                           false,
		`map(.A)[]`:                              false,
		`map(.A)?`:                               false,
		`(map(.A))`:                              false,
		`first(map(.A)[])`:                       false,
		`map(.A) as $a | $a`:                     false,
		`map(.A), 1`:                             false,
		`[.[]]`:                                  false,
		`sort`:                                   false,
		`limit(1; .[])`:                          false,
		`reduce .[] as $r (0; . + $r.A)`:         false,
	} {
		q, err := Compile(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if (q.Staged() != nil) != staged {
			t.Errorf("%s: staged = %v, want %v", expr, q.Staged() != nil, staged)
			continue
		}
		if !staged {
			continue
		}
		var want []string
		wantErr := q.Run(input, func(v any) error { want = append(want, Marshal(v)); return nil })
		got, gotErr := runStaged(q.Staged(), input.([]any))
		if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) || wantErr == nil && !slices.Equal(got, want) {
			t.Errorf("%s: staged %v (%v), want %v (%v)", expr, got, gotErr, want, wantErr)
		}
	}
}

// The variable carrying the staged values must not leak into the user's
// expression: referring to it fails to compile as it always did.
func TestStagedVariableIsNotVisible(t *testing.T) {
	for _, expr := range []string{`map($__cq_each)`, `group_by(.A) | $__cq_each`, `$__cq_each`} {
		if _, err := Compile(expr); err == nil || !strings.Contains(err.Error(), "variable not defined: $__cq_each") {
			t.Errorf("%s: compile error = %v", expr, err)
		}
	}
}

func TestFromJSONKeepsNumbers(t *testing.T) {
	v, err := FromJSON([]byte(`{"N":-0.10}`))
	if err != nil {
		t.Fatal(err)
	}
	n := v.(map[string]any)["N"]
	if num, ok := n.(json.Number); !ok || string(num) != "-0.10" {
		t.Errorf("got %T %v", n, n)
	}
}
