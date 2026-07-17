package gateway

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_WebsocketConnectionLimiter_NilIsDisabled(t *testing.T) {
	c := require.New(t)

	// A max <= 0 yields a nil limiter (limiting disabled).
	var l *WebsocketConnectionLimiter = NewWebsocketConnectionLimiter(0)
	c.Nil(l)
	c.Nil(NewWebsocketConnectionLimiter(-5))

	// All methods are nil-safe: Acquire always succeeds, Release/Active are no-ops.
	c.True(l.Acquire())
	c.True(l.Acquire())
	l.Release()
	c.Equal(int64(0), l.Active())
}

func Test_WebsocketConnectionLimiter_CapAndRelease(t *testing.T) {
	c := require.New(t)

	l := NewWebsocketConnectionLimiter(2)
	c.NotNil(l)

	c.True(l.Acquire(), "first slot should be acquired")
	c.True(l.Acquire(), "second slot should be acquired")
	c.Equal(int64(2), l.Active())

	// At capacity: further acquires are rejected and must not change the count.
	c.False(l.Acquire(), "third slot should be rejected at capacity")
	c.Equal(int64(2), l.Active(), "rejected acquire must not leak a slot")

	// Releasing frees a slot so the next acquire succeeds.
	l.Release()
	c.Equal(int64(1), l.Active())
	c.True(l.Acquire(), "slot should be available again after release")
	c.Equal(int64(2), l.Active())
}

func Test_WebsocketConnectionLimiter_ConcurrentNeverExceedsCap(t *testing.T) {
	c := require.New(t)

	const max = 8
	l := NewWebsocketConnectionLimiter(max)

	// Hammer Acquire from many goroutines; the number that succeed must never
	// exceed the cap, and Active must never read above the cap.
	var wg sync.WaitGroup
	var granted atomic.Int64
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Acquire() {
				granted.Add(1)
			}
			c.LessOrEqual(l.Active(), int64(max))
		}()
	}
	wg.Wait()

	c.Equal(int64(max), granted.Load(), "exactly cap slots should be granted")
	c.Equal(int64(max), l.Active())
}
