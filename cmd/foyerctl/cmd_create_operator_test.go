package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/identity"
)

// A password taken as a flag is an argv.
//
// An argv is readable by `ps`, by `/proc/<pid>/cmdline`, and by anything that
// reads a shell's history. Feeding the whole command on standard input keeps the
// password off the *host* — but a container running `sh -s` puts it straight
// back into an argv one process down. That workaround does real work and hides
// nothing.
//
// `--password-stdin` is docker's own spelling, chosen so nobody has to learn a
// third convention.
func TestThePasswordCanArriveOnStandardInput(t *testing.T) {
	var out, errOut bytes.Buffer
	err := createOperator(
		[]string{"--username", "admin", "--password-stdin", "--db", ""},
		strings.NewReader("a generated password\n"), &out, &errOut)
	if err != nil {
		t.Fatalf("create-operator with a password on stdin: %v", err)
	}

	hash := strings.TrimSpace(strings.TrimPrefix(out.String(), "Generated Argon2id hash: "))
	if hash == "" {
		t.Fatalf("nothing was printed: %q", out.String())
	}
	// The trailing newline is not part of the password: `printf '%s\n' | …` is
	// how every caller will write this, and a password with an invisible newline
	// in it is one nobody can type at the login form afterwards.
	ok, err := identity.VerifyPassword("a generated password", hash)
	if err != nil || !ok {
		t.Errorf("the hash does not verify against the password without its trailing newline (ok=%v, err=%v)", ok, err)
	}
	if ok, _ := identity.VerifyPassword("a generated password\n", hash); ok {
		t.Error("the trailing newline was kept, so the operator's password contains a character they cannot type")
	}
}

// Both forms is a configuration nobody meant, and silently preferring one is how
// an operator ends up with a password they did not choose.
func TestBothPasswordFormsIsRefused(t *testing.T) {
	var out, errOut bytes.Buffer
	err := createOperator(
		[]string{"--username", "admin", "--password", "from-argv", "--password-stdin", "--db", ""},
		strings.NewReader("from-stdin\n"), &out, &errOut)
	if err == nil {
		t.Fatal("both password forms were accepted, and one of them was ignored")
	}
	for _, want := range []string{"--password", "--password-stdin"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "from-argv") || strings.Contains(err.Error(), "from-stdin") {
		t.Errorf("the refusal printed the password: %v", err)
	}
}

// The flag still works, because breaking it would break every script that has it
// — but it says what it costs, on stderr, where somebody running it interactively
// will see it.
func TestThePasswordFlagStillWorksAndSaysWhatItCosts(t *testing.T) {
	var out, errOut bytes.Buffer
	err := createOperator(
		[]string{"--username", "admin", "--password", "typed-on-the-command-line", "--db", ""},
		strings.NewReader(""), &out, &errOut)
	if err != nil {
		t.Fatalf("create-operator with --password: %v", err)
	}
	if !strings.Contains(errOut.String(), "--password-stdin") {
		t.Errorf("nothing warned that the password is in this process's argv: %q", errOut.String())
	}
	if strings.Contains(errOut.String(), "typed-on-the-command-line") {
		t.Errorf("the warning printed the password: %q", errOut.String())
	}
}

// An empty stdin is a refusal rather than an empty password. A pipe that closed
// because the thing feeding it failed looks identical to one that fed nothing.
func TestAnEmptyStandardInputIsRefused(t *testing.T) {
	var out, errOut bytes.Buffer
	err := createOperator(
		[]string{"--username", "admin", "--password-stdin", "--db", ""},
		strings.NewReader(""), &out, &errOut)
	if err == nil {
		t.Fatal("an empty stdin produced an operator with an empty password")
	}
	if !strings.Contains(err.Error(), "--password-stdin") {
		t.Errorf("the refusal does not name the flag: %v", err)
	}
}
