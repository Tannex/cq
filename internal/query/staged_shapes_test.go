package query

import (
	"context"
	"fmt"
	"slices"
	"testing"
)

// runStagedSplit evaluates a staged query with its input cut at every
// position in cuts, the way the parallel caller cuts it into chunks: the
// earliest failing part ends the evaluation, and Then is not run.
func runStagedSplit(s *Staged, input []any, cuts []int) ([]string, error) {
	var each []any
	start := 0
	for _, end := range append(slices.Clone(cuts), len(input)) {
		var part []any
		err := s.Each(context.Background(), input[start:end], func(v any) error { part = append(part, v); return nil })
		each = append(each, part...)
		if err != nil {
			return nil, err
		}
		start = end
	}
	var out []string
	err := s.Then(context.Background(), input, each, func(v any) error {
		out = append(out, Marshal(v))
		return nil
	})
	return out, err
}

// Shapes chosen to probe the parser and the split: precedence at the top
// level, bindings and definitions inside f and rest, the carrying variable,
// names of internal functions, generators and errors in both stages.
func TestStagedShapesMatchWholeEvaluationAtEverySplit(t *testing.T) {
	input, err := FromJSON([]byte(`[{"A":3,"B":"x","C":[1,2]},{"A":1,"B":"y","C":[]},{"A":2,"B":"x","C":[3]},{"A":1,"B":"z","C":[4,5,6]},{"A":5,"B":null,"C":[0]},{"A":0,"B":"x","C":[7,7]},{"A":4,"B":"y","C":[8]}]`))
	if err != nil {
		t.Fatal(err)
	}
	exprs := []string{
		`map(.A) | . as [$a, $b] | $a + $b`, `map(.A) | .[] as $x | $x`, `map(.A) | reduce .[] as $x (0; . + $x)`,
		`map(.A) | def _group_by(f): "mine"; group_by(.)`, `group_by(.B) | def _group_by(f): "mine"; _group_by(1)`,
		`map(.A) | def map(f): "mine"; map(.)`, `map(def map(f): "mine"; map(.))`, `map(def g: .A; g, g)`,
		`group_by(def _group_by(f): 1; .B)`, `sort_by(. as $__cq_each | $__cq_each.A) | . as $__cq_each | $__cq_each`,
		`map(.A) | $__cq_each? // 1`, `map(.A) | try error catch .`, `map(.A) | (error)? // "alt"`,
		`map(.A) | label $o | .[] | if . > 2 then ., break $o else . end`, `map(.A) | first(.[] | select(. > 3))`,
		`map(.A) | limit(2; .[])`, `map(.A) | until(length < 3; .[1:])`, `map(.A) | path(.[1])`, `map(.A) | [paths]`,
		`map(.A) | getpath([2])`, `map(.A) | to_entries | from_entries?`, `map(.A) | .[0] |= . + 10`, `map(.A) | del(.[0, 2])`,
		`map(.A) | @json`, `map(.A) | "\(.)"`, `map(.A) | -.[0]`, `map(.A) | .[0] and .[1] or false`, `map(.A) | .[0] // .[1]`,
		`map(.A) | ., .`, `map(.A) | empty`, `map(.A) | halt`, `map(.A) | {a: .}`, `map(.A) | if length > 3 then "big" else "small" end`,
		`map(.A) | input_line_number`, `map(.A) | $ENV | type`, `map(.A) | env | type`, `map(.A) | tostream`, `map(.A) | [..] | length`,
		`map(.C[]) | add`, `map(.C[], .A)`, `map(empty)`, `map(.C | first(.[]))`, `map(.A as $a | $a)`, `map(.B // "none")`,
		`map(try (if .A == 1 then error("one") else . end) catch .)`, `map(if .A == 1 then error("one") else . end)`,
		`map(if .A == 0 then halt else . end)`, `map(if .A == 0 then halt_error else . end)`, `map(label $f | .C[] | ., break $f)`,
		`map(.C[] | error)`, `map(error(null))`, `map(.A, error("after"))`, `map(.B | ascii_downcase)`, `map(.B | ascii_downcase?)`,
		`[.[] | .A] | add`, `[.[] | .A, .B]`, `[.[] | .A | . * 2] | add`, `[.[] | . as {A: $a} | $a]`, `[.[] | . as [$a] ?// {A: $a} | $a]`,
		`[.[] | .A] | . as $x | [.[] | . + ($x | length)]`, `[.[] | select(.A > 1) | .A] | sort`,
		`group_by(.B) | map(length)`, `group_by(.C[]) | map(map(.A))`, `group_by(empty)`, `group_by(.A % 2, .B) | length`,
		`sort_by(.B, -.A) | map(.A)`, `sort_by(.C) | map(.A)`, `sort_by(.C[])`, `unique_by(.B) | map(.A)`, `unique_by(.C | length)`,
		`min_by(.B)`, `max_by(.C[])`, `min_by(empty)`, `max_by(.A, .B)`, `min_by(if .A == 2 then error("min") else .A end)`,
		`max_by(.B | ascii_downcase)`, `sort_by(.B | ascii_downcase)`, `group_by(.B | ascii_downcase?)`,
	}
	for _, expr := range exprs {
		q, err := Compile(expr)
		if err != nil {
			continue
		}
		var want []string
		wantErr := q.Run(input, func(v any) error { want = append(want, Marshal(v)); return nil })
		if q.Staged() == nil {
			t.Logf("not staged: %s", expr)
			continue
		}
		n := len(input.([]any))
		for mask := 0; mask < 1<<(n-1); mask++ {
			var cuts []int
			for i := 1; i < n; i++ {
				if mask&(1<<(i-1)) != 0 {
					cuts = append(cuts, i)
				}
			}
			got, gotErr := runStagedSplit(q.Staged(), input.([]any), cuts)
			if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) || wantErr == nil && !slices.Equal(got, want) {
				t.Errorf("%s (cuts %v): staged %v (%v), want %v (%v)", expr, cuts, got, gotErr, want, wantErr)
				break
			}
		}
	}
}
