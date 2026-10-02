package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	argon2Time    = 3
	argon2Memory  = 64 * 1024 // 64 MB
	argon2Threads = 4
	argon2KeyLen  = 32
	saltLen       = 16
)

var (
	ErrInvalidHash         = errors.New("argon2id: hash is not in the correct format")
	ErrIncompatibleVersion = errors.New("argon2id: incompatible version of argon2")
	// ErrHashBusy is a verification that did not run because every slot was
	// taken for longer than the caller is allowed to wait. It is a refusal, never
	// a match: every caller already treats an error as "not authenticated".
	ErrHashBusy = errors.New("argon2id: too many password hashes in flight")
)

// ── the memory bound ─────────────────────────────────────────────────────────
//
// Every argon2id computation here allocates `argon2Memory` — 64 MiB — for its
// whole duration, and the unknown-username decoy pays the same on purpose. With
// nothing bounding how many run at once, memory is `64 MiB × concurrent logins`:
// the control-plane container is capped at 320M (`deploy/compose.yaml`), so five
// parallel sign-ins exhaust it, and the process that dies is the one serving the
// Policy API every Wi-Fi login asks.
//
// So every hash takes a slot first, and the slot count is a memory budget rather
// than a throughput knob: `SetHashConcurrency` is called once at startup from the
// container's own limit (`cmd/foyer`). This is what Ory Kratos does and says
// out loud — its argon2 hasher is configured with a `dedicated_memory` and a
// `max-concurrent`, because a memory-hard hash with unbounded concurrency is a
// memory-exhaustion endpoint with a login form in front of it.
//
// The wait for a slot is bounded as well. An unbounded wait turns a flood into a
// queue of goroutines each holding a request open; a bounded one answers the
// excess with `ErrHashBusy`, which every caller already maps to "not
// authenticated". The rate limiter in `internal/ratelimit` is the first gate and
// this is the second: the limiter decides who may try, this decides how many
// tries the machine can afford at once.
//
// **The Policy API must never take a slot.** It is on the path of every RADIUS
// Access-Request and it has a 1–2 s budget before FreeRADIUS gives up; a Policy
// API that queued behind a flood of console sign-ins would turn a console problem
// into a Wi-Fi outage. `internal/aaa` does not import this package's hashing and
// `TestThePolicyEngineNeverHashesAPassword` holds it to that.

// DefaultHashConcurrency is what a process gets before `SetHashConcurrency`: two
// hashes, 128 MiB, which fits the 320M container with room for the rest of the
// process. Deliberately the small number — a test or a tool that never configures
// it is bounded rather than unbounded.
const DefaultHashConcurrency = 2

// DefaultHashWait is how long a caller waits for a slot before `ErrHashBusy`.
// Below the admin listener's 15 s read timeout and the portal's 10 s, so the
// refusal is written before the connection is torn down.
const DefaultHashWait = 5 * time.Second

// HashMemoryBytes is what one slot costs. Exported so the caller sizing the
// semaphore does its arithmetic with the same number this package allocates.
const HashMemoryBytes = argon2Memory * 1024

type hashSlots struct {
	mu    sync.Mutex
	slots chan struct{}
	wait  time.Duration
}

var hashing = &hashSlots{slots: make(chan struct{}, DefaultHashConcurrency), wait: DefaultHashWait}

// SetHashConcurrency sets how many argon2id computations may run at once and how
// long a caller waits for one. n below 1 is 1; wait at or below zero is
// `DefaultHashWait`.
//
// Meant to be called once, at startup, before anything is served. A call later is
// safe — a hash already running releases into the set it took its slot from — but
// for the moment of the swap the old and the new sets are both admitting, so the
// bound briefly holds for each rather than for the sum.
func SetHashConcurrency(n int, wait time.Duration) {
	if n < 1 {
		n = 1
	}
	if wait <= 0 {
		wait = DefaultHashWait
	}
	hashing.mu.Lock()
	defer hashing.mu.Unlock()
	hashing.slots = make(chan struct{}, n)
	hashing.wait = wait
}

// HashConcurrency reports the current bound.
func HashConcurrency() int {
	hashing.mu.Lock()
	defer hashing.mu.Unlock()
	return cap(hashing.slots)
}

// acquire takes a slot and returns the function that gives it back.
func (h *hashSlots) acquire() (func(), error) {
	h.mu.Lock()
	slots, wait := h.slots, h.wait
	h.mu.Unlock()

	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	default:
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-t.C:
		return nil, ErrHashBusy
	}
}

// idKey is argon2id behind the slot.
func idKey(password, salt []byte, iterations, memory uint32, threads uint8, keyLen uint32) ([]byte, error) {
	release, err := hashing.acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	return argon2.IDKey(password, salt, iterations, memory, threads, keyLen), nil
}

// HashPassword hashes a password using Argon2id with cryptographically random salt.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate random salt: %w", err)
	}

	hash, err := idKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	if err != nil {
		return "", err
	}

	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argon2Memory, argon2Time, argon2Threads, b64Salt, b64Hash)

	return encoded, nil
}

// VerifyPassword performs a constant-time comparison between the password and the encoded Argon2id hash.
func VerifyPassword(password, encodedHash string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrIncompatibleVersion
	}

	var memory uint32
	var iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, ErrInvalidHash
	}
	// The cost parameters come out of the stored string, so they are what decides
	// how much this call allocates — and the slot above is sized for
	// `argon2Memory`. A verifier asking for more would spend several slots' worth
	// of memory while holding one. Refused rather than honoured: every verifier
	// this package writes uses exactly `argon2Memory`, and raising it is a change
	// to the budget in `cmd/foyer` at the same time, not a row in a table.
	if memory == 0 || memory > argon2Memory || iterations == 0 || threads == 0 {
		return false, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHash
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrInvalidHash
	}

	calculatedHash, err := idKey([]byte(password), salt, iterations, memory, threads, uint32(len(expectedHash)))
	if err != nil {
		return false, err
	}

	if subtle.ConstantTimeCompare(expectedHash, calculatedHash) == 1 {
		return true, nil
	}

	return false, nil
}
