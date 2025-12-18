package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pokt-network/path/metrics"
	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	pathhttp "github.com/pokt-network/path/network/http"
	"github.com/pokt-network/path/observation"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
)

var (
	errHTTPRequestRejectedByParser   = errors.New("HTTP request rejected by the HTTP parser")
	errHTTPRequestRejectedByQoS      = errors.New("HTTP request rejected by service QoS instance")
	errHTTPRequestRejectedByProtocol = errors.New("HTTP request rejected by protocol instance")
)

const (
	// As of PR #340, the goal was to get the large set of changes in and enable focused investigation on the impact of parallel requests.
	// TODO_UPNEXT(@olshansk): Experiment and turn on this feature.
	// - Experiment with this feature in a single gateway and evaluate the results.
	// - Collect and analyze the metrics of this feature, ensuring it does not lead to excessive resource usage or token burn
	// - If all endpoints are sanctioned, send parallel requests by default
	// - ✅ DONE: Made configurable via concurrency_config.max_parallel_endpoints in YAML
	// - Enable parallel requests for gateways that maintain their own backend nodes as a special config

	// RelayRequestTimeout is the timeout for relay requests
	// TODO_TECHDEBT: Look into whether we can remove this variable altogether and consolidate
	// it with HTTP level timeouts.
	RelayRequestTimeout = 60 * time.Second
)

// requestContext is responsible for performing the steps necessary to complete a service request.
//
// It contains two main contexts:
//
//  1. Protocol context
//     - Supplies the list of available endpoints for the requested service to the QoS ctx
//     - Builds the Protocol ctx for the selected endpoint once it has been selected
//     - Sends the relay request to the selected endpoint using the protocol-specific implementation
//
//  2. QoS context
//     - Receives the list of available endpoints for the requested service from the Protocol instance
//     - Selects a valid endpoint from among them based on the service-specific QoS implementation
//     - Updates its internal store based on observations made during the handling of the request
//
// As of PR #72, it is limited in scope to HTTP service requests.
type requestContext struct {
	logger polylog.Logger

	// Enforces request completion deadline.
	// Passed to potentially long-running operations like protocol interactions.
	// Prevents HTTP handler timeouts that would return empty responses to clients.
	context context.Context

	// httpRequestParser is used by the request context to interpret an HTTP request as a pair of:
	// 	1. service ID
	// 	2. The service ID's corresponding QoS instance.
	httpRequestParser HTTPRequestParser

	// rpcTypeValidator validates detected RPC types against service configuration
	rpcTypeValidator *RPCTypeValidator

	// detectedRPCType stores the detected RPC type for this request
	detectedRPCType sharedtypes.RPCType

	// metricsReporter is used to export metrics based on observations made in handling service requests.
	metricsReporter RequestResponseReporter

	// dataReporter is used to export, to the data pipeline, observations made in handling service requests.
	// It is declared separately from the `metricsReporter` to be consistent with the gateway package's role
	// of explicitly defining PATH gateway's components and their interactions.
	dataReporter RequestResponseReporter

	// observationQueue handles async, sampled observation processing.
	// If nil, no async observation processing occurs.
	observationQueue *ObservationQueue

	// QoS related request context
	serviceID  protocol.ServiceID
	serviceQoS QoSService
	qosCtx     RequestQoSContext

	// Protocol related request context
	protocol Protocol
	// Multiplicity of protocol contexts to support parallel requests
	protocolContexts []ProtocolRequestContext

	// presetFailureHTTPResponse, if set, is used to return a preconstructed error response to the user.
	// For example, this is used to return an error if the specified target service ID is invalid.
	presetFailureHTTPResponse pathhttp.HTTPResponse

	// httpObservations stores the observations related to the HTTP request.
	httpObservations observation.HTTPRequestObservations
	// gatewayObservations stores gateway related observations.
	gatewayObservations *observation.GatewayObservations
	// Tracks protocol observations.
	protocolObservations *protocolobservations.Observations

	// TODO_TECHDEBT(@adshmh): refactor the interfaces and interactions with Protocol and QoS, to remove the need for this field.
	// Tracks whether the request was rejected by the QoS.
	// This is needed for handling the observations: there will be no protocol context/observations in this case.
	requestRejectedByQoS bool

	// HTTP request metadata for async observation processing.
	// These are captured from the original HTTP request for use in QueuedObservation.
	httpRequestPath    string
	httpRequestMethod  string
	httpRequestHeaders map[string]string
	httpRequestBody    []byte
	httpRequestTime    time.Time

	// originalHTTPRequest stores the original HTTP request for endpoint selection during retries.
	// Required for retry endpoint rotation to select different endpoints on each retry attempt.
	originalHTTPRequest *http.Request

	// requestID is a unique identifier for this request, used for log correlation.
	// It is either extracted from the X-Request-ID header or generated as a new UUID.
	requestID string
}

