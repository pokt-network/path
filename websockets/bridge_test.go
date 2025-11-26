package websockets

import (
	"context"
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
	)
	c.Error(err, "Should fail with invalid endpoint URL")
	c.Nil(completionChan, "Should not receive completion channel on error")
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
