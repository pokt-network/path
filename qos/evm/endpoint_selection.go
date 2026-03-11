package evm

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/selector"
	"github.com/pokt-network/path/reputation"
)

var (
	errEmptyResponseObs         = errors.New("endpoint is invalid: history of empty responses")
	errRecentInvalidResponseObs = errors.New("endpoint is invalid: recent invalid response")
	errEmptyEndpointListObs     = errors.New("received empty list of endpoints to select from")
)

// TODO_UPNEXT(@adshmh): make the invalid response timeout duration configurable
// It is set to 5 minutes because that is the session time as of #321.
const invalidResponseTimeout = 5 * time.Minute

// EndpointSelectionResult contains endpoint selection results and metadata.
type EndpointSelectionResult struct {
	// SelectedEndpoint is the chosen endpoint address
	SelectedEndpoint protocol.EndpointAddr
	// Metadata contains endpoint selection process metadata
	Metadata EndpointSelectionMetadata
}

// EndpointSelectionMetadata contains metadata about the endpoint selection process.
type EndpointSelectionMetadata struct {
	// RandomEndpointFallback indicates random endpoint selection when all endpoints failed validation
	RandomEndpointFallback bool
	// ValidationResults contains detailed information about each validation attempt (both successful and failed)
	ValidationResults []*qosobservations.EndpointValidationResult
}

// SelectMultiple returns multiple endpoint addresses from the list of available endpoints.
// Available endpoints are filtered based on their validity first.
// Endpoints are selected with TLD diversity preference when possible.
// If numEndpoints is 0, it defaults to 1. If numEndpoints is greater than available endpoints, it returns all valid endpoints.
// Note: SelectMultiple does not support archival filtering - it's used for hedge racing where we want all valid endpoints.
func (ss *serviceState) SelectMultiple(availableEndpoints protocol.EndpointAddrList, numEndpoints uint, requestID string) (protocol.EndpointAddrList, error) {
	return ss.SelectMultipleWithArchival(availableEndpoints, numEndpoints, false, requestID)
}

// SelectMultipleWithArchival returns multiple endpoint addresses with optional archival filtering.
// When requiresArchival is true, only endpoints that have passed archival capability checks are considered.
// When requiresArchival is false, all valid endpoints are considered (same behavior as SelectMultiple).
// This enables archival requests to be routed only to archival-capable endpoints.
func (ss *serviceState) SelectMultipleWithArchival(availableEndpoints protocol.EndpointAddrList, numEndpoints uint, requiresArchival bool, requestID string) (protocol.EndpointAddrList, error) {
	logger := ss.logger.With("method", "SelectMultipleWithArchival").
		With("chain_id", ss.serviceQoSConfig.getEVMChainID()).
		With("service_id", ss.serviceQoSConfig.GetServiceID()).
		With("num_endpoints", numEndpoints).
		With("requires_archival", requiresArchival).
		With("request_id", requestID)

	// CODE_PATH: Entry point for endpoint selection
	if requiresArchival {
		logger.Debug().Str("code_path", "ARCHIVAL_SELECTION_ENTRY").
			Int("available_endpoints", len(availableEndpoints)).
			Msgf("filtering %d available endpoints to select up to %d archival-capable.", len(availableEndpoints), numEndpoints)
	} else {
		logger.Debug().Str("code_path", "STANDARD_SELECTION_ENTRY").
			Int("available_endpoints", len(availableEndpoints)).
			Msgf("filtering %d available endpoints to select up to %d.", len(availableEndpoints), numEndpoints)
	}

	// Filter valid endpoints with archival filtering when required
	filteredEndpointsAddr, _, err := ss.filterValidEndpointsWithDetails(availableEndpoints, requiresArchival, requestID)
	if err != nil {
		logger.Error().Err(err).Msg("error filtering endpoints")
		return nil, err
	}

	// Select random endpoints as fallback
	if len(filteredEndpointsAddr) == 0 {
		// When requiresArchival is true, we must respect archival filtering even in fallback.
		// Filter to only archival-capable endpoints before random selection.
		if requiresArchival {
			archivalEndpoints := ss.filterArchivalEndpointsForFallback(availableEndpoints)
			if len(archivalEndpoints) > 0 {
				// CODE_PATH: Archival fallback - random selection from archival-capable endpoints
				logger.Warn().Str("code_path", "ARCHIVAL_FALLBACK_RANDOM").
					Int("archival_endpoints", len(archivalEndpoints)).
					Int("total_endpoints", len(availableEndpoints)).
					Msgf("selecting random archival endpoints (fallback) from %d archival-capable endpoints", len(archivalEndpoints))
				return selector.RandomSelectMultiple(archivalEndpoints, numEndpoints), nil
			}
			// CODE_PATH: No archival endpoints available - request will fail
			logger.Error().Str("code_path", "ARCHIVAL_NO_ENDPOINTS").
				Int("total_endpoints", len(availableEndpoints)).
				Msg("no archival-capable endpoints available for archival request")
			return nil, fmt.Errorf("no archival-capable endpoints available for archival request")
		}
		// CODE_PATH: Standard fallback - random selection, but still exclude known-stale URLs
		fallbackEndpoints := ss.filterStaleURLEndpoints(availableEndpoints)
		logger.Warn().Str("code_path", "STANDARD_FALLBACK_RANDOM").
			Int("total_endpoints", len(availableEndpoints)).
			Int("fallback_endpoints", len(fallbackEndpoints)).
			Msgf("selecting random endpoints because all endpoints failed validation from: %s", availableEndpoints.String())
		return selector.RandomSelectMultiple(fallbackEndpoints, numEndpoints), nil
	}

	// CODE_PATH: Selection success - using diversity-aware selection
	logger.Debug().Str("code_path", "SELECTION_SUCCESS").
		Int("filtered_endpoints", len(filteredEndpointsAddr)).
		Int("available_endpoints", len(availableEndpoints)).
		Msgf("filtered %d endpoints from %d available endpoints", len(filteredEndpointsAddr), len(availableEndpoints))
	return selector.SelectEndpointsWithDiversity(logger, filteredEndpointsAddr, numEndpoints), nil
}

