package cosmos

import (
	"errors"
	"fmt"
	"time"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
)

var (
	errEmptyResponseObs         = errors.New("endpoint is invalid: history of empty responses")
	errRecentInvalidResponseObs = errors.New("endpoint is invalid: recent invalid response")
)

// basicEndpointValidation returns an error if the supplied endpoint is not
// valid based on the perceived state of the CosmosSDK blockchain.
//
// It returns an error if the endpoint has recently returned:
//   - An empty response.
//   - An unmarshaling error.
//   - An invalid response.
//
// CometBFT-specific checks if the endpoint has recently returned:
//   - 'status' - invalid chain ID
//   - 'status' - catching up
//   - 'status' - block height outside sync allowance
//   - 'health' - unhealthy
//
// CosmosSDK-specific checks if the endpoint has recently returned:
//   - /cosmos/base/node/v1beta1/status - block height outside sync allowance
func (ss *serviceState) basicEndpointValidation(endpoint endpoint) error {
	ss.serviceStateLock.RLock()
	defer ss.serviceStateLock.RUnlock()

	// Check if the endpoint has returned an empty response.
	if endpoint.hasReturnedEmptyResponse {
		return fmt.Errorf("empty response validation failed: %w", errEmptyResponseObs)
	}

	// Check if the endpoint has returned an unmarshaling error within the last 30 minutes.
	if endpoint.hasReturnedUnmarshalingError && endpoint.invalidResponseLastObserved != nil {
		timeSinceInvalidResponse := time.Since(*endpoint.invalidResponseLastObserved)
		if timeSinceInvalidResponse < invalidResponseTimeout {
			return fmt.Errorf("recent unmarshaling error validation failed (%.0f minutes ago): %w",
				timeSinceInvalidResponse.Minutes(), errRecentInvalidResponseObs)
		}
	}

	// Check if the endpoint has returned an invalid response within the invalid response timeout period.
	if endpoint.hasReturnedInvalidResponse && endpoint.invalidResponseLastObserved != nil {
		timeSinceInvalidResponse := time.Since(*endpoint.invalidResponseLastObserved)
		if timeSinceInvalidResponse < invalidResponseTimeout {
			return fmt.Errorf("recent response validation failed (%.0f minutes ago): %w",
				timeSinceInvalidResponse.Minutes(), errRecentInvalidResponseObs)
		}
	}

	// Get the RPC types supported by the CosmosSDK service.
	supportedAPIs := ss.serviceQoSConfig.getSupportedAPIs()

	// If the service supports CometBFT, validate the endpoint's CometBFT checks.
	if _, ok := supportedAPIs[sharedtypes.RPCType_COMET_BFT]; ok {
		if err := ss.validateEndpointCometBFTChecks(endpoint); err != nil {
			return fmt.Errorf("cometBFT validation failed: %w", err)
		}
	}

	// If the service supports CosmosSDK, validate the endpoint's CosmosSDK checks.
	if _, ok := supportedAPIs[sharedtypes.RPCType_REST]; ok {
		if err := ss.validateEndpointCosmosSDKChecks(endpoint); err != nil {
			return fmt.Errorf("cosmos SDK validation failed: %w", err)
		}
	}

	// If the service supports EVM, validate the endpoint's EVM checks.
	if _, ok := supportedAPIs[sharedtypes.RPCType_JSON_RPC]; ok {
		if err := ss.validateEndpointEVMChecks(endpoint); err != nil {
			return fmt.Errorf("EVM validation failed: %w", err)
		}
	}

	return nil
}

