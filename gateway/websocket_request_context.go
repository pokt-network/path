package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pokt-network/path/metrics"
	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	"github.com/pokt-network/path/observation"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/websockets"
)

var _ websockets.WebsocketMessageProcessor = &websocketRequestContext{}

// websocketRequestContext is responsible for orchestrating the flow of websocket messages between client and endpoint.
// It handles:
//   - QoS validation and context building
//   - Protocol context setup (single endpoint selection vs HTTP's multiple endpoints)
//   - Message routing and observation (per-message vs HTTP's per-request)
//   - Bridge lifecycle management
//
// Key differences from HTTP requestContext:
//   - Single endpoint selection (websockets can't do parallel requests)
//   - Per-message observations instead of per-request observations
//   - Long-lived connection management vs one-shot request/response
//
// TODO_ARCHITECTURE: Separate Websocket and HTTP request contexts more cleanly
// Current: websocketRequestContext and httpRequestContext share similar patterns but are separate types
// Suggestion: Create a base requestContext interface with common functionality, then extend for protocol-specific needs
type websocketRequestContext struct {
	logger polylog.Logger

	// Enforces request completion deadline.
	context context.Context

	// httpRequestParser is used by the request context to interpret an HTTP request as a pair of:
	// 	1. service ID
	// 	2. The service ID's corresponding QoS instance.
	httpRequestParser HTTPRequestParser

	// metricsReporter is used to export metrics based on observations made in handling service requests.
	metricsReporter RequestResponseReporter

	// dataReporter is used to export, to the data pipeline, observations made in handling service requests.
	dataReporter RequestResponseReporter

	// QoS related request context
	serviceID  protocol.ServiceID
	serviceQoS QoSService
	qosCtx     RequestQoSContext

	// Protocol related request context
	protocol Protocol
	// For websockets, we only use a single protocol context
	protocolCtx ProtocolRequestContextWebsocket

	// gatewayObservations stores gateway related observations.
	gatewayObservations *observation.GatewayObservations

	// Channel for receiving message processing notifications from the bridge
	messageObservationsChan chan *observation.RequestResponseObservations
}

// ---------- Websocket Connection Establishment ----------

// initFromHTTPRequest builds the required context for serving a Websocket request.
func (wrc *websocketRequestContext) initFromHTTPRequest(httpReq *http.Request) error {
	// Initialize the logger with the HTTP request attributes.
	wrc.logger = wrc.getWebSocketConnectionLogger(httpReq)

	// Extract the service ID and find the target service's corresponding QoS instance.
	serviceID, serviceQoS, err := wrc.httpRequestParser.GetQoSService(wrc.context, httpReq)
	if err != nil {
		// Update gateway observations
		wrc.updateGatewayObservations(err)
		wrc.logger.Error().Err(err).Msg("HTTP request rejected by parser for websocket connection")
		return fmt.Errorf("websocket request rejected by parser: %w", err)
	}

	// Update the service ID and QoS instance
	wrc.serviceID = serviceID
	wrc.serviceQoS = serviceQoS

	// Set the service ID in the logger
	wrc.logger = wrc.logger.With("service_id", serviceID)

	return nil
}

// buildQoSContextFromHTTP builds the QoS context instance using the supplied HTTP request.
func (wrc *websocketRequestContext) buildQoSContextFromHTTP(_ *http.Request) error {
	logger := wrc.logger.With("method", "buildQoSContextFromHTTP")

	// TODO_TECHDEBT(@adshmh,@commoddity): Replace with ParseHTTPRequest.
	qosCtx, isValid := wrc.serviceQoS.ParseWebsocketRequest(wrc.context)

	// Reject invalid Websocket requests.
	if !isValid {
		// Update gateway observations for websocket rejection
		wrc.updateGatewayObservations(errWebsocketRequestRejectedByQoS)
		logger.Info().Msg("Websocket request rejected by QoS")
		return errWebsocketRequestRejectedByQoS
	}

	// Set the QoS context in the websocket request context.
	wrc.qosCtx = qosCtx

	return nil
}

