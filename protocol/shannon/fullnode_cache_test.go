package shannon

import (
	"testing"

	"github.com/pokt-network/path/protocol"
)

func Test_getSessionCacheKey(t *testing.T) {
	tests := []struct {
		name      string
		serviceID protocol.ServiceID
		appAddr   string
		height    int64
		expected  string
	}{
		{
			name:      "basic key generation",
			serviceID: "pocket",
			appAddr:   "pokt1abc",
			height:    100,
			expected:  "session:pocket:pokt1abc:100",
		},
		{
			name:      "height zero",
			serviceID: "pocket",
			appAddr:   "pokt1abc",
			height:    0,
			expected:  "session:pocket:pokt1abc:0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getSessionCacheKey(tt.serviceID, tt.appAddr, tt.height)
			if got != tt.expected {
				t.Errorf("getSessionCacheKey() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// Test_sessionCacheKeyStability verifies that the cache key remains stable for
// all block heights within the same session window. This is the core property
// that the fix in https://github.com/pokt-network/path/issues/509 relies on.
func Test_sessionCacheKeyStability(t *testing.T) {
	const (
		numBlocksPerSession int64 = 50
		serviceID                 = protocol.ServiceID("pocket")
		appAddr                   = "pokt1testaddr"
	)

	tests := []struct {
		name                   string
		heights                []int64
		expectedSessionStart   int64
		expectSameKey          bool
	}{
		{
			name:                 "all heights in same session produce same key",
			heights:              []int64{100, 101, 120, 149},
			expectedSessionStart: 100,
			expectSameKey:        true,
		},
		{
			name:                 "first block of session",
			heights:              []int64{150, 151, 199},
			expectedSessionStart: 150,
			expectSameKey:        true,
		},
		{
			name:                 "session boundary produces new key",
			heights:              []int64{149, 150},
			expectedSessionStart: 0, // not used for this test
			expectSameKey:        false,
		},
		{
			name:                 "height zero",
			heights:              []int64{0, 1, 49},
			expectedSessionStart: 0,
			expectSameKey:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var keys []string
			for _, h := range tt.heights {
				sessionStart := h - (h % numBlocksPerSession)
				key := getSessionCacheKey(serviceID, appAddr, sessionStart)
				keys = append(keys, key)
			}

			if tt.expectSameKey {
				expectedKey := getSessionCacheKey(serviceID, appAddr, tt.expectedSessionStart)
				for i, key := range keys {
					if key != expectedKey {
						t.Errorf("height %d: key = %q, want %q", tt.heights[i], key, expectedKey)
					}
				}
			} else {
				// Verify at least two different keys exist
				allSame := true
				for _, key := range keys[1:] {
					if key != keys[0] {
						allSame = false
						break
					}
				}
				if allSame {
					t.Error("expected different keys across session boundary, but all keys are the same")
				}
			}
		})
	}
}

// Test_sessionStartHeightComputation verifies the deterministic formula for
// computing session start height from current height and NumBlocksPerSession.
func Test_sessionStartHeightComputation(t *testing.T) {
	tests := []struct {
		name                string
		currentHeight       int64
		numBlocksPerSession int64
		expectedStart       int64
	}{
		{
			name:                "exact session boundary",
			currentHeight:       100,
			numBlocksPerSession: 50,
			expectedStart:       100,
		},
		{
			name:                "mid-session",
			currentHeight:       125,
			numBlocksPerSession: 50,
			expectedStart:       100,
		},
		{
			name:                "last block of session",
			currentHeight:       149,
			numBlocksPerSession: 50,
			expectedStart:       100,
		},
		{
			name:                "first block after boundary",
			currentHeight:       150,
			numBlocksPerSession: 50,
			expectedStart:       150,
		},
		{
			name:                "height zero",
			currentHeight:       0,
			numBlocksPerSession: 50,
			expectedStart:       0,
		},
		{
			name:                "height 1",
			currentHeight:       1,
			numBlocksPerSession: 50,
			expectedStart:       0,
		},
		{
			name:                "large height",
			currentHeight:       653615,
			numBlocksPerSession: 50,
			expectedStart:       653600,
		},
		{
			name:                "numBlocksPerSession = 1 (every block is a session)",
			currentHeight:       500,
			numBlocksPerSession: 1,
			expectedStart:       500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.currentHeight - (tt.currentHeight % tt.numBlocksPerSession)
			if got != tt.expectedStart {
				t.Errorf("sessionStartHeight(%d, %d) = %d, want %d",
					tt.currentHeight, tt.numBlocksPerSession, got, tt.expectedStart)
			}
		})
	}
}
