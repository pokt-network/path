package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/metrics"
	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/heuristic"
	"github.com/pokt-network/path/reputation"
)

// responseCheckResult contains the result of response success check.
type responseCheckResult struct {
	// Success indicates if the response should be treated as successful.
	Success bool
	// HeuristicResult contains the heuristic analysis result if payload was analyzed.
	// This is nil if no heuristic analysis was performed.
	HeuristicResult *heuristic.AnalysisResult
}

// checkResponseSuccess performs a comprehensive check to determine if a response
// should be considered successful. It combines HTTP status code checks with
// heuristic payload analysis to catch errors that might slip through with
// only HTTP status validation.
//
// This catches cases where the endpoint returns HTTP 200 but the payload
// contains a JSON-RPC error or other error indicators.
//
// Parameters:
//   - err: Any error returned from the protocol layer
//   - statusCode: HTTP status code from the endpoint
//   - responseBytes: Response payload bytes
//   - rpcType: RPC type for protocol-specific heuristic analysis
//   - logger: Logger for debugging
//
// Returns responseCheckResult with success status and optional heuristic result.
func checkResponseSuccess(
	err error,
	statusCode int,
	responseBytes []byte,
	rpcType sharedtypes.RPCType,
	logger polylog.Logger,
) responseCheckResult {
	// Protocol error - definitely not successful
	if err != nil {
		return responseCheckResult{Success: false}
	}

	// HTTP status code check (original logic)
	httpSuccess := statusCode == 0 || (statusCode >= 200 && statusCode < 300)
	if !httpSuccess {
		return responseCheckResult{Success: false}
	}

	// Heuristic payload analysis - catch errors that slip through HTTP status
	// This detects JSON-RPC errors, malformed responses, HTML error pages, empty responses, etc.
	result := heuristic.Analyze(responseBytes, statusCode, rpcType)
	if result.ShouldRetry {
		logger.Debug().
			Str("heuristic_reason", result.Reason).
			Float64("heuristic_confidence", result.Confidence).
			Str("heuristic_details", result.Details).
			Int("response_size", len(responseBytes)).
			Msg("Heuristic analysis detected error in response payload despite HTTP success status")
		return responseCheckResult{Success: false, HeuristicResult: &result}
	}

	return responseCheckResult{Success: true}
}