// handleWebsocketRequest establishes the websocket connection and starts the bridge,
// then starts listeners for both message and connection observations.
func (wrc *websocketRequestContext) handleWebsocketRequest(
	httpRequest *http.Request,
	httpResponseWriter http.ResponseWriter,
) error {
	logger := wrc.logger.With("method", "handleWebsocketRequest")

	// Start listening for message processing notifications from the bridge
	go wrc.listenForMessageNotifications()

	// Build protocol context and start the bridge
	// The protocol layer will create and return the connection observation channel
	connectionObservationChan, err := wrc.buildProtocolContextAndStartBridge(httpRequest, httpResponseWriter)
	if err != nil {
		// Update gateway observations with the error
		wrc.updateGatewayObservations(fmt.Errorf("%w: %s", errWebsocketConnectionFailed, err.Error()))
		logger.Error().Err(err).Msg("Failed to build protocol context and start bridge")
		return err
	}

	// Start listening for connection observations from the protocol layer
	// The protocol layer ensures observations are buffered until we start listening
	go wrc.listenForConnectionObservations(connectionObservationChan)

	// Set the received_time in gateway observations to mark connection establishment
	wrc.gatewayObservations.ReceivedTime = timestamppb.New(time.Now())

	logger.Info().Msg("🔌 Websocket connection established successfully")

	return nil
}

// buildProtocolContextAndStartBridge builds the Protocol context for the websocket request and immediately starts the bridge.
// This combines protocol context creation with bridge initialization.
func (wrc *websocketRequestContext) buildProtocolContextAndStartBridge(
	httpReq *http.Request,
	httpResponseWriter http.ResponseWriter,
) (<-chan *protocolobservations.Observations, error) {
	logger := wrc.logger.With("method", "buildProtocolContextAndStartBridge")

	// Retrieve the list of available endpoints for the requested service.
	// endpointLookupObs will capture the details of the endpoint lookup, including whether it is an error or success.
	availableEndpoints, endpointLookupObs, err := wrc.protocol.AvailableWebsocketEndpoints(wrc.context, wrc.serviceID, httpReq)
	if err != nil {
		logger.Error().Err(err).Msg("❌ no available endpoints could be found for websocket request")
		// Send connection failure observation manually since the connection observation channel is not available yet
		wrc.handleConnectionObservation(&endpointLookupObs)
		return nil, fmt.Errorf("no available endpoints for websocket request: %w", err)
	}

	// For websockets, select a single endpoint
	selectedEndpoint, err := wrc.qosCtx.GetEndpointSelector().Select(availableEndpoints)
	if err != nil {
		logger.Error().Err(err).Msgf("❌ no endpoints could be selected for websocket request from %d available endpoints", len(availableEndpoints))
		// Send connection failure observation manually since the connection observation channel is not available yet
		wrc.handleConnectionObservation(&endpointLookupObs)
		return nil, fmt.Errorf("no endpoints could be selected for websocket request from %d available endpoints", len(availableEndpoints))
	}
	wrc.logger = wrc.logger.With("endpoint_addr", selectedEndpoint)

	// Build protocol context and start bridge for the selected endpoint
	// This immediately establishes the Websocket connection and returns a connection observation channel
	protocolCtx, connectionObservationChan, err := wrc.protocol.BuildWebsocketRequestContextForEndpoint(
		wrc.context,
		wrc.serviceID,
		selectedEndpoint,
		wrc,
		httpReq,
		httpResponseWriter,
		wrc.messageObservationsChan,
	)
	if err != nil {
		logger.Error().Err(err).Str(
			"endpoint_addr", string(selectedEndpoint),
		).Msg("Failed to build protocol context and start bridge for websocket endpoint")
		// Send connection failure observation manually since the connection observation channel is not available in case of error
		errorObs := buildConnectionEstablishmentFailureObservation(wrc.logger, wrc.serviceID, selectedEndpoint, err)
		wrc.handleConnectionObservation(errorObs)
		return nil, fmt.Errorf("failed to build protocol context and start bridge for websocket endpoint: %w", err)
	}

	wrc.protocolCtx = protocolCtx
	logger.Info().Msgf("Successfully built protocol context and started bridge for websocket endpoint: %s", selectedEndpoint)
	return connectionObservationChan, nil
}

