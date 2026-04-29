package metrics

import (
	"github.com/pokt-network/path/qos/heuristic"
)

// Bucket constants used by SanitizeMethodLabel when an incoming method does not
// match any allowlist. Kept as named constants so dashboards / queries can refer
// to them stably.
const (
	// MethodUnknown — QoS layer produced no method (parse failure, missing payload).
	MethodUnknown = "unknown"

	// MethodOther — QoS gave us a method, but no namespace match.
	MethodOther = "other"

	// MethodOtherEVM — recognized eth_*, net_*, web3_*, debug_*, trace_*, txpool_*,
	// admin_*, erigon_* prefix but the method itself is not on the allowlist.
	MethodOtherEVM = "eth_other"

	// MethodOtherSolana — recognized as a Solana JSON-RPC shape (camelCase, no
	// underscore prefix) but the method itself is not on the allowlist.
	MethodOtherSolana = "solana_other"

	// MethodOtherCosmos — recognized as a Cosmos JSON-RPC method (REST path or
	// known prefix) but not on the allowlist.
	MethodOtherCosmos = "cosmos_other"

	// MethodOtherCometBFT — recognized as a CometBFT method shape but not on the
	// allowlist.
	MethodOtherCometBFT = "cometbft_other"

	// MethodLabelMaxLen — hard cap on the length of any method label value
	// reaching .WithLabelValues(). Belt-and-suspenders against future regressions
	// where a path or method-shaped string slips past the bucketers above.
	MethodLabelMaxLen = 64
)

// evmAllowlist holds the canonical Ethereum JSON-RPC method names plus standard
// Geth/Erigon namespaces. Source: https://ethereum.github.io/execution-apis/api-documentation/
// and the Geth/Erigon RPC documentation.
//
// Methods not on this list with a recognized namespace prefix bucket to
// MethodOtherEVM. Methods with no recognized prefix bucket to MethodOther.
//
// Update path: when a new spec method is added upstream, append it here. The
// MethodOtherEVM bucket already absorbs unknown methods so missing entries
// degrade gracefully (no cardinality leak, just less-granular dashboards).
var evmAllowlist = map[string]struct{}{
	// eth_* (Ethereum JSON-RPC spec)
	"eth_blockNumber":                         {},
	"eth_chainId":                             {},
	"eth_protocolVersion":                     {},
	"eth_syncing":                             {},
	"eth_coinbase":                            {},
	"eth_accounts":                            {},
	"eth_gasPrice":                            {},
	"eth_maxPriorityFeePerGas":                {},
	"eth_feeHistory":                          {},
	"eth_blobBaseFee":                         {},
	"eth_getBalance":                          {},
	"eth_getStorageAt":                        {},
	"eth_getTransactionCount":                 {},
	"eth_getCode":                             {},
	"eth_call":                                {},
	"eth_callMany":                            {},
	"eth_simulateV1":                          {},
	"eth_estimateGas":                         {},
	"eth_createAccessList":                    {},
	"eth_getBlockByHash":                      {},
	"eth_getBlockByNumber":                    {},
	"eth_getBlockTransactionCountByHash":      {},
	"eth_getBlockTransactionCountByNumber":    {},
	"eth_getUncleCountByBlockHash":            {},
	"eth_getUncleCountByBlockNumber":          {},
	"eth_getBlockReceipts":                    {},
	"eth_getTransactionByHash":                {},
	"eth_getTransactionByBlockHashAndIndex":   {},
	"eth_getTransactionByBlockNumberAndIndex": {},
	"eth_getTransactionReceipt":               {},
	"eth_getLogs":                             {},
	"eth_newFilter":                           {},
	"eth_newBlockFilter":                      {},
	"eth_newPendingTransactionFilter":         {},
	"eth_uninstallFilter":                     {},
	"eth_getFilterChanges":                    {},
	"eth_getFilterLogs":                       {},
	"eth_sendRawTransaction":                  {},
	"eth_sendTransaction":                     {},
	"eth_sign":                                {},
	"eth_signTransaction":                     {},
	"eth_subscribe":                           {},
	"eth_unsubscribe":                         {},
	"eth_getProof":                            {},

	// net_*
	"net_version":   {},
	"net_listening": {},
	"net_peerCount": {},

	// web3_*
	"web3_clientVersion": {},
	"web3_sha3":          {},

	// debug_* (Geth / Erigon / Reth)
	"debug_traceTransaction":             {},
	"debug_traceCall":                    {},
	"debug_traceCallMany":                {},
	"debug_traceBlockByNumber":           {},
	"debug_traceBlockByHash":             {},
	"debug_traceBlock":                   {},
	"debug_storageRangeAt":               {},
	"debug_getRawBlock":                  {},
	"debug_getRawHeader":                 {},
	"debug_getRawReceipts":               {},
	"debug_getRawTransaction":            {},
	"debug_getBadBlocks":                 {},
	"debug_getModifiedAccountsByNumber":  {},
	"debug_getModifiedAccountsByHash":    {},

	// trace_* (Erigon / OpenEthereum)
	"trace_block":                    {},
	"trace_call":                     {},
	"trace_callMany":                 {},
	"trace_filter":                   {},
	"trace_get":                      {},
	"trace_rawTransaction":           {},
	"trace_replayBlockTransactions":  {},
	"trace_replayTransaction":        {},
	"trace_transaction":              {},

	// txpool_*
	"txpool_content": {},
	"txpool_inspect": {},
	"txpool_status":  {},

	// erigon_*
	"erigon_blockNumber":       {},
	"erigon_getHeaderByNumber": {},
	"erigon_getHeaderByHash":   {},

	// admin_*
	"admin_nodeInfo": {},
	"admin_peers":    {},
}