// recordHeuristicErrorToReputation records a correcting error signal to reputation
// when heuristic analysis detects an error that the protocol layer missed.
//
// This is needed because the protocol layer records a success signal based on HTTP 200,
// but the payload may contain a JSON-RPC error or other error indicators. This function
// records a correcting negative signal to offset the incorrect success signal.
func recordHeuristicErrorToReputation(
	ctx context.Context,
	reputationSvc reputation.ReputationService,
	serviceID protocol.ServiceID,
	endpointAddr protocol.EndpointAddr,
	rpcType sharedtypes.RPCType,
	heuristicResult *heuristic.AnalysisResult,
	logger polylog.Logger,
) {
	if reputationSvc == nil || heuristicResult == nil {
		return
	}

	keyBuilder := reputationSvc.KeyBuilderForService(serviceID)
	endpointKey := keyBuilder.BuildKey(serviceID, endpointAddr, rpcType)

	// Determine signal severity based on heuristic confidence and reason
	var signal reputation.Signal
	var metricSignal string

	// High confidence heuristic errors (JSON-RPC errors, HTML pages) are more severe
	// Note: latency=0 for heuristic errors since this is a payload analysis, not timing
	if heuristicResult.Confidence >= 0.95 {
		signal = reputation.NewMajorErrorSignal("heuristic_"+heuristicResult.Reason, 0)
		metricSignal = metrics.SignalMajorError
	} else {
		signal = reputation.NewMinorErrorSignal("heuristic_" + heuristicResult.Reason)
		metricSignal = metrics.SignalMinorError
	}

	// Fire-and-forget: don't block request on reputation recording
	if err := reputationSvc.RecordSignal(ctx, endpointKey, signal); err != nil {
		logger.Warn().Err(err).Msg("Failed to record heuristic error signal to reputation")
	}

	// Record metric for heuristic-detected errors
	domain, domainErr := shannonmetrics.ExtractDomainOrHost(string(endpointAddr))
	if domainErr != nil {
		domain = shannonmetrics.ErrDomain
	}
	rpcTypeStr := metrics.NormalizeRPCType(rpcType.String())
	// Use "heuristic_error" as status code category since HTTP was 200
	metrics.RecordRelay(domain, rpcTypeStr, string(serviceID), "heuristic_error", metricSignal, metrics.RelayTypeNormal, 0)

	logger.Debug().
		Str("endpoint", string(endpointAddr)).
		Str("service_id", string(serviceID)).
		Str("heuristic_reason", heuristicResult.Reason).
		Float64("heuristic_confidence", heuristicResult.Confidence).
		Str("reputation_signal", metricSignal).
		Msg("Recorded heuristic error signal to reputation")
}

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
// handleSingleRelayRequest handles a single relay request with optional hedge racing.
// For batch requests (multiple payloads), delegates to handleBatchRelayRequest.
func (rc *requestContext) handleSingleRelayRequest() error {
	logger := rc.logger.With("method", "handleSingleRelayRequest")

	// Get RPC type from QoS-detected payload (needed for endpoint filtering and retries)
	payloads := rc.qosCtx.GetServicePayloads()
	if len(payloads) == 0 {
		return fmt.Errorf("no payloads available from QoS context")
	}

	// For batch requests, each item should go through full retry/hedge/heuristic flow independently
	if len(payloads) > 1 {
		logger.Debug().
			Int("batch_size", len(payloads)).
			Msg("Detected batch request - routing to batch handler")
		return rc.handleBatchRelayRequest(payloads)
	}

	rpcType := payloads[0].RPCType
	batchCount := len(payloads)

	// Get retry configuration for the service
	retryConfig := rc.getRetryConfigForService()

	// Determine max attempts
	maxAttempts := 1
	if retryConfig != nil && retryConfig.Enabled != nil && *retryConfig.Enabled {
		if retryConfig.MaxRetries != nil && *retryConfig.MaxRetries > 0 {
			maxAttempts = *retryConfig.MaxRetries + 1 // +1 for the initial attempt
		}
	}

	// Check if hedge racing is enabled
	var hedgeDelay time.Duration
	var connectTimeout time.Duration
	if retryConfig != nil && retryConfig.HedgeDelay != nil && *retryConfig.HedgeDelay > 0 {
		hedgeDelay = *retryConfig.HedgeDelay
		if retryConfig.ConnectTimeout != nil {
			connectTimeout = *retryConfig.ConnectTimeout
		} else {
			connectTimeout = 500 * time.Millisecond // Default
		}
		logger.Info().
			Dur("hedge_delay", hedgeDelay).
			Dur("connect_timeout", connectTimeout).
			Msg("🏁 Hedge racing enabled for this service")
	} else {
		// Debug why hedging is not enabled
		hasRetryConfig := retryConfig != nil
		hasHedgeDelay := retryConfig != nil && retryConfig.HedgeDelay != nil
		hedgeDelayValue := time.Duration(0)
		if hasHedgeDelay {
			hedgeDelayValue = *retryConfig.HedgeDelay
		}
		logger.Debug().
			Bool("has_retry_config", hasRetryConfig).
			Bool("has_hedge_delay", hasHedgeDelay).
			Dur("hedge_delay_value", hedgeDelayValue).
			Msg("Hedge racing NOT enabled - checking why")
	}

	var lastErr error
	var lastStatusCode int
	var lastEndpointAddr protocol.EndpointAddr

	// Track endpoints already tried to ensure retry endpoint rotation
	triedEndpoints := make(map[protocol.EndpointAddr]bool)
	currentProtocolCtx := rc.protocolContexts[0]
	var currentEndpointAddr protocol.EndpointAddr

	// Track overall retry attempt start time for metrics
	retryLoopStartTime := time.Now()

	// Retry loop
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Check if context already canceled before attempting (avoids wasted work)
		select {
		case <-rc.context.Done():
			logger.Debug().
				Int("attempt", attempt).
				Msg("Request canceled before attempt started")
			return rc.context.Err()
		default:
		}

		// Log retry attempt before timing to exclude logging overhead from latency measurement
		if attempt > 1 {
			// Track retry count for response headers
			rc.retryCount++

			logger.Debug().
				Int("attempt", attempt).
				Int("max_attempts", maxAttempts).
				Err(lastErr).
				Msg("Retrying relay request")

			// CRITICAL: Retry endpoint rotation - select a NEW endpoint for retry
			// Mark the previous endpoint as tried
			triedEndpoints[currentEndpointAddr] = true

			// Get fresh endpoint list from protocol (filtered by detected RPC type)
			availableEndpoints, _, err := rc.protocol.AvailableHTTPEndpoints(
				rc.context, rc.serviceID, rpcType, rc.originalHTTPRequest)
			if err != nil {
				logger.Error().Err(err).Msg("Failed to get available endpoints for retry")
				lastErr = err
				break
			}

			// Validate we have endpoints available
			if len(availableEndpoints) == 0 {
				logger.Error().Msg("No endpoints available for retry")
				lastErr = fmt.Errorf("no endpoints available for retry")
				break
			}

			// Check if the request requires archival data and filter endpoints accordingly
			// This ensures retries also respect archival requirements
			requiresArchival := false
			if archivalChecker, ok := rc.qosCtx.(ArchivalRequirementChecker); ok {
				requiresArchival = archivalChecker.RequiresArchival()
			}
			if requiresArchival {
				// Filter available endpoints using archival-aware selection
				// This returns only archival-capable endpoints
				archivalEndpoints, archivalErr := rc.qosCtx.GetEndpointSelector().SelectMultipleWithArchival(
					availableEndpoints, uint(len(availableEndpoints)), true)
				if archivalErr == nil && len(archivalEndpoints) > 0 {
					availableEndpoints = archivalEndpoints
					logger.Debug().
						Int("archival_endpoints", len(archivalEndpoints)).
						Msg("Filtered to archival-capable endpoints for retry")
				}
			}

			// Filter out endpoints we've already tried
			filteredEndpoints := filterEndpoints(availableEndpoints, triedEndpoints)

			// If all endpoints exhausted, apply backoff and reset
			if len(filteredEndpoints) == 0 {
				logger.Warn().
					Int("num_tried", len(triedEndpoints)).
					Msg("All available endpoints tried, resetting for retry with backoff")

				// Apply exponential backoff when cycling through endpoints
				backoff := calculateRetryBackoff(attempt)
				if backoff > 0 {
					select {
					case <-rc.context.Done():
						logger.Debug().Msg("Request canceled during endpoint exhaustion backoff")
						return rc.context.Err()
					case <-time.After(backoff):
						// Continue after backoff
					}
				}

				// Reset tried endpoints to allow re-selection
				filteredEndpoints = availableEndpoints
				triedEndpoints = make(map[protocol.EndpointAddr]bool)
			}

			// Select TOP-RANKED endpoint for retry (highest reputation = best chance of success)
			newEndpointAddr := rc.selectTopRankedEndpoint(filteredEndpoints, rpcType)
			if newEndpointAddr == "" {
				logger.Error().Msg("Failed to select endpoint for retry - no endpoints available")
				lastErr = fmt.Errorf("no endpoints available for retry")
				break
			}
			currentEndpointAddr = newEndpointAddr

			// Build new protocol context for the selected endpoint
			// filterByReputation=true: Retries respect reputation filtering
			newProtocolCtx, _, err := rc.protocol.BuildHTTPRequestContextForEndpoint(
				rc.context, rc.serviceID, newEndpointAddr, rpcType, rc.originalHTTPRequest, true)
			if err != nil {
				logger.Error().Err(err).
					Str("endpoint", string(newEndpointAddr)).
					Msg("Failed to build protocol context for new endpoint")
				lastErr = err
				break
			}

			currentProtocolCtx = newProtocolCtx

			logger.Debug().
				Str("new_endpoint", string(newEndpointAddr)).
				Int("attempt", attempt).
				Int("num_tried", len(triedEndpoints)).
				Msg("🔄 Switched to new endpoint for retry")
		} else {
			// First attempt: track the initial endpoint
			if len(rc.protocolContexts) > 0 {
				// Extract endpoint address from the initial protocol context
				// We'll update this after the first request based on the response
				currentEndpointAddr = lastEndpointAddr
			}
		}

		// ========================================================================
		// HEDGE RACING: For the first attempt with hedge_delay > 0, use hedge racing
		// ========================================================================
		if attempt == 1 && hedgeDelay > 0 {
			// Get available endpoints for hedge racing
			availableEndpoints, _, err := rc.protocol.AvailableHTTPEndpoints(
				rc.context, rc.serviceID, rpcType, rc.originalHTTPRequest)
			if err != nil {
				logger.Warn().Err(err).Msg("Failed to get endpoints for hedge racing, falling back to normal request")
				// Fall through to normal request
			} else if len(availableEndpoints) > 0 {
				// Filter for archival endpoints if request requires archival data
				// This ensures hedge requests also go to archival-capable endpoints
				requiresArchival := false
				if archivalChecker, ok := rc.qosCtx.(ArchivalRequirementChecker); ok {
					requiresArchival = archivalChecker.RequiresArchival()
				}
				if requiresArchival {
					archivalEndpoints, archivalErr := rc.qosCtx.GetEndpointSelector().SelectMultipleWithArchival(
						availableEndpoints, uint(len(availableEndpoints)), true)
					if archivalErr == nil && len(archivalEndpoints) > 0 {
						availableEndpoints = archivalEndpoints
						logger.Debug().
							Int("archival_endpoints", len(archivalEndpoints)).
							Msg("Filtered to archival-capable endpoints for hedge racing")
					}
				}
				// Select primary endpoint (first one, which is already reputation-sorted)
				primaryEndpoint := availableEndpoints[0]

				// Create hedge racer and run the race
				// Pass the single payload explicitly (payloads[0]) for proper hedge request handling
				racer := newHedgeRacer(rc, logger, rpcType, hedgeDelay, connectTimeout)
				hedgeResponses, hedgeErr := racer.race(rc.context, currentProtocolCtx, primaryEndpoint, availableEndpoints, payloads[0])

				// Copy hedge result to request context for response headers
				rc.hedgeResult = racer.raceResult

				logger.Debug().
					Str("hedge_result", rc.hedgeResult).
					Bool("hedge_started", racer.hedgeStarted).
					Str("primary_endpoint", string(primaryEndpoint)).
					Str("hedge_endpoint", string(racer.hedgeEndpoint)).
					Int("suppliers_tracked", len(rc.suppliersTried)).
					Err(hedgeErr).
					Msg("🔍 Hedge race completed")

				// Process hedge race result
				if hedgeErr == nil && len(hedgeResponses) > 0 {
					// Hedge race succeeded - process the winning response
					statusCode := hedgeResponses[0].HTTPStatusCode
					endpointAddr := hedgeResponses[0].EndpointAddr
					responseBytesForHeuristic := hedgeResponses[0].Bytes

					// Check if response is actually successful (HTTP status + heuristic)
					checkResult := checkResponseSuccess(hedgeErr, statusCode, responseBytesForHeuristic, rpcType, logger)
					if checkResult.Success {
						// Success! Process the response
						for _, endpointResponse := range hedgeResponses {
							rc.qosCtx.UpdateWithResponse(endpointResponse.EndpointAddr, endpointResponse.Bytes, endpointResponse.HTTPStatusCode, endpointResponse.RequestID)
							rc.tryQueueObservation(endpointResponse.EndpointAddr, endpointResponse.Bytes, endpointResponse.HTTPStatusCode)
							if len(rc.relayMetadata) < 10 {
								rc.relayMetadata = append(rc.relayMetadata, endpointResponse.Metadata)
							}
						}

						// Record batch size metric
						totalLatency := time.Since(retryLoopStartTime).Seconds()
						metrics.RecordBatchSize(metrics.NormalizeRPCType(rpcType.String()), string(rc.serviceID), batchCount, totalLatency)

						logger.Info().
							Str("endpoint", string(endpointAddr)).
							Int("status_code", statusCode).
							Str("hedge_result", rc.hedgeResult).
							Dur("total_latency", time.Since(retryLoopStartTime)).
							Msg("✅ Hedge race completed successfully")

						return nil
					}

					// Heuristic detected error - record and continue to retry
					if checkResult.HeuristicResult != nil && endpointAddr != "" {
						recordHeuristicErrorToReputation(
							rc.context,
							rc.protocol.GetReputationService(),
							rc.serviceID,
							endpointAddr,
							rpcType,
							checkResult.HeuristicResult,
							logger,
						)
					}

					lastErr = fmt.Errorf("hedge race response failed heuristic check")
					lastStatusCode = statusCode
					lastEndpointAddr = endpointAddr
					currentEndpointAddr = endpointAddr

					logger.Warn().
						Str("endpoint", string(endpointAddr)).
						Int("status_code", statusCode).
						Str("hedge_result", rc.hedgeResult).
						Int("suppliers_tracked", len(rc.suppliersTried)).
						Int("retry_count", rc.retryCount).
						Msg("🔍 Hedge race response failed heuristic check, will retry")
				} else {
					// Hedge race failed - store error and continue to retry
					lastErr = hedgeErr
					if len(hedgeResponses) > 0 {
						lastStatusCode = hedgeResponses[0].HTTPStatusCode
						lastEndpointAddr = hedgeResponses[0].EndpointAddr
						currentEndpointAddr = hedgeResponses[0].EndpointAddr
					}

					logger.Warn().Err(hedgeErr).
						Str("hedge_result", rc.hedgeResult).
						Msg("Hedge race failed, will retry with normal path")
				}

				// Mark endpoints tried by hedge racer
				triedEndpoints[primaryEndpoint] = true
				if racer.hedgeEndpoint != "" {
					triedEndpoints[racer.hedgeEndpoint] = true
				}

				// Continue to next attempt (retry)
				continue
			}
		}

		// ========================================================================
		// NORMAL REQUEST PATH (non-hedge or retry attempts)
		// ========================================================================

		// Track the start time AFTER logging to measure actual request duration
		attemptStartTime := time.Now()

		// Send the service request payload, through the protocol context, to the selected endpoint.
		// Use currentProtocolCtx which may have been updated for retry endpoint rotation
		endpointResponses, err := currentProtocolCtx.HandleServiceRequest(rc.qosCtx.GetServicePayloads())

		// Calculate how long this attempt took
		attemptDuration := time.Since(attemptStartTime)

		// Extract status code and endpoint address from responses (if any)
		statusCode := 0
		var endpointAddr protocol.EndpointAddr
		var supplierForTracking string

		if len(endpointResponses) == 0 {
			// No response from endpoint - likely protocol or network error
			logger.Warn().
				Err(err).
				Int("attempt", attempt).
				Msg("HandleServiceRequest returned empty response - protocol or network error")
			// statusCode remains 0, endpointAddr remains empty
			// Use currentEndpointAddr for supplier tracking (extract supplier from endpoint format)
			supplierForTracking = extractSupplierFromEndpoint(currentEndpointAddr)
		} else {
			statusCode = endpointResponses[0].HTTPStatusCode
			endpointAddr = endpointResponses[0].EndpointAddr
			// Update current endpoint address for tracking (used for retry rotation)
			if attempt == 1 {
				currentEndpointAddr = endpointAddr
			}
			// Get supplier from response metadata (preferred) or extract from endpoint
			supplierForTracking = endpointResponses[0].Metadata.SupplierAddress
			if supplierForTracking == "" {
				supplierForTracking = extractSupplierFromEndpoint(endpointAddr)
			}
		}

		// Track supplier tried for response headers (up to 10) - track ALL attempts including failures
		if supplierForTracking != "" && len(rc.suppliersTried) < 10 {
			// Only add if not already tracked (avoid duplicates on retry to same supplier)
			alreadyTracked := false
			for _, s := range rc.suppliersTried {
				if s == supplierForTracking {
					alreadyTracked = true
					break
				}
			}
			if !alreadyTracked {
				rc.suppliersTried = append(rc.suppliersTried, supplierForTracking)
				logger.Debug().
					Str("supplier", supplierForTracking).
					Int("attempt", attempt).
					Int("retry_count", rc.retryCount).
					Int("status_code", statusCode).
					Str("source", "handleSingleRelayRequest").
					Int("total_suppliers_tried", len(rc.suppliersTried)).
					Msg("🔍 TRACKED supplier in suppliersTried (normal/retry path)")
			}
		}

		// Extract response bytes for heuristic analysis
		var responseBytesForHeuristic []byte
		if len(endpointResponses) > 0 {
			responseBytesForHeuristic = endpointResponses[0].Bytes
		}

		// Check if the request was successful (combines HTTP status + heuristic payload analysis)
		checkResult := checkResponseSuccess(err, statusCode, responseBytesForHeuristic, rpcType, logger)
		if checkResult.Success {
			// Log when status code 0 is treated as success (investigate if this is expected behavior)
			if statusCode == 0 && err == nil {
				responseBytes := len(responseBytesForHeuristic)
				logger.Warn().
					Str("endpoint", string(endpointAddr)).
					Int("response_count", len(endpointResponses)).
					Int("response_bytes", responseBytes).
					Msg("STATUS_CODE_0: Successful request with status code 0 - protocol-level success?")
			}

			// Success! Process the response
			// TODO_TECHDEBT(@adshmh): Ensure the protocol returns exactly one response per service payload:
			// - Define a struct to contain each service payload and its corresponding response.
			// - protocol should return this new struct to clarify mapping of service payloads and the corresponding endpoint response.
			// - QoS packages should use this new struct to prepare the user response.
			// - Remove the individual endpoint response handling from the gateway package.
			//
			for _, endpointResponse := range endpointResponses {
				rc.qosCtx.UpdateWithResponse(endpointResponse.EndpointAddr, endpointResponse.Bytes, endpointResponse.HTTPStatusCode, endpointResponse.RequestID)

				// Queue observation for async parsing (sampled, non-blocking)
				rc.tryQueueObservation(endpointResponse.EndpointAddr, endpointResponse.Bytes, endpointResponse.HTTPStatusCode)

				// Capture relay metadata for response headers (sample up to 10 for batched relays)
				if len(rc.relayMetadata) < 10 {
					rc.relayMetadata = append(rc.relayMetadata, endpointResponse.Metadata)
				}
			}

			if attempt > 1 {
				logger.Debug().
					Int("attempt", attempt).
					Msg("Relay request succeeded after retry")

				// Record retry result metric (success after retries)
				totalLatency := time.Since(retryLoopStartTime).Seconds()
				metrics.RecordRetryResult(metrics.NormalizeRPCType(rpcType.String()), string(rc.serviceID), strconv.Itoa(attempt-1), metrics.RetryResultSuccess, totalLatency)
			}

			// Record batch size metric
			totalLatency := time.Since(retryLoopStartTime).Seconds()
			metrics.RecordBatchSize(metrics.NormalizeRPCType(rpcType.String()), string(rc.serviceID), batchCount, totalLatency)

			return nil
		}

		// If heuristic detected an error, record a correcting signal to reputation.
		// This is needed because the protocol layer recorded a success signal (HTTP 200),
		// but the payload contains an error.
		if checkResult.HeuristicResult != nil && endpointAddr != "" {
			recordHeuristicErrorToReputation(
				rc.context,
				rc.protocol.GetReputationService(),
				rc.serviceID,
				endpointAddr,
				rpcType,
				checkResult.HeuristicResult,
				logger,
			)
		}

		// Store the last error, status code, and endpoint for potential retry decision
		lastErr = err
		lastStatusCode = statusCode
		lastEndpointAddr = endpointAddr

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
			rc.qosCtx.UpdateWithResponse(endpointResponse.EndpointAddr, endpointResponse.Bytes, endpointResponse.HTTPStatusCode, endpointResponse.RequestID)
			rc.tryQueueObservation(endpointResponse.EndpointAddr, endpointResponse.Bytes, endpointResponse.HTTPStatusCode)
		}

		// Check if we should retry
		if attempt < maxAttempts {
			// Use currentEndpointAddr for metrics if endpointAddr is empty (protocol error with no response)
			endpointForMetrics := string(endpointAddr)
			if endpointForMetrics == "" {
				endpointForMetrics = string(currentEndpointAddr)
			}

			if !rc.shouldRetry(err, statusCode, attemptDuration, retryConfig, endpointForMetrics, checkResult.HeuristicResult) {
				logger.Debug().
					Int("attempt", attempt).
					Int("status_code", statusCode).
					Dur("attempt_duration_ms", attemptDuration).
					Msg("Request failed but retry conditions not met, stopping retries")
				break
			}

			// Small delay before retry to avoid hammering the endpoint immediately
			select {
			case <-rc.context.Done():
				logger.Debug().Msg("Request canceled during retry delay")
				return rc.context.Err()
			case <-time.After(100 * time.Millisecond):
				// Continue to next attempt
			}
		}
	}

	// All retries exhausted or conditions not met
	totalLatency := time.Since(retryLoopStartTime).Seconds()

	// Record batch size metric (even on failure)
	metrics.RecordBatchSize(metrics.NormalizeRPCType(rpcType.String()), string(rc.serviceID), batchCount, totalLatency)

	// Record retry result metric (failure) if retries were actually attempted
	if maxAttempts > 1 {
		metrics.RecordRetryResult(metrics.NormalizeRPCType(rpcType.String()), string(rc.serviceID), strconv.Itoa(maxAttempts-1), metrics.RetryResultFailure, totalLatency)
	}

	if lastErr != nil {
		logger.Error().Err(lastErr).
			Int("max_attempts", maxAttempts).
			Msg("Failed to send relay request after all retry attempts")
		// Set the protocol error on QoS context for more informative client error messages
		rc.qosCtx.SetProtocolError(lastErr)
		return lastErr
	}

	// Return error for non-success status code
	logger.Error().
		Int("status_code", lastStatusCode).
		Int("max_attempts", maxAttempts).
		Msg("Relay request failed with non-success status code after all retry attempts")
	statusErr := fmt.Errorf("relay request failed with status code %d after %d attempts", lastStatusCode, maxAttempts)
	// Set the protocol error on QoS context for more informative client error messages
	rc.qosCtx.SetProtocolError(statusErr)
	return statusErr
}