// ---------- Websocket Message Processing ----------

// TODO_TECHDEBT(@commoddity,@adshmh): This needs a few refactors to establish the gateway package's context as the coordinator of operations: e.g.
//   - receive messages from the protocol context over a channel -> pass to QoS -> use QoS context to write user's response, etc.
//   - receive observations from the protocol context over a channel -> hand over to publisher(s) like data, metrics, etc.
// Reference: https://github.com/pokt-network/path/pull/419/files#r2333916517

// ProcessClientWebsocketMessage processes a message from the client.
// It performs both Protocol-level and QoS-level message processing.
// If an error occurs, it is returned and the message is not forwarded to the endpoint.
func (wrc *websocketRequestContext) ProcessClientWebsocketMessage(msgData []byte) ([]byte, error) {
	logger := wrc.logger.With("method", "ProcessClientWebsocketMessage")

	logger.Debug().Msgf("received message from client: %s", string(msgData))

	// Process the client message using the protocol context.
	clientMessageBz, err := wrc.protocolCtx.ProcessProtocolClientWebsocketMessage(msgData)
	if err != nil {
		logger.Error().Err(err).Msg("❌ failed to perform protocol-level client message processing")
		return nil, err
	}

	return clientMessageBz, nil
}

// ProcessEndpointWebsocketMessage processes a message from the endpoint.
// It performs both Protocol-level and QoS-level message processing.
// If an error occurs, it is returned and the message is not forwarded to the client.
func (wrc *websocketRequestContext) ProcessEndpointWebsocketMessage(msgData []byte) ([]byte, *observation.RequestResponseObservations, error) {
	logger := wrc.logger.With("method", "ProcessEndpointWebsocketMessage")

	messageObservations := wrc.initializeMessageObservations()

	// Process the endpoint message using the protocol context and update the message observations.
	endpointMessageBz, protocolObservations, err := wrc.protocolCtx.ProcessProtocolEndpointWebsocketMessage(msgData)
	if err != nil {
		logger.Error().Err(err).Msg("❌ failed to perform protocol-level endpoint message processing")
		return nil, nil, err
	}
	messageObservations.Protocol = &protocolObservations

	// TODO_TECHDEBT(@commoddity): process message using QoS context and update the message observations.
	// For example, for JSON-RPC method send through Websocket, the QoS context should:
	//   - Check the request payload.
	//   - Detect it is a subscription request.
	//   - Validate the request, e.g. params field.
	//   - Reject invalid Websocket requests, similar to HTTP requests. (e.g. invalid params field)
	// messageObservations.Qos = wrc.qosCtx.ProcessProtocolEndpointWebsocketMessage(msgData)

	return endpointMessageBz, messageObservations, nil
}

// ---------- Listeners ----------

// listenForMessageNotifications listens for message processing notifications from
// the bridge and publishes observations for each message processed.
//
// This method runs in a goroutine and handles:
//   - Message observations: Received from the bridge and then broadcast to metrics and data reporters.
//   - Channel closure: Clean shutdown when bridge closes the observation channel
//   - Context cancellation: Clean shutdown when connection context is canceled
func (wrc *websocketRequestContext) listenForMessageNotifications() {
	for {
		select {
		case messageObservations, ok := <-wrc.messageObservationsChan:
			if !ok {
				// Channel was closed by the bridge, stop listening
				wrc.logger.Debug().Msg("Message observation channel closed by bridge, stopping listener")
				return
			}
			// Message was processed successfully
			wrc.BroadcastMessageObservations(messageObservations)

		case <-wrc.context.Done():
			// Context canceled, stop listening
			wrc.logger.Debug().Msg("Message notification listener stopped due to context cancellation")
			return
		}
	}
}

