package shannon

import (
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/protocol"
)

// =============================================================================
// RPC Type Fallback Tests
// =============================================================================

func TestGetRPCTypeFallback_Success(t *testing.T) {
	// Create a protocol with unified services config that includes fallbacks
	unifiedConfig := &gateway.UnifiedServicesConfig{
		Services: []gateway.ServiceConfig{
			{
				ID:       protocol.ServiceID("cosmoshub"),
				RPCTypes: []string{"json_rpc", "rest", "comet_bft"},
				RPCTypeFallbacks: map[string]string{
					"COMET_BFT": "JSON_RPC",
					"REST":      "JSON_RPC",
				},
			},
			{
				ID:       protocol.ServiceID("osmosis"),
				RPCTypes: []string{"json_rpc", "comet_bft"},
				RPCTypeFallbacks: map[string]string{
					"COMET_BFT": "JSON_RPC",
				},
			},
		},
	}

	p := &Protocol{
		logger:                polyzero.NewLogger(),
		unifiedServicesConfig: unifiedConfig,
	}

	// Test comet_bft -> json_rpc fallback for cosmoshub
	fallbackRPCType, hasFallback := p.getRPCTypeFallback(
		protocol.ServiceID("cosmoshub"),
		sharedtypes.RPCType_COMET_BFT,
	)

	require.True(t, hasFallback, "Should have fallback configured")
	require.Equal(t, sharedtypes.RPCType_JSON_RPC, fallbackRPCType, "Should fall back to JSON_RPC")

	// Test rest -> json_rpc fallback for cosmoshub
	fallbackRPCType, hasFallback = p.getRPCTypeFallback(
		protocol.ServiceID("cosmoshub"),
		sharedtypes.RPCType_REST,
	)

	require.True(t, hasFallback, "Should have fallback configured")
	require.Equal(t, sharedtypes.RPCType_JSON_RPC, fallbackRPCType, "Should fall back to JSON_RPC")

	// Test comet_bft -> json_rpc fallback for osmosis
	fallbackRPCType, hasFallback = p.getRPCTypeFallback(
		protocol.ServiceID("osmosis"),
		sharedtypes.RPCType_COMET_BFT,
	)

	require.True(t, hasFallback, "Should have fallback configured")
	require.Equal(t, sharedtypes.RPCType_JSON_RPC, fallbackRPCType, "Should fall back to JSON_RPC")
}

func TestGetRPCTypeFallback_NoFallback(t *testing.T) {
	unifiedConfig := &gateway.UnifiedServicesConfig{
		Services: []gateway.ServiceConfig{
			{
				ID:       protocol.ServiceID("eth"),
				RPCTypes: []string{"json_rpc", "websocket"},
				// No fallbacks configured
			},
			{
				ID:       protocol.ServiceID("cosmoshub"),
				RPCTypes: []string{"json_rpc", "comet_bft"},
				RPCTypeFallbacks: map[string]string{
					"COMET_BFT": "JSON_RPC",
				},
			},
		},
	}

	p := &Protocol{
		logger:                polyzero.NewLogger(),
		unifiedServicesConfig: unifiedConfig,
	}

	// Test service with no fallbacks configured
	_, hasFallback := p.getRPCTypeFallback(
		protocol.ServiceID("eth"),
		sharedtypes.RPCType_WEBSOCKET,
	)

	require.False(t, hasFallback, "Should not have fallback for eth/websocket")

	// Test RPC type with no fallback configured
	_, hasFallback = p.getRPCTypeFallback(
		protocol.ServiceID("cosmoshub"),
		sharedtypes.RPCType_REST,
	)

	require.False(t, hasFallback, "Should not have fallback for cosmoshub/rest")

	// Test non-existent service
	_, hasFallback = p.getRPCTypeFallback(
		protocol.ServiceID("nonexistent"),
		sharedtypes.RPCType_JSON_RPC,
	)

	require.False(t, hasFallback, "Should not have fallback for non-existent service")
}