// InitFromHTTPRequest builds the required context for serving an HTTP request.
// e.g.:
//   - The target service ID
//   - The Service QoS instance
func (rc *requestContext) InitFromHTTPRequest(httpReq *http.Request) error {
	// Extract or generate request ID for log correlation.
	// Check common headers: X-Request-ID, X-Correlation-ID, X-Trace-ID
	rc.requestID = httpReq.Header.Get("X-Request-ID")
	if rc.requestID == "" {
		rc.requestID = httpReq.Header.Get("X-Correlation-ID")
	}
	if rc.requestID == "" {
		rc.requestID = httpReq.Header.Get("X-Trace-ID")
	}
	if rc.requestID == "" {
		rc.requestID = uuid.New().String()
	}

	rc.logger = rc.getHTTPRequestLogger(httpReq)

	// Store original HTTP request for endpoint selection during retries
	rc.originalHTTPRequest = httpReq

	// TODO_MVP(@adshmh): The HTTPRequestParser should return a context, similar to QoS, which is then used to get a QoS instance and the observation set.
	// Extract the service ID and find the target service's corresponding QoS instance.
	serviceID, serviceQoS, err := rc.httpRequestParser.GetQoSService(rc.context, httpReq)
	rc.serviceID = serviceID
	if err != nil {
		// TODO_MVP(@adshmh): consolidate gateway-level observations in one location.
		// Update gateway observations
		rc.updateGatewayObservations(err)

		// set an error response
		rc.presetFailureHTTPResponse = rc.httpRequestParser.GetHTTPErrorResponse(rc.context, err)

		// log the error
		rc.logger.Error().Err(err).Msg(errHTTPRequestRejectedByParser.Error())
		return errHTTPRequestRejectedByParser
	}

	// FAIL FAST: Validate service ID is configured in unified config
	// This provides a clear error message with available services if service is not configured
	if serviceID != "" && rc.protocol != nil {
		unifiedConfig := rc.protocol.GetUnifiedServicesConfig()
		if unifiedConfig != nil && !unifiedConfig.HasService(serviceID) {
			// Get list of configured services for error message
			configuredServices := unifiedConfig.GetConfiguredServiceIDs()

			err := fmt.Errorf(
				"service '%s' not configured. Available services: %v",
				serviceID, configuredServices,
			)

			// Update gateway observations
			rc.updateGatewayObservations(err)

			// Set error response
			rc.presetFailureHTTPResponse = NewServiceNotConfiguredErrorResponse(
				serviceID,
				configuredServices,
				fmt.Sprintf("Service '%s' is not configured", serviceID),
			)

			// Log the error
			rc.logger.Error().Err(err).Msg("Service not configured")
			return err
		}
	}

	rc.serviceQoS = serviceQoS
	return nil
}

// ValidateRPCType detects and validates the RPC type for this request.
// It fails fast if the detected RPC type is not in the service's configured rpc_types.
func (rc *requestContext) ValidateRPCType(httpReq *http.Request) error {
	logger := rc.logger.With("method", "ValidateRPCType").With("service_id", rc.serviceID)

	// Skip if no validator configured
	if rc.rpcTypeValidator == nil {
		logger.Warn().Msg("No RPC type validator configured - skipping validation")
		return nil
	}

	// Get service's configured RPC types for detection
	unifiedConfig := rc.protocol.GetUnifiedServicesConfig()
	if unifiedConfig == nil {
		logger.Warn().Msg("No unified config available - skipping RPC type validation")
		return nil
	}

	serviceRPCTypes := unifiedConfig.GetServiceRPCTypes(rc.serviceID)
	if len(serviceRPCTypes) == 0 {
		logger.Warn().Msg("No RPC types configured for service - skipping validation")
		return nil
	}

	// Detect RPC type from HTTP request
	detector := NewRPCTypeDetector()
	rpcType, err := detector.DetectRPCType(httpReq, string(rc.serviceID), serviceRPCTypes)
	if err != nil {
		logger.Error().Err(err).Msg("RPC type detection failed")

		// Update gateway observations
		rc.updateGatewayObservations(err)

		// Set error response
		rc.presetFailureHTTPResponse = NewRPCTypeValidationErrorResponse(
			rc.serviceID,
			"unknown",
			serviceRPCTypes,
			fmt.Sprintf("Failed to detect RPC type: %s", err.Error()),
		)

		return fmt.Errorf("%w: %s", ErrRPCTypeDetectionFailed, err.Error())
	}

	// Store detected RPC type
	rc.detectedRPCType = rpcType
	logger = logger.With("detected_rpc_type", rpcType.String())
	logger.Debug().Msg("RPC type detected successfully")

	// Validate detected RPC type against service configuration
	if err := rc.rpcTypeValidator.ValidateRPCType(rc.serviceID, rpcType); err != nil {
		logger.Error().Err(err).Msg("RPC type validation failed")

		// Update gateway observations
		rc.updateGatewayObservations(err)

		// Set error response
		mapper := NewRPCTypeMapper()
		detectedTypeStr := mapper.FormatRPCType(rpcType)
		rc.presetFailureHTTPResponse = NewRPCTypeValidationErrorResponse(
			rc.serviceID,
			detectedTypeStr,
			serviceRPCTypes,
			fmt.Sprintf("RPC type '%s' is not supported by service '%s'", detectedTypeStr, rc.serviceID),
		)

		return err
	}

	logger.Info().Msg("RPC type validation successful")
	return nil
}

