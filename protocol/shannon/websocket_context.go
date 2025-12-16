package shannon

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/pokt-network/poktroll/pkg/relayer/proxy"
	servicetypes "github.com/pokt-network/poktroll/x/service/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	sdk "github.com/pokt-network/shannon-sdk"

	"github.com/pokt-network/path/gateway"
	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	reputationmetrics "github.com/pokt-network/path/metrics/reputation"
	"github.com/pokt-network/path/observation"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
	"github.com/pokt-network/path/request"
	"github.com/pokt-network/path/websockets"
)

// The requestContext implements the gateway.ProtocolRequestContextWebsocket interface.
// It handles protocol-level Websocket message processing for both client and endpoint messages.
// For example, client messages are signed and endpoint messages are validated.
var _ gateway.ProtocolRequestContextWebsocket = &websocketRequestContext{}

type websocketRequestContext struct {
	logger polylog.Logger

	// context for timeout propagation and cancellation
	context context.Context

	// fullNode is used for retrieving onchain data.
	fullNode FullNode

	// serviceID is the service ID for the request.
	serviceID protocol.ServiceID

	// relayRequestSigner is used for signing relay requests.
	relayRequestSigner RelayRequestSigner

	// selectedEndpoint:
	//   - Endpoint selected for sending a relay.
	//   - Must be set via setSelectedEndpoint before sending a relay (otherwise sending fails).
	//   - Protected by selectedEndpointMutex for thread safety.
	selectedEndpoint endpoint

	// reputationService tracks endpoint reputation scores.
	// If non-nil, signals are recorded on success/error for gradual reputation tracking.
	reputationService reputation.ReputationService
}

// ---------- Websocket Request Context Setup  ----------

// BuildWebsocketRequestContextForEndpoint creates a new Websocket protocol request context for a specified service and endpoint.
// This method immediately establishes the Websocket connection and starts the bridge.
//
// Parameters:
//   - ctx: Context for cancellation, deadlines, and logging.
//   - serviceID: The unique identifier of the target service.
//   - selectedEndpointAddr: The address of the endpoint to use for the request.
//   - httpReq: HTTP request used for Websocket upgrade and delegated mode app extraction.
//   - httpResponseWriter: HTTP response writer for Websocket upgrade.
//   - messageObservationsChan: Channel for sending message-level observations to the gateway.
func (p *Protocol) BuildWebsocketRequestContextForEndpoint(
	ctx context.Context,
	serviceID protocol.ServiceID,
	selectedEndpointAddr protocol.EndpointAddr,
	websocketMessageProcessor websockets.WebsocketMessageProcessor,
	httpReq *http.Request,
	httpResponseWriter http.ResponseWriter,
	// TODO_TECHDEBT(@commoddity): this channel should be created here, not passed to it, as protocol is the producer side of the channel.
	messageObservationsChan chan *observation.RequestResponseObservations,
) (gateway.ProtocolRequestContextWebsocket, <-chan *protocolobservations.Observations, error) {
	logger := p.logger.With(
		"method", "BuildWebsocketRequestContextForEndpoint",
		"service_id", serviceID,
		"endpoint_addr", selectedEndpointAddr,
	)

	selectedEndpoint, err := p.getPreSelectedEndpoint(ctx, serviceID, selectedEndpointAddr, httpReq, sharedtypes.RPCType_WEBSOCKET)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get pre-selected endpoint")
		return nil, nil, err
	}

	// Retrieve the relay request signer for the current gateway mode.
	permittedSigner, err := p.getGatewayModePermittedRelaySigner(p.gatewayMode)
	if err != nil {
		// Wrap the context setup error.
		// Used to generate the observation.
		err = fmt.Errorf("%w: gateway mode %s: %w", errRequestContextSetupErrSignerSetup, p.gatewayMode, err)
		return nil, nil, err
	}

	// Create Websocket request context for the pre-selected endpoint
	wrc := &websocketRequestContext{
		logger:             logger,
		context:            ctx,
		fullNode:           p.FullNode,
		selectedEndpoint:   selectedEndpoint,
		serviceID:          serviceID,
		relayRequestSigner: permittedSigner,
		reputationService:  p.reputationService,
	}

	// Create observation channel for connection-level observations only
	// Buffer size of 10 should be sufficient for connection lifecycle events
	connectionObservationChan := make(chan *protocolobservations.Observations, 10)

	// Start the Websocket bridge immediately
	// This handles connection establishment and message processing
	err = wrc.startWebSocketBridge(
		ctx,
		httpReq,
		httpResponseWriter,
		websocketMessageProcessor,
		messageObservationsChan,
		connectionObservationChan,
	)
	if err != nil {
		// Close the observation channel on error to prevent resource leaks
		close(connectionObservationChan)
		logger.Error().Err(err).Msg("Failed to start Websocket bridge")
		return nil, nil, fmt.Errorf("failed to start Websocket bridge: %w", err)
	}

	return wrc, connectionObservationChan, nil
}

