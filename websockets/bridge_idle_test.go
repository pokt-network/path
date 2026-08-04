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

// OnIdleTimeout implements IdleReporter on the shared test reconnector.
func (m *mockReconnector) OnIdleTimeout(time.Duration) {
	atomic.AddInt32(&m.idleReported, 1)
}

// shrinkIdleBounds compresses the reaper's bounds so a test observes in milliseconds what
// production observes in half an hour. Mutates package vars, so callers must not be
// parallel.
func shrinkIdleBounds(t *testing.T, threshold, interval time.Duration) {
	t.Helper()
	origThreshold, origInterval := idleConnectionThreshold, idleCheckInterval
	idleConnectionThreshold, idleCheckInterval = threshold, interval
	t.Cleanup(func() {
		idleConnectionThreshold, idleCheckInterval = origThreshold, origInterval
	})
}

// startIdleTestBridge wires a bridge between a live client and a silent endpoint, and
// returns the client connection.
func startIdleTestBridge(t *testing.T, reconnector *mockReconnector) *websocket.Conn {
	t.Helper()
	c := require.New(t)

	endpoint := httptest.NewServer(http.HandlerFunc(upgradeAndStaySilent))
	t.Cleanup(endpoint.Close)

	// The reconnector dials this same silent endpoint if anything ever rebinds; nothing in
	// these tests should.
	reconnector.url = wsURL(endpoint)

	processor := &mockWebsocketMessageProcessor{}
	obsChan := make(chan *observation.RequestResponseObservations, 100)

	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := StartBridge(
			context.Background(), polyzero.NewLogger(), r, w,
			wsURL(endpoint), http.Header{}, processor, obsChan, reconnector,
		)
		c.NoError(err)
	}))
	t.Cleanup(clientServer.Close)

	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL(clientServer), nil)
	c.NoError(err)
	t.Cleanup(func() { _ = clientConn.Close() })

	return clientConn
}

// Test_Bridge_IdleReaperClosesSubscriptionlessSilentClient covers the case the reaper
// exists for: a client that opens a connection, never subscribes, and never sends anything.
// Ping/pong keeps such a socket alive indefinitely and the staleness watchdog deliberately
// ignores it (no subscription to arm on), so before the reaper nothing closed it.
//
// It must close with 1000 (Normal Closure) — nothing failed, and a reconnect-guidance code
// would just invite the client to re-create the connection that was reaped.
func Test_Bridge_IdleReaperClosesSubscriptionlessSilentClient(t *testing.T) {
	shrinkIdleBounds(t, 40*time.Millisecond, 10*time.Millisecond)

	c := require.New(t)
	reconnector := &mockReconnector{hasSubs: false}
	clientConn := startIdleTestBridge(t, reconnector)

	// Read until the close frame arrives; the client sends nothing, so it stays idle.
	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := clientConn.ReadMessage()
	c.Error(err, "idle client must be closed by the reaper")
	c.True(
		websocket.IsCloseError(err, websocket.CloseNormalClosure),
		"idle reap must close with 1000 Normal Closure, got: %v", err,
	)

	c.Equal(int32(1), atomic.LoadInt32(&reconnector.idleReported), "the reap must be reported once")
	// A reap is not a rebind: nothing should have been reselected or reported as a stall.
	c.Equal(int32(0), atomic.LoadInt32(&reconnector.calls))
	c.Equal(int32(0), atomic.LoadInt32(&reconnector.stallRebindReported))
}

// Test_Bridge_IdleReaperSparesSubscriber is the guard that matters most. A subscription to a
// rare event is legitimately silent — potentially for hours — and reaping it would break a
// correct client. The subscription check must exempt it no matter how long the silence runs,
// with the endpoint side silent too so only HasActiveSubscriptions can be what saves it.
func Test_Bridge_IdleReaperSparesSubscriber(t *testing.T) {
	shrinkIdleBounds(t, 20*time.Millisecond, 5*time.Millisecond)
	// Keep the staleness watchdog out of it: this test is about the reaper, and a silent
	// endpoint with a subscription is exactly what that other watchdog acts on.
	origStaleness := endpointStalenessThreshold
	endpointStalenessThreshold = time.Hour
	t.Cleanup(func() { endpointStalenessThreshold = origStaleness })

	c := require.New(t)
	reconnector := &mockReconnector{hasSubs: true}
	clientConn := startIdleTestBridge(t, reconnector)

	// Well past several reap intervals: the connection must still be open.
	_ = clientConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _, err := clientConn.ReadMessage()
	c.Error(err, "no message is expected — only a timeout")
	c.False(
		websocket.IsCloseError(err, websocket.CloseNormalClosure),
		"a subscriber must never be reaped for idleness, got close: %v", err,
	)
	c.Equal(int32(0), atomic.LoadInt32(&reconnector.idleReported))
}

// Test_Bridge_IdleReaperSparesActiveJSONRPCClient covers the other exempt shape: a websocket
// JSON-RPC client holds no subscription at all, so the subscription check alone would reap
// it. Client traffic is what keeps it alive, and the clock must reset on each frame rather
// than only at connect.
func Test_Bridge_IdleReaperSparesActiveJSONRPCClient(t *testing.T) {
	shrinkIdleBounds(t, 100*time.Millisecond, 10*time.Millisecond)

	c := require.New(t)
	reconnector := &mockReconnector{hasSubs: false}
	clientConn := startIdleTestBridge(t, reconnector)

	// Send well inside the threshold, for longer than the threshold in total. If the clock
	// only started at connect, the reaper would fire partway through this loop.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := clientConn.WriteMessage(websocket.TextMessage, []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber"}`)); err != nil {
			c.FailNow("client write failed — connection was closed while active: " + err.Error())
		}
		time.Sleep(25 * time.Millisecond)
	}

	c.Equal(int32(0), atomic.LoadInt32(&reconnector.idleReported), "an active client must never be reaped")
}

// Test_Bridge_IdleReaperDisabled verifies the off-switch: a non-positive threshold means the
// ticker is never armed, so an idle subscription-less client survives.
func Test_Bridge_IdleReaperDisabled(t *testing.T) {
	shrinkIdleBounds(t, 0, 5*time.Millisecond)

	c := require.New(t)
	reconnector := &mockReconnector{hasSubs: false}
	clientConn := startIdleTestBridge(t, reconnector)

	_ = clientConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _, err := clientConn.ReadMessage()
	c.Error(err, "no message is expected — only a timeout")
	c.False(
		websocket.IsCloseError(err, websocket.CloseNormalClosure),
		"reaping is disabled at threshold <= 0, got close: %v", err,
	)
	c.Equal(int32(0), atomic.LoadInt32(&reconnector.idleReported))
}

// Test_SetIdleConnectionThreshold verifies the startup setter the config/env override uses.
func Test_SetIdleConnectionThreshold(t *testing.T) {
	c := require.New(t)
	orig := idleConnectionThreshold
	t.Cleanup(func() { idleConnectionThreshold = orig })

	SetIdleConnectionThreshold(45 * time.Minute)
	c.Equal(45*time.Minute, idleConnectionThreshold)

	SetIdleConnectionThreshold(-1)
	c.Equal(time.Duration(-1), idleConnectionThreshold, "a negative value must survive as an explicit disable")
}
