package evm

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/selector"
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
func (ss *serviceState) SelectMultiple(availableEndpoints protocol.EndpointAddrList, numEndpoints uint) (protocol.EndpointAddrList, error) {
	logger := ss.logger.With("method", "SelectMultiple").
		With("chain_id", ss.serviceQoSConfig.getEVMChainID()).
		With("service_id", ss.serviceQoSConfig.GetServiceID()).
		With("num_endpoints", numEndpoints)
	logger.Info().Msgf("filtering %d available endpoints to select up to %d.", len(availableEndpoints), numEndpoints)

	// Filter valid endpoints
	filteredEndpointsAddr, _, err := ss.filterValidEndpointsWithDetails(availableEndpoints)
	if err != nil {
		logger.Error().Err(err).Msg("error filtering endpoints")
		return nil, err
	}

	// Select random endpoints as fallback
	if len(filteredEndpointsAddr) == 0 {
		logger.Warn().Msgf("SELECTING RANDOM ENDPOINTS because all endpoints failed validation from: %s", availableEndpoints.String())
		return selector.RandomSelectMultiple(availableEndpoints, numEndpoints), nil
	}

	// Use the diversity-aware selection
	logger.Info().Msgf("filtered %d endpoints from %d available endpoints", len(filteredEndpointsAddr), len(availableEndpoints))
	return selector.SelectEndpointsWithDiversity(logger, filteredEndpointsAddr, numEndpoints), nil
}

