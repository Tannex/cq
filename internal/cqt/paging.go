package cqt

const (
	// The row budget is computed after the fixed title, search/breadcrumb,
	// table header, status/detail, and help lines.
	fixedChromeRows   = 5
	MinTerminalWidth  = 52
	MinTerminalHeight = fixedChromeRows + 3
	maxAnchorHistory  = 64
)

// VisibleRows returns the number of data rows available in the terminal. A
// non-positive result is a hard boundary: callers must not issue a row fetch.
func VisibleRows(width, height int) int {
	if width < MinTerminalWidth || height < MinTerminalHeight {
		return 0
	}
	rows := height - fixedChromeRows
	if rows < 1 {
		return 0
	}
	return rows
}

// RowBudget is the exact maximum item count used for every browse request.
func RowBudget(visibleRows int) int {
	if visibleRows <= 0 || visibleRows > int(^uint(0)>>1)/2 {
		return 0
	}
	return 2 * visibleRows
}

type pageDirection uint8

const (
	pageInitial pageDirection = iota
	pageRefresh
	pageForward
	pageBackward
	pageGrow
)

type pagePlan[A comparable] struct {
	Anchor    A
	Preserve  string
	Direction pageDirection
}

type trimResult struct {
	Start int
	End   int
}

// pager owns only row identities, cursors, and lightweight prior anchors. The
// corresponding typed rows live in the model and are sliced with trimResult.
type pager[A comparable] struct {
	keys        []string
	selected    int
	visible     int
	budget      int
	anchor      A
	more        bool
	previous    []A
	preserve    string
	trimBefore  bool
	trimAfter   bool
	needsReload bool
}

func (p *pager[A]) reset(anchor A, visible, budget int) {
	*p = pager[A]{anchor: anchor, visible: visible, budget: budget}
}

func (p *pager[A]) selectedKey() string {
	if len(p.keys) == 0 || p.selected < 0 || p.selected >= len(p.keys) {
		return p.preserve
	}
	return p.keys[p.selected]
}

func (p *pager[A]) selectedIndex() int {
	if len(p.keys) == 0 {
		return -1
	}
	if p.selected < 0 {
		return 0
	}
	if p.selected >= len(p.keys) {
		return len(p.keys) - 1
	}
	return p.selected
}

func (p *pager[A]) apply(keys []string, more bool, plan pagePlan[A]) {
	oldAnchor := p.anchor
	switch plan.Direction {
	case pageForward:
		p.previous = appendBoundedAnchor(p.previous, oldAnchor)
	case pageBackward:
		if len(p.previous) > 0 {
			p.previous = p.previous[:len(p.previous)-1]
		}
	case pageInitial:
		p.previous = nil
	}
	if p.budget > 0 && len(keys) > p.budget {
		keys = keys[:p.budget]
		more = true
	}
	p.keys = append(p.keys[:0], keys...)
	p.anchor = plan.Anchor
	p.more = more
	p.trimBefore = false
	p.trimAfter = false
	p.needsReload = false
	p.preserve = plan.Preserve
	p.selected = indexOfKey(p.keys, plan.Preserve)
	if p.selected < 0 {
		p.selected = 0
	}
	if len(p.keys) == 0 {
		p.selected = 0
	}
}

func appendBoundedAnchor[A comparable](anchors []A, anchor A) []A {
	if len(anchors) > 0 && anchors[len(anchors)-1] == anchor {
		return anchors
	}
	if len(anchors) == maxAnchorHistory {
		copy(anchors, anchors[1:])
		anchors = anchors[:maxAnchorHistory-1]
	}
	return append(anchors, anchor)
}

func indexOfKey(keys []string, wanted string) int {
	if wanted == "" {
		return -1
	}
	for i, key := range keys {
		if key == wanted {
			return i
		}
	}
	return -1
}

