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
//
// Shannon sessions are 1-based: with NumBlocksPerSession=60, sessions are
// [1,60], [61,120], [121,180], etc.
func Test_sessionCacheKeyStability(t *testing.T) {
	const (
		numBlocksPerSession int64 = 60
		serviceID                 = protocol.ServiceID("pocket")
		appAddr                   = "pokt1testaddr"
	)

	// shannonSessionStart mirrors the 1-based formula from
	// poktroll/x/shared/types/session.go GetSessionStartHeight
	shannonSessionStart := func(h int64) int64 {
		if h <= 0 {
			return 0
		}
		return h - ((h - 1) % numBlocksPerSession)
	}

	tests := []struct {
		name                 string
		heights              []int64
		expectedSessionStart int64
		expectSameKey        bool
	}{
		{
			name:                 "all heights in first session produce same key",
			heights:              []int64{1, 2, 30, 60},
			expectedSessionStart: 1,
			expectSameKey:        true,
		},
		{
			name:                 "all heights in second session produce same key",
			heights:              []int64{61, 62, 90, 120},
			expectedSessionStart: 61,
			expectSameKey:        true,
		},
		{
			name:                 "session boundary produces new key",
			heights:              []int64{60, 61},
			expectedSessionStart: 0, // not used for this test
			expectSameKey:        false,
		},
		{
			name:                 "large heights in same session",
			heights:              []int64{601, 630, 660},
			expectedSessionStart: 601,
			expectSameKey:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var keys []string
			for _, h := range tt.heights {
				sessionStart := shannonSessionStart(h)
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

// Test_sessionStartHeightComputation verifies the 1-based Shannon formula for
// computing session start height from current height and NumBlocksPerSession.
//
// Shannon formula: currentHeight - ((currentHeight - 1) % numBlocksPerSession)
// See: poktroll/x/shared/types/session.go GetSessionStartHeight
func Test_sessionStartHeightComputation(t *testing.T) {
	tests := []struct {
		name                string
		currentHeight       int64
		numBlocksPerSession int64
		expectedStart       int64
	}{
		{
			name:                "first block is session start",
			currentHeight:       1,
			numBlocksPerSession: 60,
			expectedStart:       1,
		},
		{
			name:                "mid-session",
			currentHeight:       30,
			numBlocksPerSession: 60,
			expectedStart:       1,
		},
		{
			name:                "last block of first session",
			currentHeight:       60,
			numBlocksPerSession: 60,
			expectedStart:       1,
		},
		{
			name:                "first block of second session",
			currentHeight:       61,
			numBlocksPerSession: 60,
			expectedStart:       61,
		},
		{
			name:                "last block of second session",
			currentHeight:       120,
			numBlocksPerSession: 60,
			expectedStart:       61,
		},
		{
			name:                "first block of third session",
			currentHeight:       121,
			numBlocksPerSession: 60,
			expectedStart:       121,
		},
		{
			name:                "large height",
			currentHeight:       653615,
			numBlocksPerSession: 60,
			expectedStart:       653581,
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
			got := tt.currentHeight - ((tt.currentHeight - 1) % tt.numBlocksPerSession)
			if got != tt.expectedStart {
				t.Errorf("sessionStartHeight(%d, %d) = %d, want %d",
					tt.currentHeight, tt.numBlocksPerSession, got, tt.expectedStart)
			}
		})
	}
}
