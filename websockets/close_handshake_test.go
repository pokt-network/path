package websockets

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// TestCloseEndpointConn_SendsCloseFrameInsteadOfDroppingTheSocket asserts on what the
// ENDPOINT observes, which is the only thing that matters here: the whole defect was that
// PATH's teardowns looked like abnormal disconnects to the node operator.
//
// Asserting that we called a close helper would prove nothing — the bug was that a bare
// Close() produces a read error on the peer rather than a close frame, so the test has to
// stand up a real websocket server and read what arrives.
func TestCloseEndpointConn_SendsCloseFrameInsteadOfDroppingTheSocket(t *testing.T) {
	t.Parallel()

	closeErr := make(chan error, 1)
	upgrader := websocket.Upgrader{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			closeErr <- err
			return
		}
		defer conn.Close()
		// Read until the peer goes away; the error carries how it went away.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				closeErr <- err
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)

	CloseEndpointConn(conn, websocket.CloseNormalClosure, "health check complete")

	select {
	case err := <-closeErr:
		require.Error(t, err, "endpoint should observe the connection ending")

		// A bare Close() surfaces as 1006 abnormal closure / unexpected EOF, which is
		// exactly what operators reported. gorilla synthesizes 1006 for "peer vanished",
		// so its ABSENCE is the assertion.
		require.False(t,
			websocket.IsCloseError(err, websocket.CloseAbnormalClosure),
			"endpoint saw an abnormal closure (1006) — the close handshake did not happen: %v", err,
		)
		require.NotContains(t, err.Error(), "unexpected EOF",
			"endpoint saw a truncated read rather than a close frame: %v", err)

		// And the code it did see is the one we chose.
		require.True(t,
			websocket.IsCloseError(err, websocket.CloseNormalClosure),
			"expected 1000 normal closure, got: %v", err,
		)
	case <-time.After(5 * time.Second):
		t.Fatal("endpoint never observed the connection closing")
	}
}

// TestCloseEndpointConn_NilIsANoOp keeps the helper safe on teardown paths that may run
// before a connection exists.
func TestCloseEndpointConn_NilIsANoOp(t *testing.T) {
	t.Parallel()
	require.NotPanics(t, func() { CloseEndpointConn(nil, websocket.CloseNormalClosure, "") })
}

// TestCloseEndpointConn_DoesNotBlockOnADeadPeer pins the best-effort contract: teardown
// frequently begins BECAUSE the peer already went away, and a rebind must not stall being
// polite to a socket that is not there.
func TestCloseEndpointConn_DoesNotBlockOnADeadPeer(t *testing.T) {
	t.Parallel()

	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Hang up immediately, without a close handshake.
		_ = conn.UnderlyingConn().Close()
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		CloseEndpointConn(conn, websocket.CloseNormalClosure, "peer already gone")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(closeHandshakeTimeout + 3*time.Second):
		t.Fatal("CloseEndpointConn blocked on a dead peer")
	}
}