// SelectWithMetadata returns endpoint address and selection metadata.
// Filters endpoints by validity and captures detailed validation failure information.
// Selects random endpoint if all fail validation.
//
// When requiresArchival is true, only endpoints that have passed the archival check are considered.
// When requiresArchival is false, the archival check is skipped, allowing all valid endpoints.
func (ss *serviceState) SelectWithMetadata(availableEndpoints protocol.EndpointAddrList, requiresArchival bool, requestID string) (EndpointSelectionResult, error) {
	logger := ss.logger.With("method", "SelectWithMetadata").
		With("chain_id", ss.serviceQoSConfig.getEVMChainID()).
		With("service_id", ss.serviceQoSConfig.GetServiceID()).
		With("requires_archival", requiresArchival).
		With("request_id", requestID)

	availableCount := len(availableEndpoints)
	logger.Debug().Msgf("filtering %d available endpoints.", availableCount)

	filteredEndpointsAddr, validationResults, err := ss.filterValidEndpointsWithDetails(availableEndpoints, requiresArchival, requestID)
	if err != nil {
		logger.Error().Err(err).Msg("error filtering endpoints")
		return EndpointSelectionResult{}, err
	}

	validCount := len(filteredEndpointsAddr)
	// Handle case where all endpoints failed validation
	if validCount == 0 {
		// Still exclude known-stale URLs from the fallback pool
		fallbackEndpoints := ss.filterStaleURLEndpoints(availableEndpoints)
		logger.Warn().
			Int("fallback_endpoints", len(fallbackEndpoints)).
			Msgf("SELECTING A RANDOM ENDPOINT because all endpoints failed validation from: %s", availableEndpoints.String())
		randomAvailableEndpointAddr := fallbackEndpoints[rand.Intn(len(fallbackEndpoints))]
		return EndpointSelectionResult{
			SelectedEndpoint: randomAvailableEndpointAddr,
			Metadata: EndpointSelectionMetadata{
				RandomEndpointFallback: true,
				ValidationResults:      validationResults,
			},
		}, nil
	}

	logger.Debug().Msgf("filtered %d endpoints from %d available endpoints", validCount, availableCount)

	// Select random endpoint from valid candidates
	selectedEndpointAddr := filteredEndpointsAddr[rand.Intn(validCount)]
	return EndpointSelectionResult{
		SelectedEndpoint: selectedEndpointAddr,
		Metadata: EndpointSelectionMetadata{
			RandomEndpointFallback: false,
			ValidationResults:      validationResults,
		},
	}, nil
}

