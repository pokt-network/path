package shannon

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/alitto/pond/v2"
	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/pokt-network/poktroll/pkg/relayer/proxy"
	apptypes "github.com/pokt-network/poktroll/x/application/types"
	servicetypes "github.com/pokt-network/poktroll/x/service/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	sdk "github.com/pokt-network/shannon-sdk"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/metrics"
	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	pathhttp "github.com/pokt-network/path/network/http"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
)

// TODO_IMPROVE(@commoddity): Re-evaluate how much of this code should live in the shannon-sdk package.

// TODO_TECHDEBT(@olshansk): Cleanup the code in this file by:
// - Renaming this to request_context.go
// - Moving HTTP request code to a dedicated file

// TODO_TECHDEBT(@adshmh): Make this threshold configurable.
// Maximum endpoint payload length for error logging (100 chars)
const maxEndpointPayloadLenForLogging = 100

// MaxConcurrentRelaysPerRequest limits the number of concurrent relay goroutines per request.
// This prevents DoS attacks via large batch requests that could spawn unbounded goroutines.
// ✅ DONE: Now configurable via concurrency_config.max_batch_payloads in YAML (default: 5500)

// requestContext provides all the functionality required by the gateway package
// for handling a single service request.
var _ gateway.ProtocolRequestContext = &requestContext{}

// RelayRequestSigner:
//   - Used by requestContext to sign relay requests
//   - Takes an unsigned relay request and an application
//   - Returns a relay request signed by the gateway (with delegation from the app)
//   - In future Permissionless Gateway Mode, may use the app's own private key for signing
type RelayRequestSigner interface {
	SignRelayRequest(req *servicetypes.RelayRequest, app apptypes.Application) (*servicetypes.RelayRequest, error)
}

// requestContext captures all data required for handling a single service request.
type requestContext struct {
	logger polylog.Logger

	// Upstream context for timeout propagation and cancellation
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
	selectedEndpoint      endpoint
	selectedEndpointMutex sync.RWMutex

	// currentRPCType:
	//   - Tracks the RPC type of the current relay being processed.
	//   - Set during relay execution and used when building observations.
	currentRPCType sharedtypes.RPCType

	// requestErrorObservation:
	//   - Tracks any errors encountered during request processing.
	requestErrorObservation *protocolobservations.ShannonRequestError

	// endpointObservations:
	//   - Captures observations about endpoints used during request handling.
	//   - Includes enhanced error classification for raw payload analysis.
	//   - Thread-safe: collected via channel during parallel relay processing.
	endpointObservations []*protocolobservations.ShannonEndpointObservation

	// observationsChan is used to safely collect endpoint observations from concurrent goroutines.
	// A collector goroutine reads from this channel and appends to endpointObservations.
	observationsChan chan *protocolobservations.ShannonEndpointObservation

	// currentRelayMinerError:
	//   - Tracks RelayMinerError data from the current relay response for reporting.
	//   - Set by trackRelayMinerError method and used when building observations.
	currentRelayMinerError *protocolobservations.ShannonRelayMinerError

	// HTTP client used for sending relay requests to endpoints while also capturing various debug metrics
	httpClient *pathhttp.HTTPClientWithDebugMetrics

	// fallbackEndpoints is used to retrieve a fallback endpoint by an endpoint address.
	fallbackEndpoints map[protocol.EndpointAddr]endpoint

	// Optional.
	// Puts the Gateway in LoadTesting mode if specified.
	// All relays will be sent to a fixed URL.
	// Allows measuring performance of PATH and full node(s) in isolation.
	// Applies to Single Relay ONLY
	// No parallel requests for a single relay in load testing mode.
	loadTestingConfig *LoadTestingConfig

	// relayPool is the shared worker pool for parallel relay processing.
	// Passed from Protocol, used to create task groups for batch requests.
	relayPool pond.Pool

	// concurrencyConfig controls concurrency limits for request processing.
	// Used to enforce max batch payloads and other concurrency constraints.
	// This is the GLOBAL config - per-service overrides are retrieved via unifiedServicesConfig.
	concurrencyConfig gateway.ConcurrencyConfig

	// unifiedServicesConfig provides access to per-service configuration overrides.
	// Used to get per-service concurrency limits (max_batch_payloads, max_parallel_endpoints).
	unifiedServicesConfig *gateway.UnifiedServicesConfig

	// reputationService tracks endpoint reputation scores.
	// If non-nil, signals are recorded on success/error for gradual reputation tracking.
	// When nil, no reputation-based filtering is applied.
	reputationService reputation.ReputationService

	// tieredSelector provides access to tier-based selection and probation status.
	// Used to check if endpoints are in probation when recording success signals.
	// If non-nil and endpoint is in probation, RecoverySuccessSignal is used instead of SuccessSignal.
	tieredSelector *reputation.TieredSelector
}

