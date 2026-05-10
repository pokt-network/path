package shannon

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSessionExhaustionTracker_MarkAndQuery(t *testing.T) {
	tr := newSessionExhaustionTracker()

	require.False(t, tr.IsExhausted("eth", "session-1", "pokt1abc"))

	tr.Mark("eth", "session-1", "pokt1abc")
	require.True(t, tr.IsExhausted("eth", "session-1", "pokt1abc"))

	// Different supplier on same session: not exhausted.
	require.False(t, tr.IsExhausted("eth", "session-1", "pokt1xyz"))
	// Different session for same supplier: not exhausted.
	require.False(t, tr.IsExhausted("eth", "session-2", "pokt1abc"))
	// Different service for same (session, supplier): not exhausted.
	require.False(t, tr.IsExhausted("poly", "session-1", "pokt1abc"))
}

func TestSessionExhaustionTracker_NilReceiver(t *testing.T) {
	var tr *sessionExhaustionTracker
	tr.Mark("eth", "s", "pokt1abc")                              // must not panic
	require.False(t, tr.IsExhausted("eth", "s", "pokt1abc"))     // must not panic
}

func TestSessionExhaustionTracker_EmptyArgsAreNoOps(t *testing.T) {
	tr := newSessionExhaustionTracker()
	tr.Mark("", "s", "pokt1abc")
	tr.Mark("eth", "", "pokt1abc")
	tr.Mark("eth", "s", "")
	require.Equal(t, 0, len(tr.entries))
	require.False(t, tr.IsExhausted("", "s", "pokt1abc"))
	require.False(t, tr.IsExhausted("eth", "", "pokt1abc"))
	require.False(t, tr.IsExhausted("eth", "s", ""))
}

func TestSessionExhaustionTracker_TTLExpiry(t *testing.T) {
	tr := newSessionExhaustionTracker()
	tr.Mark("eth", "session-1", "pokt1abc")

	// Fudge the stored timestamp to simulate TTL expiry without sleeping.
	tr.mu.Lock()
	tr.entries[sessionExhaustionKey{ServiceID: "eth", SessionID: "session-1", Supplier: "pokt1abc"}] = time.Now().Add(-2 * sessionExhaustionTTL)
	tr.mu.Unlock()

	require.False(t, tr.IsExhausted("eth", "session-1", "pokt1abc"))

	// Lazy reap should have dropped the entry.
	tr.mu.RLock()
	_, present := tr.entries[sessionExhaustionKey{ServiceID: "eth", SessionID: "session-1", Supplier: "pokt1abc"}]
	tr.mu.RUnlock()
	require.False(t, present, "expired entry should be lazily reaped on IsExhausted")
}

func TestSessionExhaustionTracker_Concurrent(t *testing.T) {
	tr := newSessionExhaustionTracker()

	var wg sync.WaitGroup
	for i := range 200 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			supplier := "pokt1" + strconv.Itoa(n)
			tr.Mark("eth", "session-1", supplier)
			require.True(t, tr.IsExhausted("eth", "session-1", supplier))
		}(i)
	}
	wg.Wait()

	tr.mu.RLock()
	defer tr.mu.RUnlock()
	require.Equal(t, 200, len(tr.entries))
}
