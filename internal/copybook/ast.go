// Package copybook parses COBOL copybooks (data-division record layouts)
// into an item tree.
package copybook

import "fmt"

type Usage int

const (
	UsageDisplay Usage = iota // zoned decimal / text
	UsageBinary               // COMP, COMP-4, COMP-5, BINARY
	UsagePacked               // COMP-3, PACKED-DECIMAL
	UsageFloat4               // COMP-1
	UsageFloat8               // COMP-2
)

func (u Usage) String() string {
	switch u {
	case UsageBinary:
		return "binary"
	case UsagePacked:
		return "packed"
	case UsageFloat4:
		return "comp-1"
	case UsageFloat8:
		return "comp-2"
	default:
		return "display"
	}
}

// Category of a PICTURE clause.
type Category int

const (
	CatAlphanumeric  Category = iota // X, A (and alphanumeric-edited)
	CatNumeric                       // 9 P S V only
	CatNumericEdited                 // Z * $ + - . , B 0 / CR DB with digits
)

// Picture is a parsed PICTURE clause.
type Picture struct {
	Raw      string
	Category Category
	Width    int  // storage width in characters under USAGE DISPLAY (sign overpunch excluded)
	Digits   int  // count of digit positions (9) for numeric
	Scale    int  // digits right of the implied decimal point (V/P); may be negative
	Signed   bool // leading S present
}

// ValueRange is one literal of a VALUE clause; To is empty for a single
// value, non-empty for a THRU range. Figurative constants (ZERO, SPACES,
// ...) are kept as their keyword.
type ValueRange struct {
	From string
	To   string
}

// Condition is a level-88 condition name attached to a data item.
type Condition struct {
	Name   string
	Values []ValueRange
}

// Item is one data-description entry.
type Item struct {
	Level        int
	Name         string // "FILLER" when absent
	Filler       bool
	Pic          *Picture
	Usage        Usage
	UsageSet     bool // explicit USAGE on this item (propagates to children)
	Occurs       int  // fixed count, or max for OCCURS DEPENDING ON; 0 = scalar
	OccursMin    int
	DependingOn  string // name of the ODO counter field, if any
	Redefines    string
	SignSeparate bool
	SignLeading  bool
	Sync         bool
	Justified    bool
	BlankZero    bool
	Conditions   []Condition
	Children     []*Item
	Line         int // 1-based source line of the entry

	condValues []ValueRange // literals from this entry's own VALUE clause
}

// Group reports whether the item is a group (no PICTURE, has or may have children).
func (it *Item) Group() bool {
	return it.Pic == nil && it.Usage != UsageFloat4 && it.Usage != UsageFloat8
}

// ParseError is a copybook syntax error with source position.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string { return fmt.Sprintf("copybook line %d: %s", e.Line, e.Msg) }
