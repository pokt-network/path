package selector

import (
	"os"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

func TestRandomSelectMultiple(t *testing.T) {

	testCases := []struct {
		name            string
		endpoints       protocol.EndpointAddrList
		numEndpoints    int
		expectedLength  int
		shouldReturnNil bool
	}{
		{
			name:            "request more endpoints than available returns all",
			endpoints:       protocol.EndpointAddrList{"endpoint1", "endpoint2", "endpoint3"},
			numEndpoints:    5,
			expectedLength:  3,
			shouldReturnNil: false,
		},
		{
			name:            "request exact number of endpoints returns all",
			endpoints:       protocol.EndpointAddrList{"endpoint1", "endpoint2", "endpoint3"},
			numEndpoints:    3,
			expectedLength:  3,
			shouldReturnNil: false,
		},
		{
			name:            "request subset of endpoints",
			endpoints:       protocol.EndpointAddrList{"endpoint1", "endpoint2", "endpoint3", "endpoint4", "endpoint5"},
			numEndpoints:    2,
			expectedLength:  2,
			shouldReturnNil: false,
		},
		{
			name:            "single endpoint selection",
			endpoints:       protocol.EndpointAddrList{"endpoint1", "endpoint2", "endpoint3"},
			numEndpoints:    1,
			expectedLength:  1,
			shouldReturnNil: false,
		},
		{
			name:            "empty endpoint list returns empty slice",
			endpoints:       protocol.EndpointAddrList{},
			numEndpoints:    2,
			expectedLength:  0,
			shouldReturnNil: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := RandomSelectMultiple(tc.endpoints, uint(tc.numEndpoints))

			if tc.shouldReturnNil {
				require.Nil(t, result)
				return
			}

			require.NotNil(t, result)
			require.Len(t, result, tc.expectedLength)

			// Verify all returned endpoints are from the original list
			for _, selectedEndpoint := range result {
				require.Contains(t, tc.endpoints, selectedEndpoint)
			}

			// Verify no duplicates in result
			seen := make(map[protocol.EndpointAddr]bool)
			for _, endpoint := range result {
				require.False(t, seen[endpoint], "duplicate endpoint found in result")
				seen[endpoint] = true
			}

			// Verify original slice is not modified
			originalEndpoints := protocol.EndpointAddrList{"endpoint1", "endpoint2", "endpoint3", "endpoint4", "endpoint5"}
			if len(tc.endpoints) == len(originalEndpoints) {
				for i, endpoint := range tc.endpoints {
					if i < len(originalEndpoints) {
						require.Equal(t, originalEndpoints[i], endpoint, "original slice was modified")
					}
				}
			}
		})
	}
}

func TestRandomSelectMultiple_Randomness(t *testing.T) {
	endpoints := protocol.EndpointAddrList{"endpoint1", "endpoint2", "endpoint3", "endpoint4", "endpoint5"}
	numEndpoints := uint(3)
	iterations := 100

	// Track frequency of each endpoint being selected
	selectionCount := make(map[protocol.EndpointAddr]int)

	for range iterations {
		result := RandomSelectMultiple(endpoints, numEndpoints)
		require.Len(t, result, int(numEndpoints))

		for _, endpoint := range result {
			selectionCount[endpoint]++
		}
	}

	// Each endpoint should be selected at least once across all iterations
	// This is a probabilistic test, so we use a reasonable threshold
	minExpectedSelections := iterations / len(endpoints) / 4 // 25% of average
	for _, endpoint := range endpoints {
		require.GreaterOrEqual(t, selectionCount[endpoint], minExpectedSelections,
			"endpoint %s was selected too few times (%d), indicating poor randomness",
			endpoint, selectionCount[endpoint])
	}
}

func TestSelectEndpointsWithDiversity(t *testing.T) {
	// Create a test logger
	logger := polyzero.NewLogger(polyzero.WithOutput(os.Stderr))

	testCases := []struct {
		name            string
		endpoints       protocol.EndpointAddrList
		numEndpoints    uint
		expectedLength  int
		expectDiversity bool
	}{
		{
			name:            "empty endpoint list",
			endpoints:       protocol.EndpointAddrList{},
			numEndpoints:    2,
			expectedLength:  0,
			expectDiversity: false,
		},
		{
			name:            "request zero endpoints",
			endpoints:       protocol.EndpointAddrList{"endpoint1.com", "endpoint2.net", "endpoint3.org"},
			numEndpoints:    0,
			expectedLength:  0,
			expectDiversity: false,
		},
		{
			name:            "single endpoint",
			endpoints:       protocol.EndpointAddrList{"endpoint1.com"},
			numEndpoints:    1,
			expectedLength:  1,
			expectDiversity: false,
		},
		{
			name:            "multiple endpoints with different TLDs",
			endpoints:       protocol.EndpointAddrList{"endpoint1.com", "endpoint2.net", "endpoint3.org", "endpoint4.io"},
			numEndpoints:    3,
			expectedLength:  3,
			expectDiversity: true,
		},
		{
			name:            "request more endpoints than available",
			endpoints:       protocol.EndpointAddrList{"endpoint1.com", "endpoint2.net"},
			numEndpoints:    5,
			expectedLength:  2,
			expectDiversity: true,
		},
		{
			name:            "endpoints with same TLD",
			endpoints:       protocol.EndpointAddrList{"endpoint1.com", "endpoint2.com", "endpoint3.com"},
			numEndpoints:    2,
			expectedLength:  2,
			expectDiversity: false,
		},
		{
			name:            "mixed TLD diversity",
			endpoints:       protocol.EndpointAddrList{"endpoint1.com", "endpoint2.com", "endpoint3.net", "endpoint4.org"},
			numEndpoints:    3,
			expectedLength:  3,
			expectDiversity: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := SelectEndpointsWithDiversity(logger, tc.endpoints, tc.numEndpoints)

			require.Len(t, result, tc.expectedLength)

			if tc.expectedLength == 0 {
				return
			}

			// Verify all returned endpoints are from the original list
			for _, selectedEndpoint := range result {
				require.Contains(t, tc.endpoints, selectedEndpoint)
			}

			// Verify no duplicates in result
			seen := make(map[protocol.EndpointAddr]bool)
			for _, endpoint := range result {
				require.False(t, seen[endpoint], "duplicate endpoint found in result")
				seen[endpoint] = true
			}
		})
	}
}