func TestGetRPCTypeFallback_NilConfig(t *testing.T) {
	p := &Protocol{
		logger:                polyzero.NewLogger(),
		unifiedServicesConfig: nil,
	}

	_, hasFallback := p.getRPCTypeFallback(
		protocol.ServiceID("cosmoshub"),
		sharedtypes.RPCType_COMET_BFT,
	)

	require.False(t, hasFallback, "Should not have fallback when config is nil")
}

func TestGetRPCTypeFallback_CaseInsensitive(t *testing.T) {
	unifiedConfig := &gateway.UnifiedServicesConfig{
		Services: []gateway.ServiceConfig{
			{
				ID:       protocol.ServiceID("cosmoshub"),
				RPCTypes: []string{"json_rpc", "comet_bft"},
				RPCTypeFallbacks: map[string]string{
					// Using lowercase in config
					"comet_bft": "json_rpc",
				},
			},
		},
	}

	p := &Protocol{
		logger:                polyzero.NewLogger(),
		unifiedServicesConfig: unifiedConfig,
	}

	// The RPC type constant uses uppercase COMET_BFT, but config uses lowercase
	fallbackRPCType, hasFallback := p.getRPCTypeFallback(
		protocol.ServiceID("cosmoshub"),
		sharedtypes.RPCType_COMET_BFT,
	)

	require.True(t, hasFallback, "Should handle case variations")
	require.Equal(t, sharedtypes.RPCType_JSON_RPC, fallbackRPCType, "Should fall back to JSON_RPC")
}

func TestGetRPCTypeFallback_InvalidFallbackType(t *testing.T) {
	unifiedConfig := &gateway.UnifiedServicesConfig{
		Services: []gateway.ServiceConfig{
			{
				ID:       protocol.ServiceID("cosmoshub"),
				RPCTypes: []string{"json_rpc", "comet_bft"},
				RPCTypeFallbacks: map[string]string{
					"COMET_BFT": "INVALID_RPC_TYPE",
				},
			},
		},
	}

	p := &Protocol{
		logger:                polyzero.NewLogger(),
		unifiedServicesConfig: unifiedConfig,
	}

	_, hasFallback := p.getRPCTypeFallback(
		protocol.ServiceID("cosmoshub"),
		sharedtypes.RPCType_COMET_BFT,
	)

	require.False(t, hasFallback, "Should return false for invalid fallback RPC type")
}

func TestGetRPCTypeFallback_AllRPCTypes(t *testing.T) {
	// Test that all valid RPC types can be used as fallbacks
	unifiedConfig := &gateway.UnifiedServicesConfig{
		Services: []gateway.ServiceConfig{
			{
				ID:       protocol.ServiceID("test"),
				RPCTypes: []string{"json_rpc", "rest", "comet_bft", "websocket", "grpc"},
				RPCTypeFallbacks: map[string]string{
					"COMET_BFT": "JSON_RPC",
					"REST":      "COMET_BFT",
					"WEBSOCKET": "REST",
					"GRPC":      "JSON_RPC",
				},
			},
		},
	}

	p := &Protocol{
		logger:                polyzero.NewLogger(),
		unifiedServicesConfig: unifiedConfig,
	}

	tests := []struct {
		requestedType sharedtypes.RPCType
		expectedType  sharedtypes.RPCType
	}{
		{sharedtypes.RPCType_COMET_BFT, sharedtypes.RPCType_JSON_RPC},
		{sharedtypes.RPCType_REST, sharedtypes.RPCType_COMET_BFT},
		{sharedtypes.RPCType_WEBSOCKET, sharedtypes.RPCType_REST},
		{sharedtypes.RPCType_GRPC, sharedtypes.RPCType_JSON_RPC},
	}

	for _, tt := range tests {
		t.Run(tt.requestedType.String(), func(t *testing.T) {
			fallbackRPCType, hasFallback := p.getRPCTypeFallback(
				protocol.ServiceID("test"),
				tt.requestedType,
			)

			require.True(t, hasFallback, "Should have fallback for %s", tt.requestedType.String())
			require.Equal(t, tt.expectedType, fallbackRPCType, "Incorrect fallback type for %s", tt.requestedType.String())
		})
	}
}
