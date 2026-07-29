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

	// ValidateResponse, when set, judges the endpoint's decoded response. Returning an error
	// fails the probe exactly as a transport failure would, so the caller can reject an
	// endpoint on the CONTENT of a perfectly well-formed answer — a stale block height being
	// the case this exists for.
	//
	// Supplied by the health-check executor so the staleness comparison stays where the
	// perceived chain head and sync_allowance already live, rather than being duplicated in
	// the protocol layer. Must return nil for conditions that are the gateway's fault rather
	// than the endpoint's, or a config error becomes a service-wide reputation storm.
	ValidateResponse func(responseBody []byte) error
}

// IsHandshakeOnly reports whether the probe does no more than open the connection.
func (p WebsocketProbe) IsHandshakeOnly() bool { return p.Payload == "" }
