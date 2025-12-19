package websockets

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/observation"
)

// A WebsocketMessageProcessor processes websocket messages.
// For example, the Gateway package's websocketRequestContext implements this interface.
// It then, in turn, performs both protocol-level and QoS-level message processing
// on the message data
type WebsocketMessageProcessor interface {
	// ProcessClientWebsocketMessage processes a message from the client.
	ProcessClientWebsocketMessage([]byte) ([]byte, error)

	// ProcessEndpointWebsocketMessage processes a message from the endpoint.
	ProcessEndpointWebsocketMessage([]byte) ([]byte, *observation.RequestResponseObservations, error)
}

// bridge routes data between an Endpoint and a Client.
// One bridge represents a single Websocket connection
// between a Client and a Websocket Endpoint.
//
// This is a generic websocket bridge that handles the websocket protocol
// and message routing, while delegating Gateway-level message processing to the
// provided message handler.
//
// Architecture:
// - Protocol-agnostic: Works with any protocol (Shannon, future protocols)
// - Message handler: Gateway-level processing of messages. Orchestrates protocol and QoS-level message processing.
// - Notification channels: Gateway-level notifications to trigger sending Observations.
//
// Error Handling Strategy:
// - Network-level errors (connection drops, ping failures): handleDisconnect() → cancelCtx() → async shutdown
// - Application-level errors (message processing, write failures): bridge.shutdown() → immediate cleanup
// - All error paths eventually lead to bridge.shutdown() for complete resource cleanup
//
// Full data flow: Client <---clientConn---> PATH bridge <---endpointConn---> Relay Miner bridge <------> Endpoint
//
// TODO_DOCS: Create Websocket architecture diagram
// - Document the full flow from client through bridge to endpoint
// - Include observation flow and error handling paths
// - Show interaction between gateway, protocol, and QoS layers for Websocket messages
type bridge struct {
	// ctx is used to stop the bridge when the context is canceled from either connection
	ctx    context.Context
	logger polylog.Logger

	// endpointConn is the connection to the Websocket Endpoint
	endpointConn *websocketConnection
	// clientConn is the connection to the Client
	clientConn *websocketConnection

	// msgChan receives messages from the Client and Endpoint and passes them to the other side of the bridge.
	// It is an internal channel used only by the bridge and not exposed to any other package.
	msgChan chan message

	// websocketMessageProcessor processes messages from the client and endpoint.
	websocketMessageProcessor WebsocketMessageProcessor

	// messageObservationsChan receives message observations from the websocketMessageProcessor.
	messageObservationsChan chan *observation.RequestResponseObservations

	// shutdownOnce ensures shutdown() is only called once to prevent panics from double-closing channels
	shutdownOnce sync.Once
}

// StartBridge creates a new Bridge instance with connections to both client and endpoint
// and starts the bridge. Returns a channel that will be closed when the bridge shuts down.
func StartBridge(
	ctx context.Context,
	logger polylog.Logger,
	req *http.Request,
	w http.ResponseWriter,
	websocketURL string,
	headers http.Header,
	websocketMessageProcessor WebsocketMessageProcessor,
	messageObservationsChan chan *observation.RequestResponseObservations,
) (<-chan struct{}, error) {
	logger = logger.With("component", "websocket_bridge")

	// Create a bridge-specific context that can be canceled from connections
	// This is a child of the shared Websocket context
	bridgeCtx, cancelCtx := context.WithCancel(ctx)

	// Upgrade HTTP request from client to websocket connection.
	clientConn, err := upgradeClientWebsocketConnection(logger, req, w)
	if err != nil {
		logger.Error().Err(err).Msg("❌ error upgrading client websocket connection")
		cancelCtx() // Clean up context on error
		return nil, fmt.Errorf("createWebsocketBridge: %w", err)
	}

	// Connect to the Relay Miner endpoint
	endpointConn, err := ConnectWebsocketEndpoint(logger, websocketURL, headers)
	if err != nil {
		logger.Error().Err(err).Msg("❌ error connecting to websocket endpoint")
		// Clean up the client connection that was successfully upgraded
		if closeErr := clientConn.Close(); closeErr != nil {
			logger.Warn().Err(closeErr).Msg("error closing client connection after endpoint connection failure")
		}
		cancelCtx() // Clean up context on error
		return nil, fmt.Errorf("createWebsocketBridge: %w", err)
	}

	// Create a channel to pass messages between the Client and Endpoint
	msgChan := make(chan message)

	// Create a completion channel that will be closed when the bridge shuts down
	completionChan := make(chan struct{})

	// Create bridge instance
	b := &bridge{
		logger: logger,
		ctx:    bridgeCtx, // Use the bridge-specific context

		msgChan:                   msgChan,
		websocketMessageProcessor: websocketMessageProcessor,

		messageObservationsChan: messageObservationsChan,
	}
	if err := b.validateComponents(); err != nil {
		cancelCtx() // Clean up context on error
		return nil, fmt.Errorf("❌ invalid bridge components: %w", err)
	}

	// Initialize connections with bridge context and cancel function
	b.endpointConn = newConnection(
		b.ctx,
		cancelCtx,
		logger.With("conn", "endpoint"),
		endpointConn,
		messageSourceEndpoint,
		msgChan,
	)
	b.clientConn = newConnection(
		b.ctx,
		cancelCtx,
		logger.With("conn", "client"),
		clientConn,
		messageSourceClient,
		msgChan,
	)

	// Start the bridge in a goroutine
	go func() {
		defer close(completionChan) // Signal completion when bridge shuts down
		b.start()
	}()

	// Return the completion channel so the caller can wait for bridge shutdown
	return completionChan, nil
}

