package evm

import (
	"context"
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
func (ss *serviceState) SelectMultiple(availableEndpoints protocol.EndpointAddrList, numEndpoints uint) (protocol.EndpointAddrList, error) {
	return ss.SelectMultipleWithArchival(availableEndpoints, numEndpoints, false)
}

// SelectMultipleWithArchival returns multiple endpoint addresses with optional archival filtering.
// When requiresArchival is true, only endpoints that have passed archival capability checks are considered.
// When requiresArchival is false, all valid endpoints are considered (same behavior as SelectMultiple).
// This enables archival requests to be routed only to archival-capable endpoints.
func (ss *serviceState) SelectMultipleWithArchival(availableEndpoints protocol.EndpointAddrList, numEndpoints uint, requiresArchival bool) (protocol.EndpointAddrList, error) {
	logger := ss.logger.With("method", "SelectMultipleWithArchival").
		With("chain_id", ss.serviceQoSConfig.getEVMChainID()).
		With("service_id", ss.serviceQoSConfig.GetServiceID()).
		With("num_endpoints", numEndpoints).
		With("requires_archival", requiresArchival)
	logger.Debug().Msgf("filtering %d available endpoints to select up to %d.", len(availableEndpoints), numEndpoints)

	// Filter valid endpoints with archival filtering when required
	filteredEndpointsAddr, _, err := ss.filterValidEndpointsWithDetails(availableEndpoints, requiresArchival)
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
				logger.Warn().Msgf("SELECTING RANDOM ARCHIVAL ENDPOINTS (fallback) from %d archival-capable endpoints", len(archivalEndpoints))
				return selector.RandomSelectMultiple(archivalEndpoints, numEndpoints), nil
			}
			// No archival endpoints available - log and fail gracefully
			logger.Error().Msgf("NO ARCHIVAL ENDPOINTS available for archival request from %d total endpoints", len(availableEndpoints))
			return nil, fmt.Errorf("no archival-capable endpoints available for archival request")
		}
		logger.Warn().Msgf("SELECTING RANDOM ENDPOINTS because all endpoints failed validation from: %s", availableEndpoints.String())
		return selector.RandomSelectMultiple(availableEndpoints, numEndpoints), nil
	}

	// Use the diversity-aware selection
	logger.Debug().Msgf("filtered %d endpoints from %d available endpoints", len(filteredEndpointsAddr), len(availableEndpoints))
	return selector.SelectEndpointsWithDiversity(logger, filteredEndpointsAddr, numEndpoints), nil
}

