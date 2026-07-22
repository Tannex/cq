package main

import (
	"fmt"
	"html"
	"image/color"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/compaz"
	"github.com/Tannex/cq/internal/dsnmap"
	"github.com/Tannex/cq/internal/events"
	"github.com/Tannex/cq/internal/favorites"
)

// TestRenderShot is a headless tui-shot: it drives the demo model with a
// scripted key sequence and writes every resulting frame as raw ANSI plus one
// self-contained HTML page for visual inspection. It needs no tmux, pty, or
// display, so it runs anywhere `go test` does. Skipped unless RENDER_SHOT_OUT
// is set, so the normal test run never produces artifacts.
//
//	RENDER_SHOT_OUT=artifacts/shot \
//	RENDER_SHOT_SIZE=110x30 \
//	RENDER_SHOT_STEPS="];enter;down;enter;:;type:incl ief;enter" \
//	go test ./cmd/compaz -run TestRenderShot -v
//
// Steps are ';'-separated: a named key (enter, esc, tab, up, down, left,
// right, pgup, pgdn), a single character, or "type:<text>" which sends the
// text one keystroke at a time.
func TestRenderShot(t *testing.T) {
	out := os.Getenv("RENDER_SHOT_OUT")
	if out == "" {
		t.Skip("set RENDER_SHOT_OUT to write render-shot artifacts")
	}
	isolated := t.TempDir()
	for _, name := range []string{"XDG_CONFIG_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(name, isolated)
	}
	width, height := 100, 30
	if size := os.Getenv("RENDER_SHOT_SIZE"); size != "" {
		if _, err := fmt.Sscanf(size, "%dx%d", &width, &height); err != nil {
			t.Fatalf("RENDER_SHOT_SIZE = %q, want WxH", size)
		}
	}

	model, err := compaz.NewModel(compaz.Options{Prefix: "DEMO.*"}, compaz.Dependencies{
		LoadSession:  loadDemoSession,
		ListProfiles: listDemoProfiles,
		Mappings:     dsnmap.DefaultStore(nil),
		Favorites:    favorites.DefaultStore(nil),
		Events:       events.DefaultStore(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, model, func() tea.Cmd {
		_, cmd := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
		return cmd
	}())
	drain(t, model, model.Init())

	frames := []frame{capture(model, "initial")}
	for _, step := range splitSteps(os.Getenv("RENDER_SHOT_STEPS")) {
		for _, msg := range stepMessages(t, step) {
			_, cmd := model.Update(msg)
			drain(t, model, cmd)
		}
		frames = append(frames, capture(model, step))
	}

	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	var page strings.Builder
	first := frames[0]
	fmt.Fprintf(&page, "<!doctype html><meta charset=\"utf-8\"><title>render-shot</title>"+
		"<body style=\"background:%s;color:%s;font:14px/1.25 'Cascadia Mono',Consolas,monospace\">",
		hexColor(first.bg), hexColor(first.fg))
	for i, f := range frames {
		name := fmt.Sprintf("%02d-%s.ans", i, sanitize(f.label))
		if err := os.WriteFile(filepath.Join(out, name), []byte(f.content), 0o644); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&page, "<h3 style=\"font-family:sans-serif\">%d — %s</h3>"+
			"<pre style=\"display:inline-block;border:1px solid #444;padding:8px;background:%s\">%s</pre>",
			i, html.EscapeString(f.label), hexColor(f.bg), ansiToHTML(f.content, hexColor(f.fg), hexColor(f.bg)))
	}
	index := filepath.Join(out, "index.html")
	if err := os.WriteFile(index, []byte(page.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d frames to %s", len(frames), index)
}

type frame struct {
	label, content string
	fg, bg         color.Color
}

func capture(model *compaz.Model, label string) frame {
	view := model.View()
	return frame{label: label, content: view.Content, fg: view.ForegroundColor, bg: view.BackgroundColor}
}

// drain executes a command tree breadth-first, feeding every produced message
// back into the model — the synchronous stand-in for Bubble Tea's runtime.
func drain(t *testing.T, model *compaz.Model, command tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{command}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 10000 {
			t.Fatal("command drain did not settle")
		}
		current := queue[0]
		queue = queue[1:]
		if current == nil {
			continue
		}
		message := current()
		if batch, ok := message.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		// Follow mode reschedules its poll tick forever; a synchronous
		// drain must stop at the first idle tick.
		if compaz.IsSpoolFollowTick(message) {
			continue
		}
		_, cmd := model.Update(message)
		if cmd != nil {
			queue = append(queue, cmd)
		}
	}
}

var namedKeys = map[string]rune{
	"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab,
	"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
	"pgup": tea.KeyPgUp, "pgdn": tea.KeyPgDown,
}

func splitSteps(script string) []string {
	var steps []string
	for _, step := range strings.Split(script, ";") {
		if step = strings.TrimSpace(step); step != "" {
			steps = append(steps, step)
		}
	}
	return steps
}

func stepMessages(t *testing.T, step string) []tea.Msg {
	if text, ok := strings.CutPrefix(step, "type:"); ok {
		var msgs []tea.Msg
		for _, r := range text {
			msgs = append(msgs, tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
		}
		return msgs
	}
	if code, ok := namedKeys[step]; ok {
		return []tea.Msg{tea.KeyPressMsg(tea.Key{Code: code})}
	}
	runes := []rune(step)
	if len(runes) != 1 {
		t.Fatalf("unknown step %q (named key, single character, or type:<text>)", step)
	}
	return []tea.Msg{tea.KeyPressMsg(tea.Key{Code: runes[0], Text: step})}
}

// ansiToHTML converts SGR-styled terminal text to HTML spans. It understands
// the sequences lipgloss emits (truecolor, 256-color, basic colors, bold,
// faint, italic, underline, reverse) and drops any other escape sequence.
func ansiToHTML(content, defaultFg, defaultBg string) string {
	var out strings.Builder
	var fg, bg string
	var bold, faint, italic, underline, reverse bool
	style := func() string {
		f, b := fg, bg
		if reverse {
			f, b = bg, fg
			if f == "" {
				f = defaultBg
			}
			if b == "" {
				b = defaultFg
			}
		}
		var css []string
		if f != "" {
			css = append(css, "color:"+f)
		}
		if b != "" {
			css = append(css, "background:"+b)
		}
		if bold {
			css = append(css, "font-weight:bold")
		}
		if faint {
			css = append(css, "opacity:.55")
		}
		if italic {
			css = append(css, "font-style:italic")
		}
		if underline {
			css = append(css, "text-decoration:underline")
		}
		return strings.Join(css, ";")
	}
	open := ""
	emit := func(text string) {
		if text == "" {
			return
		}
		if current := style(); current != open {
			if open != "" {
				out.WriteString("</span>")
			}
			if current != "" {
				fmt.Fprintf(&out, "<span style=\"%s\">", current)
			}
			open = current
		}
		out.WriteString(html.EscapeString(text))
	}
	for i := 0; i < len(content); {
		esc := strings.IndexByte(content[i:], 0x1b)
		if esc < 0 {
			emit(content[i:])
			break
		}
		emit(content[i : i+esc])
		i += esc
		rest := content[i:]
		switch {
		case strings.HasPrefix(rest, "\x1b["): // CSI: apply SGR, drop the rest
			end := strings.IndexFunc(rest[2:], func(r rune) bool { return r >= 0x40 && r <= 0x7e })
			if end < 0 {
				i = len(content)
				break
			}
			if rest[2+end] == 'm' {
				params := parseSGR(rest[2 : 2+end])
				for j := 0; j < len(params); j++ {
					switch p := params[j]; {
					case p == 0:
						fg, bg = "", ""
						bold, faint, italic, underline, reverse = false, false, false, false, false
					case p == 1:
						bold = true
					case p == 2:
						faint = true
					case p == 3:
						italic = true
					case p == 4:
						underline = true
					case p == 7:
						reverse = true
					case p == 22:
						bold, faint = false, false
					case p == 23:
						italic = false
					case p == 24:
						underline = false
					case p == 27:
						reverse = false
					case p == 39:
						fg = ""
					case p == 49:
						bg = ""
					case p >= 30 && p <= 37:
						fg = basicColor(p - 30)
					case p >= 90 && p <= 97:
						fg = basicColor(p - 90 + 8)
					case p >= 40 && p <= 47:
						bg = basicColor(p - 40)
					case p >= 100 && p <= 107:
						bg = basicColor(p - 100 + 8)
					case p == 38 || p == 48:
						value, used := extendedColor(params[j+1:])
						if used == 0 {
							j = len(params)
							break
						}
						if p == 38 {
							fg = value
						} else {
							bg = value
						}
						j += used
					}
				}
			}
			i += 2 + end + 1
		case strings.HasPrefix(rest, "\x1b]"): // OSC: drop to BEL or ST
			end := strings.IndexAny(rest, "\x07")
			if st := strings.Index(rest, "\x1b\\"); st >= 0 && (end < 0 || st < end) {
				end = st + 1
			}
			if end < 0 {
				i = len(content)
				break
			}
			i += end + 1
		default: // lone escape
			i++
		}
	}
	if open != "" {
		out.WriteString("</span>")
	}
	return out.String()
}

func parseSGR(raw string) []int {
	if raw == "" {
		return []int{0}
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == ':' })
	params := make([]int, 0, len(fields))
	for _, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil {
			return nil
		}
		params = append(params, value)
	}
	return params
}

// extendedColor decodes the tail of a 38/48 SGR (";2;r;g;b" or ";5;n"),
// returning the CSS color and how many params it consumed (0 if malformed).
func extendedColor(params []int) (string, int) {
	switch {
	case len(params) >= 4 && params[0] == 2:
		return fmt.Sprintf("#%02x%02x%02x", params[1]&0xff, params[2]&0xff, params[3]&0xff), 4
	case len(params) >= 2 && params[0] == 5:
		return xterm256(params[1] & 0xff), 2
	}
	return "", 0
}

func basicColor(index int) string {
	return xterm256(index)
}

func xterm256(n int) string {
	base := []string{
		"#000000", "#cd3131", "#0dbc79", "#e5e510", "#2472c8", "#bc3fbc", "#11a8cd", "#e5e5e5",
		"#666666", "#f14c4c", "#23d18b", "#f5f543", "#3b8eea", "#d670d6", "#29b8db", "#ffffff",
	}
	switch {
	case n < 16:
		return base[n]
	case n < 232:
		n -= 16
		levels := []int{0, 95, 135, 175, 215, 255}
		return fmt.Sprintf("#%02x%02x%02x", levels[n/36], levels[n/6%6], levels[n%6])
	default:
		v := 8 + (n-232)*10
		return fmt.Sprintf("#%02x%02x%02x", v, v, v)
	}
}

func hexColor(c color.Color) string {
	if c == nil {
		return "#000000"
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

func sanitize(label string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '_'
	}, label)
}
