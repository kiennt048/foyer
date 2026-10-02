package memory

import (
	"context"
	"strings"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/identity"
)

// The first-access setup, over the world. `console_setup` in the real store
// (migration 00040); a digest here, because the thing it stands in for is one.

// OpenSetup is `foyerctl open-setup`: it opens (or re-opens with a new code) the
// setup, which the API then offers. The only writer, as the command is in
// production; there is no API operation that opens one.
func (s *Store) OpenSetup(code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setupDigest = identity.SetupCodeDigest(code)
}

func (s *Store) SetupOpen(context.Context) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.setupDigest != nil, nil
}

// HasAdministrator is the same population `OpenSetup` stands in for: an operator
// with the administrator role that is enabled. A memory world with none is the
// broken fresh install, and the console must be able to say so.
func (s *Store) HasAdministrator(context.Context) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, o := range s.world.Operators {
		if o.Role == gen.Administrator && o.Enabled {
			return true, nil
		}
	}
	return false, nil
}

// CompleteSetup checks, creates and closes under one lock, which is this world's
// transaction: two callers racing get one administrator and one ErrSetupClosed.
func (s *Store) CompleteSetup(_ context.Context, code, username, password string) (gen.Operator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setupDigest == nil {
		return gen.Operator{}, api.ErrSetupClosed
	}
	if !identity.SameSetupCode(code, s.setupDigest) {
		return gen.Operator{}, api.ErrSetupCodeWrong
	}
	username = strings.ToLower(strings.TrimSpace(username))
	for _, o := range s.world.Operators {
		if strings.EqualFold(o.Username, username) {
			return gen.Operator{}, api.ErrConflict
		}
	}
	o := gen.Operator{
		Id:        s.id("op-"),
		Username:  username,
		Role:      gen.Administrator,
		Enabled:   true,
		CreatedAt: s.clock(),
	}
	s.world.Operators = append([]gen.Operator{o}, s.world.Operators...)
	s.world.Credentials[o.Id] = password
	s.setupDigest = nil
	return o, nil
}