// HandleServiceRequest:
//   - Satisfies gateway.ProtocolRequestContext interface.
//   - Uses supplied payloads to send relay requests to an endpoint.
//   - Handles both single requests and JSON-RPC batch requests concurrently when beneficial.
//   - Returns responses as an array to match interface, but gateway currently expects single response.
//   - Captures RelayMinerError data when available for reporting purposes.
func (rc *requestContext) HandleServiceRequest(payloads []protocol.Payload) ([]protocol.Response, error) {
	// Internal error: No endpoint selected.
	if rc.getSelectedEndpoint() == nil {
		response, err := rc.handleInternalError(fmt.Errorf("HandleServiceRequest: no endpoint has been selected on service %s", rc.serviceID))
		return []protocol.Response{response}, err
	}

	// Handle empty payloads.
	if len(payloads) == 0 {
		response, err := rc.handleInternalError(fmt.Errorf("HandleServiceRequest: no payloads provided for service %s", rc.serviceID))
		return []protocol.Response{response}, err
	}

	// TODO_TECHDEBT: Account for different payloads having different RPC types
	// OR refactor the single/parallel code flow altogether
	// Store the current RPC type for use in observations.
	// Only override if payload has an explicit RPC type (non-zero value).
	// This preserves the default set in BuildHTTPRequestContextForEndpoint for health checks.
	if payloads[0].RPCType != sharedtypes.RPCType_UNKNOWN_RPC {
		rc.currentRPCType = payloads[0].RPCType
	}

	// For single payload, handle directly without additional overhead.
	if len(payloads) == 1 {
		response, err := rc.sendSingleRelay(payloads[0])
		return []protocol.Response{response}, err
	}

	// Enforce configured max batch payload limit to prevent resource exhaustion
	// Use per-service override if available, otherwise fall back to global config
	maxBatchPayloads := rc.concurrencyConfig.MaxBatchPayloads
	if rc.unifiedServicesConfig != nil {
		if mergedConfig := rc.unifiedServicesConfig.GetMergedServiceConfig(rc.serviceID); mergedConfig != nil {
			if mergedConfig.ConcurrencyConfig != nil && mergedConfig.ConcurrencyConfig.MaxBatchPayloads != nil {
				maxBatchPayloads = *mergedConfig.ConcurrencyConfig.MaxBatchPayloads
			}
		}
	}
	if len(payloads) > maxBatchPayloads {
		response, err := rc.handleInternalError(fmt.Errorf("HandleServiceRequest: batch of payloads larger than allowed: %d, received: %d ", maxBatchPayloads, len(payloads)))
		return []protocol.Response{response}, err
	}

	// For multiple payloads, use parallel processing.
	return rc.handleParallelRelayRequests(payloads)
}

// sendSingleRelay handles a single relay request with full error handling and observation tracking.
// Extracted from original HandleServiceRequest logic for reuse in parallel processing.
func (rc *requestContext) sendSingleRelay(payload protocol.Payload) (protocol.Response, error) {
	// Record endpoint query time.
	endpointQueryTime := time.Now()

	// Execute relay request using the appropriate strategy based on endpoint type and network conditions
	relayResponse, err := rc.executeRelayRequestStrategy(payload)

	// Failure: Pass the response (which may contain RelayMinerError data) to error handler.
	if err != nil {
		return rc.handleEndpointError(endpointQueryTime, err)
	}

	// Success:
	// - Record observation
	// - Return response received from endpoint.
	err = rc.handleEndpointSuccess(endpointQueryTime, &relayResponse)
	return relayResponse, err
}

// TODO_TECHDEBT(@adshmh): Set and enforce a cap on the number of concurrent parallel requests for a single method call.
//
// TODO_TECHDEBT(@adshmh): Single and Multiple payloads should be handled as similarly as possible:
// - This includes using similar execution paths.
//
// TODO_TECHDEBT(@adshmh): Use the same endpoint response processing used in single relay requests:
// - Use the following on every parallel request:
//   - handleEndpointSuccess
//   - handleEndpointError
//
// handleParallelRelayRequests orchestrates parallel relay requests to a single endpoint.
// Uses pond worker pool for bounded concurrency and channels for thread-safe observation collection.
// This prevents DoS attacks via large batch requests that could spawn unbounded goroutines.
func (rc *requestContext) handleParallelRelayRequests(payloads []protocol.Payload) ([]protocol.Response, error) {
	maxBatchPayloads := rc.concurrencyConfig.MaxBatchPayloads

	logger := rc.logger.With(
		"method", "handleParallelRelayRequests",
		"num_payloads", len(payloads),
		"service_id", rc.serviceID,
		"max_concurrent", maxBatchPayloads,
	)

	logger.Debug().Msg("Starting parallel relay processing with worker pool")

	// Initialize observations channel for thread-safe collection from workers
	rc.observationsChan = make(chan *protocolobservations.ShannonEndpointObservation, len(payloads))

	// Start collector goroutine to gather observations from workers
	// Cap at max_batch_payloads since batch size is already limited to that
	observationsCollected := make(chan struct{})
	go func() {
		defer close(observationsCollected)
		for obs := range rc.observationsChan {
			if len(rc.endpointObservations) < maxBatchPayloads {
				rc.endpointObservations = append(rc.endpointObservations, obs)
			}
			// Silently drop excess observations (shouldn't happen with batch size limit)
		}
	}()

	// Create a task group from the shared pool for this batch of requests.
	// The shared pool bounds global concurrency across all requests.
	group := rc.relayPool.NewGroup()

	// Results slice - each goroutine writes to its own index, no mutex needed
	results := make([]parallelRelayResult, len(payloads))

	// Submit all relay tasks to the group
	for i, payload := range payloads {
		i, payload := i, payload // capture loop variables
		group.Submit(func() {
			startTime := time.Now()
			response, err := rc.sendSingleRelay(payload)
			duration := time.Since(startTime)

			// Each goroutine writes to its own index - no race
			results[i] = parallelRelayResult{
				index:     i,
				response:  response,
				err:       err,
				duration:  duration,
				startTime: startTime,
			}

			if err != nil {
				logger.Warn().Err(err).
					Msgf("Parallel relay request %d failed after %dms", i, duration.Milliseconds())
			}
		})
	}

	// Wait for all tasks in this group to complete
	err := group.Wait()
	if err != nil {
		return nil, err
	}

	// Close observations channel and wait for collector to finish
	close(rc.observationsChan)
	<-observationsCollected
	rc.observationsChan = nil // Reset to nil so single relay mode works normally

	return rc.convertResultsToResponses(results, rc.findFirstError(results))
}

// parallelRelayResult holds the result of a single relay request for parallel processing.
type parallelRelayResult struct {
	index     int
	response  protocol.Response
	err       error
	duration  time.Duration
	startTime time.Time
}

// findFirstError returns the first error found in the results slice.
func (rc *requestContext) findFirstError(results []parallelRelayResult) error {
	for _, result := range results {
		if result.err != nil {
			return result.err
		}
	}
	return nil
}