// CheckWebsocketConnection checks if the websocket connection to the endpoint is established.
// This method is used by the websocket hydrator to check if the endpoint supports websocket RPC type.
// It uses a simplified version of the websocket bridge connection process to avoid unnecessary overhead.
func (p *Protocol) CheckWebsocketConnection(
	ctx context.Context,
	serviceID protocol.ServiceID,
	selectedEndpointAddr protocol.EndpointAddr,
) *protocolobservations.Observations {
	logger := p.logger.With("method", "CheckWebsocketConnection")

	// Get the pre-selected endpoint.
	selectedEndpoint, err := p.getPreSelectedEndpoint(ctx, serviceID, selectedEndpointAddr, nil, sharedtypes.RPCType_WEBSOCKET)
	if err != nil {
		err = fmt.Errorf("⁉️ SHOULD NEVER HAPPEN: failed to get pre-selected endpoint: %s", err.Error())
		// Will not lead to reputation penalty as this does not indicate a problem with the endpoint, nor should it ever happen.
		return getWebsocketConnectionErrorObservation(logger, serviceID, selectedEndpoint, err)
	}

	// Get the websocket-specific URL from the selected endpoint.
	websocketEndpointURL, err := getWebsocketEndpointURL(logger, selectedEndpoint)
	if err != nil {
		err = fmt.Errorf("%w: selected endpoint does not support websocket RPC type: %s", errCreatingWebSocketConnection, err.Error())
		logger.Debug().Err(err).Msg("❌ Selected endpoint does not support websocket RPC type")
		return getWebsocketConnectionErrorObservation(logger, serviceID, selectedEndpoint, err)
	}
	logger = logger.With("websocket_url", websocketEndpointURL)

	// Get the headers for the websocket connection that will be sent to the endpoint.
	endpointConnectionHeaders, err := getWebsocketConnectionHeaders(logger, selectedEndpoint)
	if err != nil {
		err = fmt.Errorf("%w: failed to get websocket connection headers: %s", errCreatingWebSocketConnection, err.Error())
		logger.Debug().Err(err).Msg("❌ Failed to get websocket connection headers")
		return getWebsocketConnectionErrorObservation(logger, serviceID, selectedEndpoint, err)
	}

	// Test the websocket connection to the endpoint.
	_, err = websockets.ConnectWebsocketEndpoint(
		logger,
		websocketEndpointURL,
		endpointConnectionHeaders,
	)
	if err != nil {
		err = fmt.Errorf("%w: failed to connect to websocket endpoint: %s", errCreatingWebSocketConnection, err.Error())
		logger.Debug().Err(err).Msg("❌ Failed to connect to websocket endpoint")
		return getWebsocketConnectionErrorObservation(logger, serviceID, selectedEndpoint, err)
	}

	// A nil obsservation means no error occurred.
	return nil
}