// validateEndpointCometBFTChecks validates the endpoint's CometBFT checks:
// - Health status via `health` method
// - Chain ID and sync status via `status` method
// - Block height within acceptable sync tolerance
func (ss *serviceState) validateEndpointCometBFTChecks(endpoint endpoint) error {
	// Check if the endpoint's health status is valid.
	if err := ss.isCometBFTHealthValid(endpoint.checkCometBFTHealth); err != nil {
		return fmt.Errorf("cometBFT health validation failed: %w", err)
	}

	// Check if the endpoint's status information is valid.
	if err := ss.isCometBFTStatusValid(endpoint.checkCometBFTStatus); err != nil {
		return fmt.Errorf("cometBFT status validation failed: %w", err)
	}

	// Check if the endpoint's block height is valid.
	if err := ss.isCometBFTBlockHeightValid(endpoint.checkCometBFTStatus); err != nil {
		return fmt.Errorf("cometBFT block height validation failed: %w", err)
	}

	return nil
}

// isCometBFTHealthValid returns an error if:
//   - The endpoint has not had an observation of its response to a `/health` request.
//   - The endpoint's health check indicates it's unhealthy.
func (ss *serviceState) isCometBFTHealthValid(check endpointCheckCometBFTHealth) error {
	healthy, err := check.GetHealthy()
	if err != nil {
		return fmt.Errorf("%w: %v", errNoHealthObs, err)
	}

	if !healthy {
		return fmt.Errorf("%w: endpoint reported unhealthy status", errInvalidHealthObs)
	}

	return nil
}

// isCometBFTStatusValid returns an error if:
//   - The endpoint has not had an observation of its response to a `status` method request.
//   - The endpoint's chain ID does not match the expected chain ID.
//   - The endpoint is catching up to the network.
//
// This method intentionally does not check the block height sync allowance
// because it is checked separately in the `isCometBFTBlockHeightValid` method.
func (ss *serviceState) isCometBFTStatusValid(check endpointCheckCometBFTStatus) error {
	// Check chain ID
	chainID, err := check.GetChainID()
	if err != nil {
		return fmt.Errorf("%w: %v", errNoCometBFTStatusObs, err)
	}

	expectedChainID := ss.serviceQoSConfig.getCosmosSDKChainID()
	if chainID != expectedChainID {
		return fmt.Errorf("%w: chain ID %s does not match expected chain ID %s",
			errInvalidCometBFTChainIDObs, chainID, expectedChainID)
	}

	// Check if the endpoint is catching up to the network.
	catchingUp, err := check.GetCatchingUp()
	if err != nil {
		return fmt.Errorf("%w: %v", errNoCometBFTStatusObs, err)
	}

	if catchingUp {
		return fmt.Errorf("%w: endpoint is catching up to the network", errCatchingUpCometBFTObs)
	}

	return nil
}

// isCometBFTBlockHeightValid returns an error if:
//   - The endpoint has not had an observation of its response to a `status` methodrequest.
//   - The endpoint's block height is outside the sync allowance.
//
// This method is intentionally kept separate from the other CometBFT `status` method checks
// so that correct status can be checked without validating against the perceived block number.
func (ss *serviceState) isCometBFTBlockHeightValid(check endpointCheckCometBFTStatus) error {
	// Check if the endpoint's block height is within the sync allowance.
	latestBlockHeight, err := check.GetLatestBlockHeight()
	if err != nil {
		return fmt.Errorf("%w: %v", errNoCometBFTStatusObs, err)
	}
	if err := ss.validateBlockHeightSyncAllowance(latestBlockHeight); err != nil {
		return fmt.Errorf("cometBFT block height sync allowance validation failed: %w", err)
	}

	return nil
}

// validateEndpointCosmosSDKChecks validates the endpoint's CosmosSDK checks.
// Checks:
//   - Status information (block height)
func (ss *serviceState) validateEndpointCosmosSDKChecks(endpoint endpoint) error {
	// Check if the endpoint's Cosmos SDK status information is valid.
	if err := ss.isCosmosStatusValid(endpoint.checkCosmosStatus); err != nil {
		return fmt.Errorf("cosmos SDK status validation failed: %w", err)
	}

	return nil
}