// BuildQoSContextFromHTTP builds the QoS context instance using the supplied HTTP request's payload.
func (rc *requestContext) BuildQoSContextFromHTTP(httpReq *http.Request) error {
	// TODO_MVP(@adshmh): Add an HTTP request size metric/observation at the gateway/http (L7) level.
	// Required steps:
	//	1. Update QoSService interface to parse custom struct with []byte payload
	//	2. Read HTTP request body in `request` package and return struct for QoS Service
	//	3. Export HTTP observations from `request` package when reading body

	// Build the payload for the requested service using the incoming HTTP request.
	// This payload will be sent to an endpoint matching the requested service.
	// Pass the detected RPC type so QoS can use it (or apply fallback logic if UNKNOWN_RPC).
	qosCtx, isValid := rc.serviceQoS.ParseHTTPRequest(rc.context, httpReq, rc.detectedRPCType)
	rc.qosCtx = qosCtx

	if !isValid {
		// mark the request was rejected by the QoS
		rc.requestRejectedByQoS = true

		// Update gateway observations
		rc.updateGatewayObservations(errGatewayRejectedByQoS)
		rc.logger.Info().Msg(errHTTPRequestRejectedByQoS.Error())
		return errHTTPRequestRejectedByQoS
	}

	return nil
}

// BuildProtocolContextsFromHTTPRequest builds multiple Protocol contexts using the supplied HTTP request.
//
// Steps:
//  1. Get available endpoints for the requested service from the Protocol instance
//  2. Select multiple endpoints for parallel relay attempts
//  3. Build Protocol contexts for each selected endpoint
//
// The constructed Protocol instances will be used for:
//   - Sending parallel relay requests to multiple endpoints
//   - Getting the list of protocol-level observations
//
// TODO_TECHDEBT: Either rename to `PrepareProtocol` or return the built protocol context.
func (rc *requestContext) BuildProtocolContextsFromHTTPRequest(httpReq *http.Request) error {
	logger := rc.logger.With("method", "BuildProtocolContextsFromHTTPRequest").With("service_id", rc.serviceID)

	// Get RPC type from QoS-detected payload
	payloads := rc.qosCtx.GetServicePayloads()
	if len(payloads) == 0 {
		return fmt.Errorf("%w: no payloads available from QoS context", errBuildProtocolContextsFromHTTPRequest)
	}
	rpcType := payloads[0].RPCType

	logger = logger.With("rpc_type", rpcType.String())
	logger.Debug().Msg("Using detected RPC type for endpoint selection")

	// Retrieve the list of available endpoints for the requested service.
	// Filter endpoints to only those supporting the detected RPC type.
	availableEndpoints, endpointLookupObs, err := rc.protocol.AvailableHTTPEndpoints(rc.context, rc.serviceID, rpcType, httpReq)
	if err != nil {
		// error encountered: use the supplied observations as protocol observations.
		rc.updateProtocolObservations(&endpointLookupObs)
		// log and return the error
		logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Err(err).Msg("no available endpoints could be found for the request")
		return fmt.Errorf("%w: no available endpoints could be found for the request: %w", errBuildProtocolContextsFromHTTPRequest, err)
	}

	// Select multiple endpoints for parallel relay attempts
	// Use per-service max_parallel_endpoints with fallback to global config (default: 1)
	maxParallelEndpoints := rc.protocol.GetConcurrencyConfig().MaxParallelEndpoints // Global default
	if concurrencyConfig := rc.getConcurrencyConfigForService(); concurrencyConfig != nil && concurrencyConfig.MaxParallelEndpoints != nil {
		maxParallelEndpoints = *concurrencyConfig.MaxParallelEndpoints // Per-service override
	}
	selectedEndpoints, err := rc.qosCtx.GetEndpointSelector().SelectMultiple(availableEndpoints, uint(maxParallelEndpoints))
	if err != nil || len(selectedEndpoints) == 0 {
		// no protocol context will be built: use the endpointLookup observation.
		rc.updateProtocolObservations(&endpointLookupObs)
		// log and return the error
		logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msgf("no endpoints could be selected for the request from %d available endpoints", len(availableEndpoints))
		return fmt.Errorf("%w: no endpoints could be selected from %d available endpoints", errBuildProtocolContextsFromHTTPRequest, len(availableEndpoints))
	}

	// Log TLD diversity of selected endpoints
	shannonmetrics.LogEndpointTLDDiversity(logger, selectedEndpoints)

	// Prepare Protocol contexts for all selected endpoints
	numSelectedEndpoints := len(selectedEndpoints)

	rc.protocolContexts = make([]ProtocolRequestContext, 0, numSelectedEndpoints)
	var lastProtocolCtxSetupErrObs *protocolobservations.Observations

	for i, endpointAddr := range selectedEndpoints {
		logger.Debug().Msgf("Building protocol context for endpoint %d/%d: %s", i+1, numSelectedEndpoints, endpointAddr)
		// filterByReputation=true: Normal requests respect reputation filtering
		protocolCtx, protocolCtxSetupErrObs, err := rc.protocol.BuildHTTPRequestContextForEndpoint(rc.context, rc.serviceID, endpointAddr, rpcType, httpReq, true)
		if err != nil {
			lastProtocolCtxSetupErrObs = &protocolCtxSetupErrObs
			logger.Warn().Err(err).Str("endpoint_addr", string(endpointAddr)).Msgf("Failed to build protocol context for endpoint %d/%d, skipping", i+1, numSelectedEndpoints)
			// Continue with other endpoints rather than failing completely
			continue
		}
		rc.protocolContexts = append(rc.protocolContexts, protocolCtx)
		logger.Debug().Msgf("Successfully built protocol context for endpoint %d/%d: %s", i+1, numSelectedEndpoints, endpointAddr)
	}

	if len(rc.protocolContexts) == 0 {
		logger.Error().Msgf("Zero protocol contexts were built for the request with %d selected endpoints", numSelectedEndpoints)
		// error encountered: use the supplied observations as protocol observations.
		rc.updateProtocolObservations(lastProtocolCtxSetupErrObs)
		logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg(errHTTPRequestRejectedByProtocol.Error())
		return errHTTPRequestRejectedByProtocol
	}

	logger.Info().Msgf("Successfully built %d protocol contexts for the request with %d selected endpoints", len(rc.protocolContexts), numSelectedEndpoints)

	return nil
}

