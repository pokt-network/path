// Package gateway implements components for operating a gateway service.
//
// Protocol (Shannon):
// - Provide available endpoints for a service
// - Send relays to specific endpoints
//
// Gateways:
// - Select endpoints for relay transmission
//
// QoS Services:
// - Interpret user requests into endpoint payloads
// - Select optimal endpoints for request handling
//
// TODO_MVP(@adshmh): add a README with a diagram of all the above.
// TODO_MVP(@adshmh): add a section for the following packages once they are added: Metrics, Message.
package gateway

import (
	"context"
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/observation"
)

// DefaultWebsocketMessageBufferSize is the default buffer size for websocket message observations.
// This should match the default in config/router.go.
// At 100: 100 × ~3KB × 100 connections = ~30MB.
const DefaultWebsocketMessageBufferSize = 100

// Gateway handles end-to-end service requests via HandleHTTPServiceRequest:
// - Receives user request
// - Processes request
// - Returns response
//
// TODO_FUTURE: Current HTTP-only format supports JSONRPC, REST, Websockets
// and gRPC. May expand to other formats in future.
type Gateway struct {
	Logger polylog.Logger

	// HTTPRequestParser is used by the gateway instance to
	// interpret an HTTP request as a pair of service ID and
	// its corresponding QoS instance.
	HTTPRequestParser

	// The Protocol instance is used to fulfill the
	// service requests received by the gateway through
	// sending the service payload to an endpoint.
	Protocol

	// RPCTypeValidator validates detected RPC types against service configuration.
	// Used to fail fast with clear errors for unsupported RPC types.
	RPCTypeValidator *RPCTypeValidator

	// MetricsReporter is used to export metrics based on observations made in handling service requests.
	MetricsReporter RequestResponseReporter

	// DataReporter is used to export, to the data pipeline, observations made in handling service requests.
	// It is declared separately from the `MetricsReporter` to be consistent with the gateway package's role
	// of explicitly defining PATH gateway's components and their interactions.
	DataReporter RequestResponseReporter

	// WebsocketMessageBufferSize is the buffer size for websocket message observation channels.
	// Configurable to balance memory usage vs throughput for websocket connections.
	// Default: DefaultWebsocketMessageBufferSize (100)
	WebsocketMessageBufferSize int

	// ObservationQueue handles async, sampled observation processing for QoS data extraction.
	// When enabled, sampled requests are queued for deep parsing (block height, chain ID, etc.)
	// without blocking the hot path. This is optional - if nil, no async observation processing occurs.
	ObservationQueue *ObservationQueue
}

// HandleServiceRequest implements PATH gateway's service request processing.
//
// This method acts as a request router that:
// 1. Determines the type of incoming request (e.g. HTTP or Websocket upgrade)
// 2. Delegates to the appropriate handler:
//   - Websocket: Long-lived bidirectional connection with message-based observations
//   - HTTP: Request-response cycle with single observation broadcast
//
// This separation allows for different processing flows while maintaining a unified entry point.
//
// TODO_FUTURE: Refactor when adding other protocols (e.g. gRPC):
//   - Extract generic processing into common method
//   - Keep protocol-specific details separate
func (g Gateway) HandleServiceRequest(
	ctx context.Context,
	httpReq *http.Request,
	responseWriter http.ResponseWriter,
) {
	// Determine the type of service request and handle it accordingly.
	switch determineServiceRequestType(httpReq) {

	// Handle Websocket service request.
	case websocketServiceRequest:
		// The Websocket upgrade must happen in the same goroutine as the HTTP handler,
		// but the bridge will run in its own goroutine and we'll wait for completion.
		g.handleWebSocketRequest(httpReq, responseWriter)

	// Handle HTTP service request.
	default:
		g.handleHTTPServiceRequest(ctx, httpReq, responseWriter)
	}
}

