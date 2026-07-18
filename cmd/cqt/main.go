// cqt is the read-only z/OSMF data set browser for cq.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/appconfig"
	"github.com/Tannex/cq/internal/buildinfo"
	"github.com/Tannex/cq/internal/cqt"
	"github.com/Tannex/cq/internal/zosmf"
	"github.com/Tannex/cq/internal/zowe"
)

var loadDefaultSession = zowe.LoadDefault

type sessionLoadResult struct {
	session zowe.Session
	err     error
}

var runProgram = func(model *cqt.Model) error {
	_, err := tea.NewProgram(model).Run()
	return err
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "cqt:", err)
		os.Exit(1)
	}
}

func loadSession(ctx context.Context) (cqt.Session, error) {
	if err := ctx.Err(); err != nil {
		return cqt.Session{}, err
	}

	results := make(chan sessionLoadResult, 1)
	go func() {
		session, err := loadDefaultSession()
		results <- sessionLoadResult{session: session, err: err}
	}()

	select {
	case <-ctx.Done():
		// LoadDefault is synchronous and cannot be interrupted. Its goroutine may
		// remain blocked indefinitely, but cancellation must still release the UI.
		return cqt.Session{}, ctx.Err()
	case result := <-results:
		if err := ctx.Err(); err != nil {
			return cqt.Session{}, err
		}
		if result.err != nil {
			return cqt.Session{}, result.err
		}
		return cqt.Session{
			Browser: zosmf.New(result.session, nil), User: result.session.User, Encoding: result.session.Encoding,
		}, nil
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cqt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var options cqt.Options
	var showVersion bool
	fs.StringVar(&options.Prefix, "prefix", "", "initial data set prefix or pattern")
	fs.StringVar(&options.Copybook, "c", "", "local copybook file")
	fs.StringVar(&options.Copybook, "copybook", "", "local copybook file")
	fs.StringVar(&options.CopybookDSN, "copybook-dsn", "", "copybook data set or member to fetch from z/OSMF")
	fs.StringVar(&options.Format, "format", "auto", "copybook source format: auto, fixed, or free")
	fs.StringVar(&options.Record, "record", "", "01-level record when the copybook has several")
	fs.StringVar(&options.Codepage, "codepage", "", "EBCDIC codepage (default: Zowe profile encoding, then cp037)")
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "cqt — read-only z/OSMF data set browser")
		fmt.Fprintln(fs.Output(), "")
		fmt.Fprintln(fs.Output(), "usage: cqt [flags]")
		fmt.Fprintln(fs.Output(), "")
		fmt.Fprintln(fs.Output(), "flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}
	if showVersion {
		_, err := fmt.Fprintf(stdout, "cqt %s\n", buildinfo.Reported())
		return err
	}

	config, err := appconfig.DefaultLoader(nil).Load()
	if err != nil {
		return err
	}
	model, err := cqt.NewModel(options, cqt.Dependencies{
		DSNSearchPath: config.DSNSearchPath,
		LoadSession:   loadSession,
	})
	if err != nil {
		return err
	}
	if err := runProgram(model); err != nil && !errors.Is(err, tea.ErrInterrupted) {
		return err
	}
	return nil
}
