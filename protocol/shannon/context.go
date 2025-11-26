package shannon

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/pokt-network/poktroll/pkg/relayer/proxy"
	apptypes "github.com/pokt-network/poktroll/x/application/types"
	servicetypes "github.com/pokt-network/poktroll/x/service/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	sdk "github.com/pokt-network/shannon-sdk"

	"github.com/pokt-network/path/gateway"
	pathhttp "github.com/pokt-network/path/network/http"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
)

// TODO_IMPROVE(@commoddity): Re-evaluate how much of this code should live in the shannon-sdk package.

// TODO_TECHDEBT(@olshansk): Cleanup the code in this file by:
// - Renaming this to request_context.go
// - Moving HTTP request code to a dedicated file

// TODO_TECHDEBT(@adshmh): Make this threshold configurable.
//
// Maximum time to wait before using a fallback endpoint.
// TODO_TECHDEBT(@adshmh): Make this threshold configurable.
const maxWaitBeforeFallbackMillisecond = 1_000

// Maximum endpoint payload length for error logging (100 chars)
const maxEndpointPayloadLenForLogging = 100

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
// TODO_TECHDEBT(@adshmh): add sanctionedEndpointsStore to the request context.
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
	endpointObservations []*protocolobservations.ShannonEndpointObservation

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
	// Store the current RPC type for use in observations
	rc.currentRPCType = payloads[0].RPCType

	// For single payload, handle directly without additional overhead.
	if len(payloads) == 1 {
		response, err := rc.sendSingleRelay(payloads[0])
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
// Uses concurrent processing while maintaining response order and proper error handling.
func (rc *requestContext) handleParallelRelayRequests(payloads []protocol.Payload) ([]protocol.Response, error) {
	logger := rc.logger.
		With("method", "handleParallelRelayRequests").
		With("num_payloads", len(payloads)).
		With("service_id", rc.serviceID)

	logger.Debug().Msg("Starting parallel relay processing")

	resultChan := rc.launchParallelRelays(payloads)

	return rc.waitForAllRelayResponses(logger, resultChan, len(payloads))
}

// parallelRelayResult holds the result of a single relay request for parallel processing.
type parallelRelayResult struct {
	index     int
	response  protocol.Response
	err       error
	duration  time.Duration
	startTime time.Time
}

// launchParallelRelays starts all parallel relay requests and returns a result channel
func (rc *requestContext) launchParallelRelays(payloads []protocol.Payload) <-chan parallelRelayResult {
	resultChan := make(chan parallelRelayResult, len(payloads))
	var wg sync.WaitGroup

	for i, payload := range payloads {
		wg.Add(1)
		go rc.executeParallelRelay(payload, i, resultChan, &wg)
	}

	// Close channel when all goroutines complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	return resultChan
}

// TODO_TECHDEBT(@adshmh): Define/configure limits for the number of parallel requests from a single context.
//
// executeParallelRelay handles a single relay request in a goroutine
func (rc *requestContext) executeParallelRelay(
	payload protocol.Payload,
	index int,
	resultChan chan<- parallelRelayResult,
	wg *sync.WaitGroup,
) {
	defer wg.Done()

	startTime := time.Now()
	response, err := rc.sendSingleRelay(payload)
	duration := time.Since(startTime)

	result := parallelRelayResult{
		index:     index,
		response:  response,
		err:       err,
		duration:  duration,
		startTime: startTime,
	}

	resultChan <- result
}

// waitForAllRelayResponses waits for all relay responses and processes them
func (rc *requestContext) waitForAllRelayResponses(
	logger polylog.Logger,
	resultChan <-chan parallelRelayResult,
	numRequests int,
) ([]protocol.Response, error) {
	results := make([]parallelRelayResult, numRequests)
	var firstErr error

	// Collect all results
	for result := range resultChan {
		results[result.index] = result

		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			logger.Warn().Err(result.err).
				Msgf("Parallel relay request %d failed after %dms", result.index, result.duration.Milliseconds())
		}
	}

	return rc.convertResultsToResponses(results, firstErr)
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

// setSelectedEndpoint sets the selected endpoint in a thread-safe manner.
func (rc *requestContext) setSelectedEndpoint(endpoint endpoint) {
	rc.selectedEndpointMutex.Lock()
	defer rc.selectedEndpointMutex.Unlock()
	rc.selectedEndpoint = endpoint
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

	// ** Priority 2: Check Network conditions **
	// Session rollover periods
	// - Protocol relay with fallback protection during session rollover periods
	// - Sends requests in parallel to ensure reliability during network transitions
	//
	// TODO_DELETE(@adshmh): No session rollover fallback for hey service.
	case rc.fullNode.IsInSessionRollover() && rc.serviceID != "hey":
		rc.logger.Debug().Msg("Executing protocol relay with fallback protection during session rollover periods")
		// TODO_TECHDEBT(@adshmh): Separate error handling for fallback and Shannon endpoints.
		return rc.sendRelayWithFallback(payload)

	// ** Default **
	// Standard protocol relay
	// - Standard protocol relay through Shannon network
	// - Used during stable network periods with protocol endpoints
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

// sendRelayWithFallback:
// - Attempts Shannon endpoint with timeout
// - Falls back to random fallback endpoint on failure/timeout
// - Shields user from endpoint errors
// - Updates the request context's selectedEndpoint for use by logging, metrics, and data logic.
// TODO_TECHDEBT(@adshmh): This is an interim solution to be replaced with intelligent fallback.
func (rc *requestContext) sendRelayWithFallback(payload protocol.Payload) (protocol.Response, error) {
	rc.hydrateLogger("sendRelayWithFallback")

	// Convert timeout to time.Duration
	relayTimeout := time.Duration(maxWaitBeforeFallbackMillisecond) * time.Millisecond

	// Setup Shannon endpoint request:
	// - Create channel for async response
	// - Initialize response variables
	endpointResponseReceivedChan := make(chan error, 1)
	var (
		endpointResponse protocol.Response
		endpointErr      error
	)

	// Send Shannon relay in parallel:
	// - Execute request asynchronously
	// - Signal completion via channel
	go func() {
		endpointResponse, endpointErr = rc.sendProtocolRelay(payload)
		// Signal the completion of Shannon Network relay.
		endpointResponseReceivedChan <- endpointErr
	}()

	// Wait for Shannon response or timeout:
	// - If successful, return Pocket Network response from RelayMiner
	// - If error or timeout, fallback to a random fallback endpoint
	select {

	// RelayMiner responded (success or failure)
	case err := <-endpointResponseReceivedChan:
		// Successfully received and validated a response from the shannon endpoint.
		// No need to use the fallback endpoint's response.
		if err == nil {
			return endpointResponse, nil
		}

		// TODO_TECHDEBT(@adshmh): Verify correct observations/sanctions when using fallback due to endpoint error.
		//
		rc.logger.Info().Err(err).Msg("Got a response from Pocket Network, but it contained an error. Using a fallback endpoint instead")

		// Shannon endpoint failed, use fallback
		return rc.sendRelayToARandomFallbackEndpoint(payload)

	// RelayMiner timed out. Use a random fallback endpoint.
	case <-time.After(relayTimeout):
		rc.logger.Info().Msg("Timed out waiting for Pocket Network to respond. Using a fallback endpoint.")

		// Use a random fallback endpoint
		return rc.sendRelayToARandomFallbackEndpoint(payload)
	}
}

// sendRelayToARandomFallbackEndpoint:
// - Selects random fallback endpoint
// - Routes payload via selected endpoint
// - Returns error if no endpoints available
// - Updates the request context's selectedEndpoint for use by logging, metrics, and data logic.
func (rc *requestContext) sendRelayToARandomFallbackEndpoint(payload protocol.Payload) (protocol.Response, error) {
	if len(rc.fallbackEndpoints) == 0 {
		rc.logger.Warn().Msg("SHOULD HAPPEN RARELY: no fallback endpoints available for the service")
		return protocol.Response{}, fmt.Errorf("no fallback endpoints available")
	}

	rc.hydrateLogger("sendRelayToARandomFallbackEndpoint")

	// Select random fallback endpoint:
	// - Convert map to slice for random selection
	// - Pick random index
	allFallbackEndpoints := make([]endpoint, 0, len(rc.fallbackEndpoints))
	for _, endpoint := range rc.fallbackEndpoints {
		allFallbackEndpoints = append(allFallbackEndpoints, endpoint)
	}
	fallbackEndpoint := allFallbackEndpoints[rand.Intn(len(allFallbackEndpoints))]

	// TODO_TECHDEBT(@adshmh): Support tracking both the selected and fallback endpoints.
	// This is needed to support accurate visibility/sanctions against both Shannon and fallback endpoints.
	//
	// Update the selected endpoint to the randomly selected fallback endpoint
	// This ensures observations reflect the actually used endpoint
	rc.setSelectedEndpoint(fallbackEndpoint)

	// Use the randomly selected fallback endpoint to send a relay.
	relayResponse, err := rc.sendFallbackRelay(fallbackEndpoint, payload)
	if err != nil {
		rc.logger.Warn().Err(err).Msg("SHOULD NEVER HAPPEN: fallback endpoint returned an error.")
	}

	return relayResponse, err
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
	// Default: use the selected endoint's URL
	default:
		targetServerURL = selectedEndpoint.PublicURL()
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
			// Ensuring no sanctions/invalidation rules apply to LoadTesting backend server
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

	// Ensure that serialized response contains a valid HTTP status code.
	// Do not return non 2xx responses from the endpoint to the client.
	responseHTTPStatusCode := deserializedResponse.HTTPStatusCode
	if err := pathhttp.EnsureHTTPSuccess(responseHTTPStatusCode); err != nil {
		errMsg := fmt.Sprintf("Backend service returned status non-2xx: %d", responseHTTPStatusCode)
		return defaultResponse, fmt.Errorf("%w: %s", err, errMsg)
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
		app := selectedEndpoint.Session().Application
		var appAddr string
		if app != nil {
			appAddr = app.Address
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
//   - Used in cases, such as, when all endpoints are sanctioned for a service ID.
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

	// Error classification based on trusted error sources only
	endpointErrorType, recommendedSanctionType := classifyRelayError(rc.logger, endpointErr)

	// Enhanced logging with error type and error source classification
	isMalformedPayloadErr := isMalformedEndpointPayloadError(endpointErrorType)
	rc.logger.Error().
		Err(endpointErr).
		Str("error_type", endpointErrorType.String()).
		Str("sanction_type", recommendedSanctionType.String()).
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
		recommendedSanctionType,
		rc.currentRelayMinerError, // Use RelayMinerError data from request context
		rc.currentRPCType,         // Use RPC type from request context
	)

	// Track endpoint error observation for metrics and sanctioning
	rc.endpointObservations = append(rc.endpointObservations, endpointObs)

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
	rc.endpointObservations = append(rc.endpointObservations, endpointObs)

	// Return relay response received from endpoint.
	return nil
}

// sendHTTPRequest is a shared method for sending HTTP requests with common logic
func (rc *requestContext) sendHTTPRequest(
	payload protocol.Payload,
	url string,
	requestData []byte,
) ([]byte, int, error) {
	// Prepare a timeout context for the request
	timeout := time.Duration(gateway.RelayRequestTimeout) * time.Millisecond

	// TODO_INVESTIGATE: Evaluate `rc.context` vs `context.TODO` and pick the right one for timeouts.
	ctxWithTimeout, cancelFn := context.WithTimeout(context.TODO(), timeout)
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
