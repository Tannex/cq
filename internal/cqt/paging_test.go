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
	p.reset("", 2, 4)
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
	p.reset(0, 2, 4)
	p.apply([]string{"1", "2", "3", "4"}, true, p.initialPlan(0))
	p.move(2)
	plan, ok := forwardRecordPlan(&p, []int64{1, 2, 3, 4})
	if !ok || plan.Anchor != 4 || plan.Direction != pageForward {
		t.Fatalf("record forward plan = %#v ok=%v", plan, ok)
	}
}

func TestPagerResizeRetainsSessionCacheAndSelection(t *testing.T) {
	var p pager[string]
	p.reset("", 5, 24)
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

func TestPagerMovesCursorAcrossViewportBeforeScrolling(t *testing.T) {
	keys := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}
	var p pager[string]
	p.reset("", 5, 24)
	p.apply(keys, false, p.initialPlan(""))

	p.bottom()
	assertPagerWindow(t, &p, 11, 7, 4, scrollIdle)
	for _, want := range []struct {
		selected, start, offset int
		scrolling               scrollDirection
	}{
		{10, 7, 3, scrollIdle},
		{9, 7, 2, scrollIdle},
		{8, 7, 1, scrollIdle},
		{7, 7, 0, scrollIdle},
		{6, 5, 1, scrollUp},
		{5, 4, 1, scrollUp},
	} {
		p.move(-1)
		assertPagerWindow(t, &p, want.selected, want.start, want.offset, want.scrolling)
	}

	for _, want := range []struct {
		selected, start, offset int
		scrolling               scrollDirection
	}{
		{6, 4, 2, scrollIdle},
		{7, 4, 3, scrollIdle},
		{8, 4, 4, scrollIdle},
		{9, 6, 3, scrollDown},
	} {
		p.move(1)
		assertPagerWindow(t, &p, want.selected, want.start, want.offset, want.scrolling)
	}

	p.top()
	assertPagerWindow(t, &p, 0, 0, 0, scrollIdle)
	for _, want := range []struct {
		selected, start, offset int
		scrolling               scrollDirection
	}{
		{1, 0, 1, scrollIdle},
		{2, 0, 2, scrollIdle},
		{3, 0, 3, scrollIdle},
		{4, 0, 4, scrollIdle},
		{5, 2, 3, scrollDown},
		{6, 3, 3, scrollDown},
	} {
		p.move(1)
		assertPagerWindow(t, &p, want.selected, want.start, want.offset, want.scrolling)
	}
	p.move(5)
	assertPagerWindow(t, &p, 11, 7, 4, scrollDown)
}

func TestPagerPageMovementPreservesCursorScreenRow(t *testing.T) {
	keys := make([]string, 20)
	for i := range keys {
		keys[i] = string(rune('A' + i))
	}
	var p pager[string]
	p.reset("", 5, 24)
	p.apply(keys, false, p.initialPlan(""))
	p.move(6)
	assertPagerWindow(t, &p, 6, 3, 3, scrollDown)

	p.page(scrollDown)
	assertPagerWindow(t, &p, 11, 8, 3, scrollIdle)
	p.page(scrollUp)
	assertPagerWindow(t, &p, 6, 3, 3, scrollIdle)
	p.page(scrollUp)
	assertPagerWindow(t, &p, 1, 0, 1, scrollIdle)
	p.page(scrollUp)
	assertPagerWindow(t, &p, 0, 0, 0, scrollIdle)

	p.bottom()
	assertPagerWindow(t, &p, 19, 15, 4, scrollIdle)
	p.top()
	assertPagerWindow(t, &p, 0, 0, 0, scrollIdle)
}

