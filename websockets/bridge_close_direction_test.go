package websockets

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/observation"
)

// PATH sits between two peers and is a different role to each:
//
//	external client <--(PATH is the server)-- PATH --(PATH is the client)--> relay miner
//
// so one close code cannot be right for both. shutdown() previously wrote the SAME frame
// to each, which sent the relay miner 1012 "service restarting, please reconnect" —
// a server→client code telling a server to reconnect to us.
//
// This reads the close frame off BOTH ends of one real bridge in a single test, because
// the defect is precisely that the two directions were not distinguished; a test that
// looked at either side alone would have passed before the fix.
func Test_Bridge_ShutdownSendsDirectionAppropriateCloseCodes(t *testing.T) {
	c := require.New(t)

	endpointClosed := make(chan *websocket.CloseError, 1)

	// Endpoint side: upgrade, then read until PATH closes and report what it saw.
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				closeErr, ok := err.(*websocket.CloseError)
				if !ok {
					closeErr = &websocket.CloseError{Code: websocket.CloseAbnormalClosure, Text: err.Error()}
				}
				endpointClosed <- closeErr
				return
			}
		}
	}))
	defer endpoint.Close()

	// tumbleAwareReconnector implements BridgeAttacher, which is how StartBridge hands out
	// the controller in production — no reaching into bridge internals.
	reconnector := &tumbleAwareReconnector{mockReconnector: &mockReconnector{url: wsURL(endpoint)}}
	processor := &mockWebsocketMessageProcessor{}
	obsChan := make(chan *observation.RequestResponseObservations, 100)

	// Waited on at the end: the bridge goroutine reads package-level vars that other tests
	// in this package mutate and restore, so letting it outlive the test races them.
	bridgeDone := make(chan (<-chan struct{}), 1)
	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		completion, err := StartBridge(
			context.Background(), polyzero.NewLogger(), r, w,
			wsURL(endpoint), http.Header{}, processor, obsChan, reconnector,
		)
		c.NoError(err)
		bridgeDone <- completion
	}))
	defer clientServer.Close()

	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL(clientServer), nil)
	c.NoError(err)
	defer clientConn.Close()

	var controller BridgeController
	require.Eventually(t, func() bool {
		controller = reconnector.liveController()
		return controller != nil
	}, 5*time.Second, 10*time.Millisecond, "bridge never attached")

	// The gateway is terminating — the exact path a rollout takes.
	go controller.Close("gateway shutting down")

	// Client side: PATH is the server here, so 1012 "come back" is correct and useful.
	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, clientErr := clientConn.ReadMessage()
	c.Error(clientErr)
	c.True(
		websocket.IsCloseError(clientErr, websocket.CloseServiceRestart),
		"client should be told the service is restarting so it reconnects, got: %v", clientErr,
	)

	// Endpoint side: PATH is the CLIENT here. 1012 would tell a server to reconnect to us.
	select {
	case closeErr := <-endpointClosed:
		c.Equal(websocket.CloseGoingAway, closeErr.Code,
			"endpoint must be told the peer that dialed it is going away (1001), not handed a server→client code: %v", closeErr)
		c.NotEqual(websocket.CloseServiceRestart, closeErr.Code)
		c.NotEqual(websocket.CloseAbnormalClosure, closeErr.Code,
			"endpoint must not see the socket simply vanish")
	case <-time.After(5 * time.Second):
		t.Fatal("endpoint never observed a close")
	}

	select {
	case completion := <-bridgeDone:
		select {
		case <-completion:
		case <-time.After(5 * time.Second):
			t.Error("bridge goroutine did not exit")
		}
	case <-time.After(5 * time.Second):
		t.Error("bridge never started")
	}
}

// endpointCloseCode is the whole rule; pin it directly so the intent survives independent
// of the bridge wiring above.
func Test_endpointCloseCode_RemapsServerOnlyCodes(t *testing.T) {
	t.Parallel()
	c := require.New(t)

	// Server→client codes are meaningless addressed to a server we dialed.
	c.Equal(websocket.CloseGoingAway, endpointCloseCode(websocket.CloseServiceRestart))
	c.Equal(websocket.CloseGoingAway, endpointCloseCode(websocket.CloseTryAgainLater))
	c.Equal(websocket.CloseGoingAway, endpointCloseCode(websocket.CloseInternalServerErr))

	// Codes that mean the same thing in both directions pass through untouched.
	c.Equal(websocket.CloseNormalClosure, endpointCloseCode(websocket.CloseNormalClosure))
	c.Equal(websocket.CloseGoingAway, endpointCloseCode(websocket.CloseGoingAway))

	// Application codes are propagated deliberately — 4000 is the relay miner's own
	// session-expiry close coming back to it.
	c.Equal(4000, endpointCloseCode(4000))
}
