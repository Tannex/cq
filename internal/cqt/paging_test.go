package cqt

import "testing"

func TestVisibleRowsAndExactBudget(t *testing.T) {
	tests := []struct {
		width, height int
		visible       int
		budget        int
	}{
		{width: MinTerminalWidth - 1, height: 40},
		{width: 100, height: MinTerminalHeight - 1},
		{width: MinTerminalWidth, height: MinTerminalHeight, visible: 3, budget: 6},
		{width: 100, height: 25, visible: 20, budget: 40},
	}
	for _, test := range tests {
		visible := VisibleRows(test.width, test.height)
		if visible != test.visible || RowBudget(visible) != test.budget {
			t.Fatalf("VisibleRows(%d,%d)=%d budget=%d, want %d/%d", test.width, test.height, visible, RowBudget(visible), test.visible, test.budget)
		}
	}
	if RowBudget(0) != 0 || RowBudget(-1) != 0 {
		t.Fatal("non-positive visible rows must not produce a request budget")
	}
	if RowBudget(int(^uint(0)>>1)) != 0 {
		t.Fatal("overflowing visible row count must not produce a wrapped request budget")
	}
}

func TestNamePagerPrefetchesWithOneVisiblePageRemaining(t *testing.T) {
	var p pager[string]
	p.reset("", 3, 6)
	p.apply([]string{"A", "B", "C", "D", "E", "F"}, true, p.initialPlan(""))

	p.move(2)
	if p.shouldPrefetch(false) {
		t.Fatal("prefetch started before selection entered the final visible page")
	}
	p.move(1)
	if !p.shouldPrefetch(false) {
		t.Fatal("prefetch did not start with one visible page remaining")
	}
	plan, ok := forwardNamePlan(&p)
	if !ok || plan.Anchor != "F" || plan.Preserve != "D" || plan.Direction != pageForward {
		t.Fatalf("forward plan = %#v ok=%v", plan, ok)
	}

	p.apply([]string{"G", "H", "I", "J", "K", "L"}, true, plan)
	if len(p.keys) != 12 || p.selectedKey() != "D" {
		t.Fatalf("merged cache=%#v selected=%q", p.keys, p.selectedKey())
	}
	if p.shouldPrefetch(false) {
		t.Fatal("freshly extended cache immediately prefetched again")
	}
}

func TestPagerForwardMergeDeduplicatesAndPreservesSelection(t *testing.T) {
	var p pager[string]
	p.reset("", 2, 4)
	p.apply([]string{"A", "B", "C", "D"}, true, p.initialPlan(""))
	p.move(2)
	p.apply([]string{"D", "E", "F", "G"}, false, pagePlan[string]{Anchor: "D", Preserve: "C", Direction: pageForward})

	want := []string{"A", "B", "C", "D", "E", "F", "G"}
	if len(p.keys) != len(want) {
		t.Fatalf("merged keys=%#v", p.keys)
	}
	for i := range want {
		if p.keys[i] != want[i] {
			t.Fatalf("merged keys=%#v want=%#v", p.keys, want)
		}
	}
	if p.selectedKey() != "C" || p.more {
		t.Fatalf("selected=%q more=%v", p.selectedKey(), p.more)
	}
}

func TestRecordPagerPrefetchStartsAfterLastCachedRecord(t *testing.T) {
	var p pager[int64]
	p.reset(0, 2, 4)
	p.apply([]string{"1", "2", "3", "4"}, true, p.initialPlan(0))
	p.move(2)
	plan, ok := forwardRecordPlan(&p, []int64{1, 2, 3, 4})
	if !ok || plan.Anchor != 4 || plan.Preserve != "3" {
		t.Fatalf("record forward plan = %#v ok=%v", plan, ok)
	}
}

func TestPagerResizeRetainsSessionCacheAndSelection(t *testing.T) {
	var p pager[string]
	p.reset("", 5, 10)
	p.apply([]string{"A", "B", "C", "D", "E", "F"}, true, p.initialPlan(""))
	p.move(4)

	p.resize(2, 4)
	if len(p.keys) != 6 || p.selectedKey() != "E" {
		t.Fatalf("shrink keys=%#v selected=%q", p.keys, p.selectedKey())
	}
	p.resize(0, 0)
	if len(p.keys) != 6 || p.selectedKey() != "E" {
		t.Fatalf("tiny resize keys=%#v selected=%q", p.keys, p.selectedKey())
	}
	p.resize(5, 10)
	if len(p.keys) != 6 || p.selectedKey() != "E" {
		t.Fatalf("restored cache=%#v selected=%q", p.keys, p.selectedKey())
	}
}

func TestPagerSuppressesPrefetchWhileRequestPending(t *testing.T) {
	var p pager[string]
	p.reset("", 2, 4)
	p.apply([]string{"A", "B", "C", "D"}, true, p.initialPlan(""))
	p.bottom()
	if !p.shouldPrefetch(false) {
		t.Fatal("test requires prefetch-ready pager")
	}
	if p.shouldPrefetch(true) {
		t.Fatal("pending request did not suppress duplicate prefetch")
	}
}
