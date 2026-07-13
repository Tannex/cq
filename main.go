// cq — jq for COBOL. Parses a COBOL copybook and either prints the field
// layout (offset/length per field) or decodes fixed-length EBCDIC records
// (e.g. a binary dataset downloaded with Zowe CLI) into a UTF-8 JSON array.
//
//	cq CUSTOMER.cpy                          # layout as JSON
//	cq CUSTOMER.cpy customer.bin | jq '.[0]' # decode records
//	zowe files download ds "HQ.CUST" --binary --file - | cq CUSTOMER.cpy -
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Tannex/cq/internal/copybook"
	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/layout"
	"github.com/Tannex/cq/internal/record"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "cq:", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("cq", flag.ExitOnError)
	codepage := fs.String("codepage", "cp037", "EBCDIC codepage of the data (cp037, cp1047, cp1140; ascii/latin1 for testing)")
	format := fs.String("format", "auto", "copybook source format: auto, fixed (cols 7-72), or free")
	recName := fs.String("record", "", "01-level record to decode when the copybook has several (default: first)")
	pretty := fs.Bool("pretty", false, "indent JSON output")
	fillers := fs.Bool("fillers", false, "include FILLER fields in decoded output")
	maxRecs := fs.Int("max", 0, "decode at most this many records (0 = all)")
	lrecl := fs.Int("lrecl", 0, "physical record length when it exceeds the layout (extra bytes are padding)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `cq — jq for COBOL copybooks and EBCDIC data

usage: cq [flags] COPYBOOK [DATA]

With only a COPYBOOK, prints the record layout (byte offset and length of
every field) as JSON. With DATA (a file, or "-" for stdin), decodes the
fixed-length binary records into a UTF-8 JSON array.

flags:
`)
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), `
examples:
  cq CUSTOMER.cpy
  cq CUSTOMER.cpy customer.bin | jq '.[] | .CUST-NAME'
  zowe zos-files download ds "HQ.CUSTOMER.DATA" --binary --file - | cq CUSTOMER.cpy -
`)
	}
	fs.Parse(os.Args[1:])

	if fs.NArg() < 1 || fs.NArg() > 2 {
		fs.Usage()
		os.Exit(2)
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

	src, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	items, err := copybook.Parse(string(src), cbFormat)
	if err != nil {
		return err
	}
	recs, err := layout.Build(items)
	if err != nil {
		return err
	}

	if fs.NArg() == 1 {
		return printLayout(os.Stdout, recs, *pretty)
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
	d, err := record.NewDecoder(rec, cm)
	if err != nil {
		return err
	}
	d.IncludeFillers = *fillers
	d.Lrecl = *lrecl

	var in io.Reader
	if name := fs.Arg(1); name == "-" {
		in = os.Stdin
	} else {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}
	return decodeAll(os.Stdout, d, in, *pretty, *maxRecs)
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

func decodeAll(w io.Writer, d *record.Decoder, in io.Reader, pretty bool, max int) error {
	out := []byte("[")
	if _, err := w.Write(out); err != nil {
		return err
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
		js, err := d.Decode(raw)
		if err != nil {
			return fmt.Errorf("record %d: %w", n+1, err)
		}
		if pretty {
			var buf bytes.Buffer
			if err := json.Indent(&buf, js, "", "  "); err != nil {
				return err
			}
			js = buf.Bytes()
		}
		sep := ",\n"
		if n == 0 {
			sep = "\n"
		}
		if _, err := fmt.Fprintf(w, "%s%s", sep, js); err != nil {
			return err
		}
		n++
	}
	_, err := io.WriteString(w, "\n]\n")
	return err
}