// SelectWithMetadata returns endpoint address and selection metadata.
// Filters endpoints by validity and captures detailed validation failure information.
// Selects random endpoint if all fail validation.
//
// When requiresArchival is true, only endpoints that have passed the archival check are considered.
// When requiresArchival is false, the archival check is skipped, allowing all valid endpoints.
func (ss *serviceState) SelectWithMetadata(availableEndpoints protocol.EndpointAddrList, requiresArchival bool) (EndpointSelectionResult, error) {
	logger := ss.logger.With("method", "SelectWithMetadata").
		With("chain_id", ss.serviceQoSConfig.getEVMChainID()).
		With("service_id", ss.serviceQoSConfig.GetServiceID()).
		With("requires_archival", requiresArchival)

	availableCount := len(availableEndpoints)
	logger.Debug().Msgf("filtering %d available endpoints.", availableCount)

	filteredEndpointsAddr, validationResults, err := ss.filterValidEndpointsWithDetails(availableEndpoints, requiresArchival)
	if err != nil {
		logger.Error().Err(err).Msg("error filtering endpoints")
		return EndpointSelectionResult{}, err
	}

	validCount := len(filteredEndpointsAddr)
	// Handle case where all endpoints failed validation
	if validCount == 0 {
		logger.Warn().Msgf("SELECTING A RANDOM ENDPOINT because all endpoints failed validation from: %s", availableEndpoints.String())
		randomAvailableEndpointAddr := availableEndpoints[rand.Intn(availableCount)]
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
func (ss *serviceState) filterValidEndpointsWithDetails(availableEndpoints protocol.EndpointAddrList, requiresArchival bool) (protocol.EndpointAddrList, []*qosobservations.EndpointValidationResult, error) {
	logger := ss.logger.With("method", "filterValidEndpointsWithDetails").
		With("qos_instance", "evm").
		With("requires_archival", requiresArchival)

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

	ss.endpointStore.endpointsMu.RLock()
	for i, addr := range availableEndpoints {
		ep, found := ss.endpointStore.endpoints[addr]
		endpointsCopy[i] = endpointData{addr: addr, endpoint: ep, found: found}
	}
	ss.endpointStore.endpointsMu.RUnlock()

	// Now iterate without holding the lock
	var filteredEndpointsAddr protocol.EndpointAddrList
	var validationResults []*qosobservations.EndpointValidationResult

	// TODO_FUTURE: use service-specific metrics to add an endpoint ranking method
	// which can be used to assign a rank/score to a valid endpoint to guide endpoint selection.
	for _, data := range endpointsCopy {
		logger := logger.With("endpoint_addr", data.addr)
		logger.Debug().Msg("processing endpoint")

		if !data.found {
			// It is valid for an endpoint to not be in the store yet (e.g., first request,
			// no observations collected). Treat it as a fresh endpoint and allow it.
			// It will be added to the store once observations are collected.
			logger.Warn().
				Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
				Uint64("sync_allowance", ss.serviceQoSConfig.getSyncAllowance()).
				Msg("🔍 Sync allowance check SKIPPED (endpoint not yet in store - fresh endpoint)")

			// Create validation result for endpoint not in store (but still valid)
			result := &qosobservations.EndpointValidationResult{
				EndpointAddr: string(data.addr),
				Success:      true,
			}
			validationResults = append(validationResults, result)
			filteredEndpointsAddr = append(filteredEndpointsAddr, data.addr)
			logger.Debug().Msg("endpoint not in store - allowing as fresh endpoint")
			continue
		}

		if err := ss.basicEndpointValidation(data.addr, data.endpoint, requiresArchival); err != nil {
			logger.Warn().
				Err(err).
				Str("endpoint_addr", string(data.addr)).
				Msg("⚠️ SKIPPING endpoint because it failed basic validation")

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
		result := &qosobservations.EndpointValidationResult{
			EndpointAddr: string(data.addr),
			Success:      true,
			// FailureReason and FailureDetails are nil for successful validations
		}
		validationResults = append(validationResults, result)
		filteredEndpointsAddr = append(filteredEndpointsAddr, data.addr)
		logger.Debug().Msgf("endpoint passed validation: %s", data.addr)
	}

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
		// Check archival from local endpointStore first (fast path for leader replica)
		if isArchivalCapable(endpoint.checkArchival) == nil {
			return nil // Local check passed
		}

		// Local check failed - try reputation service (shared across replicas via Redis)
		// This is the key fix: non-leader replicas will get archival status from Redis
		if ss.reputationSvc != nil {
			// Build reputation key - use JSON_RPC as default RPC type for archival checks
			key := reputation.NewEndpointKey(
				ss.serviceQoSConfig.GetServiceID(),
				endpointAddr,
				sharedtypes.RPCType_JSON_RPC,
			)
			if ss.reputationSvc.IsArchivalCapable(context.Background(), key) {
				return nil // Redis check passed
			}
		}

		return fmt.Errorf("archival capability check failed: %w", errEndpointNotArchival)
	}

	return nil
}

// isBlockNumberValid returns an error if:
//   - The endpoint's block height is less than the perceived block height minus the sync allowance.
//
// Returns nil (passes) if:
//   - sync_allowance is 0 (check disabled)
//   - No perceived block number yet (no chain data to compare against)
//   - Endpoint has no block number observation (no endpoint data to validate)
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

	// If endpoint has no block number observation, skip the check (no endpoint data to validate)
	if check.parsedBlockNumberResponse == nil {
		ss.logger.Warn().
			Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
			Uint64("perceived_block", perceivedBlock).
			Uint64("sync_allowance", syncAllowance).
			Msg("🔍 Sync allowance check SKIPPED (endpoint has no block number observation)")
		return nil
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
		return fmt.Errorf("%w: block number %d is outside the sync allowance relative to min allowed block number %d and sync allowance %d",
			errOutsideSyncAllowanceBlockNumberObs, parsedBlockNumber, minAllowedBlockNumber, syncAllowance)
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

// isChainIDValid returns an error if:
//   - The endpoint has not had an observation of its response to a `eth_chainId` request.
//   - The endpoint's chain ID does not match the expected chain ID in the service state.
func (ss *serviceState) isChainIDValid(check endpointCheckChainID) error {
	expectedChainID := ss.serviceQoSConfig.getEVMChainID()

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
		return fmt.Errorf("%w: chain ID %s does not match expected chain ID %s",
			errInvalidChainIDObs, chainID, expectedChainID)
	}
	return nil
}

// filterArchivalEndpointsForFallback returns endpoints known to be archival-capable.
// Used when normal validation fails but we need to respect archival requirements.
// Checks both local endpointStore and Redis/reputation service for archival status.
func (ss *serviceState) filterArchivalEndpointsForFallback(availableEndpoints protocol.EndpointAddrList) protocol.EndpointAddrList {
	var archivalEndpoints protocol.EndpointAddrList

	// Check each endpoint for archival capability
	ss.endpointStore.endpointsMu.RLock()
	for _, addr := range availableEndpoints {
		endpoint, found := ss.endpointStore.endpoints[addr]

		// Check local store first
		if found && endpoint.checkArchival.isValid() {
			archivalEndpoints = append(archivalEndpoints, addr)
			continue
		}

		// Check reputation service (Redis) for shared archival status
		if ss.reputationSvc != nil {
			key := reputation.NewEndpointKey(
				ss.serviceQoSConfig.GetServiceID(),
				addr,
				sharedtypes.RPCType_JSON_RPC,
			)
			if ss.reputationSvc.IsArchivalCapable(context.Background(), key) {
				archivalEndpoints = append(archivalEndpoints, addr)
			}
		}
	}
	ss.endpointStore.endpointsMu.RUnlock()

	return archivalEndpoints
}
