package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"

	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	retrymetrics "github.com/pokt-network/path/metrics/retry"
	"github.com/pokt-network/path/protocol"
)

// TODO_TECHDEBT(@adshmh): A single protocol context should handle both single/parallel calls to one or more endpoints.
// Including:
// - Support for configuration of parallel requests (including fallback)
// - Generating and applying of endpoint(s) observations from all outgoing request(s).
// - Full encapsulation of the parallel request logic.
//
// parallelRelayResult is used to track the result of a parallel relay request.
// It is intended for internal use by the requestContext.
type parallelRelayResult struct {
	responses []protocol.Response
	err       error
	index     int
	duration  time.Duration
	startTime time.Time
}

// parallelRequestMetrics tracks metrics for parallel requests
type parallelRequestMetrics struct {
	numRequestsToAttempt     int
	numCompletedSuccessfully int
	numFailedOrErrored       int
	overallStartTime         time.Time
}

// TODO_TECHDEBT(@adshmh): Use a SINGLE protocol context to handle a relay request.
// - Launching multiple parallel requests to multiple endpoints is an internal protocol decision.
//
// HandleRelayRequest sends a relay from the perspective of a gateway.
//
// It performs the following steps:
//  1. Selects endpoints using the QoS context
//  2. Sends the relay to multiple selected endpoints in parallel, using the protocol contexts
//  3. Processes the first successful endpoint's response using the QoS context
//
// HandleRelayRequest is written as a template method to allow the customization of key steps,
// e.g. endpoint selection and protocol-specific details of sending a relay.
// See the following link for more details:
// https://en.wikipedia.org/wiki/Template_method_pattern
func (rc *requestContext) HandleRelayRequest() error {
	logger := rc.logger.
		With("service_id", rc.serviceID).
		With("method", "HandleRelayRequest").
		With("num_protocol_contexts", len(rc.protocolContexts))

	// Track whether this is a parallel or single request
	isParallel := len(rc.protocolContexts) > 1

	// If we have multiple protocol contexts, send parallel requests
	if isParallel {
		logger.Debug().Msgf("Handling %d parallel relay requests", len(rc.protocolContexts))
		return rc.handleParallelRelayRequests()
	}

	// Fallback to single request for backward compatibility
	logger.Debug().Msg("Handling single relay request")
	return rc.handleSingleRelayRequest()
}

