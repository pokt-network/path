package protocol

import (
	"testing"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/assert"
)

func TestPayload_EffectiveRPCType(t *testing.T) {
	tests := []struct {
		name            string
		rpcType         sharedtypes.RPCType
		originalRPCType sharedtypes.RPCType
		expected        sharedtypes.RPCType
	}{
		{
			name:            "no fallback — returns RPCType",
			rpcType:         sharedtypes.RPCType_JSON_RPC,
			originalRPCType: sharedtypes.RPCType_UNKNOWN_RPC,
			expected:        sharedtypes.RPCType_JSON_RPC,
		},
		{
			name:            "no fallback — zero OriginalRPCType returns RPCType",
			rpcType:         sharedtypes.RPCType_REST,
			originalRPCType: 0, // zero value == UNKNOWN_RPC
			expected:        sharedtypes.RPCType_REST,
		},
		{
			name:            "CometBFT fell back to JSON_RPC — returns original CometBFT",
			rpcType:         sharedtypes.RPCType_JSON_RPC,
			originalRPCType: sharedtypes.RPCType_COMET_BFT,
			expected:        sharedtypes.RPCType_COMET_BFT,
		},
		{
			name:            "REST fell back to JSON_RPC — returns original REST",
			rpcType:         sharedtypes.RPCType_JSON_RPC,
			originalRPCType: sharedtypes.RPCType_REST,
			expected:        sharedtypes.RPCType_REST,
		},
		{
			name:            "no fallback — COMET_BFT without original",
			rpcType:         sharedtypes.RPCType_COMET_BFT,
			originalRPCType: sharedtypes.RPCType_UNKNOWN_RPC,
			expected:        sharedtypes.RPCType_COMET_BFT,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Payload{
				RPCType:         tt.rpcType,
				OriginalRPCType: tt.originalRPCType,
			}
			assert.Equal(t, tt.expected, p.EffectiveRPCType())
		})
	}
}
