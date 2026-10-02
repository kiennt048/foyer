package identity

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestThePolicyEngineNeverHashesAPassword holds the one rule the argon2id bound
// depends on to be safe: **the Policy API never takes a slot.**
//
// The bound turns a sign-in flood into a queue. That is correct for the console
// and the portal, and it would be an outage for `internal/aaa`: FreeRADIUS gives
// the Policy API 1–2 s per Access-Request (`docs/rules/security.md` §2), so a
// policy decision queued behind console sign-ins is every Wi-Fi login at the venue
// timing out — or, at a fail-open site, being let on without a decision at all.
// The requirement is exactly this: overload of the console or the portal must not
// stop the Policy API answering.
//
// Read from the source rather than asserted about behaviour because the failure
// is a future edit, not a present bug: `internal/aaa` hashes nothing,
// and the day somebody verifies a password there with this package, the queue
// arrives with it. A verifier the data plane checks belongs in FreeRADIUS
// (`rlm_pap` over `radius_pap_verifiers`, bcrypt — `security.md` §1), which is on
// its own threads and its own memory and never waits here.
func TestThePolicyEngineNeverHashesAPassword(t *testing.T) {
	dir := filepath.Join("..", "aaa")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	forbidden := map[string]bool{"HashPassword": true, "VerifyPassword": true}
	fset := token.NewFileSet()
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		scanned++
		// The local name this file gives the identity package, if it imports it.
		local := ""
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == "github.com/kiennt048/foyer/internal/identity" {
				local = "identity"
				if imp.Name != nil {
					local = imp.Name.Name
				}
			}
		}
		if local == "" {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == local && forbidden[sel.Sel.Name] {
				t.Errorf("%s calls identity.%s: the Policy API would wait on the argon2id bound "+
					"behind every console and portal sign-in", fset.Position(sel.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("found no Go source in internal/aaa — the guard is scanning nothing")
	}
}
