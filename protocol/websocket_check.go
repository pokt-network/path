package protocol

// WebsocketProbe describes how deeply a websocket health check should exercise an endpoint.
//
// It exists because a handshake-only probe cannot tell a working WebSocket endpoint from a
// broken one. Measured on live bsc traffic: an operator accepted the connection, answered
// eth_blockNumber correctly, returned a valid subscription id for eth_subscribe — and then
// delivered ZERO notifications for 25 seconds while holding the socket open. A control
// operator delivered 57 frames over the same window.
//
// So neither "did it connect", nor "did it answer a request", nor "did it stay open"
// separates the two. Only "did a subscription actually deliver" does, which is what
// RequireNotification asks for.
//
// This type lives in the protocol package so the gateway can describe the probe without
// importing a concrete protocol implementation.
type WebsocketProbe struct {
	// Payload is sent over the connection after the handshake. Empty means handshake-only,
	// the original behaviour.
	Payload string

	// RequireNotification makes the probe wait, after Payload is acknowledged, for at least
	// one further frame — a subscription notification — before the deadline. An endpoint
	// that acks and then goes silent FAILS.
	//
	// Only meaningful when Payload subscribes to something. A plain request/response
	// payload is acknowledged and nothing further ever arrives, so this would fail every
	// endpoint.
	RequireNotification bool
}

// IsHandshakeOnly reports whether the probe does no more than open the connection.
func (p WebsocketProbe) IsHandshakeOnly() bool { return p.Payload == "" }