// WriteHTTPUserResponse uses the data contained in the gateway request context to write the user-facing HTTP response.
func (rc *requestContext) WriteHTTPUserResponse(w http.ResponseWriter) {
	// If the HTTP request was invalid, write a generic response.
	// e.g. if the specified target service ID was invalid.
	if rc.presetFailureHTTPResponse != nil {
		rc.writeHTTPResponse(rc.presetFailureHTTPResponse, w)
		return
	}

	// Processing a request only gets to this point if a QoS instance was matched to the request.
	// Use the QoS context to obtain an HTTP response.
	// There are 3 possible scenarios:
	//   1. The QoS instance rejected the request: QoS returns a properly formatted error response.
	//      E.g. a non-JSONRPC payload for an EVM service.
	//   2. Protocol relay failed for any reason: QoS returns a generic, properly formatted response.
	//      E.g. a JSONRPC error response.
	//   3. Protocol relay was sent successfully: QoS returns the endpoint's response.
	//      E.g. the chain ID for a `eth_chainId` request.
	rc.writeHTTPResponse(rc.qosCtx.GetHTTPResponse(), w)
}

// writeResponse uses the supplied http.ResponseWriter to write the supplied HTTP response.
func (rc *requestContext) writeHTTPResponse(response pathhttp.HTTPResponse, w http.ResponseWriter) {
	for key, value := range response.GetHTTPHeaders() {
		w.Header().Set(key, value)
	}

	statusCode := response.GetHTTPStatusCode()
	// TODO_IMPROVE: introduce handling for cases where the status code is not set.
	if statusCode == 0 {
		statusCode = http.StatusOK
	}

	responsePayload := response.GetPayload()
	logger := rc.logger.With(
		"http_response_payload_length", len(responsePayload),
		"http_response_status", statusCode,
	)

	// TODO_TECHDEBT(@adshmh): Refactor to consolidate all gateway observation updates in one function.
	// Required steps:
	// 	1. Update requestContext.WriteHTTPUserResponse to return response length
	// 	2. Update Gateway.HandleHTTPServiceRequest to use length for gateway observations
	//
	// Update response size observation
	rc.gatewayObservations.ResponseSize = uint64(len(responsePayload))

	w.WriteHeader(statusCode)

	numWrittenBz, writeErr := w.Write(responsePayload)
	if writeErr != nil {
		logger.With("http_response_bytes_written", numWrittenBz).Warn().Err(writeErr).Msg("Error writing the HTTP response.")
		return
	}

	logger.Info().Msg("Completed processing the HTTP request and returned an HTTP response.")
}