// batchPayloadResult holds the result of processing a single payload in a batch.
type batchPayloadResult struct {
	index    int
	response protocol.Response
	err      error
}

// handleBatchRelayRequest handles batch JSON-RPC requests by processing each payload
// independently through the full retry/hedge/heuristic flow.
// This ensures each batch item gets proper endpoint selection, retry, and error handling.
func (rc *requestContext) handleBatchRelayRequest(payloads []protocol.Payload) error {
	logger := rc.logger.With("method", "handleBatchRelayRequest").With("batch_size", len(payloads))
	logger.Info().Msg("Processing batch request with independent flows per item")

	startTime := time.Now()
	rpcType := payloads[0].RPCType

	// Process each payload in parallel
	resultChan := make(chan batchPayloadResult, len(payloads))
	var wg sync.WaitGroup

	for i, payload := range payloads {
		// Debug: Log each payload before processing
		logger.Debug().
			Int("payload_index", i).
			Str("payload_data_preview", func() string {
				if len(payload.Data) > 100 {
					return payload.Data[:100] + "..."
				}
				return payload.Data
			}()).
			Msg("Processing batch payload")

		wg.Add(1)
		go func(index int, p protocol.Payload) {
			defer wg.Done()
			response, err := rc.processSinglePayloadWithRetry(p, index, rpcType, logger)
			resultChan <- batchPayloadResult{
				index:    index,
				response: response,
				err:      err,
			}
		}(i, payload)
	}

	// Wait for all goroutines to complete and close channel
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results in order
	results := make([]batchPayloadResult, len(payloads))
	var hasError bool
	for result := range resultChan {
		results[result.index] = result
		if result.err != nil {
			hasError = true
		}
	}

	// Update QoS context with all responses
	for _, result := range results {
		if result.response.Bytes != nil || result.err != nil {
			rc.qosCtx.UpdateWithResponse(
				result.response.EndpointAddr,
				result.response.Bytes,
				result.response.HTTPStatusCode,
				result.response.RequestID,
			)
		}
	}

	// Record batch metrics
	totalLatency := time.Since(startTime).Seconds()
	metrics.RecordBatchSize(metrics.NormalizeRPCType(rpcType.String()), string(rc.serviceID), len(payloads), totalLatency)

	// Count successes and failures for logging
	successCount := 0
	for _, result := range results {
		if result.err == nil && result.response.HTTPStatusCode >= 200 && result.response.HTTPStatusCode < 300 {
			successCount++
		}
	}

	logger.Info().
		Int("batch_size", len(payloads)).
		Int("successes", successCount).
		Int("failures", len(payloads)-successCount).
		Dur("total_duration", time.Since(startTime)).
		Msg("Batch request processing completed")

	// Return nil - individual errors are captured in responses
	// The QoS layer will build appropriate error responses for failed items
	if hasError {
		logger.Warn().Msg("Some batch items failed - errors captured in individual responses")
	}
	return nil
}

