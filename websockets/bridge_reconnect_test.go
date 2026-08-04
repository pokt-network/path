package websockets

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/observation"
)

// mockReconnector is a test EndpointReconnector. It dials `url` for a fresh endpoint
// connection (after failing the first `errN` attempts) and returns `replay` as the
// subscription replay frames.
type mockReconnector struct {
	url                 string
	replay              [][]byte
	hasSubs             bool // value returned by HasActiveSubscriptions (arms the watchdog)
	calls               int32
	errN                int32
	outcomeSuccess      int32
	outcomeFailed       int32
	replayedReported    int32
	selectStageReported int32
	avoidCurrentCalls   int32 // ReconnectEndpoint calls that requested a different supplier
	stallRebindReported int32 // OnEndpointStallDetected(gaveUp=false)
	stallGiveupReported int32 // OnEndpointStallDetected(gaveUp=true)
	idleReported        int32 // OnIdleTimeout — see bridge_idle_test.go
}

func (m *mockReconnector) ReconnectEndpoint(_ context.Context, avoidCurrentSupplier bool) (*websocket.Conn, error) {
	n := atomic.AddInt32(&m.calls, 1)
	if avoidCurrentSupplier {
		atomic.AddInt32(&m.avoidCurrentCalls, 1)
	}
	if n <= atomic.LoadInt32(&m.errN) {
		return nil, fmt.Errorf("mock reconnect failure %d", n)
	}
	conn, _, err := websocket.DefaultDialer.Dial(m.url, nil)
	return conn, err
}

func (m *mockReconnector) SubscriptionReplayFrames() ([][]byte, error) {
	return m.replay, nil
}

func (m *mockReconnector) HasActiveSubscriptions() bool {
	return m.hasSubs
}

func (m *mockReconnector) OnReconnectOutcome(success bool, replayedSubscriptions int, stage ReconnectFailureStage) {
	if success {
		atomic.AddInt32(&m.outcomeSuccess, 1)
	} else {
		atomic.AddInt32(&m.outcomeFailed, 1)
		if stage == ReconnectStageSelect {
			atomic.AddInt32(&m.selectStageReported, 1)
		}
	}
	atomic.AddInt32(&m.replayedReported, int32(replayedSubscriptions))
}

func (m *mockReconnector) OnEndpointStallDetected(gaveUp bool) {
	if gaveUp {
		atomic.AddInt32(&m.stallGiveupReported, 1)
	} else {
		atomic.AddInt32(&m.stallRebindReported, 1)
	}
}

// upgradeAndClose upgrades a websocket request then immediately closes it, simulating
// a relay miner dropping the endpoint connection at a session boundary.
func upgradeAndClose(w http.ResponseWriter, r *http.Request) {
	conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.Close()
}

func wsURL(s *httptest.Server) string { return "ws" + strings.TrimPrefix(s.URL, "http") }

