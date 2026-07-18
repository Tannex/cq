// cqt is the read-only z/OSMF data set browser for cq.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/appconfig"
	"github.com/Tannex/cq/internal/buildinfo"
	"github.com/Tannex/cq/internal/cqt"
	"github.com/Tannex/cq/internal/zosmf"
	"github.com/Tannex/cq/internal/zowe"
)

var (
	loadDefaultSession = zowe.LoadDefault
	loadNamedSession   = zowe.LoadNamedDefault
	listZoweProfiles   = zowe.ListDefault
)

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

func loadSession(ctx context.Context, profile string) (cqt.Session, error) {
	if err := ctx.Err(); err != nil {
		return cqt.Session{}, err
	}

	results := make(chan sessionLoadResult, 1)
	go func() {
		var session zowe.Session
		var err error
		if profile == "" {
			session, err = loadDefaultSession()
		} else {
			session, err = loadNamedSession(profile)
		}
		results <- sessionLoadResult{session: session, err: err}
	}()

	select {
	case <-ctx.Done():
		// Session loading is synchronous and cannot be interrupted. Its goroutine
		// may remain blocked indefinitely, but cancellation must still release
		// the UI.
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

func listProfiles(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return listZoweProfiles()
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cqt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var options cqt.Options
	var showVersion bool
	var demoMode bool
	fs.StringVar(&options.Prefix, "prefix", "", "initial data set prefix or pattern")
	fs.StringVar(&options.Copybook, "c", "", "local copybook file")
	fs.StringVar(&options.Copybook, "copybook", "", "local copybook file")
	fs.StringVar(&options.CopybookDSN, "copybook-dsn", "", "copybook data set or member to fetch from z/OSMF")
	fs.StringVar(&options.Format, "format", "auto", "copybook source format: auto, fixed, or free")
	fs.StringVar(&options.Record, "record", "", "01-level record when the copybook has several")
	fs.StringVar(&options.Codepage, "codepage", "", "EBCDIC codepage (default: Zowe profile encoding, then cp037)")
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.BoolVar(&demoMode, "demo", false, "run with offline fake data")
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
	if demoMode && strings.TrimSpace(options.Prefix) == "" {
		options.Prefix = "DEMO.*"
	}

	config, err := appconfig.DefaultLoader(nil).Load()
	if err != nil {
		return err
	}
	deps := cqt.Dependencies{
		DSNSearchPath: config.DSNSearchPath,
		LoadSession:   loadSession,
		ListProfiles:  listProfiles,
	}
	if demoMode {
		deps.LoadSession = loadDemoSession
		deps.ListProfiles = listDemoProfiles
	}
	model, err := cqt.NewModel(options, deps)
	if err != nil {
		return err
	}
	if err := runProgram(model); err != nil && !errors.Is(err, tea.ErrInterrupted) {
		return err
	}
	return nil
}
