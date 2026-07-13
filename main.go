// cq — jq for COBOL. Parses a COBOL copybook, prints its resolved field
// layout, and converts between fixed-length COBOL records and UTF-8 JSON.
//
//	cq -c CUSTOMER.cpy                             # layout as JSON
//	cq -c CUSTOMER.cpy -d customer.bin | jq '.[0]' # decode records
//	cq -c CUSTOMER.cpy -j records.json > customer.bin
//	cq --copybook-dsn "HQ.CPY(CUSTOMER)" --data-dsn "HQ.CUST"
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/itchyny/gojq"

	"github.com/Tannex/cq/internal/copybook"
	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/layout"
	"github.com/Tannex/cq/internal/query"
	"github.com/Tannex/cq/internal/record"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "cq:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "config" {
		if len(os.Args) != 2 {
			return errors.New("usage: cq config")
		}
		return editConfig()
	}

	fs := flag.NewFlagSet("cq", flag.ExitOnError)
	copybookPath := fs.String("c", "", "local copybook file")
	copybookDSN := fs.String("copybook-dsn", "", "copybook data set or member to fetch through Zowe CLI")
	dataPath := fs.String("d", "", "data file to decode (use - for stdin; omit for layout output)")
	dataDSN := fs.String("data-dsn", "", "data set to download in binary mode through Zowe CLI")
	jsonPath := fs.String("j", "", "JSON object or array to encode as binary records (use - for stdin)")
	codepage := fs.String("codepage", "cp037", "EBCDIC codepage of the data (cp037, cp277, cp1047, cp1140, cp1142; ascii/latin1 for testing)")
	format := fs.String("format", "auto", "copybook source format: auto, fixed (cols 7-72), or free")
	recName := fs.String("record", "", "01-level record to use when the copybook has several (default: first)")
	pretty := fs.Bool("pretty", false, "indent JSON output")
	fillers := fs.Bool("fillers", false, "include FILLER fields in decoded output")
	maxRecs := fs.Int("max", 0, "decode at most this many records (0 = all)")
	lrecl := fs.Int("lrecl", 0, "physical record length when it exceeds the layout (extra bytes are padding)")
	expr := fs.String("q", "", "jq expression: run per record when decoding (output becomes a result stream, not an array), or against the layout document")
	rawOut := fs.Bool("r", false, "with -q, print string results raw instead of JSON-quoted")
	var wheres []string
	fs.Func("where", "keep only records satisfying this level-88 `condition`; prefix with ! or \"not \" to negate; repeat to AND", func(s string) error {
		wheres = append(wheres, s)
		return nil
	})
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `cq — jq for COBOL copybooks and EBCDIC data

usage: cq [flags] (-c COPYBOOK | --copybook-dsn DSN[(MEMBER)])
                  [-d DATA | --data-dsn DSN | -j JSON | DATA]
       cq config

With only a copybook source, prints the record layout as JSON. A binary data
source decodes records into a UTF-8 JSON array. -j performs the reverse and
writes binary records to stdout. DSN sources use the installed Zowe CLI.

The config command creates the user configuration file when needed and opens
it with $VISUAL, $EDITOR, or the platform text editor.

flags:
`)
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), `
examples:
  cq -c CUSTOMER.cpy
  cq -c CUSTOMER.cpy -d customer.bin | jq '.[] | .CUST-NAME'
  cq -c CUSTOMER.cpy -d customer.bin | cq -c CUSTOMER.cpy -j - > rebuilt.bin
  cq -c CUSTOMER.cpy customer.bin
  cq --copybook-dsn "HQ.COPYLIB(CUSTOMER)" --data-dsn "HQ.CUSTOMER.DATA"
  cq -q 'select(.BALANCE < 0)' -c CUSTOMER.cpy -d customer.bin
  cq -r -q '.["CUST-NAME"]' -c CUSTOMER.cpy -d customer.bin
  cq -where DTAR107-SALE -where 'not DTAR107-VOID' -c DTAR107.cbl -d sales.bin
  zowe zos-files download data-set "HQ.CUSTOMER.DATA" --binary --file customer.bin
  cq -c CUSTOMER.cpy -d customer.bin
`)
	}
	fs.Parse(os.Args[1:])
	setFlags := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

	if (*copybookPath == "") == (*copybookDSN == "") {
		return errors.New("provide exactly one copybook source: -c COPYBOOK or --copybook-dsn DSN[(MEMBER)]")
	}
	switch fs.NArg() {
	case 0:
	case 1:
		if *dataPath != "" || *dataDSN != "" {
			return errors.New("provide only one data source: -d DATA, --data-dsn DSN, or positional DATA")
		}
		*dataPath = fs.Arg(0)
	default:
		return fmt.Errorf("expected at most one positional DATA argument, got %q", fs.Args())
	}
	if *dataPath != "" && *dataDSN != "" {
		return errors.New("provide only one data source: -d DATA, --data-dsn DSN, or positional DATA")
	}
	if *jsonPath != "" && (*dataPath != "" || *dataDSN != "") {
		return errors.New("-j cannot be combined with -d, --data-dsn, or positional DATA")
	}
	if *jsonPath != "" {
		for _, name := range []string{"q", "r", "pretty", "fillers", "max", "where"} {
			if setFlags[name] {
				return fmt.Errorf("-%s cannot be combined with -j", name)
			}
		}
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	var cbFormat copybook.Format
	switch *format {
	case "auto":
		cbFormat = copybook.FormatAuto
	case "fixed":
		cbFormat = copybook.FormatFixed
	case "free":
		cbFormat = copybook.FormatFree
	default:
		return fmt.Errorf("unknown -format %q (want auto, fixed, or free)", *format)
	}

	var q *query.Query
	if *expr != "" {
		var err error
		if q, err = query.Compile(*expr); err != nil {
			return err
		}
	}

	var src []byte
	if *copybookDSN != "" {
		src, err = fetchZoweCopybook(*copybookDSN)
	} else {
		src, err = os.ReadFile(*copybookPath)
	}
	if err != nil {
		return err
	}
	resolver := newDSNCopyResolver(cfg.DSNSearchPath)
	items, err := copybook.ParseWithCopies(string(src), cbFormat, resolver.Resolve)
	if err != nil {
		return err
	}
	recs, err := layout.Build(items)
	if err != nil {
		return err
	}

	if *dataPath == "" && *dataDSN == "" && *jsonPath == "" {
		if len(wheres) > 0 {
			return fmt.Errorf("-where filters records, so it needs a data source to decode")
		}
		if q == nil {
			return printLayout(os.Stdout, recs, *pretty)
		}
		return queryLayout(os.Stdout, recs, q, *rawOut, *pretty)
	}

	rec, err := pickRecord(recs, *recName)
	if err != nil {
		return err
	}
	cm, err := decode.Codepage(*codepage)
	if err != nil {
		return err
	}
	if *lrecl > 0 && *lrecl < rec.MaxLength {
		return fmt.Errorf("-lrecl %d is shorter than the %s layout (%d bytes)", *lrecl, rec.Name, rec.MaxLength)
	}
	if *lrecl > 0 && rec.Variable() {
		return fmt.Errorf("-lrecl cannot be combined with an OCCURS DEPENDING ON record")
	}
	if *jsonPath != "" {
		e, err := record.NewEncoder(rec, cm)
		if err != nil {
			return err
		}
		e.Lrecl = *lrecl
		var in io.Reader
		if *jsonPath == "-" {
			in = os.Stdin
		} else {
			f, err := os.Open(*jsonPath)
			if err != nil {
				return err
			}
			defer f.Close()
			in = f
		}
		return record.EncodeJSON(os.Stdout, e, in)
	}
	d, err := record.NewDecoder(rec, cm)
	if err != nil {
		return err
	}
	d.IncludeFillers = *fillers
	d.Lrecl = *lrecl
	for _, w := range wheres {
		if err := d.AddWhere(w); err != nil {
			return err
		}
	}

	if *dataDSN != "" {
		data, err := downloadZoweDataSet(*dataDSN)
		if err != nil {
			return err
		}
		decodeErr := decodeAll(os.Stdout, d, data, *pretty, *maxRecs, q, *rawOut)
		return errors.Join(decodeErr, data.Close())
	}

	var in io.Reader
	if *dataPath == "-" {
		in = os.Stdin
	} else {
		f, err := os.Open(*dataPath)
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}
	return decodeAll(os.Stdout, d, in, *pretty, *maxRecs, q, *rawOut)
}