// Test_Bridge_ReconnectKeepsClientAliveAndReplays proves the core of session rebind:
// when the endpoint connection drops (session rollover), the bridge reconnects to a new
// endpoint, replays the client's subscriptions onto it, and keeps the client connection
// open — the client observes a brief gap, never a close.
func Test_Bridge_ReconnectKeepsClientAliveAndReplays(t *testing.T) {
	c := require.New(t)

	replayGot := make(chan string, 4)

	// Endpoint #2 (the new session): read the replayed subscribe frames, then push a
	// post-reconnect notification and stay alive.
	endpoint2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for i := 0; i < 2; i++ {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			replayGot <- string(msg)
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte("post-reconnect-head"))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer endpoint2.Close()

	// Endpoint #1 (the expiring session): upgrade then drop, forcing a reconnect.
	endpoint1 := httptest.NewServer(http.HandlerFunc(upgradeAndClose))
	defer endpoint1.Close()

	reconnector := &mockReconnector{
		url: wsURL(endpoint2),
		replay: [][]byte{
			[]byte(`{"id":1,"method":"eth_subscribe","params":["newHeads"]}`),
			[]byte(`{"id":2,"method":"eth_subscribe","params":["logs"]}`),
		},
	}
	processor := &mockWebsocketMessageProcessor{}
	obsChan := make(chan *observation.RequestResponseObservations, 100)

	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := StartBridge(
			context.Background(), polyzero.NewLogger(), r, w,
			wsURL(endpoint1), http.Header{}, processor, obsChan, reconnector,
		)
		c.NoError(err)
	}))
	defer clientServer.Close()

	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL(clientServer), nil)
	c.NoError(err)
	defer clientConn.Close()

	// The client stays open across the rollover and receives the post-reconnect message.
	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := clientConn.ReadMessage()
	c.NoError(err, "client must stay open across the endpoint reconnect")
	c.Equal("post-reconnect-head", string(msg))

	// Both subscriptions were replayed onto the new endpoint.
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case f := <-replayGot:
			got[f] = true
		case <-time.After(2 * time.Second):
			t.Fatal("did not receive expected replay frame")
		}
	}
	c.True(got[`{"id":1,"method":"eth_subscribe","params":["newHeads"]}`], "newHeads subscribe replayed")
	c.True(got[`{"id":2,"method":"eth_subscribe","params":["logs"]}`], "logs subscribe replayed")
	c.Equal(int32(1), atomic.LoadInt32(&reconnector.calls), "exactly one reconnect")

	// Instrumentation: the success outcome + replayed count are reported for metrics.
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&reconnector.outcomeSuccess) == 1 &&
			atomic.LoadInt32(&reconnector.replayedReported) == 2
	}, 2*time.Second, 10*time.Millisecond, "rebind success outcome (2 subs) should be reported")
	c.Equal(int32(0), atomic.LoadInt32(&reconnector.outcomeFailed))
}

// Test_Bridge_ReconnectExhaustionClosesClient verifies that when every reconnect
// attempt fails, the bridge gives up after the bounded retries and closes the client
// with 1012 (service restart) — the pre-rebind fallback — rather than looping forever.
func Test_Bridge_ReconnectExhaustionClosesClient(t *testing.T) {
	// Shrink the backoff so the bounded retries complete quickly. Not parallel: this
	// mutates a package var.
	origBase := reconnectBaseBackoff
	reconnectBaseBackoff = time.Millisecond
	defer func() { reconnectBaseBackoff = origBase }()

	c := require.New(t)

	endpoint1 := httptest.NewServer(http.HandlerFunc(upgradeAndClose))
	defer endpoint1.Close()

	reconnector := &mockReconnector{errN: 1000} // every attempt fails
	processor := &mockWebsocketMessageProcessor{}
	obsChan := make(chan *observation.RequestResponseObservations, 10)

	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = StartBridge(
			context.Background(), polyzero.NewLogger(), r, w,
			wsURL(endpoint1), http.Header{}, processor, obsChan, reconnector,
		)
	}))
	defer clientServer.Close()

	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL(clientServer), nil)
	c.NoError(err)
	defer clientConn.Close()

	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, readErr := clientConn.ReadMessage()
	c.Error(readErr, "client should be closed after reconnect exhaustion")
	c.True(
		websocket.IsCloseError(readErr, websocket.CloseServiceRestart),
		"exhausted reconnect should close client with 1012, got: %v", readErr,
	)
	c.Equal(int32(reconnectMaxAttempts), atomic.LoadInt32(&reconnector.calls), "all attempts made")
	c.Equal(int32(1), atomic.LoadInt32(&reconnector.outcomeFailed), "failed outcome reported once")
	c.Equal(int32(0), atomic.LoadInt32(&reconnector.outcomeSuccess))
	c.Equal(int32(1), atomic.LoadInt32(&reconnector.selectStageReported), "failure stage should be 'select' (reconnect/dial)")
}