// handleHTTPServiceRequest handles a standard HTTP service request.
func (g Gateway) handleHTTPServiceRequest(
	ctx context.Context,
	httpReq *http.Request,
	responseWriter http.ResponseWriter,
) {
	logger := g.Logger.With("method", "handleHTTPServiceRequest")

	// Build a gatewayRequestContext with components necessary to process requests.
	gatewayRequestCtx := &requestContext{
		logger:              g.Logger,
		context:             ctx,
		gatewayObservations: getUserRequestGatewayObservations(httpReq),
		protocol:            g.Protocol,
		httpRequestParser:   g.HTTPRequestParser,
		rpcTypeValidator:    g.RPCTypeValidator,
		metricsReporter:     g.MetricsReporter,
		dataReporter:        g.DataReporter,
		observationQueue:    g.ObservationQueue,
	}

	defer func() {
		// Write the user-facing HTTP response.
		gatewayRequestCtx.WriteHTTPUserResponse(responseWriter)

		// Broadcast all observations, e.g. protocol-level, QoS-level, etc. contained in the gateway request context.
		gatewayRequestCtx.BroadcastAllObservations()
	}()

	// Capture HTTP request metadata early for async observation processing.
	// This must happen before the body is consumed by downstream parsing.
	gatewayRequestCtx.captureHTTPRequestMetadata(httpReq)

	// Initialize the GatewayRequestContext struct using the HTTP request.
	// e.g. extract the target service ID from the HTTP request.
	err := gatewayRequestCtx.InitFromHTTPRequest(httpReq)
	if err != nil {
		return
	}

	// Validate RPC type before QoS processing.
	// This fails fast if the detected RPC type is not in the service's configured rpc_types.
	err = gatewayRequestCtx.ValidateRPCType(httpReq)
	if err != nil {
		logger.Error().Err(err).Msg("❌ RPC type validation failed")
		return
	}

	// TODO_CHECK_IF_DONE(@adshmh): Pass the context with deadline to QoS once it can handle deadlines.
	// Build the QoS context for the target service ID using the HTTP request's payload.
	err = gatewayRequestCtx.BuildQoSContextFromHTTP(httpReq)
	if err != nil {
		logger.Error().Err(err).Msg("❌ Error building QoS context for HTTP request")
		return
	}

	// TODO_TECHDEBT(@adshmh): Build a single protocol context to handle a request.
	// - Obtaining a response to the user's request is protocol context's main responsibility.
	// - The protocol context can/should:
	//   - Use fallback endpoints if needed.
	//   - Launch parallel requests to multiple endpoints if appropriate.
	//
	// TODO_CHECK_IF_DONE(@adshmh): Enhance the protocol interface used by the gateway to provide explicit error classification.
	// Implementation should:
	//   1. Differentiate between user errors (e.g., invalid Service ID in request) and system errors (e.g., endpoint timeout)
	//   2. Add error type field to protocol response structure
	//   3. Pass specific error codes from the protocol back to QoS service
	// This will allow the QoS service to return more helpful diagnostic messages and enable better metrics collection for different failure modes.
	//
	// Build the protocol context for the HTTP request.
	err = gatewayRequestCtx.BuildProtocolContextsFromHTTPRequest(httpReq)
	if err != nil {
		logger.Error().Err(err).Msg("❌ Error building protocol context for HTTP request")
		return
	}

	// Use the gateway request context to process the relay(s) corresponding to the HTTP request.
	// Any returned errors are ignored here and processed by the gateway context in the deferred calls.
	// See the `BroadcastAllObservations` method of `gateway.requestContext` struct for details.
	err = gatewayRequestCtx.HandleRelayRequest()
	if err != nil {
		logger.Error().Err(err).Msg("❌ Error processing relay request")
		return
	}
}

// handleWebSocketRequest handles Websocket connection requests.
func (g Gateway) handleWebSocketRequest(
	httpReq *http.Request,
	w http.ResponseWriter,
) {
	logger := g.Logger.With("method", "handleWebSocketRequest")

	// Use a background context for the long-lived Websocket connection lifecycle.
	// Unlike HTTP requests, Websocket connections are long-lived and should not be tied to the HTTP request context.
	// The HTTP request context gets canceled when the HTTP handler returns, which would stop the observation listener.
	// The bridge will handle its own context lifecycle management.
	websocketCtx := context.Background()

	// Determine the websocket message buffer size
	websocketBufferSize := g.WebsocketMessageBufferSize
	if websocketBufferSize <= 0 {
		websocketBufferSize = DefaultWebsocketMessageBufferSize
	}

	// Build a websocketRequestContext with components necessary to process websocket requests.
	websocketRequestCtx := &websocketRequestContext{
		logger:              g.Logger,
		context:             websocketCtx,
		gatewayObservations: getUserRequestGatewayObservations(httpReq),
		protocol:            g.Protocol,
		httpRequestParser:   g.HTTPRequestParser,
		metricsReporter:     g.MetricsReporter,
		dataReporter:        g.DataReporter,
		// Note: We do NOT close messageObservationsChan here because Websocket connections
		// outlive the HTTP handler. The channel will be closed when the Websocket actually disconnects.
		messageObservationsChan: make(chan *observation.RequestResponseObservations, websocketBufferSize),
	}

	// Initialize the websocket request context using the HTTP request.
	err := websocketRequestCtx.initFromHTTPRequest(httpReq)
	if err != nil {
		logger.Error().Err(err).Msg("❌ Error initializing websocket request context")
		return
	}

	// Build the QoS context for the target service ID using the HTTP request.
	// This replaces BuildQoSContextFromWebsocket and uses ParseHTTPRequest as single entry point.
	err = websocketRequestCtx.buildQoSContextFromHTTP(httpReq)
	if err != nil {
		logger.Error().Err(err).Msg("❌ Error building QoS context for websocket request")
		return
	}

	// Handle the websocket connection request using the websocket request context.
	// This builds the protocol context, starts the bridge, and handles observations.
	err = websocketRequestCtx.handleWebsocketRequest(httpReq, w)
	if err != nil {
		logger.Error().Err(err).Msg("❌ Error processing websocket request")
		return
	}

	// At this point, the Websocket connection has terminated and the bridge has shut down.
	// The defer block above will now execute and broadcast connection observations with:
	//   - Complete connection duration (from establishment to termination)
	//   - Final connection status and termination reason
	// This ensures we send only ONE connection observation per Websocket connection.
	logger.Info().Msg("✅ Websocket connection and bridge shutdown complete, ready to broadcast final observations")
}