// TODO_TECHDEBT(@adshmh): Handle EVERY error encountered in parallel requests.
// TODO_TECHDEBT(@adshmh): Support multiple endpoints for parallel requests.
//
// convertResultsToResponses converts parallel relay results into an array of protocol responses.
// Maintains the order of responses to match the order of input payloads.
func (rc *requestContext) convertResultsToResponses(results []parallelRelayResult, firstErr error) ([]protocol.Response, error) {
	if len(results) == 0 {
		response, err := rc.handleInternalError(fmt.Errorf("convertResultsToResponses: no results to convert"))
		return []protocol.Response{response}, err
	}

	// Create response array in the same order as input payloads.
	responses := make([]protocol.Response, len(results))

	// Process results in order.
	for i, result := range results {
		responses[i] = result.response
	}

	rc.logger.Debug().
		Int("num_responses", len(responses)).
		Bool("has_errors", firstErr != nil).
		Msg("Response conversion completed")

	return responses, firstErr
}

// GetObservations:
// - Returns Shannon protocol-level observations for the current request context.
// - Enhanced observations include detailed error classification for metrics generation.
// - Used to:
//   - Update Shannon's endpoint store
//   - Report PATH metrics (metrics package)
//   - Report requests to the data pipeline
//
// - Implements gateway.ProtocolRequestContext interface.
func (rc *requestContext) GetObservations() protocolobservations.Observations {
	return protocolobservations.Observations{
		Shannon: &protocolobservations.ShannonObservationsList{
			Observations: []*protocolobservations.ShannonRequestObservations{
				{
					ServiceId:    string(rc.serviceID),
					RequestError: rc.requestErrorObservation,
					ObservationData: &protocolobservations.ShannonRequestObservations_HttpObservations{
						HttpObservations: &protocolobservations.ShannonHTTPEndpointObservations{
							EndpointObservations: rc.endpointObservations,
						},
					},
				},
			},
		},
	}
}

// getSelectedEndpoint returns the currently selected endpoint in a thread-safe manner.
func (rc *requestContext) getSelectedEndpoint() endpoint {
	rc.selectedEndpointMutex.RLock()
	defer rc.selectedEndpointMutex.RUnlock()
	return rc.selectedEndpoint
}

// executeRelayRequestStrategy determines and executes the appropriate relay strategy.
// In particular, it includes logic that accounts for:
//  1. Endpoint type (fallback vs protocol endpoint)
//  2. Network conditions (session rollover periods)
func (rc *requestContext) executeRelayRequestStrategy(payload protocol.Payload) (protocol.Response, error) {
	selectedEndpoint := rc.getSelectedEndpoint()
	rc.hydrateLogger("executeRelayRequestStrategy")

	switch {

	// ** Priority 0: Load testing mode **
	// Use the configured load testing backend server.
	case rc.loadTestingConfig != nil:
		rc.logger.Debug().Msg("LoadTesting Mode: Sending relay to the load test RelayMiner/backend server")
		return rc.sendProtocolRelay(payload)

	// ** Priority 1: Check Endpoint type **
	// Direct fallback endpoint
	// - Bypasses protocol validation and Shannon network
	// - Used when endpoint is explicitly configured as a fallback endpoint
	case selectedEndpoint.IsFallback():
		rc.logger.Debug().Msg("Executing fallback relay")
		return rc.sendFallbackRelay(selectedEndpoint, payload)

	// ** Default **
	// Standard protocol relay
	// - Standard protocol relay through Shannon network
	// - During session rollover: Uses merged endpoints from current + extended sessions
	// - During normal operation: Uses endpoints from current session only
	// - Fallback endpoints only used when no session endpoints are available
	default:
		rc.logger.Debug().Msg("Executing standard protocol relay")
		return rc.sendProtocolRelay(payload)
	}
}

// buildHeaders creates the headers map including the RPCType header
func buildHeaders(payload protocol.Payload) map[string]string {
	headers := make(map[string]string)

	// Copy existing headers from payload
	maps.Copy(headers, payload.Headers)

	// Set the RPCType HTTP header, if set on the payload.
	// Used by endpoint/relay miner to determine correct backend service.
	if payload.RPCType != sharedtypes.RPCType_UNKNOWN_RPC {
		headers[proxy.RPCTypeHeader] = strconv.Itoa(int(payload.RPCType))
	}

	return headers
}

