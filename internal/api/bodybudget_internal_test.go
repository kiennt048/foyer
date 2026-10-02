package api

import (
	"context"
	"testing"
	"time"
)

func TestABodyBudgetGrantsInOrderAndGivesUpWithItsCaller(t *testing.T) {
	b := newBodyBudget(10)
	ctx := context.Background()

	relA, ok := b.acquire(ctx, 8)
	if !ok {
		t.Fatal("the first acquire was refused")
	}
	// Larger than the whole budget: takes all of it, once there is all of it.
	got := make(chan func(), 1)
	go func() {
		rel, _ := b.acquire(ctx, 50)
		got <- rel
	}()
	time.Sleep(20 * time.Millisecond) // queued first, so the next one is behind it
	// A waiter that gives up must not hold anybody behind it.
	cctx, cancel := context.WithCancel(ctx)
	quit := make(chan bool, 1)
	go func() { _, ok := b.acquire(cctx, 1); quit <- ok }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if <-quit {
		t.Fatal("a cancelled waiter was granted")
	}
	select {
	case <-got:
		t.Fatal("an over-budget request ran beside another")
	case <-time.After(20 * time.Millisecond):
	}
	relA()
	select {
	case rel := <-got:
		rel()
	case <-time.After(time.Second):
		t.Fatal("the over-budget request never ran after the budget emptied")
	}
	if b.free != b.total || len(b.waiters) != 0 {
		t.Fatalf("budget leaked: free %d of %d, %d waiters", b.free, b.total, len(b.waiters))
	}
}