func pickRecord(recs []*layout.Record, name string) (*layout.Record, error) {
	if name == "" {
		return recs[0], nil
	}
	for _, r := range recs {
		if r.Name == name {
			return r, nil
		}
	}
	var names []string
	for _, r := range recs {
		names = append(names, r.Name)
	}
	return nil, fmt.Errorf("no record named %q in copybook (have %v)", name, names)
}

// layoutDoc shapes the layout JSON for one record.
type layoutDoc struct {
	Record    string          `json:"record"`
	Length    int             `json:"length"`
	MinLength int             `json:"minLength,omitempty"` // only when variable
	Fields    []*layout.Field `json:"fields"`
}

func printLayout(w io.Writer, recs []*layout.Record, pretty bool) error {
	docs := make([]layoutDoc, 0, len(recs))
	for _, r := range recs {
		d := layoutDoc{Record: r.Name, Length: r.MaxLength, Fields: r.Children}
		if r.Variable() {
			d.MinLength = r.MinLength
		}
		docs = append(docs, d)
	}
	enc := json.NewEncoder(w)
	if pretty {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(docs)
}

func decodeAll(w io.Writer, d *record.Decoder, in io.Reader, pretty bool, max int, q *query.Query, rawOut bool) error {
	emit := emitter(w, rawOut, pretty)
	if q == nil {
		if _, err := io.WriteString(w, "["); err != nil {
			return err
		}
	}
	n := 0
	for max == 0 || n < max {
		raw, err := d.Next(in)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("record %d: %w", n+1, err)
		}
		if ok, err := d.Matches(raw); err != nil {
			return fmt.Errorf("record %d: %w", n+1, err)
		} else if !ok {
			continue
		}
		js, err := d.Decode(raw)
		if err != nil {
			return fmt.Errorf("record %d: %w", n+1, err)
		}
		n++
		if q != nil {
			v, err := query.FromJSON(js)
			if err != nil {
				return err
			}
			if err := q.Run(v, emit); err != nil {
				if halted(err) {
					return haltErr(err)
				}
				return fmt.Errorf("record %d: %w", n, err)
			}
			continue
		}
		if pretty {
			var buf bytes.Buffer
			if err := json.Indent(&buf, js, "", "  "); err != nil {
				return err
			}
			js = buf.Bytes()
		}
		sep := ",\n"
		if n == 1 {
			sep = "\n"
		}
		if _, err := fmt.Fprintf(w, "%s%s", sep, js); err != nil {
			return err
		}
	}
	if q != nil {
		return nil
	}
	_, err := io.WriteString(w, "\n]\n")
	return err
}