// processSinglePayloadWithRetry handles a single payload with full retry/hedge/heuristic flow.
// This is the core logic extracted for batch processing.
func (rc *requestContext) processSinglePayloadWithRetry(
	payload protocol.Payload,
	index int,
	rpcType sharedtypes.RPCType,
	parentLogger polylog.Logger,
) (protocol.Response, error) {
	logger := parentLogger.With("payload_index", index)

	// Get retry configuration
	retryConfig := rc.getRetryConfigForService()
	maxAttempts := 1
	if retryConfig != nil && retryConfig.Enabled != nil && *retryConfig.Enabled {
		if retryConfig.MaxRetries != nil && *retryConfig.MaxRetries > 0 {
			maxAttempts = *retryConfig.MaxRetries + 1
		}
	}

	// Check if hedge racing is enabled
	var hedgeDelay time.Duration
	var connectTimeout time.Duration
	if retryConfig != nil && retryConfig.HedgeDelay != nil && *retryConfig.HedgeDelay > 0 {
		hedgeDelay = *retryConfig.HedgeDelay
		if retryConfig.ConnectTimeout != nil {
			connectTimeout = *retryConfig.ConnectTimeout
		} else {
			connectTimeout = 500 * time.Millisecond
		}
	}

	var lastErr error
	var lastResponse protocol.Response
	triedEndpoints := make(map[protocol.EndpointAddr]bool)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Get available endpoints
		availableEndpoints, _, err := rc.protocol.AvailableHTTPEndpoints(
			rc.context, rc.serviceID, rpcType, rc.originalHTTPRequest)
		if err != nil || len(availableEndpoints) == 0 {
			lastErr = fmt.Errorf("no endpoints available for batch item %d", index)
			logger.Warn().Err(err).Int("attempt", attempt).Msg("No endpoints available")
			continue
		}

		// Filter for archival endpoints if request requires archival data
		requiresArchival := false
		if archivalChecker, ok := rc.qosCtx.(ArchivalRequirementChecker); ok {
			requiresArchival = archivalChecker.RequiresArchival()
		}
		if requiresArchival {
			archivalEndpoints, archivalErr := rc.qosCtx.GetEndpointSelector().SelectMultipleWithArchival(
				availableEndpoints, uint(len(availableEndpoints)), true)
			if archivalErr == nil && len(archivalEndpoints) > 0 {
				availableEndpoints = archivalEndpoints
				logger.Debug().
					Int("archival_endpoints", len(archivalEndpoints)).
					Msg("Filtered to archival-capable endpoints for batch item")
			}
		}

		// Filter out already tried endpoints for retries
		var filteredEndpoints []protocol.EndpointAddr
		for _, ep := range availableEndpoints {
			if !triedEndpoints[ep] {
				filteredEndpoints = append(filteredEndpoints, ep)
			}
		}
		if len(filteredEndpoints) == 0 {
			// All endpoints tried, use original list
			filteredEndpoints = availableEndpoints
		}

		// Select endpoint
		selectedEndpoint := rc.selectTopRankedEndpoint(filteredEndpoints, rpcType)
		if selectedEndpoint == "" {
			selectedEndpoint = filteredEndpoints[0]
		}
		triedEndpoints[selectedEndpoint] = true

		// Build protocol context for this endpoint
		protocolCtx, _, err := rc.protocol.BuildHTTPRequestContextForEndpoint(
			rc.context, rc.serviceID, selectedEndpoint, rpcType, rc.originalHTTPRequest, true)
		if err != nil {
			lastErr = err
			logger.Warn().Err(err).Str("endpoint", string(selectedEndpoint)).Msg("Failed to build protocol context")
			continue
		}

		// Hedge racing on first attempt if enabled
		if attempt == 1 && hedgeDelay > 0 && len(filteredEndpoints) > 1 {
			racer := newHedgeRacer(rc, logger, rpcType, hedgeDelay, connectTimeout)
			hedgeResponses, hedgeErr := racer.race(rc.context, protocolCtx, selectedEndpoint, filteredEndpoints, payload)

			if hedgeErr == nil && len(hedgeResponses) > 0 {
				resp := hedgeResponses[0]
				checkResult := checkResponseSuccess(nil, resp.HTTPStatusCode, resp.Bytes, rpcType, logger)
				if checkResult.Success {
					// Extract request ID from payload
					resp.RequestID = extractRequestIDFromPayload(payload)
					logger.Debug().
						Str("endpoint", string(resp.EndpointAddr)).
						Int("status", resp.HTTPStatusCode).
						Msg("Batch item succeeded via hedge racing")
					return resp, nil
				}
				// Hedge failed heuristic check, continue to retry
				lastResponse = resp
				lastErr = fmt.Errorf("hedge response failed heuristic check")
				if checkResult.HeuristicResult != nil {
					recordHeuristicErrorToReputation(rc.context, rc.protocol.GetReputationService(),
						rc.serviceID, resp.EndpointAddr, rpcType, checkResult.HeuristicResult, logger)
				}
				// Mark hedge endpoint as tried
				if racer.hedgeEndpoint != "" {
					triedEndpoints[racer.hedgeEndpoint] = true
				}
				continue
			}
			// Hedge failed, fall through to normal request
		}

		// Normal request path - send single payload
		responses, err := protocolCtx.HandleServiceRequest([]protocol.Payload{payload})
		if err != nil || len(responses) == 0 {
			lastErr = err
			if lastErr == nil {
				lastErr = fmt.Errorf("empty response for batch item %d", index)
			}
			// Capture the failed response to ensure RequestID is available for error handling
			if len(responses) > 0 {
				lastResponse = responses[0]
				lastResponse.RequestID = extractRequestIDFromPayload(payload)
			}
			logger.Warn().Err(lastErr).Int("attempt", attempt).Msg("Request failed")
			continue
		}

		resp := responses[0]
		resp.RequestID = extractRequestIDFromPayload(payload)

		// Check response success with heuristic
		checkResult := checkResponseSuccess(nil, resp.HTTPStatusCode, resp.Bytes, rpcType, logger)
		if checkResult.Success {
			logger.Debug().
				Str("endpoint", string(resp.EndpointAddr)).
				Int("status", resp.HTTPStatusCode).
				Int("attempt", attempt).
				Msg("Batch item succeeded")
			return resp, nil
		}

		// Failed heuristic check
		lastResponse = resp
		lastErr = fmt.Errorf("response failed heuristic check")
		if checkResult.HeuristicResult != nil {
			recordHeuristicErrorToReputation(rc.context, rc.protocol.GetReputationService(),
				rc.serviceID, resp.EndpointAddr, rpcType, checkResult.HeuristicResult, logger)
		}
	}

	// All attempts failed - return last response with error info
	logger.Warn().
		Err(lastErr).
		Int("max_attempts", maxAttempts).
		Msg("Batch item failed after all retry attempts")

	// Ensure we have a response with the request ID even on failure
	if lastResponse.RequestID == "" {
		lastResponse.RequestID = extractRequestIDFromPayload(payload)
	}

	return lastResponse, lastErr
}