// TODO_TECHDEBT(@adshmh): Refactor to split the selection of and interactions with the fallback endpoint.
// Aspects to consider in the refactor:
// - Individual request's settings, e.g. those determined by QoS.
// - Protocol's responsibilities: potential for a separate component/package.
// - Observations: consider separating Shannon endpoint observations from fallback endpoints.
//
// sendProtocolRelay:
//   - Sends the supplied payload as a relay request to the endpoint selected via SelectEndpoint.
//   - Enhanced error handling for more fine-grained endpoint error type classification.
//   - Captures RelayMinerError data for reporting (but doesn't use it for classification).
//   - Required to fulfill the FullNode interface.
func (rc *requestContext) sendProtocolRelay(payload protocol.Payload) (protocol.Response, error) {
	rc.hydrateLogger("sendProtocolRelay")
	rc.logger = hydrateLoggerWithPayload(rc.logger, &payload)

	selectedEndpoint := rc.getSelectedEndpoint()
	defaultResponse := protocol.Response{
		EndpointAddr: selectedEndpoint.Addr(),
	}

	// If this is a fallback endpoint, use sendFallbackRelay instead
	// This can happen during session rollover when sendRelayWithFallback spawns
	// a goroutine that calls sendProtocolRelay with a fallback endpoint selected
	if selectedEndpoint.IsFallback() {
		rc.logger.Error().Msg("SHOULD NEVER HAPPEN: Select endpoint should not be a fallback endpoint in this code path.")
		return rc.sendFallbackRelay(selectedEndpoint, payload)
	}

	// Validate endpoint and session
	app, err := rc.validateEndpointAndSession()
	if err != nil {
		return defaultResponse, err
	}

	// Build and sign the relay request
	signedRelayReq, err := rc.buildAndSignRelayRequest(payload, app)
	if err != nil {
		return defaultResponse, err
	}

	// Marshal relay request to bytes
	relayRequestBz, err := signedRelayReq.Marshal()
	if err != nil {
		return defaultResponse, fmt.Errorf("SHOULD NEVER HAPPEN: failed to marshal relay request: %w", err)
	}

	// TODO_UPNEXT(@adshmh): parse the LoadTesting server's URL in-advance.
	var targetServerURL string
	switch {
	// LoadTesting mode: use the fixed URL.
	case rc.loadTestingConfig != nil:
		if rc.loadTestingConfig.BackendServiceURL != nil {
			targetServerURL = *rc.loadTestingConfig.BackendServiceURL
		} else {
			targetServerURL = rc.loadTestingConfig.RelayMinerConfig.URL
		}
	// Default: use the RPC-type-specific URL from the selected endpoint
	default:
		targetServerURL = selectedEndpoint.GetURL(rc.currentRPCType)

		// FALLBACK HANDLING: If the URL is empty, it means the endpoint doesn't support
		// the originally requested RPC type. This happens when RPC type fallback occurred
		// during endpoint selection (e.g., COMET_BFT → JSON_RPC).
		// Try to find which RPC type the endpoint actually supports.
		if targetServerURL == "" {
			// Try common RPC types in priority order
			fallbackTypes := []sharedtypes.RPCType{
				sharedtypes.RPCType_JSON_RPC,
				sharedtypes.RPCType_REST,
				sharedtypes.RPCType_COMET_BFT,
				sharedtypes.RPCType_GRPC,
			}

			for _, rpcType := range fallbackTypes {
				if url := selectedEndpoint.GetURL(rpcType); url != "" {
					targetServerURL = url
					rc.logger.Info().
						Str("requested_rpc_type", rc.currentRPCType.String()).
						Str("actual_rpc_type", rpcType.String()).
						Str("endpoint", string(selectedEndpoint.Addr())).
						Msg("Endpoint doesn't support requested RPC type, using supported type from endpoint")
					rc.currentRPCType = rpcType // Update to the actual RPC type
					break
				}
			}
		}
	}

	// TODO_TECHDEBT(@adshmh): Add a new struct to track details about the HTTP call.
	// It should contain at-least:
	// - endpoint payload
	// - HTTP status code
	// Use the new struct to pass data around for logging/metrics/etc.
	//
	// Send the HTTP request to the protocol endpoint.
	httpRelayResponseBz, httpStatusCode, err := rc.sendHTTPRequest(payload, targetServerURL, relayRequestBz)
	if err != nil {
		rc.logger.With(
			"http_relay_response_preview", polylog.Preview(string(httpRelayResponseBz)),
			"http_status_code", httpStatusCode,
		).Error().Err(err).Msg("HTTP relay failed.")
		return defaultResponse, err
	}

	// TODO_TECHDEBT(@adshmh): Refactor to clarify the request flow via matching of processing logic:
	// PATH -> RelayMiner -> Backend Service
	// There are 2 HTTP status codes:
	// 1. From the RelayMiner when sending a relay
	// 2. From the backend service: contained in the RelayResponse struct parsed from payload returned by RelayMiner.
	//
	// Non-2xx HTTP status code received from the endpoint: build and return an error
	if httpStatusCode != http.StatusOK {
		return defaultResponse, fmt.Errorf("%w %w: %d", errSendHTTPRelay, errEndpointNon2XXHTTPStatusCode, httpStatusCode)
	}

	// LoadTesting mode using a backend server.
	// Return the backend server's response as-is.
	if rc.loadTestingConfig != nil && rc.loadTestingConfig.BackendServiceURL != nil {
		return protocol.Response{
			Bytes:          httpRelayResponseBz,
			HTTPStatusCode: httpStatusCode,
			// Intentionally leaving the endpoint address empty.
			// Ensuring no reputation penalties/filtering apply to LoadTesting backend server
			EndpointAddr: "",
		}, nil
	}

	// Validate and process the response
	response, err := rc.validateAndProcessResponse(httpRelayResponseBz)
	if err != nil {
		return defaultResponse, err
	}

	// Deserialize the response
	deserializedResponse, err := rc.deserializeRelayResponse(response)
	if err != nil {
		return defaultResponse, err
	}
	// Hydrate the response with the endpoint address
	deserializedResponse.EndpointAddr = selectedEndpoint.Addr()

	// Log non-2xx HTTP status codes for visibility, but passthrough the response
	// to preserve the backend's original HTTP status code for the client.
	responseHTTPStatusCode := deserializedResponse.HTTPStatusCode
	if err := pathhttp.EnsureHTTPSuccess(responseHTTPStatusCode); err != nil {
		rc.logger.Debug().
			Int("http_status_code", responseHTTPStatusCode).
			Msg("Backend returned non-2xx HTTP status - passing through to client")
	}

	return deserializedResponse, nil
}

// validateEndpointAndSession validates that the endpoint and session are properly configured
func (rc *requestContext) validateEndpointAndSession() (apptypes.Application, error) {
	selectedEndpoint := rc.getSelectedEndpoint()
	if selectedEndpoint == nil {
		rc.logger.Warn().Msg("SHOULD NEVER HAPPEN: No endpoint has been selected. Relay request will fail.")
		return apptypes.Application{}, fmt.Errorf("sendRelay: no endpoint has been selected on service %s", rc.serviceID)
	}

	session := selectedEndpoint.Session()
	if session.Application == nil {
		rc.logger.Warn().Msg("SHOULD NEVER HAPPEN: selected endpoint session has nil Application. Relay request will fail.")
		return apptypes.Application{}, fmt.Errorf("sendRelay: nil app on session %s for service %s", session.SessionId, rc.serviceID)
	}

	return *session.Application, nil
}