// filterValidEndpointsWithDetails returns the subset of available endpoints that are valid
// according to previously processed observations, along with detailed validation results for all endpoints.
//
// Note: This function performs validation on ALL available endpoints for a service request:
// - Each endpoint undergoes validation checks (chain ID, block number, response history, etc.)
// - Failed endpoints are captured with specific failure reasons
// - Successful endpoints are captured for metrics tracking
// - Only valid endpoints are returned for potential selection
//
// When requiresArchival is true, only endpoints that have passed the archival check are considered.
// When requiresArchival is false, the archival check is skipped, allowing all valid endpoints.
//
// Performance: Copies endpoint data under lock, then releases lock before validation loop
// to minimize lock contention on the hot path.
func (ss *serviceState) filterValidEndpointsWithDetails(availableEndpoints protocol.EndpointAddrList, requiresArchival bool, requestID string) (protocol.EndpointAddrList, []*qosobservations.EndpointValidationResult, error) {
	logger := ss.logger.With("method", "filterValidEndpointsWithDetails").
		With("qos_instance", "evm").
		With("requires_archival", requiresArchival).
		With("request_id", requestID)

	if len(availableEndpoints) == 0 {
		return nil, nil, errEmptyEndpointListObs
	}

	logger.Debug().Msgf("About to filter through %d available endpoints", len(availableEndpoints))

	// Copy endpoint data under lock to minimize lock hold time
	// This prevents lock contention with UpdateFromExtractedData on the observation path
	type endpointData struct {
		addr     protocol.EndpointAddr
		endpoint endpoint
		found    bool
	}
	endpointsCopy := make([]endpointData, len(availableEndpoints))

	// Build a URL→blockHeight lookup from ALL store entries (not just available endpoints).
	// This lets us validate fresh endpoints against known block heights for the same URL
	// (different supplier addresses staked against the same backend infrastructure).
	urlBlockHeights := make(map[string]uint64)

	ss.endpointStore.endpointsMu.RLock()
	for addr, ep := range ss.endpointStore.endpoints {
		if ep.checkBlockNumber.parsedBlockNumberResponse != nil {
			if url, err := addr.GetURL(); err == nil {
				h := *ep.checkBlockNumber.parsedBlockNumberResponse
				if existing, ok := urlBlockHeights[url]; !ok || h > existing {
					urlBlockHeights[url] = h
				}
			}
		}
	}
	for i, addr := range availableEndpoints {
		ep, found := ss.endpointStore.endpoints[addr]
		endpointsCopy[i] = endpointData{addr: addr, endpoint: ep, found: found}
	}
	ss.endpointStore.endpointsMu.RUnlock()

	// Merge cached Redis URL block heights as fallback.
	// This catches stale URLs that aren't in the local store yet
	// (e.g., new supplier addresses after session rotation).
	if cached := ss.redisURLBlockHeights.Load(); cached != nil {
		for url, h := range *cached {
			if existing, ok := urlBlockHeights[url]; !ok || h > existing {
				urlBlockHeights[url] = h
			}
		}
	}

	// Touch endpoints to update lastSeen for stale endpoint cleanup.
	// Uses a separate WLock call to avoid changing the read-heavy filtering path.
	ss.endpointStore.touchEndpoints(availableEndpoints)

	// Now iterate without holding the lock
	var filteredEndpointsAddr protocol.EndpointAddrList
	var validationResults []*qosobservations.EndpointValidationResult

	// CODE_PATH: Start validation loop
	logger.Debug().Str("code_path", "VALIDATION_LOOP_START").
		Int("endpoints_to_validate", len(endpointsCopy)).
		Msg("starting endpoint validation loop")

	// Track validation statistics for summary logging
	var validCount, invalidCount, archivalFilteredCount int

	// TODO_FUTURE: use service-specific metrics to add an endpoint ranking method
	// which can be used to assign a rank/score to a valid endpoint to guide endpoint selection.
	for _, data := range endpointsCopy {
		logger := logger.With("endpoint_addr", data.addr)
		logger.Debug().Msg("processing endpoint")

		if !data.found {
			// For archival requests: only allow fresh endpoints confirmed archival in cache,
			// but only when the cache has been populated (Len > 0). If the cache is empty
			// (cold start, health checks still running), fall back to allowing fresh endpoints
			// through to avoid blocking all archival requests indefinitely.
			if requiresArchival && ss.archivalCache != nil && ss.archivalCache.Len() > 0 {
				key := reputation.NewEndpointKey(
					ss.serviceQoSConfig.GetServiceID(),
					data.addr,
					sharedtypes.RPCType_JSON_RPC,
				)
				if isArchival, ok := ss.archivalCache.Get(key.String()); !ok || !isArchival {
					invalidCount++
					archivalFilteredCount++
					logger.Debug().Str("code_path", "FRESH_ENDPOINT_ARCHIVAL_FILTERED").
						Str("endpoint_addr", string(data.addr)).
						Msg("fresh endpoint filtered: not confirmed archival in cache")

					failureReason := qosobservations.EndpointValidationFailureReason_ENDPOINT_VALIDATION_FAILURE_REASON_ARCHIVAL_CHECK_FAILED
					failureDetails := "fresh endpoint not confirmed archival in cache"
					validationResults = append(validationResults, &qosobservations.EndpointValidationResult{
						EndpointAddr:   string(data.addr),
						Success:        false,
						FailureReason:  &failureReason,
						FailureDetails: &failureDetails,
					})
					continue
				}
			}

			// Fresh endpoint: not in the store yet. Before allowing, check if another
			// supplier's entry for the same URL has a known block height. This prevents
			// new supplier addresses behind stale infrastructure from getting a "free pass".
			syncAllowance := ss.serviceQoSConfig.getSyncAllowance()
			perceivedBlock := ss.perceivedBlockNumber.Load()
			if syncAllowance > 0 && perceivedBlock > 0 {
				if url, err := data.addr.GetURL(); err == nil {
					if urlBlock, ok := urlBlockHeights[url]; ok {
						minAllowed := perceivedBlock - syncAllowance
						if urlBlock < minAllowed {
							blocksBehind := int64(perceivedBlock) - int64(urlBlock)
							invalidCount++
							logger.Warn().
								Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
								Str("url", url).
								Uint64("url_block", urlBlock).
								Uint64("perceived_block", perceivedBlock).
								Uint64("sync_allowance", syncAllowance).
								Int64("blocks_behind", blocksBehind).
								Msg("⛔ Fresh endpoint filtered: URL has known stale block height from another supplier")

							failureReason := qosobservations.EndpointValidationFailureReason_ENDPOINT_VALIDATION_FAILURE_REASON_BLOCK_NUMBER_BEHIND
							failureDetails := fmt.Sprintf("fresh endpoint URL block height %d is %d blocks behind perceived %d", urlBlock, blocksBehind, perceivedBlock)
							validationResults = append(validationResults, &qosobservations.EndpointValidationResult{
								EndpointAddr:   string(data.addr),
								Success:        false,
								FailureReason:  &failureReason,
								FailureDetails: &failureDetails,
							})
							continue
						}
					}
				}
			}

			logger.Debug().
				Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
				Uint64("sync_allowance", ss.serviceQoSConfig.getSyncAllowance()).
				Msg("Fresh endpoint allowed (not yet in store)")

			result := &qosobservations.EndpointValidationResult{
				EndpointAddr: string(data.addr),
				Success:      true,
			}
			validationResults = append(validationResults, result)
			filteredEndpointsAddr = append(filteredEndpointsAddr, data.addr)
			continue
		}

		if err := ss.basicEndpointValidation(data.addr, data.endpoint, requiresArchival); err != nil {
			invalidCount++

			// Check if this is a structured filter error with details
			var filterErr *EndpointFilterError
			if errors.As(err, &filterErr) {
				logEvent := logger.Warn().
					Err(err).
					Str("endpoint_addr", string(data.addr)).
					Str("filter_layer", string(filterErr.Layer)).
					Str("filter_reason", filterErr.Reason)
				// Add each detail field to the log
				for k, v := range filterErr.Details {
					logEvent = logEvent.Str(k, fmt.Sprintf("%v", v))
				}
				logEvent.Msg("SKIPPING endpoint: structured validation failure")

				// CODE_PATH: Track archival filtering specifically
				if filterErr.Layer == FilterLayerArchival {
					archivalFilteredCount++
					logger.Debug().Str("code_path", "ARCHIVAL_FILTER_REJECTED").
						Str("endpoint_addr", string(data.addr)).
						Str("filter_reason", filterErr.Reason).
						Bool("redis_checked", filterErr.Details["redis_checked"] == true).
						Msg("endpoint rejected by archival filter")
				}
			} else {
				logger.Warn().
					Err(err).
					Str("endpoint_addr", string(data.addr)).
					Msg("SKIPPING endpoint because it failed basic validation")
			}

			// Create validation result for validation failure
			failureReason := ss.categorizeValidationFailure(err)
			errorMsg := err.Error()
			result := &qosobservations.EndpointValidationResult{
				EndpointAddr:   string(data.addr),
				Success:        false,
				FailureReason:  &failureReason,
				FailureDetails: &errorMsg,
			}
			validationResults = append(validationResults, result)
			continue
		}

		// Endpoint passed validation - record success and add to valid list
		validCount++
		result := &qosobservations.EndpointValidationResult{
			EndpointAddr: string(data.addr),
			Success:      true,
			// FailureReason and FailureDetails are nil for successful validations
		}
		validationResults = append(validationResults, result)
		filteredEndpointsAddr = append(filteredEndpointsAddr, data.addr)
		logger.Debug().Msgf("endpoint passed validation: %s", data.addr)
	}

	// CODE_PATH: Validation loop complete - summary
	logger.Debug().Str("code_path", "VALIDATION_COMPLETE").
		Int("valid_count", validCount).
		Int("invalid_count", invalidCount).
		Int("archival_filtered_count", archivalFilteredCount).
		Int("total_validated", len(endpointsCopy)).
		Msg("endpoint validation loop complete")

	return filteredEndpointsAddr, validationResults, nil
}