// listenForConnectionObservations listens for connection observations from the protocol layer
// and broadcasts them based on the event type (establishment, closure, failure).
func (wrc *websocketRequestContext) listenForConnectionObservations(observationChan <-chan *protocolobservations.Observations) {
	for {
		select {
		case protocolObs, ok := <-observationChan:
			if !ok {
				// Channel was closed by the protocol, stop listening
				wrc.logger.Debug().Msg("Connection observation channel closed, stopping listener")
				return
			}

			if protocolObs == nil {
				continue
			}

			// Handle connection observations
			wrc.handleConnectionObservation(protocolObs)

		case <-wrc.context.Done():
			// Context canceled, stop listening
			wrc.logger.Debug().Msg("Connection observation listener stopped due to context cancellation")
			return
		}
	}
}

// handleConnectionObservation processes a single connection observation and broadcasts it appropriately
// based on the connection event type.
func (wrc *websocketRequestContext) handleConnectionObservation(protocolObs *protocolobservations.Observations) {
	// Check if this is a Shannon Websocket connection observation
	shannonObs := protocolObs.GetShannon()
	if shannonObs == nil || len(shannonObs.GetObservations()) == 0 {
		wrc.logger.Warn().Msg("Received connection observation without Shannon data")
		return
	}

	// Process each Shannon observation (should only be connection observations)
	for _, shannonReqObs := range shannonObs.GetObservations() {
		if obsData, ok := shannonReqObs.GetObservationData().(*protocolobservations.ShannonRequestObservations_WebsocketConnectionObservation); ok {
			// Handle connection lifecycle events
			connObs := obsData.WebsocketConnectionObservation

			// Extract domain for metrics
			domain, domainErr := shannonmetrics.ExtractDomainOrHost(connObs.GetEndpointUrl())
			if domainErr != nil {
				domain = shannonmetrics.ErrDomain
			}
			serviceID := string(wrc.serviceID)

			switch connObs.GetEventType() {
			case protocolobservations.ShannonWebsocketConnectionObservation_CONNECTION_ESTABLISHED:
				wrc.logger.Debug().Msg("Received connection establishment observation from protocol layer")
				// Record successful connection establishment metric
				metrics.RecordWebsocketConnectionEstablished(domain, serviceID)
				wrc.broadcastWebsocketConnectionEstablished(protocolObs)

			case protocolobservations.ShannonWebsocketConnectionObservation_CONNECTION_CLOSED:
				wrc.logger.Debug().Msg("Received connection closure observation from protocol layer")
				// Calculate connection duration from timestamps
				var durationSeconds float64
				if connObs.GetConnectionEstablishedTimestamp() != nil && connObs.GetConnectionClosedTimestamp() != nil {
					establishedTime := connObs.GetConnectionEstablishedTimestamp().AsTime()
					closedTime := connObs.GetConnectionClosedTimestamp().AsTime()
					durationSeconds = closedTime.Sub(establishedTime).Seconds()
				}
				// Record connection closure metric with duration
				metrics.RecordWebsocketConnectionClosed(domain, serviceID, durationSeconds)
				wrc.broadcastWebsocketConnectionClosed(protocolObs)

			case protocolobservations.ShannonWebsocketConnectionObservation_CONNECTION_ESTABLISHMENT_FAILED:
				wrc.logger.Debug().Msg("Received connection establishment failure observation from protocol layer")
				// Record connection failure metric
				metrics.RecordWebsocketConnectionFailed(domain, serviceID)
				wrc.broadcastWebsocketConnectionEstablished(protocolObs) // Treat as establishment event for metrics
			}
		} else {
			wrc.logger.Warn().Msg("Received non-connection observation on connection channel")
		}
	}
}

// ---------- Websocket Message Observations ----------