// buildAndSignRelayRequest builds and signs the relay request
func (rc *requestContext) buildAndSignRelayRequest(
	payload protocol.Payload,
	app apptypes.Application,
) (*servicetypes.RelayRequest, error) {
	selectedEndpoint := rc.getSelectedEndpoint()
	// Prepare the relay request
	relayRequest, err := buildUnsignedRelayRequest(selectedEndpoint, payload)
	if err != nil {
		rc.logger.Warn().Err(err).Msg("SHOULD NEVER HAPPEN: Failed to build the unsigned relay request. Relay request will fail.")
		return nil, err
	}

	// Sign the relay request
	signedRelayReq, err := rc.signRelayRequest(relayRequest, app)
	if err != nil {
		rc.logger.Warn().Err(err).Msg("SHOULD NEVER HAPPEN: Failed to sign the relay request. Relay request will fail.")
		return nil, fmt.Errorf("sendRelay: error signing the relay request for app %s: %w", app.Address, err)
	}

	return signedRelayReq, nil
}

// validateAndProcessResponse validates the relay response and tracks relay miner errors
func (rc *requestContext) validateAndProcessResponse(
	httpRelayResponseBz []byte,
) (*servicetypes.RelayResponse, error) {
	// Validate the response - check for specific validation errors that indicate raw payload issues
	selectedEndpoint := rc.getSelectedEndpoint()
	supplierAddr := sdk.SupplierAddress(selectedEndpoint.Supplier())
	response, err := rc.fullNode.ValidateRelayResponse(supplierAddr, httpRelayResponseBz)

	// Track RelayMinerError data for tracking, regardless of validation result
	// Cross referenced against endpoint payload parse results via metrics
	rc.trackRelayMinerError(response)

	if err != nil {
		// Log raw payload for error tracking
		responseStr := string(httpRelayResponseBz)
		rc.logger.With(
			"endpoint_payload", responseStr[:min(len(responseStr), maxEndpointPayloadLenForLogging)],
			"endpoint_payload_length", len(httpRelayResponseBz),
			"validation_error", err.Error(),
		).Warn().Err(err).Msg("Failed to validate the payload from the selected endpoint. Relay request will fail.")

		// Check if this is a validation error that requires raw payload analysis
		if errors.Is(err, sdk.ErrRelayResponseValidationUnmarshal) || errors.Is(err, sdk.ErrRelayResponseValidationBasicValidation) {
			return nil, fmt.Errorf("raw_payload: %s: %w", responseStr, errMalformedEndpointPayload)
		}

		// TODO_TECHDEBT(@adshmh): Refactor to separate Shannon and Fallback endpoints.
		// The logic below is an example of techdebt resulting from conflating the two.
		//
		var appAddr string
		if session := selectedEndpoint.Session(); session != nil && session.Application != nil {
			appAddr = session.Application.Address
		}

		return nil, fmt.Errorf("relay: error verifying the relay response for app %s, endpoint %s: %w",
			appAddr, selectedEndpoint.PublicURL(), err)
	}

	return response, nil
}

// deserializeRelayResponse deserializes the relay response payload into a protocol.Response
func (rc *requestContext) deserializeRelayResponse(response *servicetypes.RelayResponse) (protocol.Response, error) {
	// The Payload field of the response from the endpoint (relay miner):
	//   - Is a serialized http.Response struct.
	//   - Needs to be deserialized to access the service's response body, status code, etc.
	deserializedResponse, err := deserializeRelayResponse(response.Payload)
	if err != nil {
		// Wrap error with detailed message
		return protocol.Response{}, fmt.Errorf("error deserializing endpoint into a POKTHTTP response: %w", err)
	}

	return deserializedResponse, nil
}

func (rc *requestContext) signRelayRequest(unsignedRelayReq *servicetypes.RelayRequest, app apptypes.Application) (*servicetypes.RelayRequest, error) {
	// Verify the relay request's metadata, specifically the session header.
	// Note: cannot use the RelayRequest's ValidateBasic() method here, as it looks for a signature in the struct, which has not been added yet at this point.
	meta := unsignedRelayReq.GetMeta()

	if meta.GetSessionHeader() == nil {
		return nil, errors.New("signRelayRequest: relay request is missing session header")
	}

	sessionHeader := meta.GetSessionHeader()
	if err := sessionHeader.ValidateBasic(); err != nil {
		return nil, fmt.Errorf("signRelayRequest: relay request session header is invalid: %w", err)
	}

	// Sign the relay request using the selected app's private key
	return rc.relayRequestSigner.SignRelayRequest(unsignedRelayReq, app)
}

// buildUnsignedRelayRequest:
//   - Builds a ready-to-sign RelayRequest using the supplied endpoint, session, and payload.
//   - Returned RelayRequest is meant to be signed and sent to the endpoint to receive its response.
func buildUnsignedRelayRequest(
	endpoint endpoint,
	payload protocol.Payload,
) (*servicetypes.RelayRequest, error) {
	// If path is not empty (e.g. for REST service request), append to endpoint URL.
	url := prepareURLFromPayload(endpoint.PublicURL(), payload)

	// TODO_TECHDEBT: Select the correct underlying request (HTTP, etc.) based on selected service.
	jsonRpcHttpReq, err := shannonJsonRpcHttpRequest(payload, url)
	if err != nil {
		return nil, fmt.Errorf("error building a JSONRPC HTTP request for url %s: %w", url, err)
	}

	relayRequest, err := embedHttpRequest(jsonRpcHttpReq)
	if err != nil {
		return nil, fmt.Errorf("error embedding a JSONRPC HTTP request for url %s: %w", endpoint.PublicURL(), err)
	}

	// TODO_MVP(@adshmh): Use new `FilteredSession` struct from Shannon SDK to get session and endpoint.
	relayRequest.Meta = servicetypes.RelayRequestMetadata{
		SessionHeader:           endpoint.Session().Header,
		SupplierOperatorAddress: endpoint.Supplier(),
	}

	return relayRequest, nil
}

