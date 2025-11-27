package cosmos

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/qos/jsonrpc"
)

func TestEndpointCheckEVMChainID_GetRequest(t *testing.T) {
	// Create a new check instance
	check := &endpointCheckEVMChainID{}

	// Get the request
	req := check.getRequest()

	// Verify the request structure
	require.Equal(t, jsonrpc.Version2, req.JSONRPC)
	require.Equal(t, jsonrpc.IDFromInt(idEVMChainIDCheck), req.ID)
	require.Equal(t, jsonrpc.Method(methodEVMChainID), req.Method)
	require.Equal(t, jsonrpc.Method("eth_chainId"), req.Method)
}

func TestEndpointCheckEVMChainID_IsExpired(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		expected  bool
	}{
		{
			name:      "not expired - future time",
			expiresAt: time.Now().Add(1 * time.Hour),
			expected:  false,
		},
		{
			name:      "expired - past time",
			expiresAt: time.Now().Add(-1 * time.Hour),
			expected:  true,
		},
		{
			name:      "expired - exactly now",
			expiresAt: time.Now(),
			expected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := &endpointCheckEVMChainID{
				expiresAt: tt.expiresAt,
			}
			require.Equal(t, tt.expected, check.IsExpired())
		})
	}
}

func TestEndpointCheckEVMChainID_GetChainID(t *testing.T) {
	tests := []struct {
		name        string
		chainID     *string
		expected    string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "no chain ID observation",
			chainID:     nil,
			expected:    "",
			expectError: true,
			errorMsg:    errNoEVMChainIDObs.Error(),
		},
		{
			name:        "valid chain ID",
			chainID:     stringPtr("0x1"),
			expected:    "0x1",
			expectError: false,
		},
		{
			name:        "empty chain ID",
			chainID:     stringPtr(""),
			expected:    "",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := &endpointCheckEVMChainID{
				chainID: tt.chainID,
			}

			chainID, err := check.GetChainID()

			if tt.expectError {
				require.Error(t, err)
				require.Equal(t, tt.errorMsg, err.Error())
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expected, chainID)
			}
		})
	}
}

// Helper function to create string pointers
func stringPtr(s string) *string {
	return &s
}