// BroadcastMessageObservations delivers the collected details regarding all aspects
// of the websocket message to all the interested parties.
func (wrc *websocketRequestContext) BroadcastMessageObservations(
	messageObservations *observation.RequestResponseObservations,
) {
	// Safety check: don't process nil observations
	if messageObservations == nil {
		wrc.logger.Warn().Msg("Received nil messageObservations, skipping broadcast")
		return
	}

	// observation-related tasks are called in Goroutines to avoid potentially blocking the handler.
	go func() {
		if protocolObservations := messageObservations.GetProtocol(); protocolObservations != nil {
			err := wrc.protocol.ApplyWebSocketObservations(protocolObservations)
			if err != nil {
				wrc.logger.Warn().Err(err).Msg("error applying protocol observations for websocket.")
			}
		}

		// Apply QoS observations
		if qosObservations := messageObservations.GetQos(); qosObservations != nil {
			if err := wrc.serviceQoS.ApplyObservations(qosObservations); err != nil {
				wrc.logger.Warn().Err(err).Msg("error applying QoS observations for websocket.")
			}
		}

		// Prepare and publish observations to both the metrics and data reporters.
		observations := &observation.RequestResponseObservations{
			Gateway:  wrc.gatewayObservations,
			Protocol: messageObservations.Protocol,
			Qos:      messageObservations.Qos,
		}
		if wrc.metricsReporter != nil {
			wrc.metricsReporter.Publish(observations)
		}
		if wrc.dataReporter != nil {
			wrc.dataReporter.Publish(observations)
		}
	}()
}

// initializeMessageObservations creates a copy of observations.
//
// Once the connection is established, gateway-level observations are shared
// between all messages for a single websocket connection so we initialize
// a copy of the `RequestResponseObservations` struct containing the gateway
// observations and the service ID.
func (wrc *websocketRequestContext) initializeMessageObservations() *observation.RequestResponseObservations {
	return &observation.RequestResponseObservations{
		ServiceId: string(wrc.serviceID),
		Gateway:   wrc.gatewayObservations,
	}
}

// ---------- Websocket Connection Observations ----------

// broadcastWebsocketConnectionEstablished broadcasts a protocol-level observation specifically
// for connection establishment to inform the metrics system that a Websocket connection was established.
// This method only publishes to the metrics reporter, not the data reporter.
func (wrc *websocketRequestContext) broadcastWebsocketConnectionEstablished(protocolObservations *protocolobservations.Observations) {
	// Create a protocol-only observation for connection establishment
	observations := &observation.RequestResponseObservations{
		ServiceId: string(wrc.serviceID),
		Protocol:  protocolObservations,
	}

	// Only publish to metrics reporter for connection tracking
	if wrc.metricsReporter != nil {
		wrc.metricsReporter.Publish(observations)
	}
}

// broadcastWebsocketConnectionClosed broadcasts a connection closure observation.
// This method publishes closure events to BOTH metrics and data pipeline.
func (wrc *websocketRequestContext) broadcastWebsocketConnectionClosed(protocolObservations *protocolobservations.Observations) {
	wrc.updateGatewayObservations(nil)

	// Update protocol observations to get final connection state
	wrc.updateProtocolObservations(nil)

	// Combine all observations into the standard RequestResponseObservations format
	observations := &observation.RequestResponseObservations{
		Gateway:  wrc.gatewayObservations,
		Protocol: protocolObservations,
		// TODO_IMPROVE: add QoS observations for Websocket connection observations.
		Qos: nil,
	}

	// Broadcast the combined observations to BOTH metrics and data pipeline
	if wrc.metricsReporter != nil {
		wrc.metricsReporter.Publish(observations)
	}
	if wrc.dataReporter != nil {
		wrc.dataReporter.Publish(observations)
	}
}

// updateProtocolObservations updates the stored protocol-level connection observations for Websocket connections.
// It is called at:
//   - Protocol context setup error during connection establishment
//   - When broadcasting connection observations
//
// This is specifically for connection-level observations, separate from per-message observations.
func (wrc *websocketRequestContext) updateProtocolObservations(
	protocolConnectionObservations *protocolobservations.Observations,
) {
	// protocol connection observation already set: skip.
	// This happens when a protocol context setup observation was reported earlier.
	if protocolConnectionObservations != nil {
		wrc.logger.Debug().
			Str("protocol_connection_observations", protocolConnectionObservations.String()).
			Msg("Setting protocol connection observations")
		return
	}

	// This should never happen: either protocol context is setup, or an observation is reported to use directly for the request.
	wrc.logger.
		With("service_id", wrc.serviceID).
		ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).
		Msg("SHOULD NEVER HAPPEN: Websocket protocol context is nil, but no protocol setup observation have been reported.")
}

