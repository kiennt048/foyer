package identity

import (
	"errors"
	"fmt"
	"regexp"

	"golang.org/x/crypto/bcrypt"
)

// The second verifier, for identities that opt in to 802.1X.
//
// `HashPassword` above is argon2id and stays the primary for every identity in
// this system. This is an ADDITIONAL verifier, stored in its own table, for the
// identities an operator explicitly enables EAP-TTLS/PAP for.
//
// # Why a second one exists at all
//
// Measured on FreeRADIUS 3.2.10 (`docs/protocol-reality.md`): `rlm_pap`
// has no argon2 scheme, and its general door — `Crypt-Password` — hands the
// string to libxcrypt, which returns its failure token `*0` for `$argon2id$`. An
// argon2id identity therefore cannot authenticate over EAP-TTLS/PAP at all, on
// the data plane Foyer actually ships. That is a fact about the tool, not about
// the protocol. The way around it chosen here is a second, crypt(3)-format
// verifier that `rlm_pap` can read, for the identities that need one.
//
// # Why bcrypt rather than yescrypt
//
// Either would serve. Only one of them is reachable:
//
//   - `go.mod` is frozen, `golang.org/x/crypto` is already a direct dependency
//     and carries bcrypt, and it carries no yescrypt.
//   - The control plane builds CGO_ENABLED=0 on alpine (`deploy/Dockerfile.foyer`),
//     so `crypt(3)` — the only implementation of yescrypt on the machine — is not
//     callable from this process at all.
//
// Both were measured to have the property that matters, in the same run:
// EAP-TTLS/PAP succeeds and PEAP-MSCHAPv2 fails. The price is stated where an
// operator sees it, and it is that **an identity opted in to 802.1X has a bcrypt
// verifier, not argon2id**.
//
// # The cost this does not hide
//
// bcrypt is memory-light where argon2id and yescrypt are memory-hard, so an
// offline attack on a stolen verifier parallelises on a GPU far better. Three
// things bound it and none of them is "bcrypt is fine":
//
//   - It is opt-in and per identity. The default population has argon2id only.
//   - The argon2id verifier is unchanged and is still what the portal and the
//     admin console authenticate against. This one answers 802.1X and nothing
//     else.
//   - It is one-way. Unlike an NT hash it is not password-equivalent, which is
//     the entire reason it may live in a column `rlm_sql` can read.
const bcryptCost = 12

// ErrPasswordTooLongForPAP is bcrypt's 72-byte input limit, surfaced by name.
//
// It is a real product constraint rather than an implementation detail, and
// non-ASCII passwords make it likelier than it looks: a Vietnamese passphrase is
// multi-byte, so 72 BYTES can be as few as ~24 characters. The caller must be
// able to tell an operator that this specific password cannot be used for
// 802.1X, rather than reporting a server error.
//
// It is deliberately not worked around by pre-hashing. Pre-hashing bcrypt's
// input is a well-known way to reintroduce password-shucking, and the honest
// answer to a 72-byte password is that this identity does not get 802.1X.
var ErrPasswordTooLongForPAP = errors.New("pap: password exceeds bcrypt's 72-byte input limit")

// papVerifierRE is the same shape migration 00016's CHECK constraint enforces.
//
// It is here as a POST-condition on this function's own output, not as a
// validator standing in for the database: the database refuses the write, and
// this refuses to hand it something it would refuse. `docs/rules/database.md`
// forbids a second source of truth for a constraint; a self-check on what this
// function just produced is not one, because it never sees a value from
// anywhere else.
var papVerifierRE = regexp.MustCompile(`^\$2[ab]\$[0-9]{2}\$[./A-Za-z0-9]{53}$`)

// HashPAPVerifier produces the bcrypt verifier FreeRADIUS's `rlm_pap` will read
// as `Crypt-Password`.
//
// The output is Go's bcrypt encoding VERBATIM — `$2a$`, not relabelled to `$2b$`.
// Measured against libxcrypt on Debian sid and Ubuntu noble: both
// prefixes verify, and the digest under a `$2b$` relabel is byte-identical to the
// one under `$2a$`, for ASCII and UTF-8 passwords alike. Relabelling would be a
// standing claim that the two algorithms agree, needing re-measurement whenever
// either side moves; storing what this process can itself verify is a claim about
// nothing.
func HashPAPVerifier(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if errors.Is(err, bcrypt.ErrPasswordTooLong) {
		return "", ErrPasswordTooLongForPAP
	}
	if err != nil {
		return "", fmt.Errorf("pap: hashing verifier: %w", err)
	}
	if !papVerifierRE.Match(h) {
		// Unreachable unless x/crypto changes its encoding, which is exactly the
		// day this must stop rather than write a value the schema will refuse
		// with a constraint violation two layers away from the cause.
		return "", fmt.Errorf("pap: produced a verifier the schema will not accept: %q", h)
	}
	return string(h), nil
}

// VerifyPAPVerifier checks a password against a stored bcrypt verifier.
//
// Foyer's own authentication paths do NOT use this — the portal and the console
// verify against argon2id, and 802.1X is verified inside FreeRADIUS, which is the
// whole architecture. It exists so that a test can assert the verifier this
// process wrote is the verifier that answers, and so that a rotation can confirm
// the new value before replacing the old one.
func VerifyPAPVerifier(verifier, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(verifier), []byte(password))
}