// categorizeValidationFailure determines the failure reason category based on the validation error.
func (ss *serviceState) categorizeValidationFailure(err error) qosobservations.EndpointValidationFailureReason {
	if errors.Is(err, errEmptyResponseObs) {
		return qosobservations.EndpointValidationFailureReason_ENDPOINT_VALIDATION_FAILURE_REASON_EMPTY_RESPONSE_HISTORY
	}
	if errors.Is(err, errRecentInvalidResponseObs) {
		return qosobservations.EndpointValidationFailureReason_ENDPOINT_VALIDATION_FAILURE_REASON_RECENT_INVALID_RESPONSE
	}
	if errors.Is(err, errOutsideSyncAllowanceBlockNumberObs) {
		return qosobservations.EndpointValidationFailureReason_ENDPOINT_VALIDATION_FAILURE_REASON_BLOCK_NUMBER_BEHIND
	}
	if errors.Is(err, errInvalidChainIDObs) {
		return qosobservations.EndpointValidationFailureReason_ENDPOINT_VALIDATION_FAILURE_REASON_CHAIN_ID_MISMATCH
	}
	if errors.Is(err, errNoBlockNumberObs) {
		return qosobservations.EndpointValidationFailureReason_ENDPOINT_VALIDATION_FAILURE_REASON_NO_BLOCK_NUMBER_OBSERVATION
	}
	if errors.Is(err, errNoChainIDObs) {
		return qosobservations.EndpointValidationFailureReason_ENDPOINT_VALIDATION_FAILURE_REASON_NO_CHAIN_ID_OBSERVATION
	}

	// Check for archival validation failures
	errorStr := err.Error()
	if strings.Contains(errorStr, "archival") {
		return qosobservations.EndpointValidationFailureReason_ENDPOINT_VALIDATION_FAILURE_REASON_ARCHIVAL_CHECK_FAILED
	}

	// Default category for unknown validation failures
	return qosobservations.EndpointValidationFailureReason_ENDPOINT_VALIDATION_FAILURE_REASON_UNKNOWN
}