func (p *Protocol) getPreSelectedEndpoint(
	ctx context.Context,
	serviceID protocol.ServiceID,
	selectedEndpointAddr protocol.EndpointAddr,
	httpReq *http.Request,
	rpcType sharedtypes.RPCType,
) (endpoint, error) {
	logger := p.logger.With("method", "getPreSelectedEndpoint")

	activeSessions, err := p.getActiveGatewaySessions(ctx, serviceID, httpReq)
	if err != nil {
		logger.Error().Err(err).Msgf("Relay request will fail due to error retrieving active sessions for service %s", serviceID)
		return nil, err
	}

	// Parse allowed suppliers from header (if present)
	allowedSuppliers := parseAllowedSuppliersHeader(httpReq)

	// Retrieve the list of endpoints (i.e. backend service URLs by external operators)
	// that can service RPC requests for the given service ID for the given apps.
	// This includes fallback logic if session endpoints are unavailable.
	// The final boolean parameter sets whether to filter by reputation.
	// The final slice parameter optionally restricts endpoints to specific allowed suppliers.
	endpoints, actualRPCType, err := p.getUniqueEndpoints(ctx, serviceID, activeSessions, true, rpcType, allowedSuppliers)
	if err != nil {
		logger.Error().Err(err).Msg(err.Error())
		return nil, err
	}

	// Log if RPC type fallback occurred
	if actualRPCType != rpcType {
		logger.Info().
			Str("requested_rpc_type", rpcType.String()).
			Str("actual_rpc_type", actualRPCType.String()).
			Msg("RPC type fallback was applied for websocket endpoint selection")
	}

	// Select the endpoint that matches the pre-selected address.
	// This ensures QoS checks are performed on the selected endpoint.
	selectedEndpoint, ok := endpoints[selectedEndpointAddr]
	if !ok {
		// Wrap the context setup error.
		// Used to generate the observation.
		err := fmt.Errorf("%w: service %s endpoint %s", errRequestContextSetupInvalidEndpointSelected, serviceID, selectedEndpointAddr)
		logger.Error().Err(err).Msg("Selected endpoint is not available.")
		return nil, err
	}

	return selectedEndpoint, nil
}

// ApplyWebSocketObservations updates protocol instance state based on endpoint observations.
// Records reputation signals from WebSocket health check observations,
// allowing endpoints to recover from failures via successful health checks.
//
// Implements gateway.Protocol interface.
func (p *Protocol) ApplyWebSocketObservations(observations *protocolobservations.Observations) error {
	// Sanity check the input
	if observations == nil || observations.GetShannon() == nil {
		p.logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: ApplyWebSocketObservations called with nil input or nil Shannon observation list.")
		return nil
	}

	shannonObservations := observations.GetShannon().GetObservations()
	if len(shannonObservations) == 0 {
		p.logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: ApplyWebSocketObservations called with nil set of Shannon request observations.")
		return nil
	}

	// Record reputation signals from observations.
	// This allows health check results (from hydrator) to update endpoint reputation scores.
	if p.reputationService != nil {
		p.recordReputationSignalsFromWebsocketObservations(shannonObservations)
	}

	return nil
}

// recordReputationSignalsFromWebsocketObservations maps websocket protocol observations to reputation signals.
// This is called by ApplyWebSocketObservations to update endpoint reputation scores based on
// health check results from the hydrator or any other observation source.
func (p *Protocol) recordReputationSignalsFromWebsocketObservations(shannonObservations []*protocolobservations.ShannonRequestObservations) {
	for _, observationSet := range shannonObservations {
		serviceID := protocol.ServiceID(observationSet.GetServiceId())

		// Process connection observations
		connObs := observationSet.GetWebsocketConnectionObservation()
		if connObs != nil {
			p.recordSignalFromWebsocketConnectionObservation(serviceID, connObs)
		}
	}
}

