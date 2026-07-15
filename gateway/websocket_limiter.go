package gateway

import "sync/atomic"

// WebsocketConnectionLimiter bounds the number of concurrent live websocket
// connections a gateway will hold open.
//
// Each websocket connection is long-lived and costs ~5-7 goroutines plus two
// TCP sockets and read/write buffers (~50-100KB total). Without a ceiling, a
// flood of upgrades — or a slow drain of never-closing connections — grows
// goroutines and file descriptors without bound and can exhaust the pod. Envoy
// rate-limits connection *attempts* at the edge, but nothing else bounds the
// count of *live* connections inside PATH; this is the internal backstop.
//
// A nil *WebsocketConnectionLimiter means limiting is disabled: all methods are
// nil-safe and Acquire always succeeds. This keeps the limiter optional without
// forcing every caller/test to construct one.
type WebsocketConnectionLimiter struct {
	max    int64
	active atomic.Int64
}

// NewWebsocketConnectionLimiter returns a limiter capping concurrent websocket
// connections at max. A max <= 0 returns nil, which disables limiting.
func NewWebsocketConnectionLimiter(max int) *WebsocketConnectionLimiter {
	if max <= 0 {
		return nil
	}
	return &WebsocketConnectionLimiter{max: int64(max)}
}

// Acquire reserves a connection slot. It returns true if a slot was reserved
// (the caller must later call Release) and false if the limiter is already at
// capacity (the caller must reject the connection and must NOT call Release).
// A nil limiter always returns true (limiting disabled).
func (l *WebsocketConnectionLimiter) Acquire() bool {
	if l == nil {
		return true
	}
	// Lock-free CAS loop: only increment when strictly below the cap. Unlike an
	// optimistic add-then-rollback, this never lets the counter overshoot the cap
	// even transiently, so a concurrent Active() read can never exceed max.
	for {
		cur := l.active.Load()
		if cur >= l.max {
			return false
		}
		if l.active.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// Release frees a slot previously reserved by a successful Acquire. It must be
// called exactly once per successful Acquire. A nil limiter is a no-op.
func (l *WebsocketConnectionLimiter) Release() {
	if l == nil {
		return
	}
	l.active.Add(-1)
}

// Active returns the number of currently reserved slots. A nil limiter reports 0.
func (l *WebsocketConnectionLimiter) Active() int64 {
	if l == nil {
		return 0
	}
	return l.active.Load()
}