// observationBroadcastTimeout is the maximum time allowed for broadcasting observations.
// This prevents goroutine leaks from hanging observation operations.
const observationBroadcastTimeout = 30 * time.Second

// BroadcastAllObservations delivers the collected details regarding all aspects
// of the service request to all the interested parties.
//
// For example:
//   - QoS-level observations; e.g. endpoint validation results
//   - Protocol-level observations; e.g. "maxed-out" endpoints.
//   - Gateway-level observations; e.g. the request ID.
func (rc *requestContext) BroadcastAllObservations() {
	// observation-related tasks are called in Goroutines to avoid potentially blocking the HTTP handler.
	go func() {
		// Add timeout to prevent goroutine leaks from hanging operations
		done := make(chan struct{})
		go func() {
			defer close(done)
			rc.broadcastObservationsInternal()
		}()

		select {
		case <-done:
			// Completed successfully
		case <-time.After(observationBroadcastTimeout):
			rc.logger.Warn().Msg("observation broadcast timed out - possible goroutine leak")
		}
	}()
}

// broadcastObservationsInternal performs the actual observation broadcasting work.
func (rc *requestContext) broadcastObservationsInternal() {
	// update gateway-level observations: no request error encountered.
	rc.updateGatewayObservations(nil)
	// update protocol-level observations: no errors encountered setting up the protocol context.
	rc.updateProtocolObservations(nil)
	if rc.protocolObservations != nil {
		err := rc.protocol.ApplyHTTPObservations(rc.protocolObservations)
		if err != nil {
			rc.logger.Warn().Err(err).Msg("error applying protocol observations.")
		}
	}

	// The service request context contains all the details the QoS needs to update its internal metrics about endpoint(s), which it should use to build
	// the qosobservations.Observations struct.
	// This ensures that separate PATH instances can communicate and share their QoS observations.
	// The QoS context will be nil if the target service ID is not specified correctly by the request.
	var qosObservations qosobservations.Observations
	if rc.qosCtx != nil {
		qosObservations = rc.qosCtx.GetObservations()

		// Only apply observations synchronously if the observation pipeline is not enabled.
		// When the pipeline is enabled, we rely on:
		// 1. Health checks (100%) for primary block height updates
		// 2. Observation pipeline (sampled async) for user request-based updates
		// This avoids heavy parsing on every request.
		observationPipelineEnabled := rc.observationQueue != nil && rc.observationQueue.IsEnabled()
		if !observationPipelineEnabled {
			if err := rc.serviceQoS.ApplyObservations(&qosObservations); err != nil {
				rc.logger.Warn().Err(err).Msg("error applying QoS observations.")
			}
		}
	}

	// Prepare and publish observations to both the metrics and data reporters.
	observations := &observation.RequestResponseObservations{
		ServiceId:   string(rc.serviceID),
		HttpRequest: &rc.httpObservations,
		Gateway:     rc.gatewayObservations,
		Protocol:    rc.protocolObservations,
		Qos:         &qosObservations,
	}

	if rc.metricsReporter != nil {
		rc.metricsReporter.Publish(observations)
	}
	// Need to account for an empty `data_reporter_config` field in the YAML config file.
	// E.g. This can happen when running the Gateway in a local environment.
	// TODO_DELETE: Skip data reporting for "hey" service
	if rc.dataReporter != nil && rc.serviceID != "hey" {
		rc.dataReporter.Publish(observations)
	}
}

// GetRequestID returns the request ID for this request context.
func (rc *requestContext) GetRequestID() string {
	return rc.requestID
}

