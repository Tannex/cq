package cqt

import (
	"fmt"
	"testing"
)

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

func TestNamePagerMovesWithinWindowThenUsesOnePageOverlap(t *testing.T) {
	var p pager[string]
	p.reset("", 3, 6)
	keys := []string{"A", "B", "C", "D", "E", "F"}
	p.apply(keys, true, p.initialPlan(""))

	for range 5 {
		before, after := p.move(1)
		if before || after {
			t.Fatal("movement inside the active window requested another page")
		}
	}
	_, after := p.move(1)
	if !after {
		t.Fatal("crossing the final row did not request forward paging")
	}
	plan, ok := forwardNamePlan(&p)
	if !ok {
		t.Fatal("forward plan was not produced")
	}
	// The inclusive z/OSMF start cursor is the first row of the next visible
	// page, so anchoring at D keeps D/E/F as the one-visible-page overlap and
	// lets the server return D plus budget-1 new items without a duplicate in
	// the response.
	if plan.Anchor != "D" || plan.Preserve != "F" || plan.Direction != pageForward {
		t.Fatalf("forward plan = %#v", plan)
	}

	p.apply([]string{"D", "E", "F", "G", "H", "I"}, true, plan)
	if p.selectedKey() != "F" || len(p.previous) != 1 || p.previous[0] != "" {
		t.Fatalf("selection/history after forward apply: selected=%q history=%#v", p.selectedKey(), p.previous)
	}
	p.top()
	back, ok := p.backwardPlan()
	if !ok || back.Anchor != "" || back.Direction != pageBackward {
		t.Fatalf("backward plan = %#v ok=%v", back, ok)
	}
	p.apply(keys, true, back)
	if p.selectedKey() != "D" || len(p.previous) != 0 {
		t.Fatalf("backward apply did not preserve boundary selection: selected=%q history=%#v", p.selectedKey(), p.previous)
	}
}

func TestRecordPagerUsesExactOnePageOverlap(t *testing.T) {
	var p pager[int64]
	p.reset(100, 2, 4)
	keys := []string{"101", "102", "103", "104"}
	p.apply(keys, true, p.initialPlan(100))
	p.bottom()
	_, after := p.move(1)
	if !after {
		t.Fatal("record boundary was not crossed")
	}
	plan, ok := forwardRecordPlan(&p, []int64{101, 102, 103, 104})
	if !ok || plan.Anchor != 102 || plan.Preserve != "104" {
		t.Fatalf("record forward plan = %#v ok=%v", plan, ok)
	}
}

func TestPagerResizeShrinksAroundSelectionAndRestoresWithSameAnchor(t *testing.T) {
	var p pager[string]
	p.reset("ANCHOR", 5, 10)
	keys := make([]string, 10)
	for i := range keys {
		keys[i] = fmt.Sprintf("ROW%02d", i+1)
	}
	p.apply(keys, true, pagePlan[string]{Anchor: "ANCHOR", Preserve: "ROW09", Direction: pageInitial})

	trim := p.resize(2, 4)
	if trim.Start != 6 || trim.End != 10 || len(p.keys) != 4 || p.selectedKey() != "ROW09" {
		t.Fatalf("shrink trim=%#v keys=%#v selected=%q", trim, p.keys, p.selectedKey())
	}
	if !p.trimBefore || !p.needsReload || len(p.keys) > p.budget {
		t.Fatalf("shrink state = %#v", p)
	}

	p.resize(5, 10)
	plan, ok := p.growPlan()
	if !ok || plan.Anchor != "ANCHOR" || plan.Preserve != "ROW09" || plan.Direction != pageGrow {
		t.Fatalf("grow plan = %#v ok=%v", plan, ok)
	}
	p.apply(keys, true, plan)
	if p.selectedKey() != "ROW09" || len(p.keys) != 10 {
		t.Fatalf("grown pager selected=%q keys=%d", p.selectedKey(), len(p.keys))
	}
}

func TestPagerTinyTerminalClearsRowsButKeepsLightweightSelection(t *testing.T) {
	var p pager[string]
	p.reset("A", 2, 4)
	p.apply([]string{"B", "C", "D", "E"}, true, pagePlan[string]{Anchor: "A", Preserve: "D", Direction: pageInitial})
	trim := p.resize(0, 0)
	if trim.End != 4 || len(p.keys) != 0 || p.preserve != "D" || !p.needsReload {
		t.Fatalf("tiny resize state=%#v trim=%#v", p, trim)
	}
	if len(p.previous) != 0 {
		t.Fatalf("tiny resize retained row data instead of lightweight anchors: %#v", p.previous)
	}
}

func TestPagerAnchorHistoryIsBounded(t *testing.T) {
	var anchors []string
	for i := 0; i < maxAnchorHistory+10; i++ {
		anchors = appendBoundedAnchor(anchors, fmt.Sprintf("A%03d", i))
	}
	if len(anchors) != maxAnchorHistory || anchors[0] != "A010" {
		t.Fatalf("bounded history length=%d first=%q", len(anchors), anchors[0])
	}
}
