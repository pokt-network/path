package evm

import (
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
	qostypes "github.com/pokt-network/path/qos/types"
)

// TestUpdateFromExtractedData_ArchivalBidirectional verifies that UpdateFromExtractedData
// correctly handles both setting (IsArchival=true) and clearing (IsArchival=false) archival status.
func TestUpdateFromExtractedData_ArchivalBidirectional(t *testing.T) {
	tests := []struct {
		name                      string
		archivalCheckPerformed    bool
		isArchival                bool
		expectArchivalStored      bool
		expectValidExpiry         bool
		expectLogMessage          string
	}{
		{
			name:                   "archival true sets local store",
			archivalCheckPerformed: true,
			isArchival:             true,
			expectArchivalStored:   true,
			expectValidExpiry:      true,
			expectLogMessage:       "Confirmed archival status",
		},
		{
			name:                   "archival false clears local store",
			archivalCheckPerformed: true,
			isArchival:             false,
			expectArchivalStored:   false,
			expectValidExpiry:      true,
			expectLogMessage:       "Cleared archival status",
		},
		{
			name:                   "no check performed leaves unchanged",
			archivalCheckPerformed: false,
			isArchival:             false, // Value doesn't matter when check not performed
			expectArchivalStored:   false,
			expectValidExpiry:      false,
			expectLogMessage:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a minimal QoS instance
			logger := polyzero.NewLogger()
			qos := NewSimpleQoSInstance(logger, "eth")

			// Create test endpoint address
			endpointAddr := protocol.EndpointAddr("pokt1supplier-https://eth.example.com")

			// Initialize endpoint in store (simulate endpoint being registered)
			qos.endpointStore.endpointsMu.Lock()
			qos.endpointStore.endpoints[endpointAddr] = endpoint{
				checkArchival: endpointCheckArchival{
					isArchival: false,
					expiresAt:  time.Time{}, // Initially not archival
				},
			}
			qos.endpointStore.endpointsMu.Unlock()

			// Create extracted data with archival status
			data := &qostypes.ExtractedData{
				ArchivalCheckPerformed: tt.archivalCheckPerformed,
				IsArchival:             tt.isArchival,
			}

			// Call UpdateFromExtractedData
			err := qos.UpdateFromExtractedData(endpointAddr, data)
			require.NoError(t, err)

			// Verify archival status in local store
			qos.endpointStore.endpointsMu.RLock()
			storedEndpoint := qos.endpointStore.endpoints[endpointAddr]
			qos.endpointStore.endpointsMu.RUnlock()

			if tt.archivalCheckPerformed {
				// Archival status should be updated
				require.Equal(t, tt.expectArchivalStored, storedEndpoint.checkArchival.isArchival,
					"archival status mismatch")

				if tt.expectValidExpiry {
					// Expiry should be set to a future time (within reasonable bounds)
					require.False(t, storedEndpoint.checkArchival.expiresAt.IsZero(),
						"expiry time should be set")
					require.True(t, storedEndpoint.checkArchival.expiresAt.After(time.Now()),
						"expiry time should be in the future")
					require.True(t, storedEndpoint.checkArchival.expiresAt.Before(time.Now().Add(9*time.Hour)),
						"expiry time should be within 9 hours (8h TTL + buffer)")
				}
			} else {
				// No archival check performed - should remain unchanged (false, zero time)
				require.False(t, storedEndpoint.checkArchival.isArchival,
					"archival status should remain false when check not performed")
				require.True(t, storedEndpoint.checkArchival.expiresAt.IsZero(),
					"expiry time should remain zero when check not performed")
			}
		})
	}
}

