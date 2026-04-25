package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDetachedHedgeCtx_InheritsDeadlineNotCancellation verifies the central
// invariant of the hedge-loser graceful drain fix: the detached context survives
// cancellation of its conceptual parent (so the loser's HTTP exchange to the
// relay miner can finish cleanly) but still respects the parent's deadline plus
// a small grace window (so we don't leak goroutines when the caller is already
// gone).
func TestDetachedHedgeCtx_InheritsDeadlineNotCancellation(t *testing.T) {
	t.Run("parent_cancellation_does_not_propagate", func(t *testing.T) {
		parent, cancelParent := context.WithCancel(context.Background())
		detached, cancelDetached := detachedHedgeCtx(parent, time.Second)
		defer cancelDetached()

		cancelParent()

		select {
		case <-detached.Done():
			t.Fatalf("detached ctx should NOT be cancelled when parent is cancelled — that's the entire point of the fix")
		case <-time.After(50 * time.Millisecond):
			// expected
		}
	})

	t.Run("parent_deadline_propagates_with_grace", func(t *testing.T) {
		parentDeadline := time.Now().Add(100 * time.Millisecond)
		parent, cancelParent := context.WithDeadline(context.Background(), parentDeadline)
		defer cancelParent()

		grace := 200 * time.Millisecond
		detached, cancelDetached := detachedHedgeCtx(parent, grace)
		defer cancelDetached()

		gotDeadline, ok := detached.Deadline()
		require.True(t, ok, "detached ctx should have a deadline when parent does")

		// Detached deadline should be parent deadline + grace.
		expected := parentDeadline.Add(grace)
		require.WithinDuration(t, expected, gotDeadline, 5*time.Millisecond)
	})

	t.Run("no_parent_deadline_uses_grace_as_timeout", func(t *testing.T) {
		grace := 50 * time.Millisecond
		detached, cancelDetached := detachedHedgeCtx(context.Background(), grace)
		defer cancelDetached()

		_, ok := detached.Deadline()
		require.True(t, ok, "detached ctx should have a deadline (the grace window) when parent has none")

		// Detached should expire after grace.
		select {
		case <-detached.Done():
			// expected
		case <-time.After(grace * 4):
			t.Fatalf("detached ctx should expire after grace window when parent has no deadline")
		}
	})
}

// TestCancelBranches_NilSafe verifies the cleanup helper tolerates nil cancels,
// which happens when a hedge race ends before a branch is constructed (e.g.,
// hedge_delay never elapsed, no alternative endpoint).
func TestCancelBranches_NilSafe(t *testing.T) {
	hr := &hedgeRacer{} // both cancel funcs nil
	require.NotPanics(t, hr.cancelBranches)

	called := 0
	hr.primaryReqCancel = func() { called++ }
	hr.cancelBranches()
	require.Equal(t, 1, called, "non-nil cancel must be called")

	// Calling a CancelFunc twice is documented as safe (no-op).
	hr.cancelBranches()
	require.Equal(t, 2, called, "cancelBranches calls each non-nil cancel once per invocation")
}