func TestPagerAppendPreservesLiveSelectionAndWindow(t *testing.T) {
	var p pager[string]
	p.reset("", 3, 6)
	p.apply([]string{"A", "B", "C", "D", "E", "F"}, true, p.initialPlan(""))
	p.move(3)
	plan, ok := forwardNamePlan(&p)
	if !ok || plan.Anchor != "F" || plan.Direction != pageForward {
		t.Fatalf("forward plan = %#v ok=%v", plan, ok)
	}
	p.move(1)
	assertPagerWindow(t, &p, 4, 3, 1, scrollDown)

	p.apply([]string{"G", "H", "I", "J", "K", "L"}, true, plan)
	assertPagerWindow(t, &p, 4, 3, 1, scrollDown)
	if p.selectedKey() != "E" {
		t.Fatalf("append restored request-time selection: %q", p.selectedKey())
	}
	p.move(1)
	assertPagerWindow(t, &p, 5, 4, 1, scrollDown)
}

func TestPagerRefreshPreservesCursorOffset(t *testing.T) {
	keys := []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L"}
	var p pager[string]
	p.reset("", 5, 24)
	p.apply(keys, false, p.initialPlan(""))
	p.bottom()
	p.move(-2)
	assertPagerWindow(t, &p, 9, 7, 2, scrollIdle)

	plan := p.refreshPlan()
	p.reset("", 5, 24)
	p.apply(keys, false, plan)
	assertPagerWindow(t, &p, 9, 7, 2, scrollIdle)

	p.reset("", 5, 24)
	p.apply(keys[:6], false, plan)
	assertPagerWindow(t, &p, 0, 0, 0, scrollIdle)
	if p.selectedKey() != "A" {
		t.Fatalf("missing preserved key selected %q", p.selectedKey())
	}
}

func TestPagerResizeRetainsAndReconcilesWindow(t *testing.T) {
	keys := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}
	var p pager[string]
	p.reset("", 5, 24)
	p.apply(keys, false, p.initialPlan(""))
	p.bottom()
	p.move(-2)
	assertPagerWindow(t, &p, 9, 7, 2, scrollIdle)

	p.resize(3, 6)
	assertPagerWindow(t, &p, 9, 7, 2, scrollIdle)
	p.resize(2, 4)
	assertPagerWindow(t, &p, 9, 8, 1, scrollIdle)
	p.resize(0, 0)
	if p.windowStart != 8 || p.selectedIndex() != 9 || p.cursorOffset() != -1 {
		t.Fatalf("tiny resize selected=%d start=%d offset=%d", p.selectedIndex(), p.windowStart, p.cursorOffset())
	}
	p.resize(5, 10)
	assertPagerWindow(t, &p, 9, 7, 2, scrollIdle)
}

func TestPagerSmallViewportsDegradeContextMargin(t *testing.T) {
	var one pager[string]
	one.reset("", 1, 3)
	one.apply([]string{"A", "B", "C"}, false, one.initialPlan(""))
	one.bottom()
	assertPagerWindow(t, &one, 2, 2, 0, scrollIdle)
	one.move(-1)
	assertPagerWindow(t, &one, 1, 1, 0, scrollUp)

	var two pager[string]
	two.reset("", 2, 3)
	two.apply([]string{"A", "B", "C"}, false, two.initialPlan(""))
	two.bottom()
	assertPagerWindow(t, &two, 2, 1, 1, scrollIdle)
	two.move(-1)
	assertPagerWindow(t, &two, 1, 1, 0, scrollIdle)
	two.move(-1)
	assertPagerWindow(t, &two, 0, 0, 0, scrollUp)
}

func assertPagerWindow[A comparable](t *testing.T, p *pager[A], selected, start, offset int, scrolling scrollDirection) {
	t.Helper()
	if p.selectedIndex() != selected || p.windowStart != start || p.cursorOffset() != offset || p.scrolling != scrolling {
		t.Fatalf("pager selected/start/offset/scrolling = %d/%d/%d/%d, want %d/%d/%d/%d", p.selectedIndex(), p.windowStart, p.cursorOffset(), p.scrolling, selected, start, offset, scrolling)
	}
	windowStart, windowEnd := p.windowRange()
	if windowStart != start || windowEnd != min(len(p.keys), start+p.visible) {
		t.Fatalf("window range = %d:%d, want %d:%d", windowStart, windowEnd, start, min(len(p.keys), start+p.visible))
	}
}