// basicEndpointValidation returns an error if the supplied endpoint is not
// valid based on the perceived state of the EVM blockchain.
//
// It returns an error if:
// - The endpoint has returned an empty response in the past.
// - The endpoint has returned an invalid response within the last 30 minutes.
// - The endpoint's response to an `eth_chainId` request is not the expected chain ID.
// - The endpoint's response to an `eth_blockNumber` request is greater than the perceived block number.
// - The endpoint's archival check is invalid, if requiresArchival is true and archival checks are enabled.
//
// When requiresArchival is true, only endpoints that have passed the archival check are considered valid.
// When requiresArchival is false, the archival check is skipped, allowing all otherwise valid endpoints.
//
// Note: This function is lock-free - perceivedBlockNumber uses atomic operations.
func (ss *serviceState) basicEndpointValidation(endpointAddr protocol.EndpointAddr, endpoint endpoint, requiresArchival bool) error {
	// Check if the endpoint has returned an empty response within the timeout period.
	// Empty responses use the same 5-minute timeout as invalid responses to allow recovery.
	if endpoint.hasReturnedEmptyResponse && endpoint.invalidResponseLastObserved != nil {
		timeSinceEmptyResponse := time.Since(*endpoint.invalidResponseLastObserved)
		if timeSinceEmptyResponse < invalidResponseTimeout {
			return fmt.Errorf("recent empty response validation failed (%.0f minutes ago): %w",
				timeSinceEmptyResponse.Minutes(), errEmptyResponseObs)
		}
	} else if endpoint.hasReturnedEmptyResponse {
		// Fallback for cases where hasReturnedEmptyResponse is true but invalidResponseLastObserved is nil
		return fmt.Errorf("empty response validation failed: %w", errEmptyResponseObs)
	}

	// Check if the endpoint has returned an invalid response within the invalid response timeout period.
	if endpoint.hasReturnedInvalidResponse && endpoint.invalidResponseLastObserved != nil {
		timeSinceInvalidResponse := time.Since(*endpoint.invalidResponseLastObserved)
		if timeSinceInvalidResponse < invalidResponseTimeout {
			return fmt.Errorf("recent invalid response validation failed (%.0f minutes ago): %w. Empty response: %t. Response validation error: %s",
				timeSinceInvalidResponse.Minutes(), errRecentInvalidResponseObs, endpoint.hasReturnedEmptyResponse, endpoint.invalidResponseError)
		}
	}

	// Check if the endpoint's block number is not more than the sync allowance behind the perceived block number.
	if err := ss.isBlockNumberValid(endpoint.checkBlockNumber); err != nil {
		return fmt.Errorf("block number validation failed: %w", err)
	}

	// Check if the endpoint's EVM chain ID matches the expected chain ID.
	if err := ss.isChainIDValid(endpoint.checkChainID); err != nil {
		return fmt.Errorf("chain ID validation failed: %w", err)
	}

	// CONDITIONAL: Only apply archival check if request requires archival data.
	// This allows non-archival requests to use all valid endpoints (larger pool).
	// Archival requests will only be routed to archival-capable endpoints.
	// Archival capability is determined by external health checks, not by QoS observations.
	if requiresArchival {
		// Build diagnostic details for logging regardless of outcome
		archivalDetails := &ArchivalFilterDetails{
			IsArchival:   endpoint.checkArchival.isArchival,
			ExpiresAt:    endpoint.checkArchival.expiresAt,
			IsExpired:    !endpoint.checkArchival.expiresAt.IsZero() && time.Now().After(endpoint.checkArchival.expiresAt),
			RedisChecked: false, // DEPRECATED: kept for backward compatibility
			CacheChecked: false,
		}
		if archivalDetails.IsExpired {
			archivalDetails.TimeSinceExpiry = time.Since(endpoint.checkArchival.expiresAt)
		}

		// Check archival from local endpointStore first (fast path for leader replica)
		if isArchivalCapable(endpoint.checkArchival) == nil {
			return nil // Local check passed
		}

		// Check local archival cache (populated by background refresh and UpdateFromExtractedData)
		// This replaces synchronous Redis call - O(1) lookup, no network latency
		if ss.archivalCache != nil {
			archivalDetails.CacheChecked = true
			key := reputation.NewEndpointKey(
				ss.serviceQoSConfig.GetServiceID(),
				endpointAddr,
				sharedtypes.RPCType_JSON_RPC,
			)
			if isArchival, ok := ss.archivalCache.Get(key.String()); ok && isArchival {
				archivalDetails.CacheIsArchival = true
				return nil // Cache check passed
			}
		}

		// Both checks failed - return structured error with diagnostic details
		return NewArchivalFilterError(string(endpointAddr), archivalDetails, errEndpointNotArchival)
	}

	return nil
}