func TestSelectEndpointsWithDiversity_TLDDiversity(t *testing.T) {
	// Create a test logger
	logger := polyzero.NewLogger(polyzero.WithOutput(os.Stderr))

	// Test case with clear TLD diversity expectation
	endpoints := protocol.EndpointAddrList{
		"provider1.com",
		"provider2.net",
		"provider3.org",
		"provider4.io",
		"provider5.com", // duplicate TLD
	}

	result := SelectEndpointsWithDiversity(logger, endpoints, 4)
	require.Len(t, result, 4)

	// Count unique TLDs in result
	tlds := make(map[string]bool)
	for _, endpoint := range result {
		// Simple TLD extraction for test
		parts := string(endpoint)
		if len(parts) > 4 {
			lastDot := -1
			for i := len(parts) - 1; i >= 0; i-- {
				if parts[i] == '.' {
					lastDot = i
					break
				}
			}
			if lastDot != -1 && lastDot < len(parts)-1 {
				tld := parts[lastDot+1:]
				tlds[tld] = true
			}
		}
	}

	// Should prefer diversity when possible
	require.GreaterOrEqual(t, len(tlds), 3, "should achieve some TLD diversity")
}

func TestSelectEndpointWithDifferentTLD(t *testing.T) {
	testCases := []struct {
		name               string
		availableEndpoints protocol.EndpointAddrList
		endpointTLDs       map[protocol.EndpointAddr]string
		usedTLDs           map[string]struct{}
		expectError        bool
		expectedTLDNotUsed bool
	}{
		{
			name:               "select endpoint with different TLD",
			availableEndpoints: protocol.EndpointAddrList{"endpoint1.com", "endpoint2.net", "endpoint3.org"},
			endpointTLDs: map[protocol.EndpointAddr]string{
				"endpoint1.com": "com",
				"endpoint2.net": "net",
				"endpoint3.org": "org",
			},
			usedTLDs:           map[string]struct{}{"com": {}},
			expectError:        false,
			expectedTLDNotUsed: true,
		},
		{
			name:               "no endpoints with different TLD available",
			availableEndpoints: protocol.EndpointAddrList{"endpoint1.com", "endpoint2.com"},
			endpointTLDs: map[protocol.EndpointAddr]string{
				"endpoint1.com": "com",
				"endpoint2.com": "com",
			},
			usedTLDs:           map[string]struct{}{"com": {}},
			expectError:        true,
			expectedTLDNotUsed: false,
		},
		{
			name:               "endpoint without TLD info is included",
			availableEndpoints: protocol.EndpointAddrList{"endpoint1.com", "endpoint2.unknown"},
			endpointTLDs: map[protocol.EndpointAddr]string{
				"endpoint1.com": "com",
			},
			usedTLDs:           map[string]struct{}{"com": {}},
			expectError:        false,
			expectedTLDNotUsed: true,
		},
		{
			name:               "empty available endpoints",
			availableEndpoints: protocol.EndpointAddrList{},
			endpointTLDs:       map[protocol.EndpointAddr]string{},
			usedTLDs:           map[string]struct{}{"com": {}},
			expectError:        true,
			expectedTLDNotUsed: false,
		},
		{
			name:               "no used TLDs constraint",
			availableEndpoints: protocol.EndpointAddrList{"endpoint1.com", "endpoint2.net"},
			endpointTLDs: map[protocol.EndpointAddr]string{
				"endpoint1.com": "com",
				"endpoint2.net": "net",
			},
			usedTLDs:           map[string]struct{}{},
			expectError:        false,
			expectedTLDNotUsed: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := selectEndpointWithDifferentTLD(tc.availableEndpoints, tc.endpointTLDs, tc.usedTLDs)

			if tc.expectError {
				require.Error(t, err)
				require.Empty(t, result)
				return
			}

			require.NoError(t, err)
			require.NotEmpty(t, result)
			require.Contains(t, tc.availableEndpoints, result)

			if tc.expectedTLDNotUsed {
				// Verify the selected endpoint's TLD is not in usedTLDs
				if tld, exists := tc.endpointTLDs[result]; exists {
					_, used := tc.usedTLDs[tld]
					require.False(t, used, "selected endpoint TLD should not be in used TLDs")
				}
			}
		})
	}
}