// validateComponents ensures the Bridge is not created with nil components.
// This is done to avoid panics and to make the Bridge's behavior more predictable.
func (b *bridge) validateComponents() error {
	switch {
	case b.messageObservationsChan == nil:
		return fmt.Errorf("messageObservationsChan is nil")
	case b.websocketMessageProcessor == nil:
		return fmt.Errorf("websocketMessageProcessor is nil")
	}
	return nil
}

// ---------- Bridge Lifecycle ----------

// Start starts the bridge and establishes a bidirectional communication
// through PATH between the Client and the selected websocket endpoint.
//
// Full data flow: Client <---clientConn---> PATH Bridge <---endpointConn---> Relay Miner Bridge <------> Endpoint
func (b *bridge) start() {
	b.logger.Info().Msg("Websocket bridge operation started successfully")

	// Listen for the context to be canceled and shut down the bridge
	go func() {
		<-b.ctx.Done()
		b.shutdown(ErrBridgeContextCanceled)
	}()

	for msg := range b.msgChan {
		switch msg.source {
		case messageSourceClient:
			b.handleClientMessage(msg)

		case messageSourceEndpoint:
			b.handleEndpointMessage(msg)
		}
	}
}

// shutdown performs immediate and complete bridge cleanup.
//
// Usage:
// - Application-level errors: Call shutdown() directly for immediate cleanup
// - Message processing failures, protocol errors, write failures to connections
// - Ensures proper Websocket close frames are sent before terminating connections
//
// Cleanup sequence:
// 1. Sends Websocket close frames to both client and endpoint with appropriate close codes
// 2. Closes both Websocket connections
// 3. Closes message channel to stop the message processing loop
//
// Close Codes:
// - CloseServiceRestart (1012): For expected service interruptions (encourages reconnection)
// - CloseInternalServerErr (1011): For unexpected server errors
//
// This method ensures all resources are cleaned up immediately and deterministically.
func (b *bridge) shutdown(err error) {
	// Use sync.Once to ensure shutdown is only called once, preventing panics from double-closing channels
	b.shutdownOnce.Do(func() {
		b.logger.Warn().Err(err).Msg("🔌👋 Websocket bridge shutting down.")

		// Determine appropriate close code and message for client reconnection guidance
		closeCode, errMsg := b.determineCloseCodeAndMessage(err)
		closeMsg := websocket.FormatCloseMessage(closeCode, errMsg)

		// Write close messages with timeout to prevent hanging on broken connections
		closeTimeout := time.Now().Add(1 * time.Second)

		if b.clientConn != nil {
			if err := b.clientConn.WriteControl(websocket.CloseMessage, closeMsg, closeTimeout); err != nil {
				b.logger.Warn().Err(err).Msg("⚠️ could not write close message to client connection")
			}
			b.clientConn.Close()
		}
		if b.endpointConn != nil {
			if err := b.endpointConn.WriteControl(websocket.CloseMessage, closeMsg, closeTimeout); err != nil {
				b.logger.Warn().Err(err).Msg("⚠️ could not write close message to endpoint connection")
			}
			b.endpointConn.Close()
		}

		// Close the message channel to stop the message loop
		close(b.msgChan)

		// Close the observation channel to signal the gateway that no more observations will be sent
		if b.messageObservationsChan != nil {
			close(b.messageObservationsChan)
		}
	})
}

