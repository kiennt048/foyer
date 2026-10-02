package main

import (
	"fmt"
	"github.com/kiennt048/foyer/internal/config"
	"os"
	"sort"
)

// Command is one foyerctl subcommand.
//
// Commands register themselves from their own file's init(), so adding one
// never edits a shared file. That is deliberate: several agents add commands
// concurrently, and a shared switch statement is the one place they would
// collide.
type Command struct {
	Name    string
	Summary string
	// Usage is the one-line argument form shown in help, without the command name.
	Usage string
	Run   func(args []string) error
}

var commands = map[string]*Command{}

// Register adds a command to the registry. It panics on a duplicate name,
// because two commands answering to one name is a build-time mistake, not a
// runtime condition to recover from.
func Register(c *Command) {
	if _, dup := commands[c.Name]; dup {
		panic("foyerctl: duplicate command registration: " + c.Name)
	}
	commands[c.Name] = c
}

func dispatch(argv []string) int {
	if len(argv) < 2 || argv[1] == "help" || argv[1] == "-h" || argv[1] == "--help" {
		printUsage()
		if len(argv) < 2 {
			return 1
		}
		return 0
	}

	cmd, ok := commands[argv[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "foyerctl: unknown command %q\n\n", argv[1])
		printUsage()
		return 1
	}

	if err := cmd.Run(argv[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "foyerctl %s: %v\n", cmd.Name, err)
		return 1
	}
	return 0
}

func printUsage() {
	fmt.Print("foyerctl - Foyer control plane management CLI\n\nUsage:\n  foyerctl <command> [options]\n\nCommands:\n")

	names := make([]string, 0, len(commands))
	width := 0
	for name := range commands {
		names = append(names, name)
		if n := len(name) + len(commands[name].Usage) + 1; n > width {
			width = n
		}
	}
	sort.Strings(names)

	for _, name := range names {
		c := commands[name]
		form := name
		if c.Usage != "" {
			form += " " + c.Usage
		}
		fmt.Printf("  %-*s  %s\n", width, form, c.Summary)
	}
}

// envOrFileDefault resolves a flag default from NAME or NAME_FILE.
//
// `deploy/compose.yaml` passes secrets as NAME_FILE (for example
// FOYER_DATABASE_URL_FILE=/run/secrets/database_url). `internal/config`
// implements the convention and `cmd/foyer` uses it; a foyerctl command reading
// os.Getenv directly cannot see it — the migrate one-shot dies with "--db or
// FOYER_DATABASE_URL is required" and the whole stack is unstartable from its own
// shipped compose file.
//
// The error is returned rather than swallowed: "both set" and "file unreadable"
// are conditions the operator has to be told about, and collapsing them into an
// empty string produces "is required", which is a lie about a value that is
// present.
func envOrFileDefault(name string) (string, error) {
	v, err := config.EnvOrFile(name)
	if err != nil {
		return "", err
	}
	return v, nil
}