// isBlockNumberValid returns an error if:
//   - The endpoint's block height is less than the perceived block height minus the sync allowance.
//   - The endpoint has no block number observation but we have chain state (unknown block height
//     is treated as potentially stale to prevent serving data from severely behind endpoints).
//
// Returns nil (passes) if:
//   - sync_allowance is 0 (check disabled)
//   - No perceived block number yet (no chain data to compare against)
//
// Note: This function is lock-free - perceivedBlockNumber uses atomic operations.
func (ss *serviceState) isBlockNumberValid(check endpointCheckBlockNumber) error {
	syncAllowance := ss.serviceQoSConfig.getSyncAllowance()

	// If sync allowance is 0, the check is disabled (only log at debug level)
	if syncAllowance == 0 {
		return nil
	}

	// Load perceived block number atomically (lock-free)
	perceivedBlock := ss.perceivedBlockNumber.Load()

	// If we don't have a perceived block number yet, skip the check (no data to compare against)
	if perceivedBlock == 0 {
		ss.logger.Warn().
			Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
			Uint64("sync_allowance", syncAllowance).
			Msg("🔍 Sync allowance check SKIPPED (no perceived block number yet)")
		return nil
	}

	// If endpoint has no block number observation but we have chain state, filter it out.
	// An endpoint with unknown block height could be severely behind (e.g., 58K blocks)
	// and would serve stale data to clients. It's safer to skip it and use endpoints
	// with known, validated block heights. The endpoint will become eligible once its
	// block height is observed via health checks, Redis sync, or relay observations.
	if check.parsedBlockNumberResponse == nil {
		ss.logger.Warn().
			Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
			Uint64("perceived_block", perceivedBlock).
			Uint64("sync_allowance", syncAllowance).
			Msg("⛔ Endpoint filtered: no block number observation (unknown block height treated as potentially stale)")
		return fmt.Errorf("no block number observation with perceived block %d: %w", perceivedBlock, errNoBlockNumberObs)
	}

	// Dereference pointer to show actual block number instead of memory address in error logs
	parsedBlockNumber := *check.parsedBlockNumberResponse

	// If the endpoint's block height is less than the perceived block height minus the sync allowance,
	// then the endpoint is behind the chain and should be filtered out.
	minAllowedBlockNumber := perceivedBlock - syncAllowance

	if parsedBlockNumber < minAllowedBlockNumber {
		blocksBehind := int64(perceivedBlock) - int64(parsedBlockNumber)
		ss.logger.Warn().
			Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
			Uint64("endpoint_block", parsedBlockNumber).
			Uint64("perceived_block", perceivedBlock).
			Uint64("sync_allowance", syncAllowance).
			Uint64("min_allowed_block", minAllowedBlockNumber).
			Int64("blocks_behind", blocksBehind).
			Msg("⛔ Endpoint filtered: block height outside sync allowance")

		// Return structured error with diagnostic details
		blockDetails := &BlockHeightFilterDetails{
			EndpointBlock:  parsedBlockNumber,
			PerceivedBlock: perceivedBlock,
			SyncAllowance:  syncAllowance,
			BlocksBehind:   blocksBehind,
		}
		return NewBlockHeightFilterError("", blockDetails, errOutsideSyncAllowanceBlockNumberObs)
	}

	// Log the sync allowance validation details only if it passes (to reduce noise)
	ss.logger.Debug().
		Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
		Uint64("endpoint_block", parsedBlockNumber).
		Uint64("perceived_block", perceivedBlock).
		Uint64("sync_allowance", syncAllowance).
		Uint64("min_allowed_block", minAllowedBlockNumber).
		Int64("blocks_behind", int64(perceivedBlock)-int64(parsedBlockNumber)).
		Bool("within_allowance", parsedBlockNumber >= minAllowedBlockNumber).
		Msg("🔍 Sync allowance validation")

	return nil
}

