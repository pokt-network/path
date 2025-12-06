package gateway

import (
	"context"
	"net/http"

	"github.com/pokt-network/path/health"
	"github.com/pokt-network/path/metrics/devtools"
	"github.com/pokt-network/path/observation"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
	"github.com/pokt-network/path/websockets"
)

// Protocol defines the core functionality of a protocol from the perspective of a gateway.
// The gateway expects a protocol to build and return a request context for a particular service ID.
type Protocol interface {
	// AvailableHTTPEndpoints returns the list of available HTTP endpoints matching both the service ID
	//
	// If the Pocket Network Gateway is in delegated mode, the staked application is passed via
	// the `App-Address` header. In all other modes, *http.Request will be nil.
	//
	// Context may contain a deadline that protocol should respect on best-effort basis.
	// Return observation if endpoint lookup fails.
	// Used as protocol observation for the request when no protocol context exists.
	AvailableHTTPEndpoints(
		context.Context,
		protocol.ServiceID,
		*http.Request,
	) (protocol.EndpointAddrList, protocolobservations.Observations, error)

	// AvailableWebsocketEndpoints returns the list of available websocket endpoints matching both the service ID
	//
	// If the Pocket Network Gateway is in delegated mode, the staked application is passed via
	// the `App-Address` header. In all other modes, *http.Request will be nil.
	//
	// Context may contain a deadline that protocol should respect on best-effort basis.
	// Return observation if endpoint lookup fails.
	// Used as protocol observation for the request when no protocol context exists.
	AvailableWebsocketEndpoints(
		context.Context,
		protocol.ServiceID,
		*http.Request,
	) (protocol.EndpointAddrList, protocolobservations.Observations, error)

	// BuildRequestContextForEndpoint builds and returns a ProtocolRequestContext containing a single selected endpoint.
	// One `ProtocolRequestContext` correspond to a single request, which is sent to a single endpoint.
	//
	// If the Pocket Network Gateway is in delegated mode, the staked application is passed via
	// the `App-Address` header. In all other modes, *http.Request will be nil.
	//
	// Context may contain a deadline that protocol should respect on best-effort basis.
	//
	// Return observation if the context setup fails.
	// Used as protocol observation for the request when no protocol context exists.
	BuildHTTPRequestContextForEndpoint(
		context.Context,
		protocol.ServiceID,
		protocol.EndpointAddr,
		*http.Request,
	) (ProtocolRequestContext, protocolobservations.Observations, error)

	// BuildWebsocketRequestContextForEndpoint builds and returns a ProtocolRequestContextWebsocket containing a single selected endpoint.
	// One `ProtocolRequestContextWebsocket` corresponds to a single long-lived websocket connection to a single endpoint.
	// This method immediately establishes the Websocket connection and starts the bridge.
	//
	// If the Pocket Network Gateway is in delegated mode, the staked application is passed via
	// the `App-Address` header. In all other modes, *http.Request will be nil.
	//
	// Return observation channel for connection-level observations (establishment, closure, errors).
	// Message observations are sent through the provided messageObservationsChan.
	// Return error if the context setup or connection establishment fails.
	BuildWebsocketRequestContextForEndpoint(
		context.Context,
		protocol.ServiceID,
		protocol.EndpointAddr,
		websockets.WebsocketMessageProcessor,
		*http.Request,
		http.ResponseWriter,
		chan *observation.RequestResponseObservations, // messageObservationsChan
	) (ProtocolRequestContextWebsocket, <-chan *protocolobservations.Observations, error)

	// SupportedGatewayModes returns the Gateway modes supported by the protocol instance.
	// See protocol/gateway_mode.go for more details.
	SupportedGatewayModes() []protocol.GatewayMode

	// ApplyHTTPObservations applies the supplied observations to the protocol instance's internal state.
	// Hypothetical example (for illustrative purposes only):
	// 	- protocol: Shannon
	// 	- observation: "endpoint maxed-out or over-serviced (i.e. onchain rate limiting)"
	// 	- result: skip the endpoint for a set time period until a new session begins.
	ApplyHTTPObservations(*protocolobservations.Observations) error

	// ApplyWebSocketObservations applies the supplied observations to the protocol instance's internal state.
	// Hypothetical example (for illustrative purposes only):
	// 	- protocol: Shannon
	// 	- observation: "endpoint maxed-out or over-serviced (i.e. onchain rate limiting)"
	// 	- result: skip the endpoint for a set time period until a new session begins.
	ApplyWebSocketObservations(*protocolobservations.Observations) error

	// TODO_FUTURE(@adshmh): support specifying the app(s) used for sending/signing synthetic relay requests by the hydrator.
	// TODO_TECHDEBT: Enable the hydrator for gateway modes beyond Centralized only.
	//
	// ConfiguredServiceIDs returns the list of service IDs that the protocol instance is configured to serve.
	ConfiguredServiceIDs() map[protocol.ServiceID]struct{}

	// GetTotalServiceEndpointsCount returns the count of all unique endpoints for a service ID
	// without filtering sanctioned endpoints.
	GetTotalServiceEndpointsCount(protocol.ServiceID, *http.Request) (int, error)

	// HydrateDisqualifiedEndpointsResponse hydrates the disqualified endpoint response with the protocol-specific data.
	HydrateDisqualifiedEndpointsResponse(protocol.ServiceID, *devtools.DisqualifiedEndpointResponse)

	// CheckWebsocketConnection checks if the websocket connection to the endpoint is established.
	CheckWebsocketConnection(context.Context, protocol.ServiceID, protocol.EndpointAddr) *protocolobservations.Observations

	// GetReputationService returns the reputation service instance used by the protocol.
	// This is used by the health check executor to record health check results.
	GetReputationService() reputation.ReputationService

	// GetEndpointsForHealthCheck returns a function that gets endpoints for health checks.
	// The returned function takes a service ID and returns endpoint info suitable for health checks.
	// Note: This does NOT filter by reputation - health checks should run against all endpoints.
	GetEndpointsForHealthCheck() func(protocol.ServiceID) ([]EndpointInfo, error)

	// GetUnifiedServicesConfig returns the unified services configuration.
	// This is used by components that need access to per-service configuration overrides.
	GetUnifiedServicesConfig() *UnifiedServicesConfig

	// GetConcurrencyConfig returns the concurrency configuration.
	// This is used by components that need to respect concurrency limits.
	GetConcurrencyConfig() ConcurrencyConfig

	// health.Check interface is used to verify protocol instance's health status.
	health.Check
}