// recordSignalFromWebsocketConnectionObservation records a reputation signal for a websocket connection observation.
func (p *Protocol) recordSignalFromWebsocketConnectionObservation(serviceID protocol.ServiceID, obs *protocolobservations.ShannonWebsocketConnectionObservation) {
	endpointAddr := protocol.EndpointAddr(obs.GetEndpointUrl())

	// Build endpoint key for reputation service with WEBSOCKET RPC type
	key := reputation.NewEndpointKey(serviceID, endpointAddr, sharedtypes.RPCType_WEBSOCKET)

	// Map observation error type to signal
	// Note: We only have errorType in observations now (sanctions removed)
	errorType := obs.GetErrorType()

	var signal reputation.Signal

	// No error = success
	if errorType == protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNSPECIFIED {
		signal = reputation.NewSuccessSignal(0)
	} else {
		// Map error type directly to reputation signal
		// See ERROR_CLASSIFICATION.md for error category documentation
		signal = errorTypeToSignal(errorType, 0)
	}

	// Record signal (fire-and-forget, non-blocking)
	ctx := context.Background()
	if err := p.reputationService.RecordSignal(ctx, key, signal); err != nil {
		p.logger.Warn().Err(err).
			Str("endpoint", string(endpointAddr)).
			Str("service", string(serviceID)).
			Str("error_type", errorType.String()).
			Msg("Failed to record reputation signal from websocket observation")
	}
}

// ---------- Connection Establishment ----------

// startWebSocketBridge creates and starts a Websocket bridge between client and endpoint.
// It handles all protocol-specific setup including headers, URL generation, and connection establishment.
// This is a private method called by BuildWebsocketRequestContextForEndpoint.
func (wrc *websocketRequestContext) startWebSocketBridge(
	ctx context.Context,
	httpRequest *http.Request,
	httpResponseWriter http.ResponseWriter,
	websocketMessageProcessor websockets.WebsocketMessageProcessor,
	messageObservationsChan chan *observation.RequestResponseObservations,
	connectionObservationChan chan *protocolobservations.Observations,
) error {
	wrc.hydratedLogger("StartWebSocketBridge")

	// Get the websocket-specific URL from the selected endpoint.
	websocketEndpointURL, err := getWebsocketEndpointURL(wrc.logger, wrc.selectedEndpoint)
	if err != nil {
		err = fmt.Errorf("%w: selected endpoint does not support websocket RPC type: %s", errCreatingWebSocketConnection, err.Error())
		wrc.logger.Error().Err(err).Msg("❌ Selected endpoint does not support websocket RPC type")

		// Record critical error signal - endpoint doesn't support required RPC type.
		// Latency=0 indicates this is a setup/configuration error, not a response time measurement.
		wrc.recordWebsocketSignal(reputation.NewCriticalErrorSignal("ws_url_not_supported", 0))
		connectionObservationChan <- getWebsocketConnectionErrorObservation(wrc.logger, wrc.serviceID, wrc.selectedEndpoint, err)

		return fmt.Errorf("selected endpoint does not support websocket RPC type: %w", err)
	}
	wrc.logger = wrc.logger.With("websocket_url", websocketEndpointURL)

	// Get the headers for the websocket connection that will be sent to the endpoint.
	endpointConnectionHeaders, err := getWebsocketConnectionHeaders(wrc.logger, wrc.selectedEndpoint)
	if err != nil {
		err = fmt.Errorf("%w: failed to get websocket connection headers: %s", errCreatingWebSocketConnection, err.Error())
		wrc.logger.Error().Err(err).Msg("❌ Failed to get websocket connection headers")

		// Record major error signal - header construction failed.
		// Latency=0 indicates this is a setup error, not a response time measurement.
		wrc.recordWebsocketSignal(reputation.NewMajorErrorSignal("ws_headers_failed", 0))
		connectionObservationChan <- getWebsocketConnectionErrorObservation(wrc.logger, wrc.serviceID, wrc.selectedEndpoint, err)

		return fmt.Errorf("failed to get websocket connection headers: %w", err)
	}

	// Start the websocket bridge and get a completion channel.
	// The websocketRequestContext handles message processing.
	bridgeCompletionChan, err := websockets.StartBridge(
		ctx,
		wrc.logger,
		httpRequest,
		httpResponseWriter,
		websocketEndpointURL,
		endpointConnectionHeaders,
		websocketMessageProcessor,
		messageObservationsChan,
	)
	if err != nil {
		err = fmt.Errorf("%w: failed to start websocket bridge: %s", errCreatingWebSocketConnection, err.Error())
		wrc.logger.Error().Err(err).Msg("❌ Failed to start Websocket bridge")

		// Record major error signal - connection establishment failed.
		// Latency=0 indicates this is a connection setup error, not a response time measurement.
		wrc.recordWebsocketSignal(reputation.NewMajorErrorSignal("ws_connection_failed", 0))
		connectionObservationChan <- getWebsocketConnectionErrorObservation(wrc.logger, wrc.serviceID, wrc.selectedEndpoint, err)

		return fmt.Errorf("failed to start websocket bridge: %w", err)
	}

	// Start goroutine to handle bridge lifecycle observations
	go func() {
		defer close(connectionObservationChan)

		// Send establishment observation immediately (buffered channel ensures it's captured)
		wrc.logger.Info().Msg("✅ Websocket bridge started successfully, sending establishment observation")
		connectionObservationChan <- getWebsocketConnectionEstablishedObservation(wrc.logger, wrc.serviceID, wrc.selectedEndpoint)

		// Wait for the bridge to complete (blocks until Websocket connection terminates)
		<-bridgeCompletionChan
		// Send closure observation
		wrc.logger.Info().Msg("🔌 Websocket connection closed, sending closure observation")
		connectionObservationChan <- getWebsocketConnectionClosedObservation(wrc.logger, wrc.serviceID, wrc.selectedEndpoint)
	}()

	return nil
}