// filterStaleURLEndpoints removes endpoints whose URL has a known stale block height
// from the endpoint store. Used by fallback paths to avoid selecting stale infrastructure
// when all endpoints fail normal validation.
func (ss *serviceState) filterStaleURLEndpoints(endpoints protocol.EndpointAddrList) protocol.EndpointAddrList {
	serviceID := string(ss.serviceQoSConfig.GetServiceID())
	syncAllowance := ss.serviceQoSConfig.getSyncAllowance()
	if syncAllowance == 0 {
		ss.logger.Warn().
			Str("service_id", serviceID).
			Int("endpoint_count", len(endpoints)).
			Msg("filterStaleURLEndpoints: sync_allowance is 0, skipping stale URL filtering")
		return endpoints
	}
	perceivedBlock := ss.perceivedBlockNumber.Load()
	if perceivedBlock == 0 {
		ss.logger.Warn().
			Str("service_id", serviceID).
			Int("endpoint_count", len(endpoints)).
			Msg("filterStaleURLEndpoints: perceived_block is 0, skipping stale URL filtering")
		return endpoints
	}
	minAllowed := perceivedBlock - syncAllowance

	// Build URL→blockHeight map from store
	urlBlockHeights := make(map[string]uint64)
	ss.endpointStore.endpointsMu.RLock()
	for addr, ep := range ss.endpointStore.endpoints {
		if ep.checkBlockNumber.parsedBlockNumberResponse != nil {
			if url, err := addr.GetURL(); err == nil {
				h := *ep.checkBlockNumber.parsedBlockNumberResponse
				if existing, ok := urlBlockHeights[url]; !ok || h > existing {
					urlBlockHeights[url] = h
				}
			}
		}
	}
	ss.endpointStore.endpointsMu.RUnlock()

	// Merge cached Redis URL block heights as fallback.
	if cached := ss.redisURLBlockHeights.Load(); cached != nil {
		for url, h := range *cached {
			if existing, ok := urlBlockHeights[url]; !ok || h > existing {
				urlBlockHeights[url] = h
			}
		}
	}

	if len(urlBlockHeights) == 0 {
		ss.logger.Warn().
			Str("service_id", serviceID).
			Int("endpoint_count", len(endpoints)).
			Uint64("perceived_block", perceivedBlock).
			Msg("filterStaleURLEndpoints: no block height data in endpoint store — health check pipeline may not be populating block heights")
		return endpoints
	}

	filtered := make(protocol.EndpointAddrList, 0, len(endpoints))
	removedCount := 0
	for _, ep := range endpoints {
		if url, err := ep.GetURL(); err == nil {
			if urlBlock, ok := urlBlockHeights[url]; ok && urlBlock < minAllowed {
				ss.logger.Warn().
					Str("service_id", serviceID).
					Str("url", url).
					Uint64("url_block", urlBlock).
					Uint64("min_allowed", minAllowed).
					Uint64("perceived_block", perceivedBlock).
					Uint64("sync_allowance", syncAllowance).
					Uint64("blocks_behind", perceivedBlock-urlBlock).
					Msg("filterStaleURLEndpoints: removed stale URL from fallback pool")
				removedCount++
				continue
			}
		}
		filtered = append(filtered, ep)
	}

	// If filtering removed everything, return original to avoid total failure
	if len(filtered) == 0 {
		ss.logger.Error().
			Str("service_id", serviceID).
			Int("total_endpoints", len(endpoints)).
			Int("removed_count", removedCount).
			Uint64("perceived_block", perceivedBlock).
			Uint64("sync_allowance", syncAllowance).
			Int("url_block_heights_count", len(urlBlockHeights)).
			Msg("filterStaleURLEndpoints: ALL endpoints are stale — returning unfiltered to avoid total failure")
		return endpoints
	}

	if removedCount > 0 {
		ss.logger.Warn().
			Str("service_id", serviceID).
			Int("removed_count", removedCount).
			Int("remaining_count", len(filtered)).
			Int("total_count", len(endpoints)).
			Msg("filterStaleURLEndpoints: removed stale URLs from fallback pool")
	}

	return filtered
}

