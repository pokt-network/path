package gateway

import (
	"fmt"
	"strings"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
)

// Canonical RPC type string values used in configuration.
// These exact strings must be used in:
// - Service rpc_types config
// - Health check type field
// - RPC-Type HTTP header (for detection optimization)
const (
	RPCTypeStringJSONRPC   = "json_rpc"
	RPCTypeStringREST      = "rest"
	RPCTypeStringCometBFT  = "comet_bft"
	RPCTypeStringWebSocket = "websocket"
	RPCTypeStringGRPC      = "grpc"
)

// RPCTypeMapper provides bidirectional mapping between config strings and protocol enum values.
type RPCTypeMapper struct{}

// NewRPCTypeMapper creates a new RPC type mapper.
func NewRPCTypeMapper() *RPCTypeMapper {
	return &RPCTypeMapper{}
}

// ParseRPCType converts a configuration string to the corresponding RPCType enum value.
// Performs case-insensitive matching and validates the string is a known type.
//
// Examples:
//   - "json_rpc" → sharedtypes.RPCType_JSON_RPC
//   - "JSON_RPC" → sharedtypes.RPCType_JSON_RPC
//   - "rest" → sharedtypes.RPCType_REST
//
// Returns error if the string is not a valid RPC type.
func (m *RPCTypeMapper) ParseRPCType(configStr string) (sharedtypes.RPCType, error) {
	if configStr == "" {
		return sharedtypes.RPCType_UNKNOWN_RPC, fmt.Errorf("RPC type string cannot be empty")
	}

	// Normalize to uppercase for lookup in poktroll's enum map
	upperStr := strings.ToUpper(configStr)

	// Use poktroll's GetRPCTypeFromConfig() for the actual conversion
	// This ensures we stay synchronized with the canonical enum definition
	rpcType, err := sharedtypes.GetRPCTypeFromConfig(upperStr)
	if err != nil {
		return sharedtypes.RPCType_UNKNOWN_RPC, fmt.Errorf(
			"unknown RPC type '%s': %w. Valid types: %v",
			configStr,
			err,
			GetAllValidRPCTypeStrings(),
		)
	}

	if rpcType == sharedtypes.RPCType_UNKNOWN_RPC {
		return sharedtypes.RPCType_UNKNOWN_RPC, fmt.Errorf(
			"unknown RPC type '%s'. Valid types: %v",
			configStr,
			GetAllValidRPCTypeStrings(),
		)
	}

	return rpcType, nil
}

// ValidateRPCTypeString validates that a string is a valid RPC type without converting it.
// This is useful for config validation where we want to check validity early
// without needing the enum value.
func (m *RPCTypeMapper) ValidateRPCTypeString(str string) error {
	_, err := m.ParseRPCType(str)
	return err
}

// FormatRPCType converts an RPCType enum value to its canonical config string representation.
// This is the inverse of ParseRPCType().
//
// Examples:
//   - sharedtypes.RPCType_JSON_RPC → "json_rpc"
//   - sharedtypes.RPCType_REST → "rest"
//   - sharedtypes.RPCType_COMET_BFT → "comet_bft"
//
// Returns empty string for UNKNOWN_RPC.
func (m *RPCTypeMapper) FormatRPCType(rpcType sharedtypes.RPCType) string {
	switch rpcType {
	case sharedtypes.RPCType_JSON_RPC:
		return RPCTypeStringJSONRPC
	case sharedtypes.RPCType_REST:
		return RPCTypeStringREST
	case sharedtypes.RPCType_COMET_BFT:
		return RPCTypeStringCometBFT
	case sharedtypes.RPCType_WEBSOCKET:
		return RPCTypeStringWebSocket
	case sharedtypes.RPCType_GRPC:
		return RPCTypeStringGRPC
	case sharedtypes.RPCType_UNKNOWN_RPC:
		return ""
	default:
		return ""
	}
}

// GetAllValidRPCTypeStrings returns a list of all valid RPC type configuration strings.
// This is useful for displaying available options in error messages and documentation.
func GetAllValidRPCTypeStrings() []string {
	return []string{
		RPCTypeStringJSONRPC,
		RPCTypeStringREST,
		RPCTypeStringCometBFT,
		RPCTypeStringWebSocket,
		RPCTypeStringGRPC,
	}
}

// GetHTTPBasedRPCTypeStrings returns only the RPC types that use HTTP delivery.
// This excludes websocket and grpc which use different transport protocols.
func GetHTTPBasedRPCTypeStrings() []string {
	return []string{
		RPCTypeStringJSONRPC,
		RPCTypeStringREST,
		RPCTypeStringCometBFT,
	}
}

// IsHTTPBasedRPCType checks if an RPC type string represents an HTTP-based protocol.
func IsHTTPBasedRPCType(rpcTypeStr string) bool {
	switch strings.ToLower(rpcTypeStr) {
	case RPCTypeStringJSONRPC, RPCTypeStringREST, RPCTypeStringCometBFT:
		return true
	default:
		return false
	}
}