// resize enforces the in-memory bound immediately. It keeps the selected row
// near the middle of the retained window and remembers enough state to refetch
// the same anchored page if rows were removed.
func (p *pager[A]) resize(visible, budget int) trimResult {
	p.visible = visible
	p.budget = budget
	if budget <= 0 {
		p.preserve = p.selectedKey()
		end := len(p.keys)
		p.keys = nil
		p.selected = 0
		p.trimBefore = end > 0
		p.trimAfter = end > 0
		p.needsReload = p.needsReload || end > 0 || p.more
		return trimResult{End: end}
	}
	if len(p.keys) <= budget {
		return trimResult{End: len(p.keys)}
	}

	selected := p.selectedIndex()
	start := selected - budget/2
	if start < 0 {
		start = 0
	}
	if start+budget > len(p.keys) {
		start = len(p.keys) - budget
	}
	end := start + budget
	p.preserve = p.selectedKey()
	p.trimBefore = p.trimBefore || start > 0
	p.trimAfter = p.trimAfter || end < len(p.keys)
	p.needsReload = true
	p.keys = append([]string(nil), p.keys[start:end]...)
	p.selected = indexOfKey(p.keys, p.preserve)
	if p.selected < 0 {
		p.selected = 0
	}
	if p.trimAfter {
		p.more = true
	}
	return trimResult{Start: start, End: end}
}

func (p *pager[A]) move(delta int) (before, after bool) {
	if len(p.keys) == 0 || delta == 0 {
		return false, false
	}
	target := p.selected + delta
	if target < 0 {
		before = true
		target = 0
	}
	if target >= len(p.keys) {
		after = true
		target = len(p.keys) - 1
	}
	p.selected = target
	p.preserve = p.selectedKey()
	return before, after
}

func (p *pager[A]) top() {
	if len(p.keys) > 0 {
		p.selected = 0
		p.preserve = p.keys[0]
	}
}

func (p *pager[A]) bottom() {
	if len(p.keys) > 0 {
		p.selected = len(p.keys) - 1
		p.preserve = p.keys[p.selected]
	}
}

func (p *pager[A]) backwardPlan() (pagePlan[A], bool) {
	if p.selectedIndex() != 0 {
		return pagePlan[A]{}, false
	}
	if p.trimBefore || p.needsReload {
		return pagePlan[A]{Anchor: p.anchor, Preserve: p.selectedKey(), Direction: pageGrow}, true
	}
	if len(p.previous) == 0 {
		return pagePlan[A]{}, false
	}
	return pagePlan[A]{
		Anchor: p.previous[len(p.previous)-1], Preserve: p.selectedKey(), Direction: pageBackward,
	}, true
}

func (p *pager[A]) growPlan() (pagePlan[A], bool) {
	if p.budget <= 0 || (!p.more && !p.needsReload && !p.trimBefore && !p.trimAfter) || len(p.keys) >= p.budget {
		return pagePlan[A]{}, false
	}
	return pagePlan[A]{Anchor: p.anchor, Preserve: p.selectedKey(), Direction: pageGrow}, true
}

func (p *pager[A]) refreshPlan() pagePlan[A] {
	return pagePlan[A]{Anchor: p.anchor, Preserve: p.selectedKey(), Direction: pageRefresh}
}

func (p *pager[A]) initialPlan(anchor A) pagePlan[A] {
	return pagePlan[A]{Anchor: anchor, Preserve: p.preserve, Direction: pageInitial}
}

func forwardNamePlan(p *pager[string]) (pagePlan[string], bool) {
	if p.selectedIndex() != len(p.keys)-1 || !p.more || len(p.keys) == 0 {
		return pagePlan[string]{}, false
	}
	index := len(p.keys) - p.visible
	if index < 0 {
		index = 0
	}
	anchor := p.keys[index]
	if anchor == p.anchor && len(p.keys) == 1 {
		return pagePlan[string]{}, false
	}
	return pagePlan[string]{Anchor: anchor, Preserve: p.selectedKey(), Direction: pageForward}, true
}

func forwardRecordPlan(p *pager[int64], numbers []int64) (pagePlan[int64], bool) {
	if p.selectedIndex() != len(p.keys)-1 || !p.more || len(numbers) != len(p.keys) || len(numbers) == 0 {
		return pagePlan[int64]{}, false
	}
	index := len(numbers) - p.visible
	if index < 0 {
		index = 0
	}
	start := numbers[index] - 1
	if start < 0 {
		start = 0
	}
	if start == p.anchor && len(numbers) == 1 {
		return pagePlan[int64]{}, false
	}
	return pagePlan[int64]{Anchor: start, Preserve: p.selectedKey(), Direction: pageForward}, true
}
