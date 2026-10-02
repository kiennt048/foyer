package portal

import (
	"context"
	"sync"
	"time"
)

// An in-memory implementation of the session and grant stores.
//
// **It is a fake and it is labelled one.** What it is evidence about is this
// package's behaviour — the ordering, the refusals, the MAC binding, the
// single-use guarantee as a *rule*. What it is not evidence about is PostgreSQL:
// the real single-use guarantee is one SQL statement with a `WHERE state =
// 'unused'` in it and a partial unique index behind it, and neither of those
// exists here. `CONTRACT.md` writes out the schema and the statement so that the
// migration which lands behind this interface is answering a stated requirement.
//
// `docs/rules/testing.md` is explicit about why the label matters: a
// hand-written mock encodes the author's assumptions, which are the same
// assumptions that produced the bug.

// MemoryStore implements SessionStore, GrantStore and Authorizations.
type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string]Session
	grants   map[string]Grant

	// authorizations is every outcome Record was given, in order. It is the
	// operator's stream standing in for `listAuthAttempts`, and it is what makes
	// *"the operator can find out"* assertable rather than a claim about a log
	// line nobody reads.
	authorizations []Authorization

	// recordFail makes Record refuse, so that the reporting path can be broken
	// on purpose. A recorder that has never failed is a recorder nobody knows
	// the shape of the failure of.
	recordFail error

	// Now is the clock expiry is read against. A session is dropped on read
	// rather than by a timer, for the same reason `Grant.StateAt` derives
	// rather than stores: a timer that does not fire leaves a row alive past
	// its bound and nothing says so.
	Now func() time.Time
}

var (
	_ SessionStore   = (*MemoryStore)(nil)
	_ GrantStore     = (*MemoryStore)(nil)
	_ Authorizations = (*MemoryStore)(nil)
)

// NewMemoryStore returns an empty store on the wall clock.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions: map[string]Session{},
		grants:   map[string]Grant{},
		Now:      time.Now,
	}
}

func (m *MemoryStore) now() time.Time {
	if m.Now == nil {
		return time.Now()
	}
	return m.Now()
}

func (m *MemoryStore) CreateSession(_ context.Context, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.TokenHash] = s
	return nil
}

// Session answers ErrNotFound for a token that does not resolve *and* for one
// whose row has expired.
//
// The two are the same answer on purpose. A caller that could tell them apart
// could tell whether a token was ever valid, which on a portal reachable by
// anyone within radio range is an oracle for other guests' session tokens.
func (m *MemoryStore) Session(_ context.Context, tokenHash string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[tokenHash]
	if !ok {
		return Session{}, ErrNotFound
	}
	if !s.ExpiresAt.IsZero() && !s.ExpiresAt.After(m.now()) {
		delete(m.sessions, tokenHash)
		return Session{}, ErrNotFound
	}
	return s, nil
}

func (m *MemoryStore) AttachGrant(_ context.Context, tokenHash, grantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[tokenHash]
	if !ok {
		return ErrNotFound
	}
	s.GrantID = grantID
	m.sessions[tokenHash] = s
	return nil
}

func (m *MemoryStore) ForgetSession(_ context.Context, tokenHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, tokenHash)
	return nil
}

func (m *MemoryStore) CreateGrant(_ context.Context, g Grant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.grants[g.ID] = g
	return nil
}

func (m *MemoryStore) Grant(_ context.Context, id string) (Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.grants[id]
	if !ok {
		return Grant{}, ErrNotFound
	}
	return g, nil
}

// Grants exposes what was issued, for an assertion that nothing was.
//
// The named security test is *"no grant is created for a MAC the driver did not
// confirm present"*, and the only way to assert the negative is to be able to
// count. A store with no way to observe emptiness would leave that test asserting
// a status code, which is what a portal that refuses for the wrong reason also
// returns.
func (m *MemoryStore) Grants() []Grant {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Grant, 0, len(m.grants))
	for _, g := range m.grants {
		out = append(out, g)
	}
	return out
}

// Sessions exposes what was bound, beside `Grants` above and for the same
// reason: a document fetch must not mint rows, and the only way to assert the
// negative is to count. Prefetches, previews and crawlers all fetch documents;
// a GET that created a session for each would fill the table with browsers
// nobody sent.
func (m *MemoryStore) Sessions() []Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// Record writes the authorization outcome to both readers at once.
//
// **One method doing two writes, and that is the interface's requirement rather
// than this implementation's shortcut.** The operator's stream and the guest's
// page are two views of one fact; an implementation that could write the event
// and not the grant would let a console show `authorize-failed` beside a portal
// showing a green tick, which is a worse failure than the silence the port
// replaces. A SQL implementation does both in one transaction; this one does both
// under one mutex.
func (m *MemoryStore) Record(_ context.Context, a Authorization) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recordFail != nil {
		return m.recordFail
	}
	m.authorizations = append(m.authorizations, a)
	if g, ok := m.grants[a.GrantID]; ok {
		g.Delivery = a.Delivery
		m.grants[a.GrantID] = g
	}
	return nil
}

// Authorizations is what Record was given, in order.
func (m *MemoryStore) Authorizations() []Authorization {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Authorization(nil), m.authorizations...)
}

// FailRecording breaks the reporting path.
//
// It exists so that the authorization-failure signal can be made to go red on
// purpose.
// `docs/rules/testing.md`: a lock that has never failed is not yet evidence,
// and a *reporting* mechanism is the worst thing to take on trust — its whole job
// is to be the only witness to a failure nobody else can see.
func (m *MemoryStore) FailRecording(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordFail = err
}

// Revoke ends a grant. Present because `PortalStatus.state` declares `revoked`
// and a value nothing can produce is a state that can never be shown to be
// right — the fifth mechanism in this repository that only looked like a check.
func (m *MemoryStore) Revoke(id string, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.grants[id]
	if !ok {
		return
	}
	g.RevokedAt = &at
	m.grants[id] = g
}