// upgradeAndStaySilent upgrades a websocket request then reads forever without ever
// writing a data frame — modeling a silent supplier stall: the connection is
// transport-alive (gorilla's read loop auto-answers pings with pongs) but delivers no
// subscription data, which ping/pong liveness cannot detect.
func upgradeAndStaySilent(w http.ResponseWriter, r *http.Request) {
	conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// Test_Bridge_StallWatchdogRebindsToDifferentSupplier proves the staleness watchdog: an
// endpoint that stays connected but delivers no subscription data past the threshold is
// rebound onto a DIFFERENT supplier (avoidCurrentSupplier=true), and the client stays
// open and receives data from the replacement.
func Test_Bridge_StallWatchdogRebindsToDifferentSupplier(t *testing.T) {
	// Shrink the watchdog bounds so the stall is detected in milliseconds. Not parallel:
	// mutates package vars.
	origThresh, origInterval := endpointStalenessThreshold, stalenessCheckInterval
	endpointStalenessThreshold = 40 * time.Millisecond
	stalenessCheckInterval = 10 * time.Millisecond
	defer func() {
		endpointStalenessThreshold, stalenessCheckInterval = origThresh, origInterval
	}()

	c := require.New(t)

	// Replacement endpoint (healthy): pushes a head and stays alive.
	endpoint2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte("post-stall-head"))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer endpoint2.Close()

	// Original endpoint: connects then goes silent (the stalling supplier).
	endpoint1 := httptest.NewServer(http.HandlerFunc(upgradeAndStaySilent))
	defer endpoint1.Close()

	reconnector := &mockReconnector{url: wsURL(endpoint2), hasSubs: true}
	processor := &mockWebsocketMessageProcessor{}
	obsChan := make(chan *observation.RequestResponseObservations, 100)

	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := StartBridge(
			context.Background(), polyzero.NewLogger(), r, w,
			wsURL(endpoint1), http.Header{}, processor, obsChan, reconnector,
		)
		c.NoError(err)
	}))
	defer clientServer.Close()

	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL(clientServer), nil)
	c.NoError(err)
	defer clientConn.Close()

	// The client stays open across the stall rebind and receives the replacement's head.
	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := clientConn.ReadMessage()
	c.NoError(err, "client must stay open across a stall-triggered rebind")
	c.Equal("post-stall-head", string(msg))

	// The rebind requested a DIFFERENT supplier, was reported as a stall rebind, and
	// succeeded.
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&reconnector.avoidCurrentCalls) == 1 &&
			atomic.LoadInt32(&reconnector.stallRebindReported) == 1 &&
			atomic.LoadInt32(&reconnector.outcomeSuccess) == 1
	}, 2*time.Second, 10*time.Millisecond, "stall rebind should avoid the current supplier and succeed")
	c.Equal(int32(0), atomic.LoadInt32(&reconnector.stallGiveupReported))
}

// Test_Bridge_StallWatchdogGivesUpAfterMaxRebinds verifies that when every replacement
// supplier is also silent, the watchdog stops after maxConsecutiveStallRebinds and closes
// the client with 1012 rather than churning forever.
func Test_Bridge_StallWatchdogGivesUpAfterMaxRebinds(t *testing.T) {
	origThresh, origInterval, origMax := endpointStalenessThreshold, stalenessCheckInterval, maxConsecutiveStallRebinds
	endpointStalenessThreshold = 40 * time.Millisecond
	stalenessCheckInterval = 10 * time.Millisecond
	maxConsecutiveStallRebinds = 2
	defer func() {
		endpointStalenessThreshold, stalenessCheckInterval, maxConsecutiveStallRebinds = origThresh, origInterval, origMax
	}()

	c := require.New(t)

	// One silent server used for the original connection AND every reconnect: nothing
	// ever delivers data, so each rebind stalls again.
	silent := httptest.NewServer(http.HandlerFunc(upgradeAndStaySilent))
	defer silent.Close()

	reconnector := &mockReconnector{url: wsURL(silent), hasSubs: true}
	processor := &mockWebsocketMessageProcessor{}
	obsChan := make(chan *observation.RequestResponseObservations, 10)

	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = StartBridge(
			context.Background(), polyzero.NewLogger(), r, w,
			wsURL(silent), http.Header{}, processor, obsChan, reconnector,
		)
	}))
	defer clientServer.Close()

	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL(clientServer), nil)
	c.NoError(err)
	defer clientConn.Close()

	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, readErr := clientConn.ReadMessage()
	c.Error(readErr, "client should be closed after the watchdog gives up")
	c.True(
		websocket.IsCloseError(readErr, websocket.CloseServiceRestart),
		"give-up should close client with 1012, got: %v", readErr,
	)
	c.Equal(int32(2), atomic.LoadInt32(&reconnector.stallRebindReported), "two stall rebinds before giving up")
	c.Equal(int32(1), atomic.LoadInt32(&reconnector.stallGiveupReported), "give-up reported once")
	c.GreaterOrEqual(atomic.LoadInt32(&reconnector.avoidCurrentCalls), int32(2), "each stall rebind avoids the current supplier")
}

