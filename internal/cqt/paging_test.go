package cqt

import "testing"

func TestVisibleRowsAndExactBudget(t *testing.T) {
	tests := []struct {
		width, height int
		visible       int
		budget        int
	}{
		{width: MinTerminalWidth - 1, height: 40},
		{width: 100, height: MinTerminalHeight(false) - 1},
		{width: MinTerminalWidth, height: MinTerminalHeight(false), visible: 3, budget: 6},
		{width: 100, height: 25, visible: 20, budget: 40},
	}
	for _, test := range tests {
		visible := VisibleRows(test.width, test.height, false)
		if visible != test.visible || RowBudget(visible) != test.budget {
			t.Fatalf("VisibleRows(%d,%d,false)=%d budget=%d, want %d/%d", test.width, test.height, visible, RowBudget(visible), test.visible, test.budget)
		}
	}
	// With the tab bar visible one extra chrome row is reserved.
	if visible := VisibleRows(MinTerminalWidth, MinTerminalHeight(true), true); visible != 3 {
		t.Fatalf("VisibleRows with tab bar = %d, want 3", visible)
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
	p.reset(3, 6)
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
	if !ok || plan.Anchor != "F" || plan.Direction != pageForward {
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
	p.reset(2, 4)
	p.apply([]string{"A", "B", "C", "D"}, true, p.initialPlan(""))
	p.move(2)
	p.apply([]string{"D", "E", "F", "G"}, false, pagePlan[string]{Anchor: "D", Direction: pageForward})

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
	p.reset(2, 4)
	p.apply([]string{"1", "2", "3", "4"}, true, p.initialPlan(0))
	p.move(2)
	plan, ok := forwardRecordPlan(&p)
	if !ok || plan.Anchor != 4 || plan.Direction != pageForward {
		t.Fatalf("record forward plan = %#v ok=%v", plan, ok)
	}
}

func TestPagerResizeRetainsSessionCacheAndSelection(t *testing.T) {
	var p pager[string]
	p.reset(5, 24)
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
	p.reset(2, 4)
	p.apply([]string{"A", "B", "C", "D"}, true, p.initialPlan(""))
	p.bottom()
	if !p.shouldPrefetch(false) {
		t.Fatal("test requires prefetch-ready pager")
	}
	if p.shouldPrefetch(true) {
		t.Fatal("pending request did not suppress duplicate prefetch")
	}
}

func TestPagerScrollsAtOneRowViewportMargin(t *testing.T) {
	keys := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}
	var p pager[string]
	p.reset(5, 24)
	p.apply(keys, false, p.initialPlan(""))

	for _, want := range []struct {
		selected, start, offset int
	}{
		{1, 0, 1},
		{2, 0, 2},
		{3, 0, 3},
		{4, 1, 3},
		{5, 2, 3},
		{6, 3, 3},
	} {
		p.move(1)
		assertPagerWindow(t, &p, want.selected, want.start, want.offset)
	}

	p.bottom()
	assertPagerWindow(t, &p, 11, 7, 4)
	for _, want := range []struct {
		selected, start, offset int
	}{
		{10, 7, 3},
		{9, 7, 2},
		{8, 7, 1},
		{7, 6, 1},
		{6, 5, 1},
		{5, 4, 1},
	} {
		p.move(-1)
		assertPagerWindow(t, &p, want.selected, want.start, want.offset)
	}
}

func TestPagerSingleStepMovementNeverJumpsAgainstDirection(t *testing.T) {
	keys := make([]string, 30)
	for i := range keys {
		keys[i] = string(rune('A' + i))
	}
	var p pager[string]
	p.reset(7, 30)
	p.apply(keys, false, p.initialPlan(""))

	previousOffset := p.cursorOffset()
	for p.selectedIndex() < len(keys)-1 {
		previousStart := p.windowStart
		p.move(1)
		offset := p.cursorOffset()
		if p.windowStart == previousStart && offset < previousOffset {
			t.Fatalf("down movement jumped upward: offset %d to %d", previousOffset, offset)
		}
		if p.windowStart > previousStart && offset != 5 && p.windowStart < len(keys)-p.visible {
			t.Fatalf("down scroll offset=%d, want one-row bottom margin", offset)
		}
		previousOffset = offset
	}

	previousOffset = p.cursorOffset()
	for p.selectedIndex() > 0 {
		previousStart := p.windowStart
		p.move(-1)
		offset := p.cursorOffset()
		if p.windowStart == previousStart && offset > previousOffset {
			t.Fatalf("up movement jumped downward: offset %d to %d", previousOffset, offset)
		}
		if p.windowStart < previousStart && offset != 1 && p.windowStart > 0 {
			t.Fatalf("up scroll offset=%d, want one-row top margin", offset)
		}
		previousOffset = offset
	}
}

func TestPagerPageMovementPreservesCursorScreenRow(t *testing.T) {
	keys := make([]string, 20)
	for i := range keys {
		keys[i] = string(rune('A' + i))
	}
	var p pager[string]
	p.reset(5, 24)
	p.apply(keys, false, p.initialPlan(""))
	p.move(6)
	assertPagerWindow(t, &p, 6, 3, 3)

	p.page(scrollDown)
	assertPagerWindow(t, &p, 11, 8, 3)
	p.page(scrollUp)
	assertPagerWindow(t, &p, 6, 3, 3)
	p.page(scrollUp)
	assertPagerWindow(t, &p, 1, 0, 1)
	p.page(scrollUp)
	assertPagerWindow(t, &p, 0, 0, 0)

	p.bottom()
	assertPagerWindow(t, &p, 19, 15, 4)
	p.top()
	assertPagerWindow(t, &p, 0, 0, 0)
}

func TestPagerAppendPreservesLiveSelectionAndWindow(t *testing.T) {
	var p pager[string]
	p.reset(3, 6)
	p.apply([]string{"A", "B", "C", "D", "E", "F"}, true, p.initialPlan(""))
	p.move(3)
	plan, ok := forwardNamePlan(&p)
	if !ok || plan.Anchor != "F" || plan.Direction != pageForward {
		t.Fatalf("forward plan = %#v ok=%v", plan, ok)
	}
	p.move(1)
	assertPagerWindow(t, &p, 4, 3, 1)

	p.apply([]string{"G", "H", "I", "J", "K", "L"}, true, plan)
	assertPagerWindow(t, &p, 4, 3, 1)
	if p.selectedKey() != "E" {
		t.Fatalf("append restored request-time selection: %q", p.selectedKey())
	}
	p.move(1)
	assertPagerWindow(t, &p, 5, 4, 1)
}

func TestPagerRefreshPreservesCursorOffset(t *testing.T) {
	keys := []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L"}
	var p pager[string]
	p.reset(5, 24)
	p.apply(keys, false, p.initialPlan(""))
	p.bottom()
	p.move(-2)
	assertPagerWindow(t, &p, 9, 7, 2)

	plan := p.refreshPlan()
	p.reset(5, 24)
	p.apply(keys, false, plan)
	assertPagerWindow(t, &p, 9, 7, 2)

	p.reset(5, 24)
	p.apply(keys[:6], false, plan)
	assertPagerWindow(t, &p, 0, 0, 0)
	if p.selectedKey() != "A" {
		t.Fatalf("missing preserved key selected %q", p.selectedKey())
	}
}

func TestPagerResizeRetainsAndReconcilesWindow(t *testing.T) {
	keys := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}
	var p pager[string]
	p.reset(5, 24)
	p.apply(keys, false, p.initialPlan(""))
	p.bottom()
	p.move(-2)
	assertPagerWindow(t, &p, 9, 7, 2)

	p.resize(3, 6)
	assertPagerWindow(t, &p, 9, 7, 2)
	p.resize(2, 4)
	assertPagerWindow(t, &p, 9, 8, 1)
	p.resize(0, 0)
	if p.windowStart != 8 || p.selectedIndex() != 9 || p.cursorOffset() != -1 {
		t.Fatalf("tiny resize selected=%d start=%d offset=%d", p.selectedIndex(), p.windowStart, p.cursorOffset())
	}
	p.resize(5, 10)
	assertPagerWindow(t, &p, 9, 7, 2)
}