// extractRequestIDFromPayload extracts the JSON-RPC request ID from a payload.
func extractRequestIDFromPayload(payload protocol.Payload) string {
	if payload.Data == "" {
		return ""
	}
	var req struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal([]byte(payload.Data), &req); err != nil {
		return ""
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		return ""
	}
	// Try integer
	var intID int
	if err := json.Unmarshal(req.ID, &intID); err == nil {
		return strconv.Itoa(intID)
	}
	// Try string
	var strID string
	if err := json.Unmarshal(req.ID, &strID); err == nil {
		return strID
	}
	return ""
}

// TODO_TECHDEBT(@adshmh): Remove this method:
// Parallel requests are an internal detail of a protocol integration package
// As of PR #388, `protocol/shannon` is the only protocol integration package.
//
// handleParallelRelayRequests orchestrates parallel relay requests and returns the first successful response.
func (rc *requestContext) handleParallelRelayRequests() error {
	parallelMetrics := &parallelRequestMetrics{
		numRequestsToAttempt: len(rc.protocolContexts),
		overallStartTime:     time.Now(),
	}
	defer rc.updateParallelRequestMetrics(parallelMetrics)

	logger := rc.logger.
		With("method", "handleParallelRelayRequests").
		With("num_protocol_contexts", len(rc.protocolContexts)).
		With("service_id", rc.serviceID)
	logger.Debug().Msg("Starting parallel relay race")

	// Get RPC type from QoS-detected payload (needed for endpoint filtering and retries)
	payloads := rc.qosCtx.GetServicePayloads()
	if len(payloads) == 0 {
		return fmt.Errorf("no payloads available from QoS context")
	}
	rpcType := payloads[0].RPCType
	batchCount := len(payloads)

	// Record batch size metric on completion (deferred)
	defer func() {
		totalLatency := time.Since(parallelMetrics.overallStartTime).Seconds()
		metrics.RecordBatchSize(metrics.NormalizeRPCType(rpcType.String()), string(rc.serviceID), batchCount, totalLatency)
	}()

	// TODO_TECHDEBT: Make sure timed out parallel requests are also sanctioned.
	ctx, cancel := context.WithTimeout(rc.context, DefaultRelayRequestTimeout)
	defer cancel()

	resultChan, qosContextMutex := rc.launchParallelRequests(ctx, logger, rpcType)

	return rc.waitForFirstSuccessfulResponse(ctx, logger, resultChan, parallelMetrics, qosContextMutex, rpcType)
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
func (rc *requestContext) launchParallelRequests(ctx context.Context, logger polylog.Logger, rpcType sharedtypes.RPCType) (<-chan parallelRelayResult, *sync.Mutex) {
	resultChan := make(chan parallelRelayResult, len(rc.protocolContexts))

	// Ensures thread-safety of QoS context operations.
	qosContextMutex := &sync.Mutex{}

	for protocolCtxIdx, protocolCtx := range rc.protocolContexts {
		go rc.executeOneOfParallelRequests(ctx, logger, protocolCtx, protocolCtxIdx, resultChan, qosContextMutex, rpcType)
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
	rpcType sharedtypes.RPCType,
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
	var lastEndpointAddr protocol.EndpointAddr

	// Track endpoints already tried to ensure retry endpoint rotation
	triedEndpoints := make(map[protocol.EndpointAddr]bool)
	currentProtocolCtx := protocolCtx
	var currentEndpointAddr protocol.EndpointAddr

	// Retry loop
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Check if context was canceled before attempting
		select {
		case <-ctx.Done():
			logger.Debug().Msgf("Request to endpoint %d canceled before attempt %d", index, attempt)
			return
		default:
		}

		// Log retry attempt before timing to exclude logging overhead from latency measurement
		if attempt > 1 {
			logger.Debug().
				Int("endpoint_index", index).
				Int("attempt", attempt).
				Int("max_attempts", maxAttempts).
				Err(lastErr).
				Msg("Retrying parallel relay request")

			// CRITICAL: Retry endpoint rotation - select a NEW endpoint for retry
			// Mark the previous endpoint as tried
			triedEndpoints[currentEndpointAddr] = true

			// Get fresh endpoint list from protocol (filtered by detected RPC type)
			availableEndpoints, _, err := rc.protocol.AvailableHTTPEndpoints(
				rc.context, rc.serviceID, rpcType, rc.originalHTTPRequest)
			if err != nil {
				logger.Error().Err(err).Int("endpoint_index", index).
					Msg("Failed to get available endpoints for retry in parallel path")
				return
			}

			// Validate we have endpoints available
			if len(availableEndpoints) == 0 {
				logger.Error().Int("endpoint_index", index).
					Msg("No endpoints available for retry in parallel path")
				return
			}

			// Check if the request requires archival data and filter endpoints accordingly
			// This ensures retries also respect archival requirements
			requiresArchival := false
			if archivalChecker, ok := rc.qosCtx.(ArchivalRequirementChecker); ok {
				requiresArchival = archivalChecker.RequiresArchival()
			}
			if requiresArchival {
				// Filter available endpoints using archival-aware selection
				archivalEndpoints, archivalErr := rc.qosCtx.GetEndpointSelector().SelectMultipleWithArchival(
					availableEndpoints, uint(len(availableEndpoints)), true)
				if archivalErr == nil && len(archivalEndpoints) > 0 {
					availableEndpoints = archivalEndpoints
					logger.Debug().
						Int("endpoint_index", index).
						Int("archival_endpoints", len(archivalEndpoints)).
						Msg("Filtered to archival-capable endpoints for parallel retry")
				}
			}

			// Filter out endpoints we've already tried
			filteredEndpoints := filterEndpoints(availableEndpoints, triedEndpoints)

			// If all endpoints exhausted, apply backoff and reset
			if len(filteredEndpoints) == 0 {
				logger.Warn().
					Int("endpoint_index", index).
					Int("num_tried", len(triedEndpoints)).
					Msg("All available endpoints tried in parallel path, resetting with backoff")

				// Apply exponential backoff when cycling through endpoints
				backoff := calculateRetryBackoff(attempt)
				if backoff > 0 {
					select {
					case <-ctx.Done():
						logger.Debug().Msgf("Endpoint %d canceled during backoff", index)
						return
					case <-time.After(backoff):
						// Continue after backoff
					}
				}

				// Reset tried endpoints to allow re-selection
				filteredEndpoints = availableEndpoints
				triedEndpoints = make(map[protocol.EndpointAddr]bool)
			}

			// Select TOP-RANKED endpoint for retry (highest reputation = best chance of success)
			newEndpointAddr := rc.selectTopRankedEndpoint(filteredEndpoints, rpcType)
			if newEndpointAddr == "" {
				logger.Error().Int("endpoint_index", index).
					Msg("Failed to select endpoint for retry in parallel path - no endpoints available")
				return
			}
			currentEndpointAddr = newEndpointAddr

			// Build new protocol context for the selected endpoint
			// filterByReputation=true: Retries respect reputation filtering
			newProtocolCtx, _, err := rc.protocol.BuildHTTPRequestContextForEndpoint(
				rc.context, rc.serviceID, newEndpointAddr, rpcType, rc.originalHTTPRequest, true)
			if err != nil {
				logger.Error().Err(err).
					Int("endpoint_index", index).
					Str("endpoint", string(newEndpointAddr)).
					Msg("Failed to build protocol context for new endpoint in parallel path")
				return
			}

			currentProtocolCtx = newProtocolCtx

			logger.Debug().
				Int("endpoint_index", index).
				Str("new_endpoint", string(newEndpointAddr)).
				Int("attempt", attempt).
				Int("num_tried", len(triedEndpoints)).
				Msg("🔄 Switched to new endpoint for retry in parallel path")
		} else {
			// First attempt: track the initial endpoint
			currentEndpointAddr = lastEndpointAddr
		}

		// Track the start time AFTER logging to measure actual request duration
		attemptStartTime := time.Now()

		responses, err := currentProtocolCtx.HandleServiceRequest(rc.qosCtx.GetServicePayloads())

		// Calculate how long this attempt took
		attemptDuration := time.Since(attemptStartTime)

		// Extract status code and endpoint address from responses (if any)
		statusCode := 0
		var endpointAddr protocol.EndpointAddr
		if len(responses) == 0 {
			// No response from endpoint - likely protocol or network error
			logger.Warn().
				Err(err).
				Int("endpoint_index", index).
				Int("attempt", attempt).
				Msg("HandleServiceRequest returned empty response in parallel path - protocol or network error")
			// statusCode remains 0, endpointAddr remains empty
		} else {
			statusCode = responses[0].HTTPStatusCode
			endpointAddr = responses[0].EndpointAddr
			// Update current endpoint address for tracking (used for retry rotation)
			if attempt == 1 {
				currentEndpointAddr = endpointAddr
			}
		}

		// Extract response bytes for heuristic analysis
		var responseBytesForHeuristic []byte
		if len(responses) > 0 {
			responseBytesForHeuristic = responses[0].Bytes
		}

		// Check if the request was successful (combines HTTP status + heuristic payload analysis)
		checkResult := checkResponseSuccess(err, statusCode, responseBytesForHeuristic, rpcType, logger)
		if checkResult.Success {
			// Log when status code 0 is treated as success (investigate if this is expected behavior)
			if statusCode == 0 && err == nil {
				responseBytes := len(responseBytesForHeuristic)
				logger.Warn().
					Str("endpoint", string(endpointAddr)).
					Int("endpoint_index", index).
					Int("response_count", len(responses)).
					Int("response_bytes", responseBytes).
					Msg("STATUS_CODE_0: Successful request with status code 0 in parallel path - protocol-level success?")
			}

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
				logger.Debug().
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

		// If heuristic detected an error, record a correcting signal to reputation.
		// This is needed because the protocol layer recorded a success signal (HTTP 200),
		// but the payload contains an error.
		if checkResult.HeuristicResult != nil && endpointAddr != "" {
			recordHeuristicErrorToReputation(
				ctx,
				rc.protocol.GetReputationService(),
				rc.serviceID,
				endpointAddr,
				rpcType,
				checkResult.HeuristicResult,
				logger,
			)
		}

		// Store the last error, responses, and endpoint for potential retry decision
		lastErr = err
		lastResponses = responses
		lastEndpointAddr = endpointAddr

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
			rc.qosCtx.UpdateWithResponse(response.EndpointAddr, response.Bytes, response.HTTPStatusCode, response.RequestID)
			rc.tryQueueObservation(response.EndpointAddr, response.Bytes, response.HTTPStatusCode)
		}
		qosContextMutex.Unlock()

		// Check if we should retry
		if attempt < maxAttempts {
			// Only check shouldRetry if we have endpoint info
			if endpointAddr == "" {
				logger.Debug().
					Int("endpoint_index", index).
					Int("attempt", attempt).
					Msg("Skipping retry check - no endpoint address available")
				break
			}

			if !rc.shouldRetry(err, statusCode, attemptDuration, retryConfig, string(endpointAddr), checkResult.HeuristicResult) {
				logger.Debug().
					Int("endpoint_index", index).
					Int("attempt", attempt).
					Int("status_code", statusCode).
					Dur("attempt_duration_ms", attemptDuration).
					Msg("Request failed but retry conditions not met, stopping retries")
				break
			}

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
			rc.qosCtx.UpdateWithResponse(response.EndpointAddr, response.Bytes, response.HTTPStatusCode, response.RequestID)

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
	rpcType sharedtypes.RPCType,
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
		logger.Debug().
			Msgf("Parallel request success: endpoint %d/%d responded in %dms",
				result.index+1, metrics.numRequestsToAttempt, overallDuration.Milliseconds())

		rc.qosCtx.UpdateWithResponse(response.EndpointAddr, response.Bytes, response.HTTPStatusCode, response.RequestID)

		// Queue observation for async parsing (sampled, non-blocking)
		rc.tryQueueObservation(response.EndpointAddr, response.Bytes, response.HTTPStatusCode)

		// Capture relay metadata for response headers (sample up to 10 for batched relays)
		if len(rc.relayMetadata) < 10 {
			rc.relayMetadata = append(rc.relayMetadata, response.Metadata)
		}

		// Track supplier tried for response headers (up to 10)
		if len(rc.suppliersTried) < 10 {
			supplierAddr := response.Metadata.SupplierAddress
			if supplierAddr != "" {
				// Only add if not already tracked (avoid duplicates)
				alreadyTracked := false
				for _, s := range rc.suppliersTried {
					if s == supplierAddr {
						alreadyTracked = true
						break
					}
				}
				if !alreadyTracked {
					rc.suppliersTried = append(rc.suppliersTried, supplierAddr)
					logger.Debug().
						Str("supplier", supplierAddr).
						Int("result_index", result.index).
						Str("source", "handleSuccessfulResponse-parallel").
						Int("total_suppliers_tried", len(rc.suppliersTried)).
						Msg("🔍 TRACKED supplier in suppliersTried (parallel path)")
				}
			}
		}
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

	// Determine cancellation reason for better observability
	if ctx.Err() == context.DeadlineExceeded {
		logger.Error().
			Str("cancellation_reason", "timeout").
			Int64("duration_ms", totalDuration).
			Int("completed_requests", metrics.numCompletedSuccessfully).
			Msg("Parallel requests timed out (DeadlineExceeded)")
		err := fmt.Errorf("parallel relay requests timed out after %dms and %d completed requests, last error: %w",
			totalDuration, metrics.numCompletedSuccessfully, lastErr)
		rc.qosCtx.SetProtocolError(err)
		return err
	} else if ctx.Err() == context.Canceled {
		logger.Debug().
			Str("cancellation_reason", "client_canceled").
			Int64("duration_ms", totalDuration).
			Int("completed_requests", metrics.numCompletedSuccessfully).
			Msg("Parallel requests canceled by client (context.Canceled)")
		err := fmt.Errorf("parallel relay requests canceled by client after %dms and %d completed requests, last error: %w",
			totalDuration, metrics.numCompletedSuccessfully, lastErr)
		rc.qosCtx.SetProtocolError(err)
		return err
	}

	// Unknown cancellation reason
	logger.Warn().
		Str("cancellation_reason", "unknown").
		Err(ctx.Err()).
		Int64("duration_ms", totalDuration).
		Int("completed_requests", metrics.numCompletedSuccessfully).
		Msg("Parallel requests canceled with unknown reason")
	err := fmt.Errorf("parallel relay requests canceled (unknown) after %dms and %d completed requests, last error: %w",
		totalDuration, metrics.numCompletedSuccessfully, lastErr)
	rc.qosCtx.SetProtocolError(err)
	return err
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

	err := fmt.Errorf("all parallel relay requests failed, last error: %w", lastErr)
	// Set the protocol error on QoS context for more informative client error messages
	rc.qosCtx.SetProtocolError(err)
	return err
}

// formatTimingLog creates a timing log string for a relay result
func (rc *requestContext) formatTimingLog(result parallelRelayResult) string {
	return fmt.Sprintf("endpoint_%d=%dms", result.index, result.duration.Milliseconds())
}

// filterEndpoints removes tried endpoints from the available list.
// Used during retry endpoint rotation to ensure we never retry the same endpoint.
func filterEndpoints(available protocol.EndpointAddrList, tried map[protocol.EndpointAddr]bool) protocol.EndpointAddrList {
	filtered := make(protocol.EndpointAddrList, 0, len(available))
	for _, ep := range available {
		if !tried[ep] {
			filtered = append(filtered, ep)
		}
	}
	return filtered
}

// calculateRetryBackoff returns the backoff duration for a retry attempt.
// Uses a simple stepped backoff strategy: 100ms, 200ms, then 400ms for all subsequent attempts.
func calculateRetryBackoff(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 0 // No backoff for first attempt
	case 2:
		return 100 * time.Millisecond
	case 3:
		return 200 * time.Millisecond
	default:
		return 400 * time.Millisecond
	}
}

// selectTopRankedEndpoint selects the highest-reputation endpoint for retry.
// This maximizes the chance of getting a successful response on retry by
// prioritizing the best-performing endpoints based on their reputation scores.
//
// Falls back to the first endpoint in the list if reputation service is unavailable.
func (rc *requestContext) selectTopRankedEndpoint(
	endpoints protocol.EndpointAddrList,
	rpcType sharedtypes.RPCType,
) protocol.EndpointAddr {
	if len(endpoints) == 0 {
		return ""
	}

	// If only one endpoint, return it directly
	if len(endpoints) == 1 {
		return endpoints[0]
	}

	// Get reputation service
	reputationSvc := rc.protocol.GetReputationService()
	if reputationSvc == nil {
		// No reputation service - fall back to first endpoint
		return endpoints[0]
	}

	// Build endpoint keys for ranking and map keys back to original endpoints
	// This is needed because with per-supplier or per-domain granularity,
	// the key's EndpointAddr may differ from the original endpoint address.
	keys := make([]reputation.EndpointKey, len(endpoints))
	keyToOriginalEndpoint := make(map[protocol.EndpointAddr]protocol.EndpointAddr)
	keyBuilder := reputationSvc.KeyBuilderForService(rc.serviceID)
	for i, ep := range endpoints {
		key := keyBuilder.BuildKey(rc.serviceID, ep, rpcType)
		keys[i] = key
		// Map the key's EndpointAddr to the original endpoint
		// If multiple endpoints map to the same key, keep the first one
		if _, exists := keyToOriginalEndpoint[key.EndpointAddr]; !exists {
			keyToOriginalEndpoint[key.EndpointAddr] = ep
		}
	}

	// Rank endpoints by score (highest first)
	rankedKeys, err := reputationSvc.RankEndpointsByScore(rc.context, keys)
	if err != nil || len(rankedKeys) == 0 {
		// Error ranking - fall back to first endpoint
		return endpoints[0]
	}

	// Return the original endpoint address corresponding to the top-ranked key
	topKey := rankedKeys[0]
	originalEndpoint, ok := keyToOriginalEndpoint[topKey.EndpointAddr]
	if !ok {
		// Shouldn't happen, but fall back to first endpoint if mapping fails
		rc.logger.Warn().
			Str("top_key", string(topKey.EndpointAddr)).
			Msg("Failed to map top-ranked key back to original endpoint, falling back")
		return endpoints[0]
	}

	rc.logger.Debug().
		Str("top_endpoint", string(originalEndpoint)).
		Str("reputation_key", string(topKey.EndpointAddr)).
		Int("num_candidates", len(endpoints)).
		Msg("🏆 Selected TOP-RANKED endpoint for retry (highest reputation)")

	return originalEndpoint
}

// extractSupplierFromEndpoint extracts the supplier address from an endpoint address.
// Endpoint format is typically: "<supplier_address>-<url>"
// e.g., "pokt1abc123-https://node.example.com" -> "pokt1abc123"
// Returns empty string if the format is unexpected.
func extractSupplierFromEndpoint(endpoint protocol.EndpointAddr) string {
	if endpoint == "" {
		return ""
	}

	endpointStr := string(endpoint)

	// Find the first dash that separates supplier from URL
	// The supplier address starts with "pokt1" and the URL typically starts with "http"
	dashIdx := strings.Index(endpointStr, "-http")
	if dashIdx > 0 {
		return endpointStr[:dashIdx]
	}

	// Fallback: try to find any dash and take the first part
	dashIdx = strings.Index(endpointStr, "-")
	if dashIdx > 0 {
		return endpointStr[:dashIdx]
	}

	return ""
}