// isChainIDValid returns an error if:
//   - The endpoint has not had an observation of its response to a `eth_chainId` request.
//   - The endpoint's chain ID does not match the expected chain ID in the service state.
func (ss *serviceState) isChainIDValid(check endpointCheckChainID) error {
	expectedChainID := ss.serviceQoSConfig.getEVMChainID()

	// When no expected chain ID is configured (e.g., NewSimpleQoSInstance),
	// chain ID validation is delegated to active health checks. Skip it here.
	if expectedChainID == "" {
		return nil
	}

	if check.chainID == nil {
		ss.logger.Debug().
			Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
			Str("expected_chain_id", string(expectedChainID)).
			Msg("🔍 Chain ID check: endpoint has no chain ID observation")
		return errNoChainIDObs
	}

	// Dereference pointer to show actual chain ID instead of memory address in error logs
	chainID := *check.chainID

	ss.logger.Debug().
		Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
		Str("endpoint_chain_id", string(chainID)).
		Str("expected_chain_id", string(expectedChainID)).
		Bool("chain_id_matches", chainID == expectedChainID).
		Msg("🔍 Chain ID validation")

	if chainID != expectedChainID {
		ss.logger.Debug().
			Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
			Str("endpoint_chain_id", string(chainID)).
			Str("expected_chain_id", string(expectedChainID)).
			Msg("❌ Endpoint failed chain ID check - mismatch")

		// Return structured error with diagnostic details
		return NewEndpointFilterError(
			FilterLayerLocalStore,
			"chain_id_mismatch",
			"", // addr not available in this function
			errInvalidChainIDObs,
			map[string]interface{}{
				"endpoint_chain_id": string(chainID),
				"expected_chain_id": string(expectedChainID),
			},
		)
	}
	return nil
}

// filterArchivalEndpointsForFallback returns endpoints known to be archival-capable.
// Used when normal validation fails but we need to respect archival requirements.
// Checks both local endpointStore and Redis/reputation service for archival status.
func (ss *serviceState) filterArchivalEndpointsForFallback(availableEndpoints protocol.EndpointAddrList) protocol.EndpointAddrList {
	logger := ss.logger.With("method", "filterArchivalEndpointsForFallback")

	// CODE_PATH: Start fallback archival filtering
	logger.Debug().Str("code_path", "FALLBACK_ARCHIVAL_FILTER_START").
		Int("endpoints_to_check", len(availableEndpoints)).
		Msg("starting fallback archival endpoint filtering")

	var archivalEndpoints protocol.EndpointAddrList
	var localHits, cacheHits int

	// Check each endpoint for archival capability
	ss.endpointStore.endpointsMu.RLock()
	for _, addr := range availableEndpoints {
		endpoint, found := ss.endpointStore.endpoints[addr]

		// Check local store first
		if found && endpoint.checkArchival.isValid() {
			localHits++
			archivalEndpoints = append(archivalEndpoints, addr)
			continue
		}

		// Check local archival cache (no Redis call in hot path)
		if ss.archivalCache != nil {
			key := reputation.NewEndpointKey(
				ss.serviceQoSConfig.GetServiceID(),
				addr,
				sharedtypes.RPCType_JSON_RPC,
			)
			if isArchival, ok := ss.archivalCache.Get(key.String()); ok && isArchival {
				cacheHits++
				archivalEndpoints = append(archivalEndpoints, addr)
			}
		}
	}
	ss.endpointStore.endpointsMu.RUnlock()

	// CODE_PATH: Complete fallback archival filtering
	logger.Debug().Str("code_path", "FALLBACK_ARCHIVAL_FILTER_COMPLETE").
		Int("archival_endpoints_found", len(archivalEndpoints)).
		Int("local_hits", localHits).
		Int("cache_hits", cacheHits).
		Int("total_checked", len(availableEndpoints)).
		Msg("fallback archival endpoint filtering complete")

	return archivalEndpoints
}