// handleSingleRelayRequest handles a single relay request (original behavior)
// handleSingleRelayRequest handles a single relay request (original behavior)
func (rc *requestContext) handleSingleRelayRequest() error {
	logger := rc.logger.With("method", "handleSingleRelayRequest")

	// Get retry configuration for the service
	retryConfig := rc.getRetryConfigForService()

	// Determine max attempts
	maxAttempts := 1
	if retryConfig != nil && retryConfig.Enabled != nil && *retryConfig.Enabled {
		if retryConfig.MaxRetries != nil && *retryConfig.MaxRetries > 0 {
			maxAttempts = *retryConfig.MaxRetries + 1 // +1 for the initial attempt
		}
	}

	var lastErr error
	var lastStatusCode int
	retryStartTime := time.Now()

	// Retry loop
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			logger.Debug().
				Int("attempt", attempt).
				Int("max_attempts", maxAttempts).
				Err(lastErr).
				Msg("Retrying relay request")
		}

		// Send the service request payload, through the protocol context, to the selected endpoint.
		// In this code path, we are always guaranteed to have exactly one protocol context.
		endpointResponses, err := rc.protocolContexts[0].HandleServiceRequest(rc.qosCtx.GetServicePayloads())

		// Extract status code and endpoint address from responses (if any)
		statusCode := 0
		var endpointAddr protocol.EndpointAddr
		if len(endpointResponses) > 0 {
			statusCode = endpointResponses[0].HTTPStatusCode
			endpointAddr = endpointResponses[0].EndpointAddr
		}

		// Check if the request was successful
		if err == nil && (statusCode == 0 || (statusCode >= 200 && statusCode < 300)) {
			// Success! Process the response
			// TODO_TECHDEBT(@adshmh): Ensure the protocol returns exactly one response per service payload:
			// - Define a struct to contain each service payload and its corresponding response.
			// - protocol should return this new struct to clarify mapping of service payloads and the corresponding endpoint response.
			// - QoS packages should use this new struct to prepare the user response.
			// - Remove the individual endpoint response handling from the gateway package.
			//
			for _, endpointResponse := range endpointResponses {
				rc.qosCtx.UpdateWithResponse(endpointResponse.EndpointAddr, endpointResponse.Bytes, endpointResponse.HTTPStatusCode)

				// Queue observation for async parsing (sampled, non-blocking)
				rc.tryQueueObservation(endpointResponse.EndpointAddr, endpointResponse.Bytes, endpointResponse.HTTPStatusCode)
			}

			if attempt > 1 {
				// Record retry success metrics
				endpointDomain := shannonmetrics.ExtractTLDFromEndpointAddr(string(endpointAddr))
				retrymetrics.RecordRetrySuccess(string(rc.serviceID), endpointDomain, attempt)

				// Record total retry latency
				retryLatency := time.Since(retryStartTime).Seconds()
				retrymetrics.RecordRetryLatency(string(rc.serviceID), true, retryLatency)

				logger.Info().
					Int("attempt", attempt).
					Msg("Relay request succeeded after retry")
			}

			return nil
		}

		// Store the last error and status code for potential retry decision
		lastErr = err
		lastStatusCode = statusCode

		// Log the error/failure
		if err != nil {
			logger.Warn().Err(err).
				Int("attempt", attempt).
				Int("max_attempts", maxAttempts).
				Msg("Relay request failed with error")
		} else {
			logger.Warn().
				Int("status_code", statusCode).
				Int("attempt", attempt).
				Int("max_attempts", maxAttempts).
				Msg("Relay request failed with non-success status code")
		}

		// Update QoS context with the failed response (if we have responses)
		for _, endpointResponse := range endpointResponses {
			rc.qosCtx.UpdateWithResponse(endpointResponse.EndpointAddr, endpointResponse.Bytes, endpointResponse.HTTPStatusCode)
			rc.tryQueueObservation(endpointResponse.EndpointAddr, endpointResponse.Bytes, endpointResponse.HTTPStatusCode)
		}

		// Check if we should retry
		if attempt < maxAttempts {
			if !rc.shouldRetry(err, statusCode, retryConfig) {
				logger.Debug().
					Int("attempt", attempt).
					Int("status_code", statusCode).
					Msg("Request failed but retry conditions not met, stopping retries")
				break
			}

			// Record retry attempt metrics
			endpointDomain := shannonmetrics.ExtractTLDFromEndpointAddr(string(endpointAddr))
			retryReason := rc.determineRetryReason(err, statusCode)
			retrymetrics.RecordRetryAttempt(string(rc.serviceID), endpointDomain, retryReason, attempt+1)
		}
	}

	// Record failed retry latency if we made retry attempts
	if maxAttempts > 1 {
		retryLatency := time.Since(retryStartTime).Seconds()
		retrymetrics.RecordRetryLatency(string(rc.serviceID), false, retryLatency)
	}

	// All retries exhausted or conditions not met
	if lastErr != nil {
		logger.Error().Err(lastErr).
			Int("max_attempts", maxAttempts).
			Msg("Failed to send relay request after all retry attempts")
		return lastErr
	}

	// Return error for non-success status code
	logger.Error().
		Int("status_code", lastStatusCode).
		Int("max_attempts", maxAttempts).
		Msg("Relay request failed with non-success status code after all retry attempts")
	return fmt.Errorf("relay request failed with status code %d after %d attempts", lastStatusCode, maxAttempts)
}

// TODO_TECHDEBT(@adshmh): Remove this method:
// Parallel requests are an internal detail of a protocol integration package
// As of PR #388, `protocol/shannon` is the only protocol integration package.
//
// handleParallelRelayRequests orchestrates parallel relay requests and returns the first successful response.
func (rc *requestContext) handleParallelRelayRequests() error {
	metrics := &parallelRequestMetrics{
		numRequestsToAttempt: len(rc.protocolContexts),
		overallStartTime:     time.Now(),
	}
	defer rc.updateParallelRequestMetrics(metrics)

	logger := rc.logger.
		With("method", "handleParallelRelayRequests").
		With("num_protocol_contexts", len(rc.protocolContexts)).
		With("service_id", rc.serviceID)
	logger.Debug().Msg("Starting parallel relay race")

	// TODO_TECHDEBT: Make sure timed out parallel requests are also sanctioned.
	ctx, cancel := context.WithTimeout(rc.context, RelayRequestTimeout)
	defer cancel()

	resultChan, qosContextMutex := rc.launchParallelRequests(ctx, logger)

	return rc.waitForFirstSuccessfulResponse(ctx, logger, resultChan, metrics, qosContextMutex)
}