// queryLayout runs the jq expression against the layout document (the same
// array printLayout writes).
func queryLayout(w io.Writer, recs []*layout.Record, q *query.Query, rawOut, pretty bool) error {
	var buf bytes.Buffer
	if err := printLayout(&buf, recs, false); err != nil {
		return err
	}
	v, err := query.FromJSON(buf.Bytes())
	if err != nil {
		return err
	}
	if err := q.Run(v, emitter(w, rawOut, pretty)); err != nil {
		if halted(err) {
			return haltErr(err)
		}
		return err
	}
	return nil
}

// emitter prints one query result per line, jq-style.
func emitter(w io.Writer, rawOut, pretty bool) func(any) error {
	return func(v any) error {
		if s, ok := v.(string); ok && rawOut {
			_, err := fmt.Fprintln(w, s)
			return err
		}
		b, err := gojq.Marshal(v)
		if err != nil {
			return err
		}
		if pretty {
			var buf bytes.Buffer
			if err := json.Indent(&buf, b, "", "  "); err == nil {
				b = buf.Bytes()
			}
		}
		_, err = fmt.Fprintf(w, "%s\n", b)
		return err
	}
}

// halted reports whether err is a halt/halt_error from the expression.
func halted(err error) bool {
	var h *query.Halt
	return errors.As(err, &h)
}

// haltErr maps a halt to the process outcome: plain halt ends cleanly,
// halt_error(msg) surfaces the message.
func haltErr(err error) error {
	var h *query.Halt
	errors.As(err, &h)
	if h.Value == nil && h.ExitCode == 0 {
		return nil
	}
	return h
}
