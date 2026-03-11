package gateway

import (
	"errors"
)

// Publicly exposed errors
var (
	// no service ID was provided by the user.
	ErrGatewayNoServiceIDProvided = errors.New("no service ID provided")
)

// Internal errors
var (
	// QoS instance rejected the request.
	// e.g. HTTP payload could not be unmarshaled into a JSONRPC request.
	errGatewayRejectedByQoS = errors.New("QoS instance rejected the request")

	// Error building protocol contexts from HTTP request.
	errBuildProtocolContextsFromHTTPRequest = errors.New("error building protocol contexts from HTTP request")

	// Websocket request was rejected by QoS instance.
	// e.g. Websocket subscription request validation failed.
	errWebsocketRequestRejectedByQoS = errors.New("websocket request rejected by QoS instance")

	// Websocket connection establishment failed.
	// e.g. Failed to upgrade HTTP connection to Websocket or connect to endpoint.
	errWebsocketConnectionFailed = errors.New("websocket connection establishment failed")

	// errWebsocketProtocolContextNotReady is returned when a message is received
	// before the protocol context has been initialized.
	// This guards against a race condition where the bridge starts processing
	// messages before BuildWebsocketRequestContextForEndpoint returns and
	// protocolCtx is assigned.
	errWebsocketProtocolContextNotReady = errors.New("websocket protocol context not initialized")
)