// sendFallbackRelay:
//   - Sends the supplied payload as a relay request to the fallback endpoint.
//   - This bypasses protocol-level request processing and validation.
//   - This DOES NOT get sent to a RelayMiner.
//   - Returns the response received from the fallback endpoint.
//   - Used in cases, such as, when all endpoints are filtered out (low reputation) for a service ID.
func (rc *requestContext) sendFallbackRelay(
	fallbackEndpoint endpoint,
	payload protocol.Payload,
) (protocol.Response, error) {
	// Get the fallback URL for the fallback endpoint.
	// If the RPC type is unknown or not configured, it will default URL.
	endpointFallbackURL := fallbackEndpoint.GetURL(payload.RPCType)

	// Prepare the fallback URL with optional path
	fallbackURL := prepareURLFromPayload(endpointFallbackURL, payload)

	// Send the HTTP request to the fallback endpoint.
	httpResponseBz, httpStatusCode, err := rc.sendHTTPRequest(
		payload,
		fallbackURL,
		[]byte(payload.Data),
	)

	if err != nil {
		return protocol.Response{
			EndpointAddr: fallbackEndpoint.Addr(),
		}, err
	}

	// TODO_CONSIDERATION(@adshmh): Are there any scenarios where a fallback endpoint should return a non-2xx HTTP status code?
	// Examples: a fallback endpoint for a RESTful service.
	//
	// Non-2xx HTTP status code: build and return an error.
	if httpStatusCode != http.StatusOK {
		return protocol.Response{
			EndpointAddr: fallbackEndpoint.Addr(),
		}, fmt.Errorf("%w %w: %d", errSendHTTPRelay, errEndpointNon2XXHTTPStatusCode, httpStatusCode)
	}

	// Build and return the fallback response
	return protocol.Response{
		Bytes:          httpResponseBz,
		HTTPStatusCode: httpStatusCode,
		EndpointAddr:   fallbackEndpoint.Addr(),
	}, nil
}

// trackRelayMinerError:
//   - Tracks RelayMinerError data from the RelayResponse for reporting purposes.
//   - Updates the requestContext with RelayMinerError data.
//   - Will be included in observations.
//   - Logs RelayMinerError details for visibility.
func (rc *requestContext) trackRelayMinerError(relayResponse *servicetypes.RelayResponse) {
	// Check if RelayResponse contains RelayMinerError data
	if relayResponse == nil || relayResponse.RelayMinerError == nil {
		// No RelayMinerError data to track
		return
	}

	relayMinerErr := relayResponse.RelayMinerError
	rc.hydrateLogger("trackRelayMinerError")

	// Log RelayMinerError details for visibility
	rc.logger.With(
		"relay_miner_error_codespace", relayMinerErr.Codespace,
		"relay_miner_error_code", relayMinerErr.Code,
		"relay_miner_error_message", relayMinerErr.Message,
	).Info().Msg("RelayMiner returned an error in RelayResponse (captured for reporting)")

	// Store RelayMinerError data in request context for use in observations
	rc.currentRelayMinerError = &protocolobservations.ShannonRelayMinerError{
		Codespace: relayMinerErr.Codespace,
		Code:      relayMinerErr.Code,
		Message:   relayMinerErr.Message,
	}
}

// handleInternalError:
//   - Called if request processing fails (before sending to any endpoints).
//   - DEV_NOTE: Should NEVER happen; investigate any logged entries from this method.
//   - Records internal error on request for observations.
//   - Logs error entry.
func (rc *requestContext) handleInternalError(internalErr error) (protocol.Response, error) {
	rc.hydrateLogger("handleInternalError")

	// Log the internal error.
	rc.logger.Error().Err(internalErr).Msg("Internal error occurred. This should be investigated as a bug.")

	// Set request processing error for generating observations.
	rc.requestErrorObservation = buildInternalRequestProcessingErrorObservation(internalErr)

	return protocol.Response{}, internalErr
}