// updateParallelRequestMetrics updates gateway observations with parallel request metrics
func (rc *requestContext) updateParallelRequestMetrics(metrics *parallelRequestMetrics) {
	numCanceledByContext := metrics.numRequestsToAttempt - metrics.numCompletedSuccessfully - metrics.numFailedOrErrored
	rc.updateGatewayObservationsWithParallelRequests(
		metrics.numRequestsToAttempt,
		metrics.numCompletedSuccessfully,
		metrics.numFailedOrErrored,
		numCanceledByContext,
	)
}

// launchParallelRequests starts all parallel relay requests and returns a result channel and mutex for QoS context operations
func (rc *requestContext) launchParallelRequests(ctx context.Context, logger polylog.Logger) (<-chan parallelRelayResult, *sync.Mutex) {
	resultChan := make(chan parallelRelayResult, len(rc.protocolContexts))

	// Ensures thread-safety of QoS context operations.
	qosContextMutex := &sync.Mutex{}

	for protocolCtxIdx, protocolCtx := range rc.protocolContexts {
		go rc.executeOneOfParallelRequests(ctx, logger, protocolCtx, protocolCtxIdx, resultChan, qosContextMutex)
	}

	return resultChan, qosContextMutex
}

// executeOneOfParallelRequests handles a single relay request in a goroutine
func (rc *requestContext) executeOneOfParallelRequests(
	ctx context.Context,
	logger polylog.Logger,
	protocolCtx ProtocolRequestContext,
	index int,
	resultChan chan<- parallelRelayResult,
	qosContextMutex *sync.Mutex,
) {
	startTime := time.Now()

	// Get retry configuration for the service
	retryConfig := rc.getRetryConfigForService()

	// Determine max attempts
	maxAttempts := 1
	if retryConfig != nil && retryConfig.Enabled != nil && *retryConfig.Enabled {
		if retryConfig.MaxRetries != nil && *retryConfig.MaxRetries > 0 {
			maxAttempts = *retryConfig.MaxRetries + 1 // +1 for the initial attempt
		}
	}

	var lastErr error
	var lastResponses []protocol.Response

	// Retry loop
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Check if context was canceled before attempting
		select {
		case <-ctx.Done():
			logger.Debug().Msgf("Request to endpoint %d canceled before attempt %d", index, attempt)
			return
		default:
		}

		if attempt > 1 {
			logger.Debug().
				Int("endpoint_index", index).
				Int("attempt", attempt).
				Int("max_attempts", maxAttempts).
				Err(lastErr).
				Msg("Retrying parallel relay request")
		}

		responses, err := protocolCtx.HandleServiceRequest(rc.qosCtx.GetServicePayloads())

		// Extract status code and endpoint address from responses (if any)
		statusCode := 0
		var endpointAddr protocol.EndpointAddr
		if len(responses) > 0 {
			statusCode = responses[0].HTTPStatusCode
			endpointAddr = responses[0].EndpointAddr
		}

		// Check if the request was successful
		if err == nil && (statusCode == 0 || (statusCode >= 200 && statusCode < 300)) {
			// Success! Send the result
			duration := time.Since(startTime)
			result := parallelRelayResult{
				responses: responses,
				err:       nil,
				index:     index,
				duration:  duration,
				startTime: startTime,
			}

			if attempt > 1 {
				// Record retry success metrics
				endpointDomain := shannonmetrics.ExtractTLDFromEndpointAddr(string(endpointAddr))
				retrymetrics.RecordRetrySuccess(string(rc.serviceID), endpointDomain, attempt)

				// Record total retry latency
				retryLatency := time.Since(startTime).Seconds()
				retrymetrics.RecordRetryLatency(string(rc.serviceID), true, retryLatency)

				logger.Info().
					Int("endpoint_index", index).
					Int("attempt", attempt).
					Msg("Parallel relay request succeeded after retry")
			}

			select {
			case resultChan <- result:
				// Result sent successfully
			case <-ctx.Done():
				logger.Debug().Msgf("Request to endpoint %d canceled after success on attempt %d", index, attempt)
			}
			return
		}

		// Store the last error and responses for potential retry decision
		lastErr = err
		lastResponses = responses

		// Log the error/failure
		if err != nil {
			logger.Warn().Err(err).
				Int("endpoint_index", index).
				Int("attempt", attempt).
				Int("max_attempts", maxAttempts).
				Msgf("Parallel relay request to endpoint %d failed with error (attempt %d/%d)", index, attempt, maxAttempts)
		} else {
			logger.Warn().
				Int("endpoint_index", index).
				Int("status_code", statusCode).
				Int("attempt", attempt).
				Int("max_attempts", maxAttempts).
				Msgf("Parallel relay request to endpoint %d failed with status code %d (attempt %d/%d)", index, statusCode, attempt, maxAttempts)
		}

		// Update QoS context with the failed response (if we have responses)
		qosContextMutex.Lock()
		for _, response := range responses {
			rc.qosCtx.UpdateWithResponse(response.EndpointAddr, response.Bytes, response.HTTPStatusCode)
			rc.tryQueueObservation(response.EndpointAddr, response.Bytes, response.HTTPStatusCode)
		}
		qosContextMutex.Unlock()

		// Check if we should retry
		if attempt < maxAttempts {
			if !rc.shouldRetry(err, statusCode, retryConfig) {
				logger.Debug().
					Int("endpoint_index", index).
					Int("attempt", attempt).
					Int("status_code", statusCode).
					Msg("Request failed but retry conditions not met, stopping retries")
				break
			}

			// Record retry attempt metrics
			endpointDomain := shannonmetrics.ExtractTLDFromEndpointAddr(string(endpointAddr))
			retryReason := rc.determineRetryReason(err, statusCode)
			retrymetrics.RecordRetryAttempt(string(rc.serviceID), endpointDomain, retryReason, attempt+1)

			// Small delay before retry to avoid hammering the endpoint immediately
			// We don't use exponential backoff here because parallel requests have their own timeout
			select {
			case <-ctx.Done():
				logger.Debug().Msgf("Request to endpoint %d canceled during retry delay", index)
				return
			case <-time.After(100 * time.Millisecond):
				// Continue to next attempt
			}
		}
	}

	// Record failed retry latency if we made retry attempts
	if maxAttempts > 1 {
		retryLatency := time.Since(startTime).Seconds()
		retrymetrics.RecordRetryLatency(string(rc.serviceID), false, retryLatency)
	}

	// All retries exhausted - send the failure result
	duration := time.Since(startTime)
	result := parallelRelayResult{
		responses: lastResponses,
		err:       lastErr,
		index:     index,
		duration:  duration,
		startTime: startTime,
	}

	// TODO_TECHDEBT(@adshmh): refactor the parallel requests feature:
	// 1. Ensure parallel requests are handled correctly by the QoS layer: e.g. cannot use the most recent response as best anymore.
	// 2. Simplify the parallel requests feature: it may be best to fully encapsulate it in the protocol/shannon package.
	if lastErr != nil {
		qosContextMutex.Lock()
		for _, response := range lastResponses {
			rc.qosCtx.UpdateWithResponse(response.EndpointAddr, response.Bytes, response.HTTPStatusCode)

			// Queue observation for async parsing (sampled, non-blocking)
			rc.tryQueueObservation(response.EndpointAddr, response.Bytes, response.HTTPStatusCode)
		}
		qosContextMutex.Unlock()
	}

	select {
	case resultChan <- result:
		// Result sent successfully
	case <-ctx.Done():
		logger.Debug().Msgf("Request to endpoint %d canceled after %dms and %d retry attempts", index, duration.Milliseconds(), maxAttempts)
	}
}