// getWebsocketConnectionHeaders returns headers for the websocket connection:
//   - Target-Service-Id: The service ID of the target service
//   - App-Address: The address of the session's application
//   - Rpc-Type: Always "websocket" for websocket connection requests
func getWebsocketConnectionHeaders(logger polylog.Logger, selectedEndpoint endpoint) (http.Header, error) {
	// Requests to fallback endpoints bypass the protocol so RelayMiner headers are not needed.
	// TODO_IMPROVE(@commoddity,@adshmh): Cleanly separate fallback endpoint handling from the protocol package.
	// TODO_ARCHITECTURE: Extract fallback endpoint handling from protocol package
	// Current: Fallback logic is scattered with if wrc.selectedEndpoint.IsFallback() checks
	// Suggestion: Use strategy pattern or separate fallback handler to cleanly separate concerns
	if selectedEndpoint.IsFallback() {
		return http.Header{}, nil
	}

	// If the selected endpoint is a protocol endpoint, add the headers
	// that the RelayMiner requires to forward the request to the Endpoint.
	return getRelayMinerConnectionHeaders(logger, selectedEndpoint)
}

// getRelayMinerConnectionHeaders returns headers for RelayMiner websocket connections.
func getRelayMinerConnectionHeaders(logger polylog.Logger, selectedEndpoint endpoint) (http.Header, error) {
	logger.With("method", "getRelayMinerConnectionHeaders")

	sessionHeader := selectedEndpoint.Session().GetHeader()

	if sessionHeader == nil {
		logger.Error().Msg("❌ SHOULD NEVER HAPPEN: Error getting relay miner connection headers: session header is nil")
		return http.Header{}, fmt.Errorf("session header is nil")
	}

	return http.Header{
		request.HTTPHeaderTargetServiceID: {sessionHeader.ServiceId},
		request.HTTPHeaderAppAddress:      {sessionHeader.ApplicationAddress},
		proxy.RPCTypeHeader:               {strconv.Itoa(int(sharedtypes.RPCType_WEBSOCKET))},
	}, nil
}

// getWebsocketEndpointURL returns the websocket URL for the selected endpoint.
// This URL is used to establish the websocket connection to the endpoint.
func getWebsocketEndpointURL(logger polylog.Logger, selectedEndpoint endpoint) (string, error) {
	logger.With("method", "getWebsocketEndpointURL")

	websocketURL, err := selectedEndpoint.WebsocketURL()
	if err != nil {
		logger.Error().Err(err).Msg("❌ Selected endpoint does not support websocket RPC type")
		return "", err
	}

	return websocketURL, nil
}

