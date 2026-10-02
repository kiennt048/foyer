package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/store"
)

func init() {
	Register(&Command{
		Name:    "open-setup",
		Usage:   "[--db <url>]",
		Summary: "Open the console's first-access setup and print its one-time code",
		Run:     runOpenSetup,
	})
}

// open-setup is how a fresh install gets its first administrator without a
// shipped credential (`internal/api`'s handlers_setup.go has the survey).
// `deploy/first-boot.sh` runs it and prints what it prints; **an operator never runs it by hand** — the installer they
// already ran is the only command in the path.
//
// It prints the code on standard output and nothing else, so the installer can
// capture it; everything a person reads goes to standard error. The code is not
// stored anywhere but its SHA-256 in `console_setup`, so running it again prints a
// new one and retires the last — the recovery for an operator who lost the first.
//
// With an enabled administrator already in the database it opens nothing, prints
// nothing on standard output and succeeds: the installer re-run on a set-up
// install is the ordinary case, not an error.
func runOpenSetup(args []string) error {
	fs := flag.NewFlagSet("open-setup", flag.ExitOnError)
	fs.SetOutput(os.Stderr)
	dbDefault, err := envOrFileDefault("FOYER_DATABASE_URL")
	if err != nil {
		return err
	}
	dbURL := fs.String("db", dbDefault, "PostgreSQL database URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dbURL == "" {
		return errors.New("--db or FOYER_DATABASE_URL is required")
	}
	ctx := context.Background()
	st, err := store.NewStore(ctx, *dbURL)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer st.Close()
	return openSetup(os.Stdout, os.Stderr, func(digest []byte) error {
		return st.ConsoleSetup().Open(ctx, digest)
	})
}

func openSetup(stdout, stderr io.Writer, open func(digest []byte) error) error {
	code, err := identity.NewSetupCode()
	if err != nil {
		return fmt.Errorf("drawing a setup code: %w", err)
	}
	switch err := open(identity.SetupCodeDigest(code)); {
	case errors.Is(err, store.ErrSetupNotNeeded):
		fmt.Fprintln(stderr, "An administrator already exists; the console needs no setup.")
		return nil
	case err != nil:
		return fmt.Errorf("opening the setup: %w", err)
	}
	fmt.Fprintln(stdout, code)
	fmt.Fprintln(stderr, "The console's first-access setup is open. The code above creates the first administrator, once.")
	return nil
}