// waitForFirstSuccessfulResponse waits for the first successful response or handles all failures
func (rc *requestContext) waitForFirstSuccessfulResponse(
	ctx context.Context,
	logger polylog.Logger,
	resultChan <-chan parallelRelayResult,
	metrics *parallelRequestMetrics,
	qosContextMutex *sync.Mutex,
) error {
	var lastErr error
	var responseTimings []string

	for metrics.numCompletedSuccessfully < metrics.numRequestsToAttempt {
		select {
		case result := <-resultChan:
			responseTimings = append(responseTimings, rc.formatTimingLog(result))

			if result.err == nil {
				return rc.handleSuccessfulResponse(logger, result, metrics, qosContextMutex)
			} else {
				rc.handleFailedResponse(logger, result, metrics, &lastErr)
			}

		case <-ctx.Done():
			return rc.handleContextDone(ctx, logger, metrics, lastErr)
		}
	}

	return rc.handleAllRequestsFailed(logger, metrics, responseTimings, lastErr)
}

// handleSuccessfulResponse processes the first successful response
func (rc *requestContext) handleSuccessfulResponse(
	logger polylog.Logger,
	result parallelRelayResult,
	metrics *parallelRequestMetrics,
	qosContextMutex *sync.Mutex,
) error {
	metrics.numCompletedSuccessfully++
	overallDuration := time.Since(metrics.overallStartTime)

	qosContextMutex.Lock()
	defer qosContextMutex.Unlock()

	for _, response := range result.responses {
		endpointDomain := shannonmetrics.ExtractTLDFromEndpointAddr(string(response.EndpointAddr))

		logger.Info().
			Str("endpoint_domain", endpointDomain).
			Msgf("Parallel request success: endpoint %d/%d responded in %dms",
				result.index+1, metrics.numRequestsToAttempt, overallDuration.Milliseconds())

		rc.qosCtx.UpdateWithResponse(response.EndpointAddr, response.Bytes, response.HTTPStatusCode)

		// Queue observation for async parsing (sampled, non-blocking)
		rc.tryQueueObservation(response.EndpointAddr, response.Bytes, response.HTTPStatusCode)
	}

	return nil
}

