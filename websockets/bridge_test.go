package websockets

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/observation"
)

func Test_Bridge_StartBridge(t *testing.T) {
	c := require.New(t)

	// Create a simple endpoint server
	endpointServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error("Error upgrading endpoint connection:", err)
			return
		}
		defer conn.Close()

		// Echo any messages received
		for {
			messageType, message, err := conn.ReadMessage()
			if err != nil {
				return // Connection closed
			}
			if err := conn.WriteMessage(messageType, message); err != nil {
				return
			}
		}
	}))
	defer endpointServer.Close()

	// Create mock message processor
	messageProcessor := &mockWebsocketMessageProcessor{}

	// Create channel for observation notifications
	observationsChan := make(chan *observation.RequestResponseObservations, 100)

	// Get the websocket URL for the endpoint
	endpointURL := "ws" + strings.TrimPrefix(endpointServer.URL, "http")

	// Create a test client connection
	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Start the bridge using the client request
		completionChan, err := StartBridge(
			context.Background(), // Use background context for tests
			polyzero.NewLogger(),
			r,
			w,
			endpointURL,
			http.Header{},
			messageProcessor,
			observationsChan,
			nil, // reconnector: rebind disabled in these tests
		)
		c.NoError(err)
		c.NotNil(completionChan, "Should receive completion channel")
	}))
	defer clientServer.Close()

	// Connect to the client server as a websocket client
	clientURL := "ws" + strings.TrimPrefix(clientServer.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(clientURL, nil)
	c.NoError(err)
	defer clientConn.Close()

	// Send a test message
	testMessage := "test message"
	err = clientConn.WriteMessage(websocket.TextMessage, []byte(testMessage))
	c.NoError(err)

	// Wait for processing and check for observations
	timeout := time.After(2 * time.Second)
	select {
	case obs := <-observationsChan:
		c.NotNil(obs, "Should receive observation")
		c.Equal("test-service", obs.ServiceId, "Service ID should match")
	case <-timeout:
		t.Log("No observation received - this is expected if no endpoint messages were processed")
	}
}

func Test_Bridge_StartBridge_ErrorCases(t *testing.T) {
	c := require.New(t)

	// Create mock message processor
	messageProcessor := &mockWebsocketMessageProcessor{}

	// Create channel for observation notifications
	observationsChan := make(chan *observation.RequestResponseObservations, 10)

	// Test with invalid endpoint URL
	clientReq := httptest.NewRequest("GET", "/ws", nil)
	clientReq.Header.Set("Upgrade", "websocket")
	clientReq.Header.Set("Connection", "Upgrade")
	clientReq.Header.Set("Sec-Websocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	clientReq.Header.Set("Sec-Websocket-Version", "13")

	clientRespWriter := httptest.NewRecorder()

	// This should fail because the endpoint URL is invalid
	completionChan, err := StartBridge(
		context.Background(), // Use background context for tests
		polyzero.NewLogger(),
		clientReq,
		clientRespWriter,
		"invalid-url",
		http.Header{},
		messageProcessor,
		observationsChan,
		nil, // reconnector: rebind disabled in these tests
	)
	c.Error(err, "Should fail with invalid endpoint URL")
	c.Nil(completionChan, "Should not receive completion channel on error")
}

// Test_Bridge_NoPanicOnProcessingError reproduces the race condition where
// shutdown triggered by a message processing error (e.g. proto unmarshal failure)
// could cause a "send on closed channel" panic in connLoop.
//
// Scenario: The endpoint rapidly sends messages. The processor fails on the first
// endpoint message, triggering shutdown(). Meanwhile connLoop is still reading
// messages from the endpoint and trying to send them on msgChan. Before the fix,
// shutdown() closed msgChan causing connLoop's send to panic.
func Test_Bridge_NoPanicOnProcessingError(t *testing.T) {
	// Run multiple iterations to increase the chance of hitting the race window.
	// The -race flag and t.Parallel() provide most of the contention;
	// a handful of iterations is sufficient.
	for i := 0; i < 10; i++ {
		t.Run(fmt.Sprintf("iteration_%d", i), func(t *testing.T) {
			t.Parallel()
			c := require.New(t)

			// Create an endpoint server that rapidly sends messages without waiting
			// for client requests. This simulates a subscription-style endpoint
			// (like Solana websocket) that pushes data continuously.
			endpointServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upgrader := websocket.Upgrader{}
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()

				// Rapidly send messages to trigger the race: connLoop reads messages
				// while the bridge is shutting down due to a processing error.
				for j := 0; j < 100; j++ {
					if err := conn.WriteMessage(websocket.TextMessage, []byte("push message")); err != nil {
						return
					}
				}
			}))
			defer endpointServer.Close()

			// Use a processor that always fails on endpoint messages, simulating
			// the proto unmarshal error that triggers bridge shutdown.
			messageProcessor := &failingEndpointProcessor{}

			observationsChan := make(chan *observation.RequestResponseObservations, 100)
			endpointURL := "ws" + strings.TrimPrefix(endpointServer.URL, "http")

			clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				completionChan, err := StartBridge(
					context.Background(),
					polyzero.NewLogger(),
					r,
					w,
					endpointURL,
					http.Header{},
					messageProcessor,
					observationsChan,
					nil, // reconnector: rebind disabled in these tests
				)
				if err != nil {
					return
				}

				// Wait for the bridge to shut down cleanly (no panic).
				select {
				case <-completionChan:
					// Bridge shut down cleanly - this is the success case.
				case <-time.After(5 * time.Second):
					t.Error("bridge did not shut down within timeout")
				}
			}))
			defer clientServer.Close()

			clientURL := "ws" + strings.TrimPrefix(clientServer.URL, "http")
			clientConn, _, err := websocket.DefaultDialer.Dial(clientURL, nil)
			c.NoError(err)
			defer clientConn.Close()

			// Send a client message to ensure the bridge is fully active.
			_ = clientConn.WriteMessage(websocket.TextMessage, []byte("hello"))

			// Give the bridge a moment to process the rapid endpoint messages and shut down.
			time.Sleep(50 * time.Millisecond)
		})
	}
}