// getHTTPRequestLogger returns a logger with attributes set using the supplied HTTP request.
func (rc *requestContext) getHTTPRequestLogger(httpReq *http.Request) polylog.Logger {
	var urlStr string
	if httpReq.URL != nil {
		urlStr = httpReq.URL.String()
	}

	return rc.logger.With(
		"request_id", rc.requestID,
		"http_req_url", urlStr,
		"http_req_host", httpReq.Host,
		"http_req_remote_addr", httpReq.RemoteAddr,
		"http_req_content_length", httpReq.ContentLength,
	)
}

// updateProtocolObservations updates the stored protocol-level observations.
// It is called at:
// - Protocol context setup error.
// - When broadcasting observations.
func (rc *requestContext) updateProtocolObservations(protocolContextSetupErrorObservation *protocolobservations.Observations) {
	// protocol observation already set: skip.
	// This happens when a protocol context setup observation was reported earlier.
	if rc.protocolObservations != nil {
		return
	}

	// protocol context setup error observation is set: skip.
	if protocolContextSetupErrorObservation != nil {
		rc.protocolObservations = protocolContextSetupErrorObservation
		return
	}

	// Check if we have multiple protocol contexts and use the first successful one
	if len(rc.protocolContexts) > 0 {
		// TODO_TECHDEBT: Aggregate observations from all protocol contexts for better insights.
		// Currently using only the first context's observations for backward compatibility
		rc.logger.Debug().Msgf("%d protocol contexts were built for the request, but only using the first one for observations", len(rc.protocolContexts))
		observations := rc.protocolContexts[0].GetObservations()
		rc.protocolObservations = &observations
		return
	}

	// QoS rejected the request: there is no protocol context/observation.
	if rc.requestRejectedByQoS {
		return
	}

	// This should never happen: either protocol context is setup, or an observation is reported to use directly for the request.
	rc.logger.
		With("service_id", rc.serviceID).
		ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).
		Msg("SHOULD NEVER HAPPEN: protocol context is nil, but no protocol setup observation have been reported.")
}

// updateGatewayObservations
// - updates the gateway-level observations in the request context with other metadata in the request context.
// - sets the gateway observation error with the one provided, if not already set
func (rc *requestContext) updateGatewayObservations(err error) {
	// set the service ID on the gateway observations.
	rc.gatewayObservations.ServiceId = string(rc.serviceID)

	// Update the request completion time on the gateway observation
	rc.gatewayObservations.CompletedTime = timestamppb.Now()

	// No errors: skip.
	if err == nil {
		return
	}

	// Request error already set: skip.
	if rc.gatewayObservations.GetRequestError() != nil {
		return
	}

	switch {
	// Service ID not specified
	case errors.Is(err, ErrGatewayNoServiceIDProvided):
		rc.logger.Error().Err(err).Msg("No service ID specified in the HTTP headers. Request will fail.")
		rc.gatewayObservations.RequestError = &observation.GatewayRequestError{
			// Set the error kind
			ErrorKind: observation.GatewayRequestErrorKind_GATEWAY_REQUEST_ERROR_KIND_MISSING_SERVICE_ID,
			// Use the error message as error details.
			Details: err.Error(),
		}

	// Request was rejected by the QoS instance.
	// e.g. HTTP payload could not be unmarshaled into a JSONRPC request.
	case errors.Is(err, errGatewayRejectedByQoS):
		rc.logger.Error().Err(err).Msg("QoS instance rejected the request. Request will fail.")
		rc.gatewayObservations.RequestError = &observation.GatewayRequestError{
			// Set the error kind
			ErrorKind: observation.GatewayRequestErrorKind_GATEWAY_REQUEST_ERROR_KIND_REJECTED_BY_QOS,
			// Use the error message as error details.
			Details: err.Error(),
		}

	default:
		rc.logger.Warn().Err(err).Msg("SHOULD NEVER HAPPEN: unrecognized gateway-level request error.")
		// Set a generic request error observation
		rc.gatewayObservations.RequestError = &observation.GatewayRequestError{
			// unspecified error kind: this should not happen
			ErrorKind: observation.GatewayRequestErrorKind_GATEWAY_REQUEST_ERROR_KIND_UNSPECIFIED,
			// Use the error message as error details.
			Details: err.Error(),
		}
	}
}

// updateGatewayObservationsWithParallelRequests updates the gateway observations with parallel request metrics.
//
// It is called when the gateway handles a parallel request and used for downstream metrics.
func (rc *requestContext) updateGatewayObservationsWithParallelRequests(numRequests, numSuccessful, numFailed, numCanceled int) {
	rc.gatewayObservations.GatewayParallelRequestObservations = &observation.GatewayParallelRequestObservations{
		NumRequests:   int32(numRequests),
		NumSuccessful: int32(numSuccessful),
		NumFailed:     int32(numFailed),
		NumCanceled:   int32(numCanceled),
	}
}