// handleFailedResponse processes a failed response
func (rc *requestContext) handleFailedResponse(
	logger polylog.Logger,
	result parallelRelayResult,
	metrics *parallelRequestMetrics,
	lastErr *error,
) {
	metrics.numFailedOrErrored++
	logger.Warn().Err(result.err).
		Msgf("Request to endpoint %d failed after %dms", result.index, result.duration.Milliseconds())
	*lastErr = result.err
}

// handleContextDone processes context cancellation or timeout
func (rc *requestContext) handleContextDone(
	ctx context.Context,
	logger polylog.Logger,
	metrics *parallelRequestMetrics,
	lastErr error,
) error {
	totalDuration := time.Since(metrics.overallStartTime).Milliseconds()

	if ctx.Err() == context.DeadlineExceeded {
		logger.Error().Msgf("Parallel requests timed out after %dms and %d completed requests",
			totalDuration, metrics.numCompletedSuccessfully)
		return fmt.Errorf("parallel relay requests timed out after %dms and %d completed requests, last error: %w",
			totalDuration, metrics.numCompletedSuccessfully, lastErr)
	}

	logger.Debug().Msg("Parallel requests canceled")
	return fmt.Errorf("parallel relay requests canceled after %dms and %d completed requests, last error: %w",
		totalDuration, metrics.numCompletedSuccessfully, lastErr)
}

// handleAllRequestsFailed processes the case where all requests failed
func (rc *requestContext) handleAllRequestsFailed(
	logger polylog.Logger,
	metrics *parallelRequestMetrics,
	responseTimings []string,
	lastErr error,
) error {
	totalDuration := time.Since(metrics.overallStartTime).Milliseconds()
	timingsStr := strings.Join(responseTimings, ", ")

	logger.Error().Msgf("All %d parallel requests failed after %dms with individual request durations: %s",
		metrics.numRequestsToAttempt, totalDuration, timingsStr)

	return fmt.Errorf("all parallel relay requests failed, last error: %w", lastErr)
}

// formatTimingLog creates a timing log string for a relay result
func (rc *requestContext) formatTimingLog(result parallelRelayResult) string {
	return fmt.Sprintf("endpoint_%d=%dms", result.index, result.duration.Milliseconds())
}