// TestUpdateFromExtractedData_BlockNumber verifies that block number updates work correctly.
func TestUpdateFromExtractedData_BlockNumber(t *testing.T) {
	logger := polyzero.NewLogger()
	qos := NewSimpleQoSInstance(logger, "eth")

	endpointAddr := protocol.EndpointAddr("pokt1supplier-https://eth.example.com")

	// Initialize endpoint in store
	qos.endpointStore.endpointsMu.Lock()
	qos.endpointStore.endpoints[endpointAddr] = endpoint{}
	qos.endpointStore.endpointsMu.Unlock()

	// Create extracted data with block height
	data := &qostypes.ExtractedData{
		BlockHeight: 12345,
	}

	// Call UpdateFromExtractedData
	err := qos.UpdateFromExtractedData(endpointAddr, data)
	require.NoError(t, err)

	// Verify block number in local store
	qos.endpointStore.endpointsMu.RLock()
	storedEndpoint := qos.endpointStore.endpoints[endpointAddr]
	qos.endpointStore.endpointsMu.RUnlock()

	require.NotNil(t, storedEndpoint.checkBlockNumber.parsedBlockNumberResponse)
	require.Equal(t, uint64(12345), *storedEndpoint.checkBlockNumber.parsedBlockNumberResponse)
}

// TestUpdateFromExtractedData_InvalidBlockHeight verifies that InvalidBlockHeight
// stores block 0 so the filter catches the broken endpoint.
func TestUpdateFromExtractedData_InvalidBlockHeight(t *testing.T) {
	logger := polyzero.NewLogger()
	qos := NewSimpleQoSInstance(logger, "eth")

	endpointAddr := protocol.EndpointAddr("pokt1supplier-https://broken.example.com")

	// Initialize endpoint in store (no block observation yet)
	qos.endpointStore.endpointsMu.Lock()
	qos.endpointStore.endpoints[endpointAddr] = endpoint{}
	qos.endpointStore.endpointsMu.Unlock()

	// Create extracted data with InvalidBlockHeight flag (simulates "result":[] for eth_blockNumber)
	data := &qostypes.ExtractedData{
		BlockHeight:        0, // Extraction failed
		InvalidBlockHeight: true,
	}

	err := qos.UpdateFromExtractedData(endpointAddr, data)
	require.NoError(t, err)

	// Verify block number is stored as &0 (not nil)
	qos.endpointStore.endpointsMu.RLock()
	storedEndpoint := qos.endpointStore.endpoints[endpointAddr]
	qos.endpointStore.endpointsMu.RUnlock()

	require.NotNil(t, storedEndpoint.checkBlockNumber.parsedBlockNumberResponse,
		"parsedBlockNumberResponse should be &0 (not nil) so the filter catches it")
	require.Equal(t, uint64(0), *storedEndpoint.checkBlockNumber.parsedBlockNumberResponse,
		"block number should be 0 for broken supplier")
}

// TestUpdateFromExtractedData_InvalidBlockHeight_DoesNotAffectPerceived verifies that
// an invalid block height (0) does not lower the perceived block number.
func TestUpdateFromExtractedData_InvalidBlockHeight_DoesNotAffectPerceived(t *testing.T) {
	logger := polyzero.NewLogger()
	qos := NewSimpleQoSInstance(logger, "eth")

	// Set a perceived block number
	qos.perceivedBlockNumber.Store(100000)

	endpointAddr := protocol.EndpointAddr("pokt1supplier-https://broken.example.com")
	qos.endpointStore.endpointsMu.Lock()
	qos.endpointStore.endpoints[endpointAddr] = endpoint{}
	qos.endpointStore.endpointsMu.Unlock()

	data := &qostypes.ExtractedData{
		BlockHeight:        0,
		InvalidBlockHeight: true,
	}

	err := qos.UpdateFromExtractedData(endpointAddr, data)
	require.NoError(t, err)

	// Perceived block should NOT change (0 is not added to consensus)
	require.Equal(t, uint64(100000), qos.perceivedBlockNumber.Load(),
		"perceived block should remain unchanged when InvalidBlockHeight is set")
}

// TestUpdateFromExtractedData_NilData verifies that nil data is handled gracefully.
func TestUpdateFromExtractedData_NilData(t *testing.T) {
	logger := polyzero.NewLogger()
	qos := NewSimpleQoSInstance(logger, "eth")

	endpointAddr := protocol.EndpointAddr("pokt1supplier-https://eth.example.com")

	// Call with nil data - should not panic
	err := qos.UpdateFromExtractedData(endpointAddr, nil)
	require.NoError(t, err)
}
