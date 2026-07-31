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

	// ErrEndpointTumbled indicates an operator asked the bridge to move this connection
	// onto a different supplier (admin tumble). Unlike ErrEndpointStalled it implies
	// nothing about endpoint health — the current endpoint may be perfectly fine and is
	// being moved for traffic-distribution reasons — but it takes the same reconnect
	// path, so the rebind avoids reselecting the currently bound supplier.
	ErrEndpointTumbled = errors.New("endpoint tumbled: operator-requested rebind to a different supplier")

	// ErrEndpointSessionExpired indicates the session the endpoint connection is bound to
	// has ended, and the supplier never closed the socket to tell us.
	//
	// Every other rebind trigger is REACTIVE — the endpoint hangs up (relay miner close
	// 4000 at session expiry), or the staleness watchdog notices silence. Neither fires
	// when a supplier keeps streaming past its own session: endpoint→client frames carry
	// no signature and need no session, so data flows exactly as before and the watchdog
	// stays quiet because the feed is alive. The connection is then stranded on a supplier
	// outside the current session — invisible to reputation and unreachable by endpoint
	// selection — until the client itself disconnects. Worse for the client, every
	// client→endpoint frame is signed against the dead session, so it can no longer add a
	// subscription while its existing stream keeps running.
	//
	// This is the proactive trigger for that case. It takes the ordinary rollover path
	// (supplier continuity is fine if the supplier is still in the new session), unlike
	// ErrEndpointStalled/ErrEndpointTumbled which must land elsewhere.
	ErrEndpointSessionExpired = errors.New("endpoint session expired: bound session ended without the supplier disconnecting")
)