// ---------- Client Message Processing ----------

// ProcessProtocolClientWebsocketMessage processes a message from the client.
// Implements gateway.ProtocolRequestContextWebsocket interface.
func (wrc *websocketRequestContext) ProcessProtocolClientWebsocketMessage(msgData []byte) ([]byte, error) {
	wrc.hydratedLogger("ProcessClientWebsocketMessage")

	wrc.logger.Debug().Msgf("received message from client: %s", string(msgData))

	// If the selected endpoint is a fallback endpoint, skip signing the message.
	// Fallback endpoints bypass the protocol so the raw message is sent to the endpoint.
	// TODO_IMPROVE(@commoddity,@adshmh): Cleanly separate fallback endpoint handling from the protocol package.
	if wrc.selectedEndpoint.IsFallback() {
		return msgData, nil
	}

	// If the selected endpoint is a protocol endpoint, we need to sign the message.
	signedRelayRequest, err := wrc.signClientWebsocketMessage(msgData)
	if err != nil {
		wrc.logger.Error().Err(err).Msg("❌ failed to sign request")
		return nil, err
	}

	return signedRelayRequest, nil
}

// signClientWebsocketMessage signs a message from the client using the Relay Request Signer.
func (wrc *websocketRequestContext) signClientWebsocketMessage(msgData []byte) ([]byte, error) {
	wrc.hydratedLogger("signClientWebsocketMessage")

	unsignedRelayRequest := &servicetypes.RelayRequest{
		Meta: servicetypes.RelayRequestMetadata{
			SessionHeader:           wrc.selectedEndpoint.Session().GetHeader(),
			SupplierOperatorAddress: wrc.selectedEndpoint.Supplier(),
		},
		Payload: msgData,
	}

	app := wrc.selectedEndpoint.Session().GetApplication()
	if app == nil {
		wrc.logger.Error().Msg("❌ SHOULD NEVER HAPPEN: session application is nil")
		return nil, fmt.Errorf("session application is nil")
	}

	signedRelayRequest, err := wrc.relayRequestSigner.SignRelayRequest(unsignedRelayRequest, *app)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errRelayRequestWebsocketMessageSigningFailed, err.Error())
	}

	relayRequestBz, err := signedRelayRequest.Marshal()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errRelayRequestWebsocketMessageSigningFailed, err.Error())
	}

	return relayRequestBz, nil
}

// ---------- Endpoint Message Processing ----------

// ProcessProtocolEndpointWebsocketMessage processes a message from the endpoint.
func (wrc *websocketRequestContext) ProcessProtocolEndpointWebsocketMessage(
	msgData []byte,
) ([]byte, protocolobservations.Observations, error) {
	wrc.hydratedLogger("ProcessEndpointWebsocketMessage")
	startTime := time.Now()

	wrc.logger.Debug().Msgf("received message from endpoint: %s", string(msgData))

	// If the selected endpoint is a fallback endpoint, skip validation.
	// Fallback endpoints bypass the protocol so the raw message is sent to the endpoint.
	// TODO_IMPROVE(@commoddity,@adshmh): Cleanly separate fallback endpoint handling from the protocol package.
	if wrc.selectedEndpoint.IsFallback() {
		// Record success signal for fallback endpoint messages
		wrc.recordWebsocketSignal(reputation.NewSuccessSignal(time.Since(startTime)))
		return msgData, getWebsocketMessageSuccessObservation(wrc.logger, wrc.serviceID, wrc.selectedEndpoint, msgData), nil
	}

	// If the selected endpoint is a protocol endpoint, we need to validate the message.
	validatedRelayResponse, err := wrc.validateEndpointWebsocketMessage(msgData)
	if err != nil {
		wrc.logger.Error().Err(err).Msg("❌ failed to validate relay response")
		// Record error signal for message validation failure
		wrc.recordWebsocketSignal(reputation.NewMajorErrorSignal("ws_message_validation_failed", time.Since(startTime)))
		return nil, getWebsocketMessageErrorObservation(wrc.logger, wrc.serviceID, wrc.selectedEndpoint, msgData, err), err
	}

	// Record success signal for validated message
	wrc.recordWebsocketSignal(reputation.NewSuccessSignal(time.Since(startTime)))
	return validatedRelayResponse, getWebsocketMessageSuccessObservation(wrc.logger, wrc.serviceID, wrc.selectedEndpoint, msgData), nil
}

