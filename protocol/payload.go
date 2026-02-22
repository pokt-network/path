package protocol

import (
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
)

// Payload represents the HTTP(s) requests proxied between clients and backend services.
// TODO_DOCUMENT(@adshmh): Add more examples (e.g. for RESTful services)
// TODO_IMPROVE(@adshmh): Use an interface here that returns the serialized form of the request.
type Payload struct {
	Data    string
	Method  string
	Path    string
	Headers map[string]string
	// RPCType indicates the type of RPC protocol for routing to appropriate backend ports:
	// - RPCType_REST: Cosmos SDK REST API (typically port 1317)
	// - RPCType_COMET_BFT: CometBFT RPC (typically port 26657)
	// - RPCType_JSON_RPC: EVM JSON-RPC (typically port 8545)
	RPCType sharedtypes.RPCType

	// OriginalRPCType preserves the RPC type detected from the request before any
	// fallback was applied. When COMET_BFT falls back to JSON_RPC for routing
	// (because no suppliers stake comet_bft endpoints), this field retains
	// COMET_BFT so downstream analysis (e.g., heuristic error detection) can
	// make protocol-aware decisions.
	// Zero value (UNKNOWN_RPC) means no fallback occurred — use RPCType directly.
	OriginalRPCType sharedtypes.RPCType

	// JSONRPCMethod is the JSON-RPC method name (e.g., "eth_blockNumber", "eth_getLogs").
	// Set at QoS request parsing time. Used by the heuristic analyzer for method-aware
	// response validation (e.g., "result":[] is valid for eth_getLogs but not eth_blockNumber).
	// Empty for non-JSON-RPC requests (REST, WebSocket).
	JSONRPCMethod string
}

// EffectiveRPCType returns the most specific RPC type for protocol-aware analysis.
// If OriginalRPCType is set (fallback occurred), returns that; otherwise returns RPCType.
func (p Payload) EffectiveRPCType() sharedtypes.RPCType {
	if p.OriginalRPCType != sharedtypes.RPCType_UNKNOWN_RPC {
		return p.OriginalRPCType
	}
	return p.RPCType
}

// EmptyPayload returns an empty payload struct.
// It should only be used when an error is encountered and the actual request cannot be
// proxied, parsed or otherwise processed.
func EmptyErrorPayload() Payload {
	return Payload{
		// This is an intentional placeholder to distinguish errors in retrieving payloads
		// from explicit empty payloads set by PATH.
		Data:    "PATH_EmptyErrorPayload",
		Method:  "",
		Path:    "",
		Headers: map[string]string{},
		RPCType: sharedtypes.RPCType_UNKNOWN_RPC,
	}
}
