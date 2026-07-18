package cqt

const (
	// The row budget is computed after the fixed title, search/breadcrumb,
	// table header, status/detail, and help lines.
	fixedChromeRows   = 5
	MinTerminalWidth  = 52
	MinTerminalHeight = fixedChromeRows + 3
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
)

type pagePlan[A comparable] struct {
	Anchor    A
	Preserve  string
	Direction pageDirection
}

// pager owns cached row identities and selection state. Typed rows live in the
// model. The cache may grow for the lifetime of the current browse screen, but
// every individual request remains bounded by budget.
type pager[A comparable] struct {
	keys     []string
	selected int
	visible  int
	budget   int
	more     bool
	preserve string
}

func (p *pager[A]) reset(_ A, visible, budget int) {
	*p = pager[A]{visible: visible, budget: budget}
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

// apply replaces the cache for an initial/refresh response and appends unseen
// identities for a forward response. Selection is restored by identity.
func (p *pager[A]) apply(keys []string, more bool, plan pagePlan[A]) {
	if p.budget > 0 && len(keys) > p.budget {
		keys = keys[:p.budget]
		more = true
	}

	selected := p.selectedKey()
	if plan.Preserve != "" {
		selected = plan.Preserve
	}
	if plan.Direction == pageForward {
		seen := make(map[string]struct{}, len(p.keys)+len(keys))
		for _, key := range p.keys {
			seen[key] = struct{}{}
		}
		for _, key := range keys {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			p.keys = append(p.keys, key)
		}
	} else {
		p.keys = append(p.keys[:0], keys...)
	}

	p.more = more
	p.preserve = selected
	p.selected = indexOfKey(p.keys, selected)
	if p.selected < 0 {
		p.selected = 0
	}
	if len(p.keys) == 0 {
		p.selected = 0
	}
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

// resize changes future request sizing without discarding cached rows. A tiny
// terminal retains the cache and selection but callers must cancel row work.
func (p *pager[A]) resize(visible, budget int) {
	p.visible = visible
	p.budget = budget
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

func (p *pager[A]) shouldPrefetch(pending bool) bool {
	if pending || p.budget <= 0 || p.visible <= 0 || !p.more || len(p.keys) == 0 {
		return false
	}
	return p.selectedIndex()+p.visible >= len(p.keys)
}

func (p *pager[A]) initialPlan(anchor A) pagePlan[A] {
	return pagePlan[A]{Anchor: anchor, Preserve: p.preserve, Direction: pageInitial}
}

func forwardNamePlan(p *pager[string]) (pagePlan[string], bool) {
	if !p.shouldPrefetch(false) {
		return pagePlan[string]{}, false
	}
	anchor := p.keys[len(p.keys)-1]
	return pagePlan[string]{Anchor: anchor, Preserve: p.selectedKey(), Direction: pageForward}, true
}

func forwardRecordPlan(p *pager[int64], numbers []int64) (pagePlan[int64], bool) {
	if !p.shouldPrefetch(false) || len(numbers) != len(p.keys) || len(numbers) == 0 {
		return pagePlan[int64]{}, false
	}
	return pagePlan[int64]{Anchor: numbers[len(numbers)-1], Preserve: p.selectedKey(), Direction: pageForward}, true
}