// validateEndpointWebsocketMessage validates a message from the endpoint using the Shannon FullNode.
// TODO_IMPROVE(@adshmh): Compare this to 'validateAndProcessResponse' and align the two implementations
// w.r.t design, error handling, etc...
func (wrc *websocketRequestContext) validateEndpointWebsocketMessage(msgData []byte) ([]byte, error) {
	wrc.hydratedLogger("validateEndpointWebsocketMessage")

	// Validate the relay response using the Shannon FullNode
	relayResponse, err := wrc.fullNode.ValidateRelayResponse(sdk.SupplierAddress(wrc.selectedEndpoint.Supplier()), msgData)
	if err != nil {
		wrc.logger.Error().Err(err).Msg("❌ failed to validate relay response in websocket message")
		return nil, fmt.Errorf("%w: %s", errRelayResponseInWebsocketMessageValidationFailed, err.Error())
	}
	wrc.logger.Debug().Msgf("received message from protocol endpoint: %s", string(relayResponse.Payload))

	return relayResponse.Payload, nil
}

// ---------- Logger Helpers ----------

// hydratedLogger:
// - Enhances the base logger with information from the request context.
// - Includes:
//   - Method name
//   - Service ID
//   - Selected endpoint supplier
//   - Selected endpoint URL
func (wrc *websocketRequestContext) hydratedLogger(methodName string) {
	logger := wrc.logger.With(
		"request_type", "websocket",
		"method", methodName,
		"service_id", wrc.serviceID,
	)

	defer func() {
		wrc.logger = logger
	}()

	// No endpoint specified on request context.
	// - This should never happen.
	selectedEndpoint := wrc.selectedEndpoint
	if selectedEndpoint == nil {
		return
	}

	logger = logger.With(
		"selected_endpoint_supplier", selectedEndpoint.Supplier(),
		"selected_endpoint_url", selectedEndpoint.PublicURL(),
	)

	sessionHeader := selectedEndpoint.Session().Header
	if sessionHeader == nil {
		return
	}

	logger = logger.With(
		"selected_endpoint_app", sessionHeader.ApplicationAddress,
	)
}

// ---------- Reputation Signal Recording ----------

// recordWebsocketSignal records a reputation signal for the websocket endpoint.
// This tracks the health of endpoints used for websocket connections.
func (wrc *websocketRequestContext) recordWebsocketSignal(signal reputation.Signal) {
	if wrc.reputationService == nil || wrc.selectedEndpoint == nil {
		return
	}

	endpointKey := reputation.NewEndpointKey(wrc.serviceID, wrc.selectedEndpoint.Addr(), sharedtypes.RPCType_WEBSOCKET)

	// Extract domain for metrics
	endpointDomain, domainErr := shannonmetrics.ExtractDomainOrHost(wrc.selectedEndpoint.PublicURL())
	if domainErr != nil {
		endpointDomain = shannonmetrics.ErrDomain
	}

	wrc.logger.Debug().
		Str("endpoint_addr", string(wrc.selectedEndpoint.Addr())).
		Str("endpoint_domain", endpointDomain).
		Str("signal_type", string(signal.Type)).
		Msg("Recording websocket reputation signal")

	// Fire-and-forget: don't block request on reputation recording
	if err := wrc.reputationService.RecordSignal(wrc.context, endpointKey, signal); err != nil {
		wrc.logger.Warn().Err(err).Msg("Failed to record websocket reputation signal")
		reputationmetrics.RecordError("record_signal", "storage_error")
	} else {
		reputationmetrics.RecordSignal(string(wrc.serviceID), string(signal.Type), reputationmetrics.EndpointTypeWebSocket, endpointDomain)
	}
}
