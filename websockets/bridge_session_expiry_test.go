package websockets

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/observation"
)

// sessionAwareReconnector adds the optional session-expiry interfaces to the base mock, so a
// test can drive "my bound session has ended" independently of endpoint health.
type sessionAwareReconnector struct {
	*mockReconnector
	expired          atomic.Bool
	expiryChecks     atomic.Int32
	expiryReported   atomic.Int32
	clearOnReconnect bool // emulate a successful rebind landing on the current session
}

func (s *sessionAwareReconnector) BoundSessionExpired() bool {
	s.expiryChecks.Add(1)
	return s.expired.Load()
}

func (s *sessionAwareReconnector) OnSessionExpiryRebindRequested() {
	s.expiryReported.Add(1)
}

func (s *sessionAwareReconnector) ReconnectEndpoint(ctx context.Context, avoidCurrentSupplier bool) (*websocket.Conn, error) {
	conn, err := s.mockReconnector.ReconnectEndpoint(ctx, avoidCurrentSupplier)
	if err == nil && s.clearOnReconnect {
		// A rebind lands on the current session, so the next check is healthy.
		s.expired.Store(false)
	}
	return conn, err
}

// startSessionBridge wires a bridge whose endpoint stays connected and silent — the state in
// which no other rebind trigger fires.
func startSessionBridge(
	t *testing.T,
	reconnector EndpointReconnector,
	endpointURL string,
) (*websocket.Conn, chan *observation.RequestResponseObservations) {
	t.Helper()
	c := require.New(t)

	processor := &mockWebsocketMessageProcessor{}
	obsChan := make(chan *observation.RequestResponseObservations, 100)

	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := StartBridge(
			context.Background(), polyzero.NewLogger(), r, w,
			endpointURL, http.Header{}, processor, obsChan, reconnector,
		)
		c.NoError(err)
	}))
	t.Cleanup(clientServer.Close)

	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL(clientServer), nil)
	c.NoError(err)
	t.Cleanup(func() { _ = clientConn.Close() })
	return clientConn, obsChan
}

// withFastSessionChecks shrinks the watchdog interval for the duration of a test.
func withFastSessionChecks(t *testing.T, every time.Duration) {
	t.Helper()
	orig := sessionExpiryCheckInterval
	sessionExpiryCheckInterval = every
	t.Cleanup(func() { sessionExpiryCheckInterval = orig })
}

// The defect this exists for: a supplier that keeps streaming past the end of its own session
// never disconnects, and because data is still flowing the staleness watchdog stays quiet. The
// connection is stranded outside the session with nothing to move it.
func Test_Bridge_SessionExpiry_RebindsAStrandedConnection(t *testing.T) {
	c := require.New(t)
	withFastSessionChecks(t, 20*time.Millisecond)

	// Replacement endpoint proves the client ends up served by the new binding.
	endpoint2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte("post-session-rebind"))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer endpoint2.Close()

	// Original endpoint: connected, healthy, never hangs up — exactly the stranding case.
	endpoint1 := httptest.NewServer(http.HandlerFunc(upgradeAndStaySilent))
	defer endpoint1.Close()

	reconnector := &sessionAwareReconnector{
		mockReconnector:  &mockReconnector{url: wsURL(endpoint2), hasSubs: true},
		clearOnReconnect: true,
	}
	clientConn, _ := startSessionBridge(t, reconnector, wsURL(endpoint1))

	// Healthy session: the watchdog must run and must NOT rebind.
	require.Eventually(t, func() bool { return reconnector.expiryChecks.Load() >= 3 },
		2*time.Second, 10*time.Millisecond, "the session watchdog must actually run")
	c.Equal(int32(0), atomic.LoadInt32(&reconnector.calls),
		"a current session must never trigger a rebind")

	// The session ends and the supplier keeps streaming.
	reconnector.expired.Store(true)

	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&reconnector.outcomeSuccess) == 1
	}, 2*time.Second, 10*time.Millisecond, "an expired bound session must force a rebind")

	c.Equal(int32(1), reconnector.expiryReported.Load(),
		"the rebind must be reported as session-expiry so it is not labelled a rollover")

	// The client is untouched and now served by the replacement.
	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := clientConn.ReadMessage()
	c.NoError(err, "the client must stay connected across a session-expiry rebind")
	c.Equal("post-session-rebind", string(msg))
}

