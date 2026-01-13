package gateway

import (
	"testing"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRPCTypeMapper_ParseRPCType(t *testing.T) {
	mapper := NewRPCTypeMapper()

	tests := []struct {
		name        string
		input       string
		expected    sharedtypes.RPCType
		expectError bool
	}{
		// Valid lowercase inputs (canonical format)
		{
			name:        "parse json_rpc",
			input:       "json_rpc",
			expected:    sharedtypes.RPCType_JSON_RPC,
			expectError: false,
		},
		{
			name:        "parse rest",
			input:       "rest",
			expected:    sharedtypes.RPCType_REST,
			expectError: false,
		},
		{
			name:        "parse comet_bft",
			input:       "comet_bft",
			expected:    sharedtypes.RPCType_COMET_BFT,
			expectError: false,
		},
		{
			name:        "parse websocket",
			input:       "websocket",
			expected:    sharedtypes.RPCType_WEBSOCKET,
			expectError: false,
		},
		{
			name:        "parse grpc",
			input:       "grpc",
			expected:    sharedtypes.RPCType_GRPC,
			expectError: false,
		},
		// Case-insensitive inputs
		{
			name:        "parse JSON_RPC uppercase",
			input:       "JSON_RPC",
			expected:    sharedtypes.RPCType_JSON_RPC,
			expectError: false,
		},
		{
			name:        "parse Json_Rpc mixed case",
			input:       "Json_Rpc",
			expected:    sharedtypes.RPCType_JSON_RPC,
			expectError: false,
		},
		{
			name:        "parse REST uppercase",
			input:       "REST",
			expected:    sharedtypes.RPCType_REST,
			expectError: false,
		},
		// Invalid inputs
		{
			name:        "empty string returns error",
			input:       "",
			expected:    sharedtypes.RPCType_UNKNOWN_RPC,
			expectError: true,
		},
		{
			name:        "unknown type returns error",
			input:       "unknown",
			expected:    sharedtypes.RPCType_UNKNOWN_RPC,
			expectError: true,
		},
		{
			name:        "invalid type returns error",
			input:       "invalid_type",
			expected:    sharedtypes.RPCType_UNKNOWN_RPC,
			expectError: true,
		},
		{
			name:        "old jsonrpc format not recognized",
			input:       "jsonrpc",
			expected:    sharedtypes.RPCType_UNKNOWN_RPC,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := mapper.ParseRPCType(tt.input)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, sharedtypes.RPCType_UNKNOWN_RPC, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestRPCTypeMapper_ValidateRPCTypeString(t *testing.T) {
	mapper := NewRPCTypeMapper()

	tests := []struct {
		name        string
		input       string
		expectError bool
	}{
		{name: "valid json_rpc", input: "json_rpc", expectError: false},
		{name: "valid rest", input: "rest", expectError: false},
		{name: "valid comet_bft", input: "comet_bft", expectError: false},
		{name: "valid websocket", input: "websocket", expectError: false},
		{name: "valid grpc", input: "grpc", expectError: false},
		{name: "invalid empty", input: "", expectError: true},
		{name: "invalid unknown", input: "unknown", expectError: true},
		{name: "invalid old format", input: "jsonrpc", expectError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := mapper.ValidateRPCTypeString(tt.input)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRPCTypeMapper_FormatRPCType(t *testing.T) {
	mapper := NewRPCTypeMapper()

	tests := []struct {
		name     string
		input    sharedtypes.RPCType
		expected string
	}{
		{
			name:     "format JSON_RPC",
			input:    sharedtypes.RPCType_JSON_RPC,
			expected: "json_rpc",
		},
		{
			name:     "format REST",
			input:    sharedtypes.RPCType_REST,
			expected: "rest",
		},
		{
			name:     "format COMET_BFT",
			input:    sharedtypes.RPCType_COMET_BFT,
			expected: "comet_bft",
		},
		{
			name:     "format WEBSOCKET",
			input:    sharedtypes.RPCType_WEBSOCKET,
			expected: "websocket",
		},
		{
			name:     "format GRPC",
			input:    sharedtypes.RPCType_GRPC,
			expected: "grpc",
		},
		{
			name:     "format UNKNOWN_RPC returns empty",
			input:    sharedtypes.RPCType_UNKNOWN_RPC,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mapper.FormatRPCType(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRPCTypeMapper_RoundTrip(t *testing.T) {
	mapper := NewRPCTypeMapper()

	// Test that we can convert from string -> enum -> string and get back the same value
	inputs := []string{"json_rpc", "rest", "comet_bft", "websocket", "grpc"}

	for _, input := range inputs {
		t.Run("roundtrip "+input, func(t *testing.T) {
			// Parse string to enum
			enum, err := mapper.ParseRPCType(input)
			require.NoError(t, err)

			// Format enum back to string
			output := mapper.FormatRPCType(enum)

			// Should match original input
			assert.Equal(t, input, output)
		})
	}
}

func TestGetAllValidRPCTypeStrings(t *testing.T) {
	result := GetAllValidRPCTypeStrings()

	// Should contain exactly 5 types
	assert.Len(t, result, 5)

	// Should contain all expected types
	assert.Contains(t, result, "json_rpc")
	assert.Contains(t, result, "rest")
	assert.Contains(t, result, "comet_bft")
	assert.Contains(t, result, "websocket")
	assert.Contains(t, result, "grpc")
}

func TestGetHTTPBasedRPCTypeStrings(t *testing.T) {
	result := GetHTTPBasedRPCTypeStrings()

	// Should contain exactly 3 HTTP-based types
	assert.Len(t, result, 3)

	// Should contain HTTP types
	assert.Contains(t, result, "json_rpc")
	assert.Contains(t, result, "rest")
	assert.Contains(t, result, "comet_bft")

	// Should NOT contain non-HTTP types
	assert.NotContains(t, result, "websocket")
	assert.NotContains(t, result, "grpc")
}

func TestIsHTTPBasedRPCType(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// HTTP-based types
		{name: "json_rpc is HTTP", input: "json_rpc", expected: true},
		{name: "rest is HTTP", input: "rest", expected: true},
		{name: "comet_bft is HTTP", input: "comet_bft", expected: true},

		// Non-HTTP types
		{name: "websocket is not HTTP", input: "websocket", expected: false},
		{name: "grpc is not HTTP", input: "grpc", expected: false},

		// Case-insensitive
		{name: "JSON_RPC uppercase is HTTP", input: "JSON_RPC", expected: true},
		{name: "REST uppercase is HTTP", input: "REST", expected: true},

		// Invalid types
		{name: "unknown is not HTTP", input: "unknown", expected: false},
		{name: "empty is not HTTP", input: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsHTTPBasedRPCType(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
