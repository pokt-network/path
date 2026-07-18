package websockets

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	url    string
	replay [][]byte
	calls  int32
	errN   int32
}

func (m *mockReconnector) ReconnectEndpoint(_ context.Context) (*websocket.Conn, error) {
	n := atomic.AddInt32(&m.calls, 1)
	if n <= atomic.LoadInt32(&m.errN) {
		return nil, fmt.Errorf("mock reconnect failure %d", n)
	}
	conn, _, err := websocket.DefaultDialer.Dial(m.url, nil)
	return conn, err
}

func (m *mockReconnector) SubscriptionReplayFrames() ([][]byte, error) {
	return m.replay, nil
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