// SelectWithMetadata returns endpoint address and selection metadata.
// Filters endpoints by validity and captures detailed validation failure information.
// Selects random endpoint if all fail validation.
func (ss *serviceState) SelectWithMetadata(availableEndpoints protocol.EndpointAddrList) (EndpointSelectionResult, error) {
	logger := ss.logger.With("method", "SelectWithMetadata").
		With("chain_id", ss.serviceQoSConfig.getEVMChainID()).
		With("service_id", ss.serviceQoSConfig.GetServiceID())

	availableCount := len(availableEndpoints)
	logger.Info().Msgf("filtering %d available endpoints.", availableCount)

	filteredEndpointsAddr, validationResults, err := ss.filterValidEndpointsWithDetails(availableEndpoints)
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

	logger.Info().Msgf("filtered %d endpoints from %d available endpoints", validCount, availableCount)

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
func (ss *serviceState) filterValidEndpointsWithDetails(availableEndpoints protocol.EndpointAddrList) (protocol.EndpointAddrList, []*qosobservations.EndpointValidationResult, error) {
	ss.endpointStore.endpointsMu.RLock()
	defer ss.endpointStore.endpointsMu.RUnlock()

	logger := ss.logger.With("method", "filterValidEndpointsWithDetails").With("qos_instance", "evm")

	if len(availableEndpoints) == 0 {
		return nil, nil, errEmptyEndpointListObs
	}

	logger.Info().Msgf("About to filter through %d available endpoints", len(availableEndpoints))

	var filteredEndpointsAddr protocol.EndpointAddrList
	var validationResults []*qosobservations.EndpointValidationResult

	// TODO_FUTURE: use service-specific metrics to add an endpoint ranking method
	// which can be used to assign a rank/score to a valid endpoint to guide endpoint selection.
	for _, availableEndpointAddr := range availableEndpoints {
		logger := logger.With("endpoint_addr", availableEndpointAddr)
		logger.Info().Msg("processing endpoint")

		endpoint, found := ss.endpointStore.endpoints[availableEndpointAddr]
		if !found {
			logger.Warn().Msgf("❓ SKIPPING endpoint %s because it was not found in PATH's endpoint store.", availableEndpointAddr)

			// Create validation result for endpoint not found
			failureDetails := "endpoint not found in PATH's endpoint store"
			result := &qosobservations.EndpointValidationResult{
				EndpointAddr:   string(availableEndpointAddr),
				Success:        false,
				FailureReason:  qosobservations.EndpointValidationFailureReason_ENDPOINT_VALIDATION_FAILURE_REASON_ENDPOINT_NOT_FOUND.Enum(),
				FailureDetails: &failureDetails,
			}
			validationResults = append(validationResults, result)
			continue
		}

		if err := ss.basicEndpointValidation(endpoint); err != nil {
			logger.Warn().Err(err).Msgf("⚠️ SKIPPING %s endpoint because it failed basic validation: %v", availableEndpointAddr, err)

			// Create validation result for validation failure
			failureReason := ss.categorizeValidationFailure(err)
			errorMsg := err.Error()
			result := &qosobservations.EndpointValidationResult{
				EndpointAddr:   string(availableEndpointAddr),
				Success:        false,
				FailureReason:  &failureReason,
				FailureDetails: &errorMsg,
			}
			validationResults = append(validationResults, result)
			continue
		}

		// Endpoint passed validation - record success and add to valid list
		result := &qosobservations.EndpointValidationResult{
			EndpointAddr: string(availableEndpointAddr),
			Success:      true,
			// FailureReason and FailureDetails are nil for successful validations
		}
		validationResults = append(validationResults, result)
		filteredEndpointsAddr = append(filteredEndpointsAddr, availableEndpointAddr)
		logger.Info().Msgf("✅ endpoint passed validation: %s", availableEndpointAddr)
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
// - The endpoint's archival check is invalid, if enabled.
func (ss *serviceState) basicEndpointValidation(endpoint endpoint) error {
	ss.serviceStateLock.RLock()
	defer ss.serviceStateLock.RUnlock()

	// Check if the endpoint has returned an empty response.
	if endpoint.hasReturnedEmptyResponse {
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

	// Check if the endpoint has returned an archival balance for the perceived block number.
	if err := ss.archivalState.isArchivalBalanceValid(endpoint.checkArchival); err != nil {
		return fmt.Errorf("archival balance validation failed: %w", err)
	}

	return nil
}

// isBlockNumberValid returns an error if:
//   - The endpoint has not had an observation of its response to a `eth_blockNumber` request.
//   - The endpoint's block height is less than the perceived block height minus the sync allowance.
func (ss *serviceState) isBlockNumberValid(check endpointCheckBlockNumber) error {
	if ss.perceivedBlockNumber == 0 {
		return errNoBlockNumberObs
	}

	if check.parsedBlockNumberResponse == nil {
		return errNoBlockNumberObs
	}

	// Dereference pointer to show actual block number instead of memory address in error logs
	parsedBlockNumber := *check.parsedBlockNumberResponse

	// If the endpoint's block height is less than the perceived block height minus the sync allowance,
	// then the endpoint is behind the chain and should be filtered out.
	syncAllowance := ss.serviceQoSConfig.getSyncAllowance()
	minAllowedBlockNumber := ss.perceivedBlockNumber - syncAllowance
	if parsedBlockNumber < minAllowedBlockNumber {
		return fmt.Errorf("%w: block number %d is outside the sync allowance relative to min allowed block number %d and sync allowance %d",
			errOutsideSyncAllowanceBlockNumberObs, parsedBlockNumber, minAllowedBlockNumber, syncAllowance)
	}

	return nil
}

// isChainIDValid returns an error if:
//   - The endpoint has not had an observation of its response to a `eth_chainId` request.
//   - The endpoint's chain ID does not match the expected chain ID in the service state.
func (ss *serviceState) isChainIDValid(check endpointCheckChainID) error {
	if check.chainID == nil {
		return errNoChainIDObs
	}

	// Dereference pointer to show actual chain ID instead of memory address in error logs
	chainID := *check.chainID

	expectedChainID := ss.serviceQoSConfig.getEVMChainID()
	if chainID != expectedChainID {
		return fmt.Errorf("%w: chain ID %s does not match expected chain ID %s",
			errInvalidChainIDObs, chainID, expectedChainID)
	}
	return nil
}
