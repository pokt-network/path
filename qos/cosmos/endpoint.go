package cosmos

import (
	"time"
)

// endpoint maintains QoS data on a single endpoint for a CosmosSDK-based blockchain service.
type endpoint struct {
	// Response validation tracking
	hasReturnedEmptyResponse     bool
	hasReturnedInvalidResponse   bool
	hasReturnedUnmarshalingError bool
	invalidResponseLastObserved  *time.Time

	// *** CometBFT-specific checks ***

	// Checks chain ID, catching up status, and latest block height via JSON-RPC `status`
	checkCometBFTStatus endpointCheckCometBFTStatus
	// Checks node health via JSON-RPC `health`
	checkCometBFTHealth endpointCheckCometBFTHealth

	// *** CosmosSDK-specific checks ***
	// Checks Cosmos SDK status via REST `/cosmos/base/node/v1beta1/status`
	checkCosmosStatus endpointCheckCosmosStatus

	// *** EVM-specific checks ***
	// Checks EVM chain ID via eth_chainId
	checkEVMChainID endpointCheckEVMChainID

	// lastSeen is updated when the endpoint appears in an active session.
	// Used by stale endpoint cleanup to remove entries that are no longer active.
	lastSeen time.Time
}