// solanaAllowlist holds the canonical Solana JSON-RPC method names.
// Source: https://solana.com/docs/rpc/http
var solanaAllowlist = map[string]struct{}{
	"getAccountInfo":                    {},
	"getBalance":                        {},
	"getBlock":                          {},
	"getBlockCommitment":                {},
	"getBlockHeight":                    {},
	"getBlockProduction":                {},
	"getBlocks":                         {},
	"getBlocksWithLimit":                {},
	"getBlockTime":                      {},
	"getClusterNodes":                   {},
	"getEpochInfo":                      {},
	"getEpochSchedule":                  {},
	"getFeeForMessage":                  {},
	"getFirstAvailableBlock":            {},
	"getGenesisHash":                    {},
	"getHealth":                         {},
	"getHighestSnapshotSlot":            {},
	"getIdentity":                       {},
	"getInflationGovernor":              {},
	"getInflationRate":                  {},
	"getInflationReward":                {},
	"getLargestAccounts":                {},
	"getLatestBlockhash":                {},
	"getLeaderSchedule":                 {},
	"getMaxRetransmitSlot":              {},
	"getMaxShredInsertSlot":             {},
	"getMinimumBalanceForRentExemption": {},
	"getMultipleAccounts":               {},
	"getProgramAccounts":                {},
	"getRecentPerformanceSamples":       {},
	"getRecentPrioritizationFees":       {},
	"getSignaturesForAddress":           {},
	"getSignatureStatuses":              {},
	"getSlot":                           {},
	"getSlotLeader":                     {},
	"getSlotLeaders":                    {},
	"getStakeMinimumDelegation":         {},
	"getSupply":                         {},
	"getTokenAccountBalance":            {},
	"getTokenAccountsByDelegate":        {},
	"getTokenAccountsByOwner":           {},
	"getTokenLargestAccounts":           {},
	"getTokenSupply":                    {},
	"getTransaction":                    {},
	"getTransactionCount":               {},
	"getVersion":                        {},
	"getVoteAccounts":                   {},
	"isBlockhashValid":                  {},
	"minimumLedgerSlot":                 {},
	"requestAirdrop":                    {},
	"sendTransaction":                   {},
	"simulateTransaction":               {},
}

// cometBFTAllowlist is derived at init time from heuristic.CometBFTMethodList()
// — single source of truth, no drift. If a method is added/removed in the
// heuristic package, this allowlist updates automatically on the next build.
var cometBFTAllowlist = func() map[string]struct{} {
	names := heuristic.CometBFTMethodList()
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[n] = struct{}{}
	}
	return m
}()

// cosmosJSONRPCAllowlist covers Cosmos chains' non-CometBFT JSON-RPC methods.
// Most Cosmos JSON-RPC traffic is CometBFT (covered above); this is the small
// remainder.
var cosmosJSONRPCAllowlist = map[string]struct{}{
	"check_tx":           {},
	"broadcast_evidence": {},
}
