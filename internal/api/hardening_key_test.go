package api

import (
	"strings"
	"testing"
)

// TestAnAccountBucketKeyDoesNotGrowWithTheUsername: the limiter bounds how many
// keys it holds, not how large they are, and the username is caller-typed text
// with no `maxLength`. A key that was the username itself would make each bucket
// as large as the body that carried it — 16 KiB on the portal, a megabyte on the
// console — for its whole refill period.
func TestAnAccountBucketKeyDoesNotGrowWithTheUsername(t *testing.T) {
	huge := strings.Repeat("u", 1<<20)
	if n := len(accountKey(huge)); n > 64 {
		t.Fatalf("a 1 MiB username made a %d-byte bucket key; it must not depend on the input's length", n)
	}
	// Normalisation still applies before the digest: these are one account.
	if accountKey("  Kim.Administrator ") != accountKey("kim.administrator") {
		t.Fatal("case and surrounding space changed the account's bucket")
	}
	if accountKey("kim") == accountKey("kin") {
		t.Fatal("two accounts share a bucket")
	}
}
