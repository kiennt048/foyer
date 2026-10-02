package identity

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestTheFloodTestAssumesTheDefaultBound keeps the literal in
// `TestNoMoreArgon2HashesInFlightThanTheBound` equal to the real default, and
// the default equal to what a 320M container can afford.
func TestTheFloodTestAssumesTheDefaultBound(t *testing.T) {
	if DefaultHashConcurrency != 2 {
		t.Fatalf("DefaultHashConcurrency = %d; the flood test asserts 2", DefaultHashConcurrency)
	}
	if HashConcurrency() != DefaultHashConcurrency {
		t.Fatalf("HashConcurrency() = %d at rest, want the default %d", HashConcurrency(), DefaultHashConcurrency)
	}
	if got := DefaultHashConcurrency * HashMemoryBytes; got > 160<<20 {
		t.Fatalf("the default bound costs %d MiB, over half of the 320M container", got>>20)
	}
}

// TestAHashThatCannotGetASlotIsRefusedNotQueuedForever: the wait is bounded, and
// what a caller gets at the end of it is an error — which every caller maps to
// "not authenticated" — and never a match.
func TestAHashThatCannotGetASlotIsRefusedNotQueuedForever(t *testing.T) {
	encoded, err := HashPassword("another-password")
	if err != nil {
		t.Fatal(err)
	}
	SetHashConcurrency(1, time.Millisecond)
	defer SetHashConcurrency(DefaultHashConcurrency, DefaultHashWait)

	var wg sync.WaitGroup
	var busy, matched atomic.Int32
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := VerifyPassword("another-password", encoded)
			switch {
			case errors.Is(err, ErrHashBusy):
				busy.Add(1)
				if ok {
					t.Error("a refused verification reported a match")
				}
			case err == nil && ok:
				matched.Add(1)
			}
		}()
	}
	wg.Wait()
	if busy.Load() == 0 {
		t.Fatal("six verifications against one slot and a 1 ms wait, and none was refused")
	}
	if matched.Load() == 0 {
		t.Fatal("no verification got the slot at all")
	}
}