// panicProcessor panics on a client message — models a bug in message processing or
// (by extension) the rebind path that runs on the same bridge goroutine.
type panicProcessor struct{}

func (panicProcessor) ProcessClientWebsocketMessage([]byte) ([]byte, error) {
	panic("boom in client message processing")
}

func (panicProcessor) ProcessEndpointWebsocketMessage(b []byte) ([]byte, *observation.RequestResponseObservations, error) {
	return b, nil, nil
}

// Test_Bridge_PanicRecoveredClosesClient verifies that a panic on the bridge goroutine
// is recovered and closes the client connection cleanly instead of crashing the whole
// process. If recovery were missing, the panic would take down the test binary.
func Test_Bridge_PanicRecoveredClosesClient(t *testing.T) {
	c := require.New(t)

	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer endpoint.Close()

	obsChan := make(chan *observation.RequestResponseObservations, 10)
	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = StartBridge(
			context.Background(), polyzero.NewLogger(), r, w,
			wsURL(endpoint), http.Header{}, panicProcessor{}, obsChan, nil,
		)
	}))
	defer clientServer.Close()

	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL(clientServer), nil)
	c.NoError(err)
	defer clientConn.Close()

	// Trigger the panic path; the client message is processed on the bridge goroutine.
	_ = clientConn.WriteMessage(websocket.TextMessage, []byte("trigger-panic"))

	// The client must be closed (recovered), and — critically — reaching this assertion
	// at all proves the process did not crash.
	_ = clientConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, readErr := clientConn.ReadMessage()
	c.Error(readErr, "client should be closed after a recovered bridge panic")
}

// swallowProcessor swallows (returns nil, no error) any endpoint message equal to
// "SWALLOW" and echoes everything else. Models the subscription registry consuming a
// replay response so the client never sees it.
type swallowProcessor struct{}

func (swallowProcessor) ProcessClientWebsocketMessage(b []byte) ([]byte, error) { return b, nil }

func (swallowProcessor) ProcessEndpointWebsocketMessage(b []byte) ([]byte, *observation.RequestResponseObservations, error) {
	if string(b) == "SWALLOW" {
		return nil, nil, nil
	}
	return b, &observation.RequestResponseObservations{ServiceId: "test-service"}, nil
}

// Test_Bridge_SwallowedEndpointMessageNotForwarded verifies that an endpoint message
// the processor swallows (nil payload, no error) is not forwarded to the client, while
// subsequent messages still are.
func Test_Bridge_SwallowedEndpointMessageNotForwarded(t *testing.T) {
	c := require.New(t)

	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte("SWALLOW"))
		_ = conn.WriteMessage(websocket.TextMessage, []byte("KEEP"))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer endpoint.Close()

	obsChan := make(chan *observation.RequestResponseObservations, 10)
	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = StartBridge(
			context.Background(), polyzero.NewLogger(), r, w,
			wsURL(endpoint), http.Header{}, swallowProcessor{}, obsChan, nil,
		)
	}))
	defer clientServer.Close()

	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL(clientServer), nil)
	c.NoError(err)
	defer clientConn.Close()

	// The first message the client sees must be "KEEP": "SWALLOW" was dropped.
	_ = clientConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := clientConn.ReadMessage()
	c.NoError(err)
	c.Equal("KEEP", string(msg), "swallowed message must not reach the client")
}

