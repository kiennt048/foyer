package identity

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// argon2InFlight counts the goroutines that are inside an argon2 computation
// started from this package, right now.
//
// Read off the goroutine stacks rather than off a counter this package keeps, and
// that is the point: a counter inside the fix is removed with the fix, and a test
// that reads it cannot be run against the code it is meant to catch. A stack is
// the same fact on either side — a goroutine whose frames include both
// `x/crypto/argon2` and this package is a caller whose 64 MiB is allocated. A
// caller waiting for a slot has no argon2 frame; argon2's own lane goroutines have
// no frame from this package.
func argon2InFlight(buf []byte) int {
	n := runtime.Stack(buf, true)
	count := 0
	for _, g := range bytes.Split(buf[:n], []byte("\n\n")) {
		if bytes.Contains(g, []byte("golang.org/x/crypto/argon2.")) &&
			bytes.Contains(g, []byte("internal/identity.")) {
			count++
		}
	}
	return count
}

// TestNoMoreArgon2HashesInFlightThanTheBound locks the memory half of the hash
// bound.
//
// Twelve verifications at once — a sign-in flood — must never have more than
// `HashConcurrency()` argon2id computations running, because each one is 64 MiB
// and the container is 320M. Before the bound existed all twelve ran at once:
// 768 MiB on a box with 320.
func TestNoMoreArgon2HashesInFlightThanTheBound(t *testing.T) {
	encoded, err := HashPassword("a-password-nobody-types")
	if err != nil {
		t.Fatal(err)
	}
	// The bound is the process default, written as a literal so this test runs
	// unchanged against a tree without the bound — which is how it is shown to
	// fail there. `TestTheFloodTestAssumesTheDefaultBound` keeps the literal
	// honest, and every test that changes the bound restores it.
	const bound = 2

	const callers = 12
	var (
		wg      sync.WaitGroup
		done    atomic.Bool
		peak    int
		samples int
	)
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		buf := make([]byte, 1<<22)
		for !done.Load() {
			if n := argon2InFlight(buf); n > peak {
				peak = n
			}
			samples++
			time.Sleep(time.Millisecond)
		}
	}()

	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			// The answer is not the subject — the concurrency is. A caller that
			// waited past the bound's own timeout is refused, which is correct
			// and happens under `-race`, where each hash is several times slower.
			_, _ = VerifyPassword(fmt.Sprintf("guess-%d", i), encoded)
		}(i)
	}
	close(start)
	wg.Wait()
	done.Store(true)
	<-sampled

	t.Logf("peak argon2 computations in flight: %d (bound %d, %d callers, %d samples)",
		peak, bound, callers, samples)
	if peak == 0 {
		t.Fatal("never observed a computation in flight — the sampler is measuring nothing")
	}
	if peak > bound {
		t.Fatalf("%d argon2id computations ran at once; the bound is %d (each is 64 MiB)", peak, bound)
	}
}

// TestAVerifierCannotAskForMoreMemoryThanASlotHolds: the cost parameters are read
// out of the stored string, so a verifier with `m=` above what this package
// writes would allocate several slots' worth of memory while holding one.
func TestAVerifierCannotAskForMoreMemoryThanASlotHolds(t *testing.T) {
	encoded, err := HashPassword("x")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(encoded, "$")
	parts[3] = fmt.Sprintf("m=%d,t=1,p=1", argon2Memory*4)
	inflated := strings.Join(parts, "$")

	ok, err := VerifyPassword("x", inflated)
	if ok || !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("a verifier asking for %d MiB was honoured: ok=%v err=%v", argon2Memory*4>>10, ok, err)
	}
}