// determineCloseCodeAndMessage determines the appropriate Websocket close code and message
// based on the error that caused the bridge shutdown. This guides client reconnection behavior.
//
// Close Code Guidelines (RFC 6455):
// - 1012 (Service Restart): Server is restarting; client should attempt reconnection
// - 1011 (Internal Error): Server encountered an unexpected condition; reconnection may help
// - 1002 (Protocol Error): Protocol violation; client should not reconnect automatically
// - 1003 (Unsupported Data): Data type cannot be accepted; client should not reconnect
func (b *bridge) determineCloseCodeAndMessage(err error) (int, string) {
	// Check for specific error types using errors.Is for proper error chain handling
	switch {
	case errors.Is(err, ErrBridgeContextCanceled):
		// Expected shutdown - encourage reconnection
		return websocket.CloseServiceRestart, "service restarting, please reconnect"

	case errors.Is(err, ErrBridgeEndpointUnavailable):
		// Endpoint issues - encourage reconnection (may be temporary)
		return websocket.CloseServiceRestart, "endpoint temporarily unavailable, please reconnect"

	case errors.Is(err, ErrBridgeMessageProcessingFailed):
		// Message processing errors - could be transient or client issue
		return websocket.CloseInternalServerErr, "message processing error occurred"

	case errors.Is(err, ErrBridgeConnectionFailed):
		// Connection-level errors - likely network issues
		return websocket.CloseInternalServerErr, "connection error occurred"

	default:
		// Handle context.Canceled specifically (from context package)
		if err.Error() == "context canceled" {
			return websocket.CloseServiceRestart, "service restarting, please reconnect"
		}

		// Unknown errors - use internal error code
		return websocket.CloseInternalServerErr, fmt.Sprintf("bridge error: %s", err.Error())
	}
}

// ---------- Client Message Handling ----------

// handleClientMessage processes a message from the Client and sends it to the endpoint.
//
// Error Handling:
// - Message processing errors: shutdown() immediately (application-level failure)
// - Write errors to endpoint: shutdown() immediately (communication failure)
func (b *bridge) handleClientMessage(msg message) {
	// Process the message through the client message handler
	processedData, err := b.websocketMessageProcessor.ProcessClientWebsocketMessage(msg.data)
	if err != nil {
		b.logger.Error().Err(err).Msg("❌ error processing client message, shutting down bridge")

		b.shutdown(fmt.Errorf("%w: client message processing failed: %w", ErrBridgeMessageProcessingFailed, err))
		return
	}

	b.logger.Debug().Msgf("🔗 client message successfully processed, sending message to endpoint: %s", string(processedData))

	// Send the processed message to the endpoint
	if err := b.endpointConn.WriteMessage(msg.messageType, processedData); err != nil {
		b.logger.Error().Err(err).Msg("❌ error writing client message to endpoint, shutting down bridge")

		b.shutdown(fmt.Errorf("%w: failed to write client message to endpoint: %w", ErrBridgeConnectionFailed, err))
		return
	}
}

// ---------- Endpoint Message Handling ----------

// handleEndpointMessage processes a message from the Endpoint and sends it to the Client.
// The bridge notifies the gateway about message processing results through channels,
// allowing the gateway to handle observations without the bridge knowing about them.
//
// Error Handling:
// - Message processing errors: shutdown() immediately (application-level failure)
// - Write errors to client: shutdown() immediately (communication failure)
//
// Note: Session rollover disconnections from endpoints are expected and handled gracefully.
func (b *bridge) handleEndpointMessage(msg message) {
	// Process the message through the endpoint message handler
	processedData, msgObservations, err := b.websocketMessageProcessor.ProcessEndpointWebsocketMessage(msg.data)

	// Notify gateway about message processing results (only if observations were created)
	defer func() {
		if msgObservations != nil {
			b.sendMessageObservations(msgObservations)
		}
	}()

	if err != nil {
		b.logger.Error().Err(err).Msg("❌ error processing endpoint message, shutting down bridge")
		b.shutdown(fmt.Errorf("%w: endpoint message processing failed: %w", ErrBridgeMessageProcessingFailed, err))
		return
	}

	// Send the processed message to the client
	// NOTE: On session rollover, the Endpoint will disconnect the Endpoint connection, which will trigger this
	// error. This is expected and the Client is expected to handle the reconnection in their connection logic.
	if err := b.clientConn.WriteMessage(msg.messageType, processedData); err != nil {
		b.logger.Error().Err(err).Msg("❌ error writing endpoint message to client, shutting down bridge")
		b.shutdown(fmt.Errorf("%w: failed to write endpoint message to client: %w", ErrBridgeConnectionFailed, err))
		return
	}

	b.logger.Debug().Msgf("🔗 endpoint message successfully processed, sending message to client: %s", string(processedData))

}

// ---------- Message Observation Sending ----------

// sendMessageObservations sends message observations to the gateway.
// If the channel is full or closed, the message observations are dropped.
// This is done to avoid blocking the main thread.
func (b *bridge) sendMessageObservations(msgObservations *observation.RequestResponseObservations) {
	select {
	case b.messageObservationsChan <- msgObservations:
		// Successfully sent
	default:
		// Channel is full, log but don't block
		b.logger.Warn().Msg("messageObservationsChan is full, dropping message observations")
	}
}