// tumbleAwareReconnector is a mockReconnector that also implements BridgeAttacher and
// TumbleReporter, so a test can grab the live bridge's controller and assert that a
// tumble is reported distinctly from a stall.
type tumbleAwareReconnector struct {
	*mockReconnector

	mu             sync.Mutex
	controller     BridgeController
	detached       bool
	tumblesRequest int32
}

func (t *tumbleAwareReconnector) AttachBridge(ctl BridgeController) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ctl == nil {
		t.detached = true
		t.controller = nil
		return
	}
	t.controller = ctl
}

func (t *tumbleAwareReconnector) OnTumbleRequested() {
	atomic.AddInt32(&t.tumblesRequest, 1)
}

func (t *tumbleAwareReconnector) liveController() BridgeController {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.controller
}

func (t *tumbleAwareReconnector) isDetached() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.detached
}

// Test_Bridge_TumbleRebindsWithoutDroppingClient is the core guarantee of the admin
// tumble: an operator can move a live connection onto a different supplier and the client
// never notices — it stays connected and keeps receiving data from the replacement.
func Test_Bridge_TumbleRebindsWithoutDroppingClient(t *testing.T) {
	c := require.New(t)

	// Replacement endpoint: pushes a distinguishable frame so the test can prove the
	// client is now being served by it.
	endpoint2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte("post-tumble-head"))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer endpoint2.Close()

	// Original endpoint: healthy and quiet. A tumble must move it anyway — unlike the
	// stall watchdog, a tumble implies nothing about endpoint health.
	endpoint1 := httptest.NewServer(http.HandlerFunc(upgradeAndStaySilent))
	defer endpoint1.Close()

	reconnector := &tumbleAwareReconnector{
		mockReconnector: &mockReconnector{url: wsURL(endpoint2), hasSubs: true},
	}
	processor := &mockWebsocketMessageProcessor{}
	obsChan := make(chan *observation.RequestResponseObservations, 100)

	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := StartBridge(
			context.Background(), polyzero.NewLogger(), r, w,
			wsURL(endpoint1), http.Header{}, processor, obsChan, reconnector,
		)
		c.NoError(err)
	}))
	defer clientServer.Close()

	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL(clientServer), nil)
	c.NoError(err)
	defer clientConn.Close()

	// The bridge must have handed its controller to the reconnector on startup —
	// without that, nothing is tumble-able.
	var ctl BridgeController
	require.Eventually(t, func() bool {
		ctl = reconnector.liveController()
		return ctl != nil
	}, 2*time.Second, 10*time.Millisecond, "bridge must register itself for tumbling")

	c.True(ctl.Tumble(), "a live rebind-capable bridge must accept a tumble")

	// The client stays open and is served by the replacement endpoint.
	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := clientConn.ReadMessage()
	c.NoError(err, "client must stay connected across an operator tumble")
	c.Equal("post-tumble-head", string(msg))

	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&reconnector.avoidCurrentCalls) == 1 &&
			atomic.LoadInt32(&reconnector.outcomeSuccess) == 1
	}, 2*time.Second, 10*time.Millisecond, "tumble must reselect a DIFFERENT supplier and succeed")

	// A tumble is reported as a tumble, never as a stall — they are distinct metric
	// labels and conflating them would make an operator action look like an endpoint
	// fault.
	c.Equal(int32(1), atomic.LoadInt32(&reconnector.tumblesRequest))
	c.Equal(int32(0), atomic.LoadInt32(&reconnector.stallRebindReported),
		"an operator tumble must not be reported as a stall")
	c.Equal(int32(0), atomic.LoadInt32(&reconnector.stallGiveupReported))
}