// A session-expiry rebind takes the ORDINARY rollover path: the supplier may well still be in
// the new session, and reusing it is the seamless outcome. Only stalls and tumbles must land
// elsewhere.
func Test_Bridge_SessionExpiry_DoesNotAvoidTheCurrentSupplier(t *testing.T) {
	c := require.New(t)
	withFastSessionChecks(t, 20*time.Millisecond)

	endpoint2 := httptest.NewServer(http.HandlerFunc(upgradeAndStaySilent))
	defer endpoint2.Close()
	endpoint1 := httptest.NewServer(http.HandlerFunc(upgradeAndStaySilent))
	defer endpoint1.Close()

	reconnector := &sessionAwareReconnector{
		mockReconnector:  &mockReconnector{url: wsURL(endpoint2), hasSubs: true},
		clearOnReconnect: true,
	}
	startSessionBridge(t, reconnector, wsURL(endpoint1))

	reconnector.expired.Store(true)
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&reconnector.outcomeSuccess) == 1
	}, 2*time.Second, 10*time.Millisecond)

	c.Equal(int32(0), atomic.LoadInt32(&reconnector.avoidCurrentCalls),
		"session expiry needs a live session, not a different operator")
}

// A stranded connection is still delivering data to its subscriber. If we cannot move it, the
// right outcome is to stop trying — never to close the client, and never to rebind on every
// tick forever.
func Test_Bridge_SessionExpiry_GivesUpQuietlyWithoutClosingTheClient(t *testing.T) {
	c := require.New(t)
	withFastSessionChecks(t, 20*time.Millisecond)

	endpoint2 := httptest.NewServer(http.HandlerFunc(upgradeAndStaySilent))
	defer endpoint2.Close()
	endpoint1 := httptest.NewServer(http.HandlerFunc(upgradeAndStaySilent))
	defer endpoint1.Close()

	// clearOnReconnect=false: every rebind lands on a still-expired session, the pathological
	// case the cap exists for.
	reconnector := &sessionAwareReconnector{
		mockReconnector:  &mockReconnector{url: wsURL(endpoint2), hasSubs: true},
		clearOnReconnect: false,
	}
	clientConn, _ := startSessionBridge(t, reconnector, wsURL(endpoint1))
	reconnector.expired.Store(true)

	// It must stop at the cap rather than rebinding on every tick.
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&reconnector.outcomeSuccess) >= int32(maxConsecutiveSessionRebinds)
	}, 3*time.Second, 10*time.Millisecond)

	before := atomic.LoadInt32(&reconnector.calls)
	checksBefore := reconnector.expiryChecks.Load()
	require.Eventually(t, func() bool { return reconnector.expiryChecks.Load() >= checksBefore+5 },
		2*time.Second, 10*time.Millisecond, "the watchdog must keep ticking")
	c.LessOrEqual(atomic.LoadInt32(&reconnector.calls), int32(maxConsecutiveSessionRebinds),
		"rebinds must stop at the cap, not continue every tick")
	c.Equal(before, atomic.LoadInt32(&reconnector.calls))

	// Crucially the client is still alive — an out-of-session connection still delivers data,
	// so dropping it would be worse than leaving it.
	c.Equal(int32(0), atomic.LoadInt32(&reconnector.stallGiveupReported),
		"session expiry must never feed the stall give-up path that closes clients")
	_ = clientConn.SetWriteDeadline(time.Now().Add(time.Second))
	c.NoError(clientConn.WriteMessage(websocket.TextMessage, []byte("still here")),
		"the client connection must remain usable after giving up on rebinding")
}

// A reconnector that does not implement the optional interface must be entirely unaffected —
// no ticker, no checks, no behaviour change.
func Test_Bridge_SessionExpiry_InertWithoutTheOptionalInterface(t *testing.T) {
	c := require.New(t)
	withFastSessionChecks(t, 20*time.Millisecond)

	endpoint2 := httptest.NewServer(http.HandlerFunc(upgradeAndStaySilent))
	defer endpoint2.Close()
	endpoint1 := httptest.NewServer(http.HandlerFunc(upgradeAndStaySilent))
	defer endpoint1.Close()

	plain := &mockReconnector{url: wsURL(endpoint2), hasSubs: true}
	clientConn, _ := startSessionBridge(t, plain, wsURL(endpoint1))

	time.Sleep(300 * time.Millisecond) // many ticker periods
	c.Equal(int32(0), atomic.LoadInt32(&plain.calls),
		"a reconnector without the interface must never be rebound by this path")

	_ = clientConn.SetWriteDeadline(time.Now().Add(time.Second))
	c.NoError(clientConn.WriteMessage(websocket.TextMessage, []byte("ok")))
}
