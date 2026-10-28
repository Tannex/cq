package compaz

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

var queryResultMarker = regexp.MustCompile(`result-row-(\d{3})`)

func renderedQueryResults(t *testing.T, model *Model) []int {
	t.Helper()
	view := ansi.Strip(model.View().Content)
	var rows []int
	for _, match := range queryResultMarker.FindAllStringSubmatch(view, -1) {
		row, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		t.Fatalf("query results are not visible:\n%s", view)
	}
	return rows
}

func TestQueryPagingReachesEveryRenderedResult(t *testing.T) {
	const resultCount = 100
	for _, size := range []tea.WindowSizeMsg{{Width: 90, Height: 24}, {Width: 52, Height: 13}} {
		for _, footer := range []string{"summary", "runtime_error", "setup_error"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size.Width, size.Height, footer), func(t *testing.T) {
				model, _ := queryModel(t, nil)
				applyMessage(t, model, size)
				openQuery(t, model)
				model.recordQuery("length", true)
				model.recordQuery("map(.)", true)
				model.query.input.SetValue("draft expression")
				model.query.input.SetCursor(3)
				model.query.lines = make([]string, resultCount)
				for i := range model.query.lines {
					model.query.lines[i] = fmt.Sprintf("result-row-%03d", i+1)
				}
				model.query.done, model.query.matches = true, resultCount
				switch footer {
				case "runtime_error":
					model.query.lastErr = strings.Repeat("runtime error detail ", 10)
				case "setup_error":
					model.query.err = strings.Repeat("setup error detail ", 15)
				}
				history := slices.Clone(model.queryHistory)
				for _, direction := range []struct {
					name     string
					key      rune
					boundary int
				}{{"forward", tea.KeyPgDown, resultCount}, {"backward", tea.KeyPgUp, 1}} {
					seen := make(map[int]bool, resultCount)
					for step := 0; step <= resultCount; step++ {
						rows := renderedQueryResults(t, model)
						for _, row := range rows {
							seen[row] = true
						}
						if slices.Contains(rows, direction.boundary) {
							for range 2 {
								applyMessage(t, model, keyPress(direction.key, ""))
								if got := renderedQueryResults(t, model); !slices.Equal(got, rows) {
									t.Fatalf("%s at boundary changed results from %v to %v", direction.name, rows, got)
								}
							}
							break
						}
						applyMessage(t, model, keyPress(direction.key, ""))
					}
					for row := 1; row <= resultCount; row++ {
						if !seen[row] {
							t.Fatalf("%s paging skipped result %d", direction.name, row)
						}
					}
				}
				if got := model.query.input.Value(); got != "draft expression" || model.query.input.Position() != 3 {
					t.Fatalf("paging changed draft or cursor: %q at %d", got, model.query.input.Position())
				}
				if !slices.Equal(model.queryHistory, history) || model.query.historyIndex != len(history) {
					t.Fatal("paging changed query history")
				}
			})
		}
	}
}

func TestQueryPagingUsesResizedViewport(t *testing.T) {
	model, _ := queryModel(t, nil)
	applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 24})
	openQuery(t, model)
	model.query.lines = make([]string, 100)
	for i := range model.query.lines {
		model.query.lines[i] = fmt.Sprintf("result-row-%03d", i+1)
	}
	model.query.done, model.query.matches = true, len(model.query.lines)
	for _, size := range []tea.WindowSizeMsg{{Width: 90, Height: 24}, {Width: 52, Height: 13}, {Width: 90, Height: 24}} {
		applyMessage(t, model, size)
		before := renderedQueryResults(t, model)
		applyMessage(t, model, keyPress(tea.KeyPgDown, ""))
		after := renderedQueryResults(t, model)
		if after[0] != before[len(before)-1]+1 {
			t.Fatalf("%dx%d: page down went from %v to %v", size.Width, size.Height, before, after)
		}
		applyMessage(t, model, keyPress(tea.KeyPgUp, ""))
		if got := renderedQueryResults(t, model); !slices.Equal(got, before) {
			t.Fatalf("%dx%d: page up returned %v, want %v", size.Width, size.Height, got, before)
		}
	}
}
