package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/store"
)

func init() {
	Register(&Command{
		Name:    "create-operator",
		Usage:   "--username <user> [--password-stdin | --password <pw>]",
		Summary: "Create an operator with an Argon2id password",
		Run:     runCreateOperator,
	})
}

func runCreateOperator(args []string) error {
	return createOperator(args, os.Stdin, os.Stdout, os.Stderr)
}

// createOperator takes its streams as arguments so that the password path can be
// driven by a test. It is the whole reason this is not `runCreateOperator`: the
// thing worth testing here is where the password came from, and that is
// unobservable when the only source is the process's own standard input.
//
// ── why --password-stdin exists ──────────────────────────────────────────────
//
// `--password` is an argv, readable by `ps`, by `/proc/<pid>/cmdline`, and by
// anything that reads a shell's history. Feeding the whole command line on
// standard input keeps the password off the *host* — but running `sh -s` inside
// the container then puts it straight back into an argv one process down. That
// workaround does real work and hides nothing.
//
// The flag is docker's own spelling, so nobody has to learn a third convention.
func createOperator(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("create-operator", flag.ExitOnError)
	fs.SetOutput(stderr)
	username := fs.String("username", "", "Operator username")
	password := fs.String("password", "", "Operator password (appears in this process's argv; prefer --password-stdin)")
	passwordStdin := fs.Bool("password-stdin", false, "Read the password from standard input")
	role := fs.String("role", "administrator", "Role (administrator or staff)")
	// The only writer of `operators.must_change_password`. There is no API
	// operation that sets it, deliberately -- see migration 00017.
	mustChange := fs.Bool("must-change-password", false,
		"Mark this password as temporary: the API refuses every operation but signing out and changing it until the operator chooses their own.")
	dbDefault, err := envOrFileDefault("FOYER_DATABASE_URL")
	if err != nil {
		return err
	}
	dbURL := fs.String("db", dbDefault, "PostgreSQL database URL")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *username == "" {
		return errors.New("--username is required")
	}

	pw, err := operatorPassword(*password, *passwordStdin, stdin, stderr)
	if err != nil {
		return err
	}

	hash, err := identity.HashPassword(pw)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	if *dbURL == "" {
		fmt.Fprintf(stdout, "Generated Argon2id hash: %s\n", hash)
		return nil
	}

	ctx := context.Background()
	st, err := store.NewStore(ctx, *dbURL)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer st.Close()

	op := &store.Operator{
		ID:           *username,
		Username:     *username,
		PasswordHash: hash,
		Role:         *role,
		Enabled:      true,

		// Set here and nowhere else: a password handed to somebody from the host
		// is temporary. (The first administrator comes from the console's setup,
		// `open-setup`, never from a shipped password.) An operator created by an
		// administrator in the console gets a password that administrator chose,
		// and demanding they change it would be password-expiry policy wearing this
		// flag's clothes.
		MustChangePassword: *mustChange,
	}
	if err := st.CreateOperator(ctx, op); err != nil {
		return fmt.Errorf("saving operator: %w", err)
	}
	if *mustChange {
		fmt.Fprintf(stdout, "Created operator %q with role %q; it must change this password before the console will answer anything else\n", *username, *role)
		return nil
	}
	fmt.Fprintf(stdout, "Created operator %q with role %q\n", *username, *role)
	return nil
}

// operatorPassword resolves where the password came from.
//
// No message here ever contains the password: these lines reach terminals,
// journals and support bundles.
func operatorPassword(flagValue string, fromStdin bool, stdin io.Reader, stderr io.Writer) (string, error) {
	switch {
	case flagValue != "" && fromStdin:
		// Not a precedence rule. Preferring one silently is how an operator ends
		// up with a password they did not choose and cannot work out.
		return "", errors.New("--password and --password-stdin are both set; one of them would be ignored and there is no safe way to guess which")

	case fromStdin:
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("--password-stdin: reading standard input: %w", err)
		}
		// Only the trailing newline. `printf '%s\n' | …` is how every caller
		// writes this, and a password carrying an invisible newline is one
		// nobody can type at the login form afterwards. Trimming more would
		// silently alter a generated password that ends in a space.
		pw := strings.TrimRight(string(b), "\r\n")
		if pw == "" {
			// A pipe that closed because the thing feeding it failed looks
			// exactly like one that fed nothing, so this is a refusal rather
			// than an operator with an empty password.
			return "", errors.New("--password-stdin was given and standard input was empty")
		}
		return pw, nil

	case flagValue != "":
		for _, line := range []string{
			"warning: --password puts the password in this process's argv, where ps,",
			"         /proc/<pid>/cmdline and shell history can read it. Prefer piping it:",
			"           foyerctl create-operator --username admin --password-stdin < a-file",
		} {
			fmt.Fprintln(stderr, line)
		}
		return flagValue, nil
	}
	return "", errors.New("a password is required: --password-stdin, or --password")
}