// updateGatewayObservations updates the gateway-level observations in the websocket request context.
func (wrc *websocketRequestContext) updateGatewayObservations(err error) {
	// set the service ID on the gateway observations.
	wrc.gatewayObservations.ServiceId = string(wrc.serviceID)

	// Update the request completion time on the gateway observation
	wrc.gatewayObservations.CompletedTime = timestamppb.Now()

	// No errors: skip.
	if err == nil {
		return
	}

	// Request error already set: skip.
	if wrc.gatewayObservations.GetRequestError() != nil {
		return
	}

	// Classify Websocket-specific errors based on error type
	switch {
	// Service ID not specified
	case errors.Is(err, ErrGatewayNoServiceIDProvided):
		wrc.logger.Error().Err(err).Msg("No service ID specified in the HTTP headers. Websocket request will fail.")
		wrc.gatewayObservations.RequestError = &observation.GatewayRequestError{
			ErrorKind: observation.GatewayRequestErrorKind_GATEWAY_REQUEST_ERROR_KIND_MISSING_SERVICE_ID,
			Details:   err.Error(),
		}

	// Websocket request was rejected by QoS instance
	case errors.Is(err, errWebsocketRequestRejectedByQoS):
		wrc.logger.Error().Err(err).Msg("QoS instance rejected the Websocket request. Request will fail.")
		wrc.gatewayObservations.RequestError = &observation.GatewayRequestError{
			ErrorKind: observation.GatewayRequestErrorKind_GATEWAY_REQUEST_ERROR_KIND_WEBSOCKET_REJECTED_BY_QOS,
			Details:   err.Error(),
		}

	// Websocket connection establishment failed
	case errors.Is(err, errWebsocketConnectionFailed):
		wrc.logger.Error().Err(err).Msg("Websocket connection establishment failed. Request will fail.")
		wrc.gatewayObservations.RequestError = &observation.GatewayRequestError{
			ErrorKind: observation.GatewayRequestErrorKind_GATEWAY_REQUEST_ERROR_KIND_WEBSOCKET_CONNECTION_FAILED,
			Details:   err.Error(),
		}

	// Generic QoS rejection (fallback for backward compatibility)
	case errors.Is(err, errGatewayRejectedByQoS):
		wrc.logger.Error().Err(err).Msg("QoS instance rejected the request. Request will fail.")
		wrc.gatewayObservations.RequestError = &observation.GatewayRequestError{
			ErrorKind: observation.GatewayRequestErrorKind_GATEWAY_REQUEST_ERROR_KIND_REJECTED_BY_QOS,
			Details:   err.Error(),
		}

	default:
		wrc.logger.Warn().Err(err).Msg("SHOULD NEVER HAPPEN: unrecognized Websocket gateway-level request error.")
		// Set a generic request error observation
		wrc.gatewayObservations.RequestError = &observation.GatewayRequestError{
			// unspecified error kind: this should not happen
			ErrorKind: observation.GatewayRequestErrorKind_GATEWAY_REQUEST_ERROR_KIND_UNSPECIFIED,
			Details:   err.Error(),
		}
	}
}

// getWebSocketConnectionLogger returns a logger with attributes set using the supplied HTTP request.
func (wrc *websocketRequestContext) getWebSocketConnectionLogger(httpReq *http.Request) polylog.Logger {
	var urlStr string
	if httpReq.URL != nil {
		urlStr = httpReq.URL.String()
	}

	return wrc.logger.With(
		"ws_req_url", urlStr,
		"ws_req_host", httpReq.Host,
		"ws_req_remote_addr", httpReq.RemoteAddr,
		"ws_req_content_length", httpReq.ContentLength,
		"request_type", "websocket",
	)
}
