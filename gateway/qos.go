package gateway

import (
	"context"
	"net/http"
	"time"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/metrics/devtools"
	pathhttp "github.com/pokt-network/path/network/http"
	"github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	qostypes "github.com/pokt-network/path/qos/types"
)

// RequestQoSContext
//
// Represents interactions between the gateway and the QoS instance for a given service request.
//
// Construction methods:
// - Parse an organic request from an end-user.
// - Rebuild from a shared context deserialized from another PATH instance.
type RequestQoSContext interface {
	// TODO_TECHDEBT: Should eventually return []Payload
	// - Allows mapping a single RelayRequest into multiple ServiceRequests.
	// - Example: A batch relay request on JSONRPC should decompose into multiple independent requests.
	GetServicePayloads() []protocol.Payload

	// TODO_FUTURE:
	// - Add retry-related return values to UpdateWithResponse,
	//   or add retry-related methods (e.g., Failed(), ShouldRetry()).
	//
	// UpdateWithResponse:
	// - Informs the request QoS context of the payload returned by a specific endpoint.
	// - Response is for the service payload produced by GetServicePayload.
	// - httpStatusCode is the original HTTP status code from the backend endpoint.
	// - requestID is the JSON-RPC request ID that this response corresponds to (for batch processing).
	//   For non-JSON-RPC or single requests, this can be empty and the ID will be extracted from the response.
	UpdateWithResponse(endpointAddr protocol.EndpointAddr, endpointSerializedResponse []byte, httpStatusCode int, requestID string)

	// SetProtocolError:
	// - Sets a protocol-level error that occurred before any endpoint could respond.
	// - Used to provide more specific error messages to clients.
	// - Examples: "no valid endpoints available", "RPC type not supported", etc.
	// - If set, GetHTTPResponse should use this error for a more informative response.
	SetProtocolError(err error)

	// GetHTTPResponse:
	// - Returns the user-facing HTTP response.
	// - Response depends on the current state of the service request context.
	// - State is set at context creation and updated via UpdateWithResponse.
	// - If never updated, may return 404 HTTP status.
	GetHTTPResponse() pathhttp.HTTPResponse

	// GetObservations:
	// - Returns QoS-level observations in the context.
	//
	// Example:
	//   Context:
	//     - Service: Solana
	//     - SelectedEndpoint: `endpoint_101`
	//     - Request: `getHealth`
	//     - Endpoint response: error
	//   Observation:
	//     - `endpoint_101` is unhealthy.
	GetObservations() qos.Observations

	// GetEndpointSelector:
	// - Enables specialized endpoint selection (e.g., method-based selection for EVM requests).
	GetEndpointSelector() protocol.EndpointSelector
}

// QoSContextBuilder
//
// Builds the QoS context required for all steps of a service request.
// Example: Generate a user-facing HTTP response from an endpoint's response.
type QoSContextBuilder interface {
	// ParseHTTPRequest:
	// - Ensures the HTTP request is valid for the target service.
	// - detectedRPCType: The RPC type detected by the gateway (or UNKNOWN_RPC if detection failed)
	// - Services should respect this if not UNKNOWN_RPC, or apply their own fallback logic
	ParseHTTPRequest(ctx context.Context, httpReq *http.Request, detectedRPCType sharedtypes.RPCType) (RequestQoSContext, bool)

	// ParseWebsocketRequest:
	// - Ensures a Websocket request is valid for the target service.
	// - Websocket connection requests have no body, so no parsing needed.
	// - If service supports Websocket, returns a valid RequestQoSContext.
	//
	// TODO_TECHDEBT(@adshmh,@commoddity): Remove ParseWebsocketRequest and update ParseHTTPRequest to be the single entry point to QoS.
	// It should perform basic validation of the HTTP handshake request in the case that it is a Websocket request.
	// eg. check that the request is a websocket request, check headers, etc.
	ParseWebsocketRequest(context.Context) (RequestQoSContext, bool)
}