// TODO_TECHDEBT(@adshmh): Support tracking errors for Shannon and fallback endpoints.
// This would allow visibility into potential degradation of fallback endpoints.
//
// handleEndpointError:
//   - Records endpoint error observation with enhanced classification and returns the response.
//   - Tracks endpoint error in observations with detailed categorization for metrics.
//   - Includes any RelayMinerError data that was captured via trackRelayMinerError.
func (rc *requestContext) handleEndpointError(
	endpointQueryTime time.Time,
	endpointErr error,
) (protocol.Response, error) {
	rc.hydrateLogger("handleEndpointError")
	selectedEndpoint := rc.getSelectedEndpoint()
	selectedEndpointAddr := selectedEndpoint.Addr()

	// Classify error and get reputation signal directly
	// See ERROR_CLASSIFICATION.md for detailed error category documentation
	latency := time.Since(endpointQueryTime)
	endpointErrorType, signal := classifyErrorAsSignal(rc.logger, endpointErr, latency)

	// Enhanced logging with error type and reputation signal
	isMalformedPayloadErr := isMalformedEndpointPayloadError(endpointErrorType)
	rc.logger.Error().
		Err(endpointErr).
		Str("error_type", endpointErrorType.String()).
		Str("signal_type", string(signal.Type)).
		Float64("signal_impact", signal.GetDefaultImpact()).
		Bool("is_malformed_payload_error", isMalformedPayloadErr).
		Msg("relay error occurred. Service request will fail.")

	// Build enhanced observation with RelayMinerError data from request context
	endpointObs := buildEndpointErrorObservation(
		rc.logger,
		selectedEndpoint,
		endpointQueryTime,
		time.Now(), // Timestamp: endpoint query completed.
		endpointErrorType,
		fmt.Sprintf("relay error: %v", endpointErr),
		rc.currentRelayMinerError, // Use RelayMinerError data from request context
		rc.currentRPCType,         // Use RPC type from request context
	)

	// Track endpoint error observation for metrics
	// Use channel if available (parallel processing), otherwise append directly (single relay)
	if rc.observationsChan != nil {
		rc.observationsChan <- endpointObs
	} else {
		rc.endpointObservations = append(rc.endpointObservations, endpointObs)
	}

	// Record reputation signal if reputation service is enabled.
	// This provides gradual scoring based on error severity.
	if rc.reputationService != nil {
		keyBuilder := rc.reputationService.KeyBuilderForService(rc.serviceID)
		endpointKey := keyBuilder.BuildKey(rc.serviceID, selectedEndpointAddr, rc.currentRPCType)

		// Fire-and-forget: don't block request on reputation recording
		if err := rc.reputationService.RecordSignal(rc.context, endpointKey, signal); err != nil {
			rc.logger.Warn().Err(err).Msg("Failed to record reputation signal for error")
		}
	}

	// Record relay metric for failed request
	domain, domainErr := shannonmetrics.ExtractDomainOrHost(string(selectedEndpointAddr))
	if domainErr != nil {
		domain = shannonmetrics.ErrDomain
	}
	rpcTypeStr := metrics.NormalizeRPCType(rc.currentRPCType.String())
	reputationSignal := mapSignalTypeToMetricSignal(signal.Type)

	// Extract status code from error if possible, otherwise use "error"
	statusCodeStr := "error"
	if statusCode, ok := extractHTTPStatusCode(endpointErr); ok {
		statusCodeStr = metrics.GetStatusCodeCategory(statusCode)
	}

	// Determine relay type - errors are recorded as normal type (probation detection requires success)
	relayType := metrics.RelayTypeNormal
	metrics.RecordRelay(domain, rpcTypeStr, string(rc.serviceID), statusCodeStr, reputationSignal, relayType, latency.Seconds())

	// Return error.
	return protocol.Response{EndpointAddr: selectedEndpointAddr},
		fmt.Errorf("relay: error sending relay for service %s endpoint %s: %w",
			rc.serviceID, selectedEndpointAddr, endpointErr,
		)
}

// handleEndpointSuccess:
//   - Records successful endpoint observation and returns the response.
//   - Tracks endpoint success in observations with timing data for performance metrics.
//   - Includes any RelayMinerError data that was captured via trackRelayMinerError.
//   - Builds and returns protocol response from endpoint's returned data.
func (rc *requestContext) handleEndpointSuccess(
	endpointQueryTime time.Time,
	endpointResponse *protocol.Response,
) error {
	rc.hydrateLogger("handleEndpointSuccess")
	rc.logger = rc.logger.With("endpoint_response_payload_len", len(endpointResponse.Bytes))
	rc.logger.Debug().Msg("Successfully deserialized the response received from the selected endpoint.")

	selectedEndpoint := rc.getSelectedEndpoint()
	selectedEndpointAddr := selectedEndpoint.Addr()

	// Build success observation with timing data and any RelayMinerError data from request context
	endpointObs := buildEndpointSuccessObservation(
		rc.logger,
		selectedEndpoint,
		endpointQueryTime,
		time.Now(), // Timestamp: endpoint query completed.
		endpointResponse,
		rc.currentRelayMinerError, // Use RelayMinerError data from request context
		rc.currentRPCType,         // Use RPC type from request context
	)

	// Track endpoint success observation for metrics
	// Use channel if available (parallel processing), otherwise append directly (single relay)
	if rc.observationsChan != nil {
		rc.observationsChan <- endpointObs
	} else {
		rc.endpointObservations = append(rc.endpointObservations, endpointObs)
	}

	// Record reputation signal if reputation service is enabled.
	latency := time.Since(endpointQueryTime)
	var reputationSignal string
	var relayType string

	if rc.reputationService != nil {
		keyBuilder := rc.reputationService.KeyBuilderForService(rc.serviceID)
		endpointKey := keyBuilder.BuildKey(rc.serviceID, selectedEndpointAddr, rc.currentRPCType)

		// Check if endpoint is in probation - use RecoverySuccessSignal (+15) for probation,
		// SuccessSignal (+1) for normal requests. This helps low-scoring endpoints recover faster.
		var signal reputation.Signal
		if rc.tieredSelector != nil && rc.tieredSelector.Config().Probation.Enabled && rc.tieredSelector.IsInProbation(endpointKey) {
			// Probation endpoint succeeded - use recovery signal for stronger positive reinforcement
			signal = reputation.NewRecoverySuccessSignal(latency)
			reputationSignal = metrics.SignalOK
			relayType = metrics.RelayTypeProbation

			// Record that a request was routed to a probation endpoint
			probationDomain, _ := shannonmetrics.ExtractDomainOrHost(string(selectedEndpointAddr))
			if probationDomain == "" {
				probationDomain = shannonmetrics.ErrDomain
			}
			metrics.RecordProbationEvent(probationDomain, metrics.NormalizeRPCType(rc.currentRPCType.String()), string(rc.serviceID), metrics.ProbationEventRouted)

			rc.logger.Debug().
				Str("endpoint", string(selectedEndpointAddr)).
				Str("service_id", string(rc.serviceID)).
				Dur("latency", latency).
				Msg("Recording recovery success signal for probation endpoint")
		} else {
			// Normal request - use standard success signal
			signal = reputation.NewSuccessSignal(latency)
			reputationSignal = metrics.SignalOK
			relayType = metrics.RelayTypeNormal
		}

		// Fire-and-forget: don't block request on reputation recording
		if err := rc.reputationService.RecordSignal(rc.context, endpointKey, signal); err != nil {
			rc.logger.Warn().Err(err).Msg("Failed to record reputation signal for success")
		}

		// Record additional latency penalty signals if applicable
		// This is done by checking the per-service latency config and determining
		// if the response was slow enough to warrant an additional penalty signal
		rc.recordLatencyPenaltySignalsIfNeeded(endpointKey, latency)
	} else {
		reputationSignal = metrics.SignalOK
		relayType = metrics.RelayTypeNormal
	}

	// Record relay metric for successful request
	domain, domainErr := shannonmetrics.ExtractDomainOrHost(string(selectedEndpointAddr))
	if domainErr != nil {
		domain = shannonmetrics.ErrDomain
	}
	statusCodeStr := metrics.GetStatusCodeCategory(endpointResponse.HTTPStatusCode)
	rpcTypeStr := metrics.NormalizeRPCType(rc.currentRPCType.String())
	metrics.RecordRelay(domain, rpcTypeStr, string(rc.serviceID), statusCodeStr, reputationSignal, relayType, latency.Seconds())

	// Return relay response received from endpoint.
	return nil
}

