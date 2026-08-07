package shannon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

func Test_websocketShutdownAll_ClosesEveryConnectionAcrossServices(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	eth := registerN(r, protocol.ServiceID("eth"), "one.example", 3)
	gnosis := registerN(r, protocol.ServiceID("gnosis"), "two.example", 2)

	closed := r.shutdownAll(context.Background(), "gateway shutting down")

	c.Equal(5, closed)
	for _, ctrl := range append(append([]*fakeController{}, eth...), gnosis...) {
		c.Equal(1, ctrl.closeCount(), "every live connection must be closed, not just one service's")
	}
}

func Test_websocketShutdownAll_EmptyRegistryIsANoOp(t *testing.T) {
	c := require.New(t)
	c.Equal(0, newWebsocketConnRegistry().shutdownAll(context.Background(), "gateway shutting down"))
}

// The real bridge's Close() runs shutdown(), which calls AttachBridge(nil) -> deregister,
// taking the registry's write lock. Sweeping while holding even the read lock therefore
// self-deadlocks against the very thing Close is supposed to do.
//
// This is the failure the snapshot-then-release in shutdownAll exists to prevent, and it
// is invisible to a fake that only records the call — so this fake deregisters itself,
// exactly like production.
func Test_websocketShutdownAll_DoesNotDeadlockWhenCloseDeregisters(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()
	svc := protocol.ServiceID("eth")

	const n = 8
	for i := 0; i < n; i++ {
		wrc := &websocketRequestContext{}
		ctrl := &deregisteringController{registry: r, serviceID: svc, wrc: wrc}
		r.register(svc, wrc, ctrl, "one.example", "supplier-one", func() uint64 { return 0 })
	}

	done := make(chan int, 1)
	go func() {
		done <- r.shutdownAll(context.Background(), "gateway shutting down")
	}()

	select {
	case closed := <-done:
		c.Equal(n, closed)
	case <-time.After(10 * time.Second):
		t.Fatal("shutdownAll deadlocked against deregister — the snapshot must be taken before the lock is released")
	}

	// And the registry is genuinely drained, not merely unblocked.
	r.mu.RLock()
	remaining := len(r.conns)
	r.mu.RUnlock()
	c.Zero(remaining)
}

// A pod that cannot finish being polite inside its grace period must still exit. Equally,
// a sweep that timed out having closed half the connections must not report a clean sweep
// — the count is what completed, not what was attempted.
func Test_websocketShutdownAll_HonoursTheDeadlineAndReportsOnlyWhatClosed(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()
	svc := protocol.ServiceID("eth")

	// One connection whose peer never answers the close handshake, and one that does.
	stuck := &fakeController{closeAt: make(chan struct{})}
	r.register(svc, &websocketRequestContext{}, stuck, "slow.example", "supplier-slow", func() uint64 { return 0 })
	quick := registerN(r, svc, "fast.example", 1)[0]

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	closed := r.shutdownAll(ctx, "gateway shutting down")
	elapsed := time.Since(start)

	c.Less(elapsed, 5*time.Second, "a hung peer must not hold up process exit")
	c.Equal(1, closed, "only the connection that actually closed may be counted")
	c.Equal(1, quick.closeCount())

	// Release the stuck close so the goroutine does not outlive the test.
	close(stuck.closeAt)
}

// deregisteringController mimics a real bridge: closing it removes it from the registry,
// which needs the registry's write lock.
type deregisteringController struct {
	registry  *websocketConnRegistry
	serviceID protocol.ServiceID
	wrc       *websocketRequestContext

	mu     sync.Mutex
	closed bool
}

func (d *deregisteringController) Tumble() bool { return false }

func (d *deregisteringController) Close(string) {
	d.registry.deregister(d.serviceID, d.wrc)
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
}