// Test_Bridge_ClientGetsCloseFrameOnEndpointConnectFailure verifies that when the
// upstream endpoint connection fails AFTER the client upgrade has succeeded, the
// client receives a legible Websocket close frame (1013 Try Again Later) instead
// of an abnormal/protocol-error closure. This mirrors production: a randomly
// selected supplier resets the bridge connection, and the client should be told
// to reconnect (which draws a different supplier) rather than see a 1002/1006.
func Test_Bridge_ClientGetsCloseFrameOnEndpointConnectFailure(t *testing.T) {
	c := require.New(t)

	// Stand up an endpoint server, capture its address, then close it so the
	// bridge's upstream dial is refused — simulating a supplier that refuses/resets
	// the connection right after the client upgrade succeeds.
	endpointServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	endpointURL := "ws" + strings.TrimPrefix(endpointServer.URL, "http")
	endpointServer.Close() // dials to this address now fail (connection refused)

	messageProcessor := &mockWebsocketMessageProcessor{}
	observationsChan := make(chan *observation.RequestResponseObservations, 10)

	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// StartBridge upgrades the client, then fails to reach the (dead) upstream.
		_, _ = StartBridge(
			context.Background(),
			polyzero.NewLogger(),
			r,
			w,
			endpointURL,
			http.Header{},
			messageProcessor,
			observationsChan,
			nil, // reconnector: rebind disabled in these tests
		)
	}))
	defer clientServer.Close()

	clientURL := "ws" + strings.TrimPrefix(clientServer.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(clientURL, nil)
	c.NoError(err, "client upgrade should succeed before the upstream failure")
	defer clientConn.Close()

	// The client must receive a well-formed close frame with code 1013, not an
	// abnormal closure (1006) or protocol error (1002).
	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, readErr := clientConn.ReadMessage()
	c.Error(readErr, "expected a close frame from the gateway")
	c.True(
		websocket.IsCloseError(readErr, websocket.CloseTryAgainLater),
		"client should receive a 1013 (Try Again Later) close frame, got: %v", readErr,
	)
}

// Test_Bridge_SanitizesReservedCloseCode verifies that when the endpoint drops its
// TCP connection with no close handshake — gorilla surfaces this as a reserved
// *CloseError{Code: 1006} — PATH does NOT forward the reserved 1006 to the client.
// Instead the client receives a valid 1011 (Internal Error) close frame. Without
// sanitization the relay miner rejects the forwarded frame ("bad close code 1006")
// and clients see a malformed close.
func Test_Bridge_SanitizesReservedCloseCode(t *testing.T) {
	c := require.New(t)

	// Endpoint upgrades, then abruptly closes the underlying TCP connection with no
	// close frame — the exact condition that makes gorilla synthesize a 1006.
	endpointServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.UnderlyingConn().Close()
	}))
	defer endpointServer.Close()

	messageProcessor := &mockWebsocketMessageProcessor{}
	observationsChan := make(chan *observation.RequestResponseObservations, 10)
	endpointURL := "ws" + strings.TrimPrefix(endpointServer.URL, "http")

	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = StartBridge(
			context.Background(),
			polyzero.NewLogger(),
			r,
			w,
			endpointURL,
			http.Header{},
			messageProcessor,
			observationsChan,
			nil, // reconnector: rebind disabled in these tests
		)
	}))
	defer clientServer.Close()

	clientURL := "ws" + strings.TrimPrefix(clientServer.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(clientURL, nil)
	c.NoError(err)
	defer clientConn.Close()

	// The client must receive a valid (non-reserved) close code. Before the fix it
	// would receive the reserved 1006 propagated straight from the endpoint.
	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, readErr := clientConn.ReadMessage()
	c.Error(readErr, "expected a close frame from the gateway")
	c.False(
		websocket.IsCloseError(readErr, websocket.CloseAbnormalClosure),
		"client must NOT receive a reserved 1006 close code, got: %v", readErr,
	)
	c.True(
		websocket.IsCloseError(readErr, websocket.CloseInternalServerErr),
		"reserved endpoint code should be sanitized to 1011, got: %v", readErr,
	)
}

// Mock implementations for testing

type mockWebsocketMessageProcessor struct{}

func (m *mockWebsocketMessageProcessor) ProcessClientWebsocketMessage(msgData []byte) ([]byte, error) {
	// Echo the message as-is (no protocol-specific processing)
	return msgData, nil
}

func (m *mockWebsocketMessageProcessor) ProcessEndpointWebsocketMessage(msgData []byte) ([]byte, *observation.RequestResponseObservations, error) {
	// Echo the message as-is and return mock observations
	mockObservations := &observation.RequestResponseObservations{
		ServiceId: "test-service",
		Gateway: &observation.GatewayObservations{
			ServiceId: "test-service",
		},
	}
	return msgData, mockObservations, nil
}

// failingEndpointProcessor always returns an error for endpoint messages,
// simulating the proto unmarshal failure that triggers bridge shutdown.
type failingEndpointProcessor struct{}

func (m *failingEndpointProcessor) ProcessClientWebsocketMessage(msgData []byte) ([]byte, error) {
	return msgData, nil
}

func (m *failingEndpointProcessor) ProcessEndpointWebsocketMessage([]byte) ([]byte, *observation.RequestResponseObservations, error) {
	return nil, nil, fmt.Errorf("proto: illegal wireType 6")
}
