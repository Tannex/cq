// compaz is Compa/z, the z/OSMF data set browser for cq (formerly cqt).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/appconfig"
	"github.com/Tannex/cq/internal/browserstate"
	"github.com/Tannex/cq/internal/buildinfo"
	"github.com/Tannex/cq/internal/compaz"
	"github.com/Tannex/cq/internal/dsnmap"
	"github.com/Tannex/cq/internal/events"
	"github.com/Tannex/cq/internal/favorites"
	"github.com/Tannex/cq/internal/zosmf"
	"github.com/Tannex/cq/internal/zowe"
)

var (
	loadDefaultSession = zowe.LoadDefault
	loadNamedSession   = zowe.LoadNamedDefault
	listZoweProfiles   = zowe.ListDefault
	newPrefixStore     = browserstate.DefaultStore
)

type sessionLoadResult struct {
	session zowe.Session
	err     error
}

var runProgram = func(model *compaz.Model) error {
	_, err := tea.NewProgram(model).Run()
	return err
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "compaz:", err)
		os.Exit(1)
	}
}

// loadSession resolves credentials and encoding from the selected Zowe profile.
func loadSession(ctx context.Context, profile string) (compaz.Session, error) {
	if err := ctx.Err(); err != nil {
		return compaz.Session{}, err
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
		return compaz.Session{}, ctx.Err()
	case result := <-results:
		if err := ctx.Err(); err != nil {
			return compaz.Session{}, err
		}
		if result.err != nil {
			return compaz.Session{}, result.err
		}
		session := result.session
		return compaz.Session{
			Browser: zosmf.New(session, nil), User: session.User, Encoding: session.Encoding,
			PrefixScope: prefixScope(session),
		}, nil
	}
}

// prefixScope includes the resolved endpoint and user so identically named
// profiles in different projects cannot share a remembered prefix. Credentials
// are deliberately excluded; password/token rotation keeps the same scope.
func prefixScope(session zowe.Session) string {
	key, _ := json.Marshal([]any{session.Profile, session.Protocol, session.Host, session.Port, session.BasePath, session.User})
	return string(key)
}

func listProfiles(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return listZoweProfiles()
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("compaz", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var showVersion bool
	var demoMode bool
	fs.BoolVar(&demoMode, "demo", false, "run with offline fake data")
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Compa/z — z/OSMF data set browser (formerly cqt)")
		fmt.Fprintln(fs.Output(), "")
		fmt.Fprintln(fs.Output(), "usage: compaz [--demo]")
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
		_, err := fmt.Fprintf(stdout, "compaz %s\n", buildinfo.Reported())
		return err
	}

	config, err := appconfig.DefaultLoader(nil).Load()
	if err != nil {
		return err
	}
	deps := compaz.Dependencies{
		DSNSearchPath: config.DSNSearchPath,
		LoadSession: func(ctx context.Context, profile string) (compaz.Session, error) {
			return loadSession(ctx, profile)
		},
		ListProfiles: listProfiles,
		Mappings:     dsnmap.DefaultStore(nil),
		Favorites:    favorites.DefaultStore(nil),
		Events:       events.DefaultStore(nil),
		Prefixes:     newPrefixStore(nil),
	}
	var options compaz.Options
	if demoMode {
		deps.Prefixes = nil
		options.Prefix = "DEMO.*"
		deps.LoadSession = loadDemoSession
		deps.ListProfiles = listDemoProfiles
	}
	model, err := compaz.NewModel(options, deps)
	if err != nil {
		return err
	}
	if err := runProgram(model); err != nil && !errors.Is(err, tea.ErrInterrupted) {
		return err
	}
	return nil
}