// QoSEndpointCheckGenerator
//
// Returns one or more service request contexts that:
// - Provide data on endpoint quality by sending payloads and parsing responses.
// - Checks are service-specific; the QoS instance decides what checks to run.
type QoSEndpointCheckGenerator interface {
	// TODO_FUTURE:
	// - Add GetOptionalQualityChecks() to collect additional QoS data (e.g., endpoint latency).
	//
	// GetRequiredQualityChecks:
	// - Returns required quality checks for a QoS instance to assess endpoint validity.
	// - Example: EVM QoS may skip block height check if chain ID check already failed.
	GetRequiredQualityChecks(protocol.EndpointAddr) []RequestQoSContext

	// TODO_TECHDEBT(@commoddity): Currently websocket QoS only performs a protocol-level check
	// which determines if an endpoint connection request is successful or not.
	//
	// This only require a simple bool that tells the hydrator if the endpoint should be checked
	// for Websocket connection and applies protocol-level sanctions if it fails.
	//
	// In the future, we may want to add QoS-level checks that take into account specific endpoint
	// responses to apply websocket-related filtering at the QoS level.
	//
	// CheckWebsocketConnection
	//  - Checks if the endpoint supports Websocket connections.
	//  - Returns a boolean indicating whether the endpoint should be checked for Websocket connection.
	CheckWebsocketConnection() bool
}

// TODO_IMPLEMENT:
// - Add a QoS instance per service supported by the gateway (e.g., Ethereum, Solana, RESTful).
//
// QoSService:
// - Represents the embedded definition of a service (e.g., JSONRPC blockchain).
// - Responsibilities:
//  1. QoSRequestParser: Translates service requests (currently only HTTP) into service request contexts.
//  2. EndpointSelector: Chooses the best endpoint for a specific service request.
type QoSService interface {
	QoSContextBuilder
	QoSEndpointCheckGenerator

	// ApplyObservations:
	// - Applies QoS-related observations to the local QoS instance.
	// - TODO_FUTURE: Observations can be:
	//   - "local": from requests sent to an endpoint by THIS PATH instance.
	//   - "shared": from QoS observations shared by OTHER PATH instances.
	ApplyObservations(*qos.Observations) error

	// UpdateFromExtractedData:
	// - Updates QoS state from extracted observation data.
	// - Called by the observation pipeline after async parsing completes.
	// - This method updates endpoint state (e.g., perceived block number) without
	//   blocking the hot path of user requests.
	// - Data is extracted via DataExtractor from sampled requests and health checks.
	UpdateFromExtractedData(endpointAddr protocol.EndpointAddr, data *qostypes.ExtractedData) error

	// GetPerceivedBlockNumber:
	// - Returns the perceived current block number for the service.
	// - Used by health checks for block height validation.
	// - Returns 0 if no block number has been observed yet.
	GetPerceivedBlockNumber() uint64

	// HydrateDisqualifiedEndpointsResponse:
	// - Fills the disqualified endpoint response with QoS-specific data.
	HydrateDisqualifiedEndpointsResponse(protocol.ServiceID, *devtools.DisqualifiedEndpointResponse)
}

// QoSArchivalReporter is an optional interface that QoS services can implement
// to report archival capability status for endpoints.
// Used by the /ready endpoint to include archival information in endpoint details.
type QoSArchivalReporter interface {
	// GetEndpointArchivalStatus returns the archival status for a specific endpoint.
	// Returns (isArchival, expiresAt) if the endpoint has been checked for archival capability.
	// Returns (false, zero time) if the endpoint has not been checked or is not archival-capable.
	GetEndpointArchivalStatus(endpointAddr protocol.EndpointAddr) (isArchival bool, expiresAt time.Time)
}

// QoSServiceRegistry provides access to QoS services by service ID.
// Used by the protocol to query endpoint-specific QoS data like archival status.
type QoSServiceRegistry interface {
	// GetQoSServiceForServiceID returns the QoS service for a given service ID.
	// Returns nil if no QoS service is registered for the service ID.
	GetQoSServiceForServiceID(serviceID protocol.ServiceID) QoSService
}
