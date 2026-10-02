package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/store"
)

// A shared secret is constrained where the operator is still holding it.
//
// `Create` sealed arbitrary bytes, so a paste that brought a trailing newline or a
// byte from another encoding with it was stored happily. The failure arrived much
// later, from inside `NasProjection().Sync`, as
//
//	ERROR: invalid byte sequence for encoding "UTF8": 0xff (SQLSTATE 22021)
//
// on an operation the operator did not start, naming neither the client nor the
// secret. `nas.secret` is `text` and FreeRADIUS's client definition is text, so the
// constraint is real rather than defensive.
func TestASharedSecretMustBeText(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()

	cases := map[string]struct {
		secret []byte
		says   string
	}{
		"a byte from another encoding": {[]byte{'s', 'e', 'c', 0xff, 'r', 'e', 't'}, "non-ASCII byte at byte 3"},
		"a trailing newline from a paste": {
			[]byte("shared-secret-value\n"), "control character at byte 19"},
		"a NUL, which text cannot hold at all": {[]byte("abc\x00def"), "control character at byte 3"},
		// Invisible in every UI that will ever display it, and it makes the
		// stored secret differ from the controller's by a character nobody sees.
		"a leading space": {[]byte(" shared-secret-value"), "space at byte 0"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ss := &store.SharedSecret{ID: uniq("sec"), SiteID: f.site.ID, Label: "Pasted from a controller"}
			err := f.st.SharedSecrets().Create(ctx, ss, tc.secret)
			if err == nil {
				t.Fatal("accepted, and the failure is now somebody else's to explain")
			}
			if !errors.Is(err, store.ErrSecretNotText) {
				t.Fatalf("refused with %v, which callers cannot tell from a write failure", err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal is %q and does not say %q, so the operator cannot find the byte", err, tc.says)
			}
			// Never the byte's value: this reaches logs.
			if strings.Contains(err.Error(), string(tc.secret)) {
				t.Error("the refusal echoes the secret")
			}

			// Rotate has the identical hole and takes the identical check.
			if err := f.st.SharedSecrets().Rotate(ctx, f.secret.ID, tc.secret); !errors.Is(err, store.ErrSecretNotText) {
				t.Errorf("Rotate accepted what Create refused: %v", err)
			}
		})
	}

	t.Run("a printable secret is still accepted and still projects", func(t *testing.T) {
		ss := &store.SharedSecret{ID: uniq("sec"), SiteID: f.site.ID, Label: "Generated"}
		fatalf(t, f.st.SharedSecrets().Create(ctx, ss, []byte("A1b2C3d4E5f6!~")), "creating an ordinary secret")

		c := f.client(t, "10.92.0.1/32", func(rc *store.RadiusClient) { rc.SharedSecretID = &ss.ID })
		res, err := f.st.NasProjection().Sync(ctx)
		fatalf(t, err, "syncing the projection")
		if len(res.Skipped) != 0 {
			t.Fatalf("client %s was skipped: %+v", c.ID, res.Skipped)
		}
	})
}

// The row that predates the constraint, or was written by something outside this
// package.
//
// `Create` cannot help there, so the projection has to name what it cannot project.
// Skipped rather than fatal: one corrupt row must not take every other client on the
// site off the air with it.
func TestSyncNamesTheClientWhoseSecretCannotBeProjected(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()

	// Sealed through the keyring so the row is well-formed in every way except the
	// bytes inside the envelope — which is exactly the shape of a row written
	// before Create checked.
	bad := &store.SharedSecret{ID: uniq("sec"), SiteID: f.site.ID, Label: "Written before the constraint"}
	fatalf(t, f.st.SharedSecrets().Create(ctx, bad, []byte("placeholder")), "creating the secret")
	f.reseal(t, bad.ID, []byte{'s', 'e', 'c', 0xff, 'r', 'e', 't'})

	c := f.client(t, "10.93.0.1/32", func(rc *store.RadiusClient) { rc.SharedSecretID = &bad.ID })

	res, err := f.st.NasProjection().Sync(ctx)
	if err != nil {
		t.Fatalf("one unprojectable secret failed the whole sync, taking every other client with it: %v", err)
	}
	found := false
	for _, s := range res.Skipped {
		if s.ClientID != c.ID {
			continue
		}
		found = true
		for _, want := range []string{bad.ID, "non-ASCII byte at byte 3"} {
			if !strings.Contains(s.Reason, want) {
				t.Errorf("the skip reads %q and does not name %q", s.Reason, want)
			}
		}
	}
	if !found {
		t.Fatalf("client %s was projected or dropped silently; skipped = %+v", c.ID, res.Skipped)
	}
}
