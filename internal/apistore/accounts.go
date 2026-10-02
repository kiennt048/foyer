package apistore

import (
	"context"
	"errors"
	"strings"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/store"
)

// AccountDirectory — guest identities.
//
// argon2id, always. An account opted in to PEAP-MSCHAPv2 additionally has an NT
// hash in `account_nt_hashes`, envelope-encrypted, which this port deliberately
// cannot reach: the opt-in has its own repository method with its own name, because
// storing password-equivalent material is a decision rather than a detail.

func (a *Adapter) Accounts(ctx context.Context, siteID, search string, w api.Window) (api.Page[gen.Account], error) {
	return fill(w,
		func(cursor string, limit int) (store.Page[store.Account], error) {
			return a.st.Accounts().List(ctx, listOptions(siteID, search, cursor, limit))
		},
		func(acc store.Account) (gen.Account, bool) { return accountOut(acc), true },
	)
}

// CreateAccount hashes the password here and hands the store a verifier.
//
// `internal/store` never sees a cleartext password — its CONTRACT.md says so — and
// the hashing lives in `internal/identity`, which is where the argon2id parameters
// are decided once rather than per call site.
//
// **The plan's `enforcedBy` is derived, not accepted**, from `enforcementIn` — the
// same function `CreateBatch` reads, for the same reason. `AccountCreate.plan` is
// typed `PlanRequest` rather than `Plan`; typed as `Plan`, a client could not merely
// *send* the field, it would be *required* to, and a path that copied the caller's
// value straight through would store it. A client that asserts `nas` on a data
// allowance in a site whose weakest access point has `hardQuota: false` has told
// the console a cap is being applied by hardware nobody asked, and the console
// renders it as authoritative — `docs/rules/radius.md` on who enforces a quota, and
// `docs/rules/frontend.md` on a figure that arrives without its caveat.
func (a *Adapter) CreateAccount(ctx context.Context, in gen.AccountCreate) (gen.Account, error) {
	hash, err := identity.HashPassword(in.Password)
	if err != nil {
		return gen.Account{}, err
	}
	acc := &store.Account{
		ID:           a.newID("acc-"),
		SiteID:       in.SiteId,
		Username:     strings.TrimSpace(in.Username),
		DisplayName:  in.DisplayName,
		PasswordHash: hash,
		Enabled:      true,
	}
	if in.Plan != nil {
		enforced, err := a.enforcementIn(ctx, in.SiteId, *in.Plan)
		if err != nil {
			return gen.Account{}, err
		}
		acc.Plan = planIn(*in.Plan, enforced)
	}
	// The 802.1X opt-in, made from the same cleartext in the same request and
	// written in the same transaction: an account whose opt-in is refused is not
	// created.
	var pap string
	if in.EapTtlsPap != nil && *in.EapTtlsPap {
		if pap, err = papVerifier(in.Password); err != nil {
			return gen.Account{}, err
		}
	}
	if err := a.st.Accounts().CreateWithCredentials(ctx, acc, pap, operatorName(ctx)); err != nil {
		return gen.Account{}, credentialOut(err)
	}
	return accountOut(*acc), nil
}

// papVerifier is identity.HashPAPVerifier with its one refusal named for the API.
func papVerifier(password string) (string, error) {
	v, err := identity.HashPAPVerifier(password)
	if errors.Is(err, identity.ErrPasswordTooLongForPAP) {
		return "", api.ErrPasswordTooLongFor8021X
	}
	return v, err
}

func credentialOut(err error) error {
	if errors.Is(err, store.ErrPAPUsernameTaken) {
		return api.ErrPAPUsernameTaken
	}
	return out(err)
}

// operatorName is the signed-in operator's username, for `opted_in_by`.
func operatorName(ctx context.Context) *string {
	if u := api.OperatorUsername(ctx); u != "" {
		return &u
	}
	return nil
}

// AmendAccount changes what `updateAccount` declares.
//
// The credentials go first and in one transaction (`SetCredentials`): a refusal
// there — no password for an opt-in, one too long for bcrypt, an 802.1X username
// another venue holds — changes nothing, including the display name and the
// enabled flag in the same body.
func (a *Adapter) AmendAccount(ctx context.Context, id string, in api.AccountAmendment) (gen.Account, error) {
	if in.Password != nil || in.EapTtlsPap != nil {
		has, err := a.st.Accounts().Has8021X(ctx, id)
		if err != nil {
			return gen.Account{}, out(err)
		}
		want := has
		if in.EapTtlsPap != nil {
			want = *in.EapTtlsPap
		}
		var ch store.CredentialChange
		ch.By = operatorName(ctx)
		if in.Password != nil {
			hash, err := identity.HashPassword(*in.Password)
			if err != nil {
				return gen.Account{}, err
			}
			ch.PasswordHash = &hash
		}
		switch {
		case want && in.Password != nil:
			// A new password on an opted-in identity replaces both verifiers, so
			// the portal and the network never answer to different passwords.
			v, err := papVerifier(*in.Password)
			if err != nil {
				return gen.Account{}, err
			}
			ch.PAPVerifier = &v
		case want && !has:
			return gen.Account{}, api.ErrPasswordRequiredFor8021X
		case !want && has:
			ch.OptOut8021X = true
		}
		if _, err := a.st.Accounts().SetCredentials(ctx, id, ch); err != nil {
			return gen.Account{}, credentialOut(err)
		}
	}
	patch := store.AccountPatch{Enabled: in.Enabled}
	if in.DisplayName != nil {
		patch.DisplayName = &in.DisplayName
	}
	acc, err := a.st.Accounts().Update(ctx, id, patch)
	if err != nil {
		return gen.Account{}, out(err)
	}
	return accountOut(*acc), nil
}

func (a *Adapter) DeleteAccount(ctx context.Context, id string) error {
	return out(a.st.Accounts().Delete(ctx, id))
}