// recordLatencyPenaltySignalsIfNeeded checks if the response latency exceeds penalty thresholds
// and records additional penalty signals (slow_response, very_slow_response) if needed.
// This allows endpoints to be penalized for slow responses even when the request succeeds.
func (rc *requestContext) recordLatencyPenaltySignalsIfNeeded(
	endpointKey reputation.EndpointKey,
	latency time.Duration,
) {
	// Get the latency config for this service (respects per-service overrides)
	latencyConfig := rc.getLatencyConfigForService()

	// Check if an additional latency penalty signal is needed
	penaltySignalType := reputation.ClassifyLatency(latency, latencyConfig)
	if penaltySignalType == nil {
		return // No penalty needed
	}

	// Create and record the appropriate penalty signal
	var penaltySignal reputation.Signal
	switch *penaltySignalType {
	case reputation.SignalTypeSlowResponse:
		penaltySignal = reputation.NewSlowResponseSignal(latency)
	case reputation.SignalTypeVerySlowResponse:
		penaltySignal = reputation.NewVerySlowResponseSignal(latency)
	default:
		return // Unknown signal type, skip
	}

	// Fire-and-forget: don't block request on reputation recording
	if err := rc.reputationService.RecordSignal(rc.context, endpointKey, penaltySignal); err != nil {
		rc.logger.Warn().Err(err).
			Str("signal_type", string(*penaltySignalType)).
			Dur("latency", latency).
			Msg("Failed to record latency penalty signal")
	}
}

// getLatencyConfigForService returns the latency config for the current service.
// This fetches the config from the reputation service, which handles per-service overrides.
func (rc *requestContext) getLatencyConfigForService() reputation.LatencyConfig {
	return rc.reputationService.GetLatencyConfigForService(rc.serviceID)
}

// mapSignalTypeToMetricSignal converts a reputation.SignalType to a metrics signal string
func mapSignalTypeToMetricSignal(signalType reputation.SignalType) string {
	switch signalType {
	case reputation.SignalTypeSuccess, reputation.SignalTypeRecoverySuccess:
		return metrics.SignalOK
	case reputation.SignalTypeSlowResponse:
		return metrics.SignalSlow
	case reputation.SignalTypeVerySlowResponse:
		return metrics.SignalSlowASF
	case reputation.SignalTypeMinorError:
		return metrics.SignalMinorError
	case reputation.SignalTypeMajorError:
		return metrics.SignalMajorError
	case reputation.SignalTypeCriticalError:
		return metrics.SignalCriticalError
	case reputation.SignalTypeFatalError:
		return metrics.SignalFatalError
	default:
		return metrics.SignalOK
	}
}

// sendHTTPRequest is a shared method for sending HTTP requests with common logic
func (rc *requestContext) sendHTTPRequest(
	payload protocol.Payload,
	url string,
	requestData []byte,
) ([]byte, int, error) {
	// Prepare a timeout context for the request.
	// Use rc.context as parent to respect request-level cancellation signals.
	timeout := time.Duration(gateway.RelayRequestTimeout) * time.Millisecond
	ctxWithTimeout, cancelFn := context.WithTimeout(rc.context, timeout)
	defer cancelFn()

	// Build headers including RPCType header
	headers := buildHeaders(payload)

	// Send the HTTP request
	httpResponseBz, httpStatusCode, err := rc.httpClient.SendHTTPRelay(
		ctxWithTimeout,
		rc.logger,
		url,
		payload.Method,
		requestData,
		headers,
	)

	if err != nil {
		// Endpoint failed to respond before the timeout expires
		// Wrap the net/http error with our classification error
		// Include the net/http error for HTTP relay error classification.
		wrappedErr := fmt.Errorf("%w: %w", errSendHTTPRelay, err)

		selectedEndpoint := rc.getSelectedEndpoint()
		rc.logger.With(
			"http_response_preview", polylog.Preview(string(httpResponseBz)),
			"http_status_code", httpStatusCode,
		).Debug().Err(wrappedErr).Msgf("Failed to receive a response from the selected endpoint: '%s'. Relay request will FAIL", selectedEndpoint.Addr())
		return httpResponseBz, httpStatusCode, fmt.Errorf("error sending request to endpoint %s: %w", selectedEndpoint.Addr(), wrappedErr)
	}

	return httpResponseBz, httpStatusCode, nil
}

// prepareURLFromPayload constructs the URL for requests, including optional path.
// Adding the path ensures that REST requests' path is forwarded to the endpoint.
func prepareURLFromPayload(endpointURL string, payload protocol.Payload) string {
	url := endpointURL
	if payload.Path != "" {
		url = fmt.Sprintf("%s%s", url, payload.Path)
	}
	return url
}

// hydrateLogger:
// - Enhances the base logger with information from the request context.
// - Includes:
//   - Method name
//   - Service ID
//   - Selected endpoint supplier
//   - Selected endpoint URL
func (rc *requestContext) hydrateLogger(methodName string) {
	logger := rc.logger.With(
		"request_type", "http",
		"method", methodName,
		"service_id", rc.serviceID,
	)

	defer func() {
		rc.logger = logger
	}()

	// No endpoint specified on request context.
	// - This should never happen.
	selectedEndpoint := rc.getSelectedEndpoint()
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