// captureHTTPRequestMetadata captures the HTTP request metadata for async observation processing.
// This method reads the request body and restores it so it can be read again by downstream processing.
// It should be called early in the request lifecycle before the body is consumed.
//
// The captured metadata is stored in the requestContext for later use when queuing observations.
func (rc *requestContext) captureHTTPRequestMetadata(httpReq *http.Request) {
	rc.httpRequestTime = time.Now()
	rc.httpRequestPath = httpReq.URL.Path
	rc.httpRequestMethod = httpReq.Method

	// Capture headers (excluding sensitive ones)
	rc.httpRequestHeaders = make(map[string]string)
	for key, values := range httpReq.Header {
		if len(values) > 0 {
			// Skip sensitive headers (case-insensitive check)
			lowerKey := strings.ToLower(key)
			if lowerKey == "authorization" || lowerKey == "cookie" || lowerKey == "x-api-key" {
				continue
			}
			rc.httpRequestHeaders[key] = values[0]
		}
	}

	// Read and restore the request body
	if httpReq.Body != nil {
		bodyBytes, err := io.ReadAll(httpReq.Body)
		if err != nil {
			rc.logger.Debug().Err(err).Msg("Failed to read request body for observation queue")
			return
		}
		// Store the body bytes
		rc.httpRequestBody = bodyBytes
		// Restore the body so it can be read again
		httpReq.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	}
}

// tryQueueObservation attempts to queue an observation for async processing.
// This method is non-blocking - if the queue is full or disabled, it silently skips.
// It creates a QueuedObservation with all the captured request/response context.
//
// This should be called after UpdateWithResponse() to include the response data.
func (rc *requestContext) tryQueueObservation(endpointAddr protocol.EndpointAddr, responseBody []byte, httpStatusCode int) {
	// Skip if observation queue is not configured
	if rc.observationQueue == nil {
		return
	}

	// Skip if the queue is not enabled
	if !rc.observationQueue.IsEnabled() {
		return
	}

	// Calculate latency from request start time
	latency := time.Since(rc.httpRequestTime)

	// Create the queued observation with all context
	obs := &QueuedObservation{
		ServiceID:          rc.serviceID,
		EndpointAddr:       endpointAddr,
		Source:             SourceUserRequest,
		Timestamp:          time.Now(),
		Latency:            latency,
		RequestID:          rc.requestID,
		RequestPath:        rc.httpRequestPath,
		RequestHTTPMethod:  rc.httpRequestMethod,
		RequestHeaders:     rc.httpRequestHeaders,
		RequestBody:        rc.httpRequestBody,
		ResponseStatusCode: httpStatusCode,
		ResponseBody:       responseBody,
	}

	// Try to queue (non-blocking, sampled)
	rc.observationQueue.TryQueue(obs)
}

// getRetryConfigForService retrieves the retry configuration for the current service.
// Returns nil if no retry configuration is available or if the service is not found.
func (rc *requestContext) getRetryConfigForService() *ServiceRetryConfig {
	if rc.protocol == nil {
		return nil
	}

	unifiedConfig := rc.protocol.GetUnifiedServicesConfig()
	if unifiedConfig == nil {
		return nil
	}

	// Get merged service config (with defaults applied)
	mergedConfig := unifiedConfig.GetMergedServiceConfig(rc.serviceID)
	if mergedConfig == nil {
		return nil
	}

	return mergedConfig.RetryConfig
}

// getConcurrencyConfigForService returns the merged concurrency configuration for the current service.
// This includes both global defaults and per-service overrides.
func (rc *requestContext) getConcurrencyConfigForService() *ServiceConcurrencyConfig {
	unifiedConfig := rc.protocol.GetUnifiedServicesConfig()
	if unifiedConfig == nil {
		return nil
	}

	// Get merged service config (with defaults applied)
	mergedConfig := unifiedConfig.GetMergedServiceConfig(rc.serviceID)
	if mergedConfig == nil {
		return nil
	}

	return mergedConfig.ConcurrencyConfig
}