// Test_Bridge_TumbleNeverClosesClientAfterRepeatedUse guards the give-up limit: the stall
// watchdog closes the client after maxConsecutiveStallRebinds, and a tumble must NOT feed
// that counter. Otherwise repeatedly redistributing connections would start killing them.
func Test_Bridge_TumbleNeverClosesClientAfterRepeatedUse(t *testing.T) {
	origMax := maxConsecutiveStallRebinds
	maxConsecutiveStallRebinds = 2
	defer func() { maxConsecutiveStallRebinds = origMax }()

	c := require.New(t)

	// Replacement endpoint that keeps accepting connections and stays quiet, so nothing
	// but the give-up logic could close the client.
	endpoint2 := httptest.NewServer(http.HandlerFunc(upgradeAndStaySilent))
	defer endpoint2.Close()
	endpoint1 := httptest.NewServer(http.HandlerFunc(upgradeAndStaySilent))
	defer endpoint1.Close()

	reconnector := &tumbleAwareReconnector{
		mockReconnector: &mockReconnector{url: wsURL(endpoint2), hasSubs: true},
	}
	processor := &mockWebsocketMessageProcessor{}
	obsChan := make(chan *observation.RequestResponseObservations, 100)

	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := StartBridge(
			context.Background(), polyzero.NewLogger(), r, w,
			wsURL(endpoint1), http.Header{}, processor, obsChan, reconnector,
		)
		c.NoError(err)
	}))
	defer clientServer.Close()

	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL(clientServer), nil)
	c.NoError(err)
	defer clientConn.Close()

	var ctl BridgeController
	require.Eventually(t, func() bool {
		ctl = reconnector.liveController()
		return ctl != nil
	}, 2*time.Second, 10*time.Millisecond)

	// Tumble well past the give-up limit, waiting for each to land so they do not
	// coalesce in the depth-1 channel.
	const tumbles = 5
	for i := 0; i < tumbles; i++ {
		ctl.Tumble()
		want := int32(i + 1)
		require.Eventually(t, func() bool {
			return atomic.LoadInt32(&reconnector.outcomeSuccess) >= want
		}, 2*time.Second, 5*time.Millisecond, "tumble %d should complete", i+1)
	}

	c.Equal(int32(0), atomic.LoadInt32(&reconnector.stallGiveupReported),
		"tumbling must never trip the stall give-up path")

	// The client is still alive: a ping round-trips.
	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	c.NoError(clientConn.WriteMessage(websocket.TextMessage, []byte("still-here")),
		"client must remain connected after repeated tumbles")
}

// Test_Bridge_TumbleDeregistersOnShutdown verifies the registry cannot be left holding a
// handle to a dead bridge.
func Test_Bridge_TumbleDeregistersOnShutdown(t *testing.T) {
	c := require.New(t)

	endpoint1 := httptest.NewServer(http.HandlerFunc(upgradeAndStaySilent))
	defer endpoint1.Close()

	reconnector := &tumbleAwareReconnector{
		mockReconnector: &mockReconnector{url: wsURL(endpoint1), hasSubs: false},
	}
	processor := &mockWebsocketMessageProcessor{}
	obsChan := make(chan *observation.RequestResponseObservations, 100)

	ctx, cancel := context.WithCancel(context.Background())
	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := StartBridge(
			ctx, polyzero.NewLogger(), r, w,
			wsURL(endpoint1), http.Header{}, processor, obsChan, reconnector,
		)
		c.NoError(err)
	}))
	defer clientServer.Close()

	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL(clientServer), nil)
	c.NoError(err)
	defer clientConn.Close()

	require.Eventually(t, func() bool {
		return reconnector.liveController() != nil
	}, 2*time.Second, 10*time.Millisecond)

	cancel() // tear the bridge down

	require.Eventually(t, func() bool {
		return reconnector.isDetached() && reconnector.liveController() == nil
	}, 2*time.Second, 10*time.Millisecond, "a shut-down bridge must deregister itself")
}
