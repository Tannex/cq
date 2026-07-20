package compaz

import (
	"math"
	"strconv"
)

const (
	// The row budget is computed after the fixed title, search/breadcrumb,
	// chrome rule, table header, status/detail, and help lines.
	fixedChromeRows  = 6
	MinTerminalWidth = 52
)

// MinTerminalHeight returns the smallest usable terminal height.
func MinTerminalHeight() int {
	return fixedChromeRows + 3
}

// VisibleRows returns the number of data rows available in the terminal. A
// non-positive result is a hard boundary: callers must not issue a row fetch.
func VisibleRows(width, height int) int {
	if width < MinTerminalWidth || height < MinTerminalHeight() {
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
	if visibleRows <= 0 || visibleRows > math.MaxInt/2 {
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

type scrollDirection int8

const (
	scrollUp   scrollDirection = -1
	scrollIdle scrollDirection = 0
	scrollDown scrollDirection = 1
)

type pagePlan[A comparable] struct {
	Anchor       A
	Preserve     string
	CursorOffset int
	Direction    pageDirection
}

// pager owns cached row identities, selection, and the visible row window.
// Typed rows live in the model. The cache may grow for the lifetime of the
// current browse screen, but every individual request remains bounded by budget.
type pagerNavigator interface {
	selectedKey() string
	move(int) (bool, bool)
	page(scrollDirection)
	top()
	bottom()
}

type pager[A comparable] struct {
	keys        []string
	selected    int
	windowStart int
	visible     int
	budget      int
	more        bool
	preserve    string
}

func (p *pager[A]) reset(visible, budget int) {
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

func (p *pager[A]) selectKey(key string) bool {
	index := indexOfKey(p.keys, key)
	if index < 0 {
		return false
	}
	p.selected = index
	p.normalizeWindow()
	p.preserve = p.selectedKey()
	return true
}

func (p *pager[A]) windowRange() (int, int) {
	if len(p.keys) == 0 || p.visible <= 0 {
		return 0, 0
	}
	start := p.windowStart
	return start, min(len(p.keys), start+p.visible)
}

func (p *pager[A]) cursorOffset() int {
	selected := p.selectedIndex()
	start, end := p.windowRange()
	if selected < start || selected >= end {
		return -1
	}
	return selected - start
}

func (p *pager[A]) atEnd() bool {
	return len(p.keys) > 0 && !p.more && p.selectedIndex() == len(p.keys)-1
}

// apply replaces the cache for an initial/refresh response and appends unseen
// identities for a forward response. Replacement selection is restored by
// identity; append preserves the live selection and window.
func (p *pager[A]) apply(keys []string, more bool, plan pagePlan[A]) {
	if p.budget > 0 && len(keys) > p.budget {
		keys = keys[:p.budget]
		more = true
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
		p.more = more
		p.normalizeWindow()
		p.preserve = p.selectedKey()
		return
	}

	selected := p.selectedKey()
	if plan.Preserve != "" {
		selected = plan.Preserve
	}
	p.keys = append(p.keys[:0], keys...)
	p.more = more
	p.selected = indexOfKey(p.keys, selected)
	if p.selected < 0 {
		p.selected = 0
		p.windowStart = 0
	} else if plan.Direction == pageRefresh {
		p.windowStart = p.selected - max(0, plan.CursorOffset)
	} else {
		p.windowStart = 0
	}
	if len(p.keys) == 0 {
		p.selected = 0
		p.windowStart = 0
		p.preserve = selected
		return
	}
	p.normalizeWindow()
	p.preserve = p.selectedKey()
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
	if visible > 0 {
		p.normalizeWindow()
	}
}

func (p *pager[A]) normalizeWindow() {
	if len(p.keys) == 0 {
		p.selected = 0
		p.windowStart = 0
		return
	}
	p.selected = min(max(0, p.selected), len(p.keys)-1)
	if p.visible <= 0 {
		return
	}
	lastStart := max(0, len(p.keys)-p.visible)
	p.windowStart = min(max(0, p.windowStart), lastStart)
	if p.selected < p.windowStart {
		p.windowStart = p.selected
	}
	if p.selected >= p.windowStart+p.visible {
		p.windowStart = p.selected - p.visible + 1
	}
	p.windowStart = min(max(0, p.windowStart), lastStart)
}

func (p *pager[A]) move(delta int) (before, after bool) {
	if len(p.keys) == 0 || delta == 0 {
		return false, false
	}
	target := p.selected + delta
	before = target < 0
	after = target >= len(p.keys)
	direction := scrollDown
	steps := delta
	if delta < 0 {
		direction = scrollUp
		steps = -delta
	}
	for i := 0; i < steps; i++ {
		if !p.moveOne(direction) {
			break
		}
	}
	return before, after
}

func (p *pager[A]) moveOne(direction scrollDirection) bool {
	p.normalizeWindow()
	target := p.selected + int(direction)
	if target < 0 || target >= len(p.keys) {
		return false
	}
	p.selected = target
	if p.visible > 0 {
		margin := 0
		if p.visible > 2 {
			margin = 1
		}
		offset := p.selected - p.windowStart
		switch direction {
		case scrollUp:
			if offset < margin {
				p.windowStart = p.selected - margin
			}
		case scrollDown:
			maxOffset := p.visible - 1 - margin
			if offset > maxOffset {
				p.windowStart = p.selected - maxOffset
			}
		}
		p.normalizeWindow()
	}
	p.preserve = p.selectedKey()
	return true
}

func (p *pager[A]) page(direction scrollDirection) {
	if len(p.keys) == 0 || p.visible <= 0 || direction == scrollIdle {
		return
	}
	p.normalizeWindow()
	offset := p.cursorOffset()
	if offset < 0 {
		offset = 0
	}
	target := p.selected + int(direction)*p.visible
	target = min(max(0, target), len(p.keys)-1)
	p.selected = target
	p.windowStart = target - offset
	p.normalizeWindow()
	p.preserve = p.selectedKey()
}

func (p *pager[A]) top() {
	if len(p.keys) > 0 {
		p.selected = 0
		p.windowStart = 0
		p.preserve = p.keys[0]
	}
}

func (p *pager[A]) bottom() {
	if len(p.keys) > 0 {
		p.selected = len(p.keys) - 1
		p.windowStart = max(0, len(p.keys)-p.visible)
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

func (p *pager[A]) refreshPlan() pagePlan[A] {
	return pagePlan[A]{Preserve: p.selectedKey(), CursorOffset: max(0, p.cursorOffset()), Direction: pageRefresh}
}

// forwardPlan builds a forward prefetch plan anchored on the last cached key,
// parsed into the pager's anchor type.
func forwardPlan[A comparable](p *pager[A], parse func(string) (A, bool)) (pagePlan[A], bool) {
	if !p.shouldPrefetch(false) {
		return pagePlan[A]{}, false
	}
	anchor, ok := parse(p.keys[len(p.keys)-1])
	if !ok {
		return pagePlan[A]{}, false
	}
	return pagePlan[A]{Anchor: anchor, Direction: pageForward}, true
}

func forwardNamePlan(p *pager[string]) (pagePlan[string], bool) {
	return forwardPlan(p, func(key string) (string, bool) { return key, true })
}

func forwardRecordPlan(p *pager[int64]) (pagePlan[int64], bool) {
	// Record keys are the decimal encoding of the record number, so the last
	// key recovers the anchor without a parallel numbers slice.
	return forwardPlan(p, func(key string) (int64, bool) {
		number, err := strconv.ParseInt(key, 10, 64)
		return number, err == nil
	})
}
