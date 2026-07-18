package websockets

import "errors"

// Bridge shutdown error types used to determine appropriate Websocket close codes
var (
	// ErrBridgeContextCanceled indicates the bridge was shut down due to context cancellation
	// This typically happens during graceful shutdown or when the gateway context is canceled
	ErrBridgeContextCanceled = errors.New("bridge context canceled")

	// ErrBridgeMessageProcessingFailed indicates the bridge was shut down due to message processing errors
	// This includes protocol errors, QoS validation failures, or message transformation failures
	ErrBridgeMessageProcessingFailed = errors.New("bridge message processing failed")

	// ErrBridgeConnectionFailed indicates the bridge was shut down due to connection-level failures
	// This includes write failures, connection drops, or network-level errors
	ErrBridgeConnectionFailed = errors.New("bridge connection failed")

	// ErrBridgeEndpointUnavailable indicates the bridge was shut down because the endpoint became unavailable
	// This includes endpoint disconnections or endpoint-side errors
	ErrBridgeEndpointUnavailable = errors.New("bridge endpoint unavailable")

	// ErrEndpointStalled indicates the endpoint connection is transport-alive (still
	// answering pings) but has delivered no subscription data past the staleness
	// threshold — a silent supplier stall that ping/pong liveness cannot detect. It is
	// raised by the staleness watchdog as a synthetic disconnect to force a session
	// rebind onto a DIFFERENT supplier, and is distinguished from an ordinary
	// (session-rollover) disconnect via errors.Is so the reconnect avoids reselecting
	// the stalling supplier.
	ErrEndpointStalled = errors.New("endpoint stalled: no subscription data past staleness threshold")
)
