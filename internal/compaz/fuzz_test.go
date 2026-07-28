package compaz

import (
	"context"
	"strings"
	"testing"

	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/zosmf"
)

// FuzzSpoolContentRendering pushes hostile spool text — ANSI escapes,
// control bytes, invalid UTF-8, very long lines — through the whole spool
// pipeline: paged viewer, bulk download, include filter, find, follow view
// with the fresh-line fade, and the exit re-anchor. Every state must render;
// nothing may panic.
func FuzzSpoolContentRendering(f *testing.F) {
	f.Add("plain line\nsecond line", "ERROR")
	f.Add("\x1b[31mred\x1b[0m\nline with \x00 NUL and \x07 bell", "red")
	f.Add("\xff\xfe invalid \x80 utf8", "\xff")
	f.Add(strings.Repeat("wide ", 4000), "wide")
	f.Add("tab\tand\rcarriage\x1b]0;title\x07", "")
	f.Add("", "pattern")

	f.Fuzz(func(t *testing.T, text, pattern string) {
		lines := strings.Split(text, "\n")
		content := lines
		model := spoolFollowModel(t, &content)
		_ = model.mainView()

		runSpoolCommand(t, model, "incl "+pattern)
		_ = model.mainView()
		runSpoolCommand(t, model, "f "+pattern)
		_ = model.mainView()
		runSpoolCommand(t, model, "incl")

		if runSpoolFollowCommand(t, model, "follow") {
			_ = model.mainView()
			content = append(content, lines...)
			spoolFollowTick(t, model)
			_ = model.mainView()
			executeCommand(t, model, model.handleAction(actionUp))
			_ = model.mainView()
		}
	})
}

// FuzzRecordContentRendering pushes hostile data set bytes through the
// records viewer: EBCDIC display conversion, syntax detection and
// highlighting, widest-line tracking, and horizontal panning. Every frame
// must render; nothing may panic.
func FuzzRecordContentRendering(f *testing.F) {
	f.Add([]byte("HELLO WORLD"))
	f.Add([]byte{0x00, 0xFF, 0x1B, '[', '3', '1', 'm'})
	f.Add([]byte(strings.Repeat("\xEE", 8000)))
	// The model decodes with cp037, so the JCL/COBOL seeds that feed the
	// syntax detector and highlighter must be EBCDIC bytes — ASCII shapes
	// decode to control-rune soup and never reach highlighting.
	cm, err := decode.Codepage("cp037")
	if err != nil {
		f.Fatal(err)
	}
	for _, source := range []string{
		"//JOB1    JOB (ACCT),'NAME'",
		"       IDENTIFICATION DIVISION.",
	} {
		encoded, err := decode.EncodeString(source, 80, cm)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(encoded)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		half := len(data) / 2
		browser := &fakeBrowser{
			listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
				return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "IBMUSER.SEQ", Organization: "PS"}}}, nil
			},
			readRecords: func(_ context.Context, request zosmf.ReadRecordsRequest) (zosmf.RecordPage, error) {
				return zosmf.RecordPage{Records: []zosmf.Record{
					{Number: 1, Data: data},
					{Number: 2, Data: data[:half]},
					{Number: 3, Data: data[half:]},
				}, Start: request.Start}, nil
			},
		}
		// cp037 exercises the EBCDIC display path with arbitrary bytes.
		model := readyModel(t, Options{Prefix: "IBMUSER.*", Codepage: "cp037"}, browser, "IBMUSER", "", 100, 15)
		executeCommand(t, model, model.openSelection())
		_ = model.mainView()
		executeCommand(t, model, model.handleAction(actionWideRight))
		_ = model.mainView()
		executeCommand(t, model, model.handleAction(actionDown))
		_ = model.mainView()
	})
}