// shouldRetry determines if a request should be retried based on the error, status code, duration, and retry config.
// Returns true if the request should be retried.
// The requestDuration parameter is the time the failed request took - if it exceeds MaxRetryLatency, no retry is attempted.
// The endpointDomain parameter is used for metrics recording when retries are skipped due to budget exceeded.
func (rc *requestContext) shouldRetry(err error, statusCode int, requestDuration time.Duration, retryConfig *ServiceRetryConfig, endpointDomain string) bool {
	// No retry config or retry disabled
	if retryConfig == nil || retryConfig.Enabled == nil || !*retryConfig.Enabled {
		if rc.logger != nil {
			rc.logger.Debug().
				Str("service_id", string(rc.serviceID)).
				Bool("retry_enabled", false).
				Msg("[RETRY] Retry disabled or no config")
		}
		return false
	}

	// Check time budget first - if the failed request took too long, don't retry
	// This prevents making users wait even longer after a slow failure
	if retryConfig.MaxRetryLatency != nil && *retryConfig.MaxRetryLatency > 0 {
		if requestDuration > *retryConfig.MaxRetryLatency {
			if rc.logger != nil {
				rc.logger.Debug().
					Str("service_id", string(rc.serviceID)).
					Int("status_code", statusCode).
					Dur("request_duration_ms", requestDuration).
					Dur("max_retry_latency_ms", *retryConfig.MaxRetryLatency).
					Msg("[RETRY] Request took too long, skipping retry (time budget exceeded)")
			}
			return false
		}
		if rc.logger != nil {
			rc.logger.Debug().
				Str("service_id", string(rc.serviceID)).
				Int("status_code", statusCode).
				Dur("request_duration_ms", requestDuration).
				Dur("max_retry_latency_ms", *retryConfig.MaxRetryLatency).
				Msg("[RETRY] Request duration within time budget, checking retry conditions")
		}
	}

	// Check for 5xx errors if configured
	if retryConfig.RetryOn5xx != nil && *retryConfig.RetryOn5xx && statusCode >= 500 && statusCode < 600 {
		// Record retry metric
		domain, _ := shannonmetrics.ExtractDomainOrHost(endpointDomain)
		metrics.RecordRetryDistribution(domain, string(rc.detectedRPCType), string(rc.serviceID), metrics.RetryReason5xx)

		if rc.logger != nil {
			rc.logger.Debug().
				Str("service_id", string(rc.serviceID)).
				Int("status_code", statusCode).
				Dur("request_duration_ms", requestDuration).
				Str("retry_reason", "5xx_error").
				Msg("[RETRY] Will retry due to 5xx status code")
		}
		return true
	}

	// If no error, nothing more to check
	if err == nil {
		if rc.logger != nil {
			rc.logger.Debug().
				Str("service_id", string(rc.serviceID)).
				Int("status_code", statusCode).
				Msg("[RETRY] No error and not 5xx, will not retry")
		}
		return false
	}

	// Check for timeout errors if configured
	if retryConfig.RetryOnTimeout != nil && *retryConfig.RetryOnTimeout {
		// Check if error is a timeout (context.DeadlineExceeded or contains "timeout")
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "timeout") {
			// Record retry metric
			domain, _ := shannonmetrics.ExtractDomainOrHost(endpointDomain)
			metrics.RecordRetryDistribution(domain, string(rc.detectedRPCType), string(rc.serviceID), metrics.RetryReasonTimeout)

			if rc.logger != nil {
				rc.logger.Debug().
					Str("service_id", string(rc.serviceID)).
					Err(err).
					Dur("request_duration_ms", requestDuration).
					Str("retry_reason", "timeout").
					Msg("[RETRY] Will retry due to timeout error")
			}
			return true
		}
	}

	// Check for connection errors if configured
	if retryConfig.RetryOnConnection != nil && *retryConfig.RetryOnConnection {
		// Check if error is a connection error (contains "connection" or "dial")
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "connection") || strings.Contains(errMsg, "dial") || strings.Contains(errMsg, "network") {
			// Record retry metric
			domain, _ := shannonmetrics.ExtractDomainOrHost(endpointDomain)
			metrics.RecordRetryDistribution(domain, string(rc.detectedRPCType), string(rc.serviceID), metrics.RetryReasonConnection)

			if rc.logger != nil {
				rc.logger.Debug().
					Str("service_id", string(rc.serviceID)).
					Err(err).
					Dur("request_duration_ms", requestDuration).
					Str("retry_reason", "connection_error").
					Msg("[RETRY] Will retry due to connection error")
			}
			return true
		}
	}

	if rc.logger != nil {
		rc.logger.Debug().
			Str("service_id", string(rc.serviceID)).
			Err(err).
			Int("status_code", statusCode).
			Dur("request_duration_ms", requestDuration).
			Msg("[RETRY] No retry conditions met")
	}
	return false
}
