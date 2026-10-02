package apistore

import (
	"context"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/store"
)

// AdminSessionStore — the console's own session, and nothing else's.
//
// The token arrives already hashed and leaves already hashed: `CreateAdminSession`
// takes a hash and `LookupAdminSession` takes one, so a database dump contains no
// live session. The CSRF token is stored in the clear and the asymmetry is the
// point — it grants nothing on its own and only has a job when presented alongside
// the cookie, and `getAuthSession` has to be able to hand it back to a client that
// cannot read an httpOnly cookie.

func (a *Adapter) CreateAdminSession(ctx context.Context, s api.AdminSession) error {
	return out(a.st.Authn().Create(ctx, &store.OperatorSession{
		TokenHash:  s.TokenHash,
		OperatorID: s.OperatorID,
		CSRFToken:  s.CSRFToken,
		ExpiresAt:  s.ExpiresAt,
	}))
}

// LookupAdminSession resolves a hashed token.
//
// Expiry and the operator's enabled flag are in the repository's `WHERE` clause
// rather than checked afterwards, so a disabled operator's live session stops
// working on the next request — which is what an administrator disabling an account
// expects to have bought. All three failures arrive here as `ErrNotFound`, and the
// caller treats that as "nobody is signed in" rather than as an error, because
// telling them apart would say whether a token was ever valid.
func (a *Adapter) LookupAdminSession(ctx context.Context, tokenHash string) (api.AdminSession, error) {
	sess, _, err := a.st.Authn().Resolve(ctx, tokenHash)
	if err != nil {
		return api.AdminSession{}, out(err)
	}
	return api.AdminSession{
		TokenHash:  sess.TokenHash,
		CSRFToken:  sess.CSRFToken,
		OperatorID: sess.OperatorID,
		ExpiresAt:  sess.ExpiresAt,
	}, nil
}

func (a *Adapter) DeleteAdminSession(ctx context.Context, tokenHash string) error {
	return out(a.st.Authn().Delete(ctx, tokenHash))
}