// isCosmosStatusValid returns an error if:
//   - The endpoint has not had an observation of its response to a `/cosmos/base/node/v1beta1/status` request.
//   - The endpoint's block height is outside the sync allowance.
func (ss *serviceState) isCosmosStatusValid(check endpointCheckCosmosStatus) error {
	// Check if the endpoint's block height is within the sync allowance.
	latestBlockHeight, err := check.GetHeight()
	if err != nil {
		return fmt.Errorf("%w: %v", errNoCosmosStatusObs, err)
	}
	if err := ss.validateBlockHeightSyncAllowance(latestBlockHeight); err != nil {
		return fmt.Errorf("cosmos SDK block height sync allowance validation failed: %w", err)
	}

	return nil
}

// validateBlockHeightSyncAllowance returns an error if:
//   - The endpoint's block height is outside the latest block height minus the sync allowance.
//
// Returns nil (passes) if:
//   - sync_allowance is 0 (check disabled)
//   - No perceived block number yet (no chain data to compare against)
//   - Endpoint has no block number observation (latestBlockHeight is 0)
func (ss *serviceState) validateBlockHeightSyncAllowance(latestBlockHeight uint64) error {
	syncAllowance := ss.serviceQoSConfig.getSyncAllowance()

	// If sync allowance is 0, the check is disabled
	if syncAllowance == 0 {
		ss.logger.Warn().
			Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
			Msg("🔍 Sync allowance check DISABLED (sync_allowance=0)")
		return nil
	}

	// If we don't have a perceived block number yet, skip the check (no data to compare against)
	if ss.perceivedBlockNumber == 0 {
		ss.logger.Warn().
			Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
			Uint64("sync_allowance", syncAllowance).
			Msg("🔍 Sync allowance check SKIPPED (no perceived block number yet)")
		return nil
	}

	// If endpoint has no block number observation, skip the check (no endpoint data to validate)
	if latestBlockHeight == 0 {
		ss.logger.Warn().
			Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
			Uint64("perceived_block", ss.perceivedBlockNumber).
			Uint64("sync_allowance", syncAllowance).
			Msg("🔍 Sync allowance check SKIPPED (endpoint has no block number observation)")
		return nil
	}

	ss.logger.Warn().
		Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
		Uint64("sync_allowance", syncAllowance).
		Uint64("perceived_block", ss.perceivedBlockNumber).
		Uint64("endpoint_block", latestBlockHeight).
		Msg("🔍 Sync allowance check ENABLED")

	minAllowedBlockNumber := ss.perceivedBlockNumber - syncAllowance
	if latestBlockHeight < minAllowedBlockNumber {
		ss.logger.Warn().
			Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
			Uint64("endpoint_block", latestBlockHeight).
			Uint64("min_allowed_block", minAllowedBlockNumber).
			Uint64("sync_allowance", syncAllowance).
			Msg("❌ Endpoint failed sync allowance check - too far behind")
		return fmt.Errorf("%w: block number %d is outside the sync allowance relative to min allowed block number %d and sync allowance %d",
			errOutsideSyncAllowanceBlockNumberObs, latestBlockHeight, minAllowedBlockNumber, syncAllowance)
	}
	return nil
}

// validateEndpointEVMChecks validates the endpoint's EVM checks.
// Checks:
//   - EVM Chain ID matches expected EVM Chain ID.
func (ss *serviceState) validateEndpointEVMChecks(endpoint endpoint) error {
	if err := ss.isEVMChainIDValid(endpoint.checkEVMChainID); err != nil {
		return fmt.Errorf("EVM chain ID validation failed: %w", err)
	}

	return nil
}

// isEVMChainIDValid returns an error if:
//   - The endpoint has not had an observation of its response to a `eth_chainId` request.
//   - The endpoint's chain ID does not match the expected chain ID.
func (ss *serviceState) isEVMChainIDValid(check endpointCheckEVMChainID) error {
	evmChainID, err := check.GetChainID()
	if err != nil {
		return err
	}

	expectedEVMChainID := ss.serviceQoSConfig.getEVMChainID()
	if evmChainID != expectedEVMChainID {
		return fmt.Errorf("%w: chain ID %s does not match expected chain ID %s",
			errInvalidEVMChainIDObs, evmChainID, expectedEVMChainID)
	}

	return nil
}