func TestPagerSmallViewportsDegradeContextMargin(t *testing.T) {
	var one pager[string]
	one.reset(1, 3)
	one.apply([]string{"A", "B", "C"}, false, one.initialPlan(""))
	one.bottom()
	assertPagerWindow(t, &one, 2, 2, 0)
	one.move(-1)
	assertPagerWindow(t, &one, 1, 1, 0)

	var two pager[string]
	two.reset(2, 3)
	two.apply([]string{"A", "B", "C"}, false, two.initialPlan(""))
	two.bottom()
	assertPagerWindow(t, &two, 2, 1, 1)
	two.move(-1)
	assertPagerWindow(t, &two, 1, 1, 0)
	two.move(-1)
	assertPagerWindow(t, &two, 0, 0, 0)
}

func assertPagerWindow[A comparable](t *testing.T, p *pager[A], selected, start, offset int, _ ...scrollDirection) {
	t.Helper()
	if p.selectedIndex() != selected || p.windowStart != start || p.cursorOffset() != offset {
		t.Fatalf("pager selected/start/offset = %d/%d/%d, want %d/%d/%d", p.selectedIndex(), p.windowStart, p.cursorOffset(), selected, start, offset)
	}
	windowStart, windowEnd := p.windowRange()
	if windowStart != start || windowEnd != min(len(p.keys), start+p.visible) {
		t.Fatalf("window range = %d:%d, want %d:%d", windowStart, windowEnd, start, min(len(p.keys), start+p.visible))
	}
}