// ProtocolRequestContext defines the functionality expected by the gateway from the protocol,
// for a particular service ID.
//
// These include but not limited to:
//  1. Listing the endpoints available for sending relays for a specific service.
//  2. Send a relay to a specific endpoint and return its response.
//
// The implementation of this interface is in the relayer/shannon package.
type ProtocolRequestContext interface {
	// HandleServiceRequest sends the supplied payload to the endpoint selected using the above SelectEndpoint method,
	// and receives and verifies the response.
	HandleServiceRequest([]protocol.Payload) ([]protocol.Response, error)

	// GetObservations builds and returns the set of protocol-specific observations using the current context.
	//
	// Hypothetical illustrative example.
	//
	// If the context is:
	// 	- Protocol: Shannon
	//	- SelectedEndpoint: `endpoint_101`
	//	- Event: HandleServiceRequest returned a "maxed-out endpoint" error
	//
	// Then the observation can be:
	//  - `maxed-out endpoint` on `endpoint_101`.
	GetObservations() protocolobservations.Observations
}

// ProtocolRequestContextWebsocket defines the functionality expected by the gateway from the protocol,
// specifically for websocket requests
type ProtocolRequestContextWebsocket interface {
	// ProcessProtocolClientWebsocketMessage processes a message from the client.
	ProcessProtocolClientWebsocketMessage([]byte) ([]byte, error)

	// ProcessProtocolEndpointWebsocketMessage processes a message from the endpoint.
	ProcessProtocolEndpointWebsocketMessage([]byte) ([]byte, protocolobservations.Observations, error)
}
