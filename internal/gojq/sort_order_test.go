package gojq

import (
	"encoding/json"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"
)

// stableOrder must agree with the standard library's stable sort for every
// size, including those that split across goroutines.
func TestStableOrderMatchesSliceStable(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	for _, n := range []int{0, 1, 2, 3, 12, 13, 100, 4095, 4096, 4097, 50_000, 300_000} {
		for _, distinct := range []int{1, 3, 1000, 1 << 30} {
			keys := make([]int, n)
			for i := range keys {
				keys[i] = rng.IntN(distinct)
			}
			want := make([]int32, n)
			for i := range want {
				want[i] = int32(i)
			}
			sort.SliceStable(want, func(i, j int) bool { return keys[want[i]] < keys[want[j]] })
			got := stableOrder(n, func(i, j int32) int { return keys[i] - keys[j] })
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("n=%d distinct=%d: order differs from sort.SliceStable", n, distinct)
			}
		}
	}
	// Presorted and reversed inputs take the early-exit and full-merge paths.
	const n = 20_000
	ascending := stableOrder(n, func(i, j int32) int { return int(i - j) })
	descending := stableOrder(n, func(i, j int32) int { return int(j - i) })
	for i := range n {
		if ascending[i] != int32(i) || descending[i] != int32(n-1-i) {
			t.Fatalf("presorted input: ascending[%d]=%d descending[%d]=%d", i, ascending[i], i, descending[i])
		}
	}
}

// Prepared keys must compare exactly as the raw keys do under Compare, and
// preparing them must leave the caller's values alone.
func TestSortKeysCompareLikeCompare(t *testing.T) {
	raw := []any{
		json.Number("10.50"), json.Number("10.5"), json.Number("9"), json.Number("12345678901234567890"), json.Number("-0.25"),
		3, 2.5, "b", "a", "", nil, true, false,
		[]any{json.Number("2")}, []any{json.Number("10")}, []any{"b"}, []any{"a"}, []any{}, []any{nil}, []any{1, "x"}, []any{1},
		[]any{[]any{json.Number("1.0")}}, []any{[]any{json.Number("1")}}, []any{json.Number("7"), json.Number("7.00")},
		map[string]any{"a": json.Number("1")}, map[string]any{"a": json.Number("1.0")}, map[string]any{},
	}
	snapshot, _ := json.Marshal(raw)
	keys := sortKeys(raw)
	if after, _ := json.Marshal(raw); string(after) != string(snapshot) {
		t.Fatal("sortKeys modified its input")
	}
	for i := range raw {
		for j := range raw {
			if got, want := compareSortKeys(keys[i], keys[j]), Compare(raw[i], raw[j]); got != want {
				t.Errorf("compareSortKeys(%v, %v) = %d, Compare gives %d", raw[i], raw[j], got, want)
			}
		}
	}
	if untouched := []any{"a", 1, nil}; &sortKeys(untouched)[0] != &untouched[0] {
		t.Error("sortKeys copied keys that needed no preparing")
	}
}
