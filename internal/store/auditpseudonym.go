package store

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// An erasure's reach into the audit log (migration 00052).
//
// The audit log is the operator's accountability record and is not erased: the
// *event* stays. What goes is the guest's identifier, replaced wherever it appears
// in `subject_id`, `subject_name` or `params` by `pseudonym:` and sixteen hex digits
// of HMAC-SHA256 over the subject's canonical identifier. The migration has the
// survey (authentik deletes, PacketFence keeps, EDPB 01/2025 names the keyed hash)
// and why Foyer pseudonymises.
//
// **The key** is HKDF-derived from FOYER_KEK with its own label, as phone sign-in's
// code key is (phonecodekey.go): the one secret this deployment keeps out of the
// database, so the pseudonym cannot be reversed by somebody holding a dump. Same
// subject, same pseudonym, under one KEK — so "these three entries were about one
// guest" survives. A deployment with no keyring gets a random pseudonym per
// erasure: unlinkable across erasures, which is the stronger privacy and the weaker
// accountability, and the only honest answer without a key.

const auditPseudonymInfo = "foyer/audit-pseudonym/v1"

func deriveAuditPseudonymKey(kek []byte) ([]byte, error) {
	k, err := hkdf.Key(sha256.New, kek, nil, auditPseudonymInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("deriving the audit-pseudonym key: %w", err)
	}
	return k, nil
}

// canonical is the one spelling of the subject the pseudonym is computed over.
func (s Subject) canonical() string {
	switch s.Kind {
	case SubjectMAC:
		return s.MACHex
	case SubjectAccount:
		return s.AccountID
	case SubjectPhone:
		if len(s.PhoneDigits) > 0 {
			return s.PhoneDigits[0]
		}
	case SubjectEmail:
		return s.Email
	}
	return ""
}

// AuditPseudonym is what the audit log names this subject by after its erasure.
func (s Subject) AuditPseudonym(keys *Keyring) (string, error) {
	var sum []byte
	if keys != nil {
		m := hmac.New(sha256.New, keys.active.auditPseudonym)
		m.Write([]byte(string(s.Kind) + "\x00" + s.canonical()))
		sum = m.Sum(nil)
	} else {
		sum = make([]byte, 8)
		if _, err := rand.Read(sum); err != nil {
			return "", err
		}
	}
	return "pseudonym:" + hex.EncodeToString(sum[:8]), nil
}

// auditLiterals are the exact identifiers an audit entry may carry for this
// subject, besides a MAC (which the function matches in any spelling).
func (s Subject) auditLiterals() []string {
	var out []string
	add := func(v string) {
		if len(v) >= 3 {
			out = append(out, v)
		}
	}
	switch s.Kind {
	case SubjectAccount:
		add(s.Username)
		add(s.AccountID)
	case SubjectPhone:
		for _, v := range s.PhoneSpellings {
			add(v)
		}
	case SubjectEmail:
		add(s.Email)
	}
	return out
}

func init() {
	for _, c := range []PersonalColumn{
		{Table: "audit_log", Column: "subject_name", Holds: "what an entry is about, by name: a session's or a grant's MAC, an account's username",
			Identifies: []SubjectKind{SubjectMAC, SubjectAccount, SubjectPhone, SubjectEmail}, Pseudonymised: true},
		{Table: "audit_log", Column: "subject_id", Holds: "what an entry is about, by id: an account's id",
			Identifies: []SubjectKind{SubjectMAC, SubjectAccount, SubjectPhone, SubjectEmail}, Pseudonymised: true},
		{Table: "audit_log", Column: "params", Holds: "the details of the change, which may name the guest it was about",
			Identifies: []SubjectKind{SubjectMAC, SubjectAccount, SubjectPhone, SubjectEmail}, Pseudonymised: true},
	} {
		RegisterPersonalColumn(c)
	}

	// Last: every other eraser has run, so nothing later writes the identifier
	// back, and the erasure's own entry (which names the subject masked) is written
	// after this in the same transaction.
	RegisterEraser(Eraser{Table: "audit_log", Kinds: []SubjectKind{SubjectMAC, SubjectAccount, SubjectPhone, SubjectEmail},
		Order: 100, Pseudonymises: true,
		Erase: func(ctx context.Context, tx pgx.Tx, s Subject) (int64, error) {
			if !strings.HasPrefix(s.pseudonym, "pseudonym:") {
				return 0, fmt.Errorf("store: an erasure reached the audit log with no pseudonym")
			}
			var mac *string
			if s.Kind == SubjectMAC {
				mac = &s.MACHex
			}
			var n int64
			err := tx.QueryRow(ctx, `SELECT foyer_audit_pseudonymise($1, $2, $3)`,
				mac, s.auditLiterals(), s.pseudonym).Scan(&n)
			return n, mapErr(err)
		}})
}
