package shannon

import (
	"sync"
	"sync/atomic"
	"time"
)

// sessionExhaustionTTL bounds how long an entry stays in the tracker before
// being lazily reaped on the next Mark or IsExhausted call. Sessions on
// Shannon are short (~10 blocks ≈ 10 min); 30 min covers ~3 session
// lifecycles which is enough to absorb clock skew and session-boundary races
// while preventing the map from growing unbounded if the reaping path is
// somehow not triggered.
const sessionExhaustionTTL = 30 * time.Minute

// sessionExhaustionEvictThreshold gates the opportunistic GC inside Mark.
// Walking the whole map is O(n); we only walk when it has grown past this
// size to keep the steady-state Mark cost ~O(1).
const sessionExhaustionEvictThreshold = 1024

// sessionExhaustionTracker records (serviceID, sessionID, supplier) tuples
// where the supplier's relay-miner has signaled the application's per-session
// stake budget is exhausted ("over-servicing"). Once flagged, PATH must skip
// that supplier for the rest of the session — every additional relay would
// be rejected the same way, wasting latency and making the reputation-fix
// look incomplete.
//
// In-memory only and per-PATH-pod: each pod independently learns which
// suppliers are exhausted. We don't share via Redis because (a) the signal
// is dense enough that each pod will quickly converge on the same set, and
// (b) a Redis hop on every endpoint-selection call is not worth the cost
// for a session-scoped, short-lived flag. After session end the entries are
// reaped lazily.
type sessionExhaustionTracker struct {
	mu      sync.RWMutex
	entries map[string]time.Time

	// markCount is incremented on every Mark and gates the opportunistic GC
	// frequency without taking the write lock just to bump a counter.
	markCount atomic.Uint64
}

func newSessionExhaustionTracker() *sessionExhaustionTracker {
	return &sessionExhaustionTracker{
		entries: make(map[string]time.Time),
	}
}

// sessionExhaustionKey builds the map key. Service ID is included so two
// services that happen to share a session ID prefix don't cross-contaminate.
func sessionExhaustionKey(serviceID, sessionID, supplier string) string {
	return serviceID + "|" + sessionID + "|" + supplier
}

// Mark flags the (service, session, supplier) tuple as over-serviced for the
// remainder of the session. Subsequent IsExhausted calls return true until
// the entry expires via TTL.
func (t *sessionExhaustionTracker) Mark(serviceID, sessionID, supplier string) {
	if t == nil || serviceID == "" || sessionID == "" || supplier == "" {
		return
	}
	key := sessionExhaustionKey(serviceID, sessionID, supplier)

	t.mu.Lock()
	t.entries[key] = time.Now()
	count := t.markCount.Add(1)
	if len(t.entries) > sessionExhaustionEvictThreshold && count%64 == 0 {
		t.reapLocked()
	}
	t.mu.Unlock()
}

// IsExhausted reports whether the supplier is currently flagged as exhausted
// for the (service, session). Lazily reaps the entry if it has expired.
func (t *sessionExhaustionTracker) IsExhausted(serviceID, sessionID, supplier string) bool {
	if t == nil || serviceID == "" || sessionID == "" || supplier == "" {
		return false
	}
	key := sessionExhaustionKey(serviceID, sessionID, supplier)

	t.mu.RLock()
	markedAt, ok := t.entries[key]
	t.mu.RUnlock()

	if !ok {
		return false
	}
	if time.Since(markedAt) <= sessionExhaustionTTL {
		return true
	}

	// Expired — drop the entry under the write lock and report not-exhausted.
	t.mu.Lock()
	if v, ok := t.entries[key]; ok && time.Since(v) > sessionExhaustionTTL {
		delete(t.entries, key)
	}
	t.mu.Unlock()
	return false
}

// reapLocked walks the map and drops entries older than the TTL. Caller must
// hold the write lock. O(n) — only invoked when the map exceeds the eviction
// threshold and only every 64 marks to bound amortized cost.
func (t *sessionExhaustionTracker) reapLocked() {
	cutoff := time.Now().Add(-sessionExhaustionTTL)
	for k, v := range t.entries {
		if v.Before(cutoff) {
			delete(t.entries, k)
		}
	}
}
