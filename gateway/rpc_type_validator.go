package gateway

import (
	"fmt"

	"github.com/pokt-network/path/protocol"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
)

var (
	// ErrUnsupportedRPCType is returned when a detected RPC type is not in service's rpc_types config
	ErrUnsupportedRPCType = fmt.Errorf("unsupported RPC type for service")

	// ErrRPCTypeDetectionFailed is returned when RPC type detection fails
	ErrRPCTypeDetectionFailed = fmt.Errorf("RPC type detection failed")

	// ErrServiceNotConfigured is returned when a requested service is not configured
	ErrServiceNotConfigured = fmt.Errorf("service not configured")
)

// RPCTypeValidator validates RPC types against service configuration
type RPCTypeValidator struct {
	unifiedConfig *UnifiedServicesConfig
	rpcTypeMapper *RPCTypeMapper
}

// NewRPCTypeValidator creates a new RPC type validator
func NewRPCTypeValidator(
	unifiedConfig *UnifiedServicesConfig,
	rpcTypeMapper *RPCTypeMapper,
) *RPCTypeValidator {
	return &RPCTypeValidator{
		unifiedConfig: unifiedConfig,
		rpcTypeMapper: rpcTypeMapper,
	}
}

// ValidateRPCType checks if the detected RPC type is in service's configured rpc_types list
func (v *RPCTypeValidator) ValidateRPCType(
	serviceID protocol.ServiceID,
	rpcType sharedtypes.RPCType,
) error {
	// Get service's configured RPC types
	serviceRPCTypes := v.unifiedConfig.GetServiceRPCTypes(serviceID)
	if len(serviceRPCTypes) == 0 {
		return fmt.Errorf("%w: service '%s' has no configured rpc_types", ErrServiceNotConfigured, serviceID)
	}

	// Convert detected enum to string
	detectedRPCTypeStr := v.rpcTypeMapper.FormatRPCType(rpcType)

	// Check if detected type is in allowed list
	for _, allowedType := range serviceRPCTypes {
		if allowedType == detectedRPCTypeStr {
			// Valid RPC type
			return nil
		}
	}

	// RPC type not in allowed list
	return fmt.Errorf(
		"%w: service '%s' does not support RPC type '%s'. Allowed types: %v",
		ErrUnsupportedRPCType,
		serviceID,
		detectedRPCTypeStr,
		serviceRPCTypes,
	)
}

// ValidateServiceConfigured checks if a service is configured in UnifiedServicesConfig
func (v *RPCTypeValidator) ValidateServiceConfigured(serviceID protocol.ServiceID) error {
	if !v.unifiedConfig.HasService(serviceID) {
		configuredServices := v.unifiedConfig.GetConfiguredServiceIDs()
		return fmt.Errorf(
			"%w: service '%s' not configured. Available services: %v",
			ErrServiceNotConfigured,
			serviceID,
			configuredServices,
		)
	}
	return nil
}
