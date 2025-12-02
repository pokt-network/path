package shannon

import (
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

func TestNewSanctionedEndpointsStore_DefaultConfig(t *testing.T) {
	logger := polyzero.NewLogger()

	// Create store with zero config - should use defaults
	store := newSanctionedEndpointsStore(logger, SanctionConfig{})

	require.NotNil(t, store)
	require.NotNil(t, store.sessionSanctionsCache)
	require.NotNil(t, store.permanentSanctions)

	// Verify defaults were applied
	require.Equal(t, defaultSessionSanctionExpiration, store.config.SessionSanctionDuration)
	require.Equal(t, defaultSanctionCacheCleanupInterval, store.config.CacheCleanupInterval)
}

func TestNewSanctionedEndpointsStore_CustomConfig(t *testing.T) {
	logger := polyzero.NewLogger()

	customDuration := 30 * time.Minute
	customCleanup := 5 * time.Minute

	config := SanctionConfig{
		SessionSanctionDuration: customDuration,
		CacheCleanupInterval:    customCleanup,
	}

	store := newSanctionedEndpointsStore(logger, config)

	require.NotNil(t, store)
	require.Equal(t, customDuration, store.config.SessionSanctionDuration)
	require.Equal(t, customCleanup, store.config.CacheCleanupInterval)
}

func TestNewSanctionedEndpointsStore_PartialConfig(t *testing.T) {
	logger := polyzero.NewLogger()

	// Only set one value, the other should use default
	config := SanctionConfig{
		SessionSanctionDuration: 15 * time.Minute,
		// CacheCleanupInterval not set - should default
	}

	store := newSanctionedEndpointsStore(logger, config)

	require.NotNil(t, store)
	require.Equal(t, 15*time.Minute, store.config.SessionSanctionDuration)
	require.Equal(t, defaultSanctionCacheCleanupInterval, store.config.CacheCleanupInterval)
}

func TestSanctionConfig_HydrateDefaults(t *testing.T) {
	tests := []struct {
		name                            string
		inputConfig                     SanctionConfig
		expectedSessionSanctionDuration time.Duration
		expectedCacheCleanupInterval    time.Duration
	}{
		{
			name:                            "zero values get defaults",
			inputConfig:                     SanctionConfig{},
			expectedSessionSanctionDuration: defaultSessionSanctionExpiration,
			expectedCacheCleanupInterval:    defaultSanctionCacheCleanupInterval,
		},
		{
			name: "custom values preserved",
			inputConfig: SanctionConfig{
				SessionSanctionDuration: 45 * time.Minute,
				CacheCleanupInterval:    3 * time.Minute,
			},
			expectedSessionSanctionDuration: 45 * time.Minute,
			expectedCacheCleanupInterval:    3 * time.Minute,
		},
		{
			name: "partial config - only duration set",
			inputConfig: SanctionConfig{
				SessionSanctionDuration: 20 * time.Minute,
			},
			expectedSessionSanctionDuration: 20 * time.Minute,
			expectedCacheCleanupInterval:    defaultSanctionCacheCleanupInterval,
		},
		{
			name: "partial config - only cleanup set",
			inputConfig: SanctionConfig{
				CacheCleanupInterval: 7 * time.Minute,
			},
			expectedSessionSanctionDuration: defaultSessionSanctionExpiration,
			expectedCacheCleanupInterval:    7 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.inputConfig.HydrateDefaults()

			require.Equal(t, tt.expectedSessionSanctionDuration, result.SessionSanctionDuration)
			require.Equal(t, tt.expectedCacheCleanupInterval, result.CacheCleanupInterval)
		})
	}
}

// TestSessionSanction_ExpiresAfterConfiguredDuration verifies that session sanctions
// actually expire after the configured SessionSanctionDuration.
func TestSessionSanction_ExpiresAfterConfiguredDuration(t *testing.T) {
	logger := polyzero.NewLogger()

	// Use a very short duration for testing (100ms)
	shortDuration := 100 * time.Millisecond
	config := SanctionConfig{
		SessionSanctionDuration: shortDuration,
		CacheCleanupInterval:    50 * time.Millisecond, // Cleanup frequently for faster expiration
	}

	store := newSanctionedEndpointsStore(logger, config)

	// Create a test endpoint with session info
	testEndpoint := createTestEndpoint("supplier1", "https://endpoint1.example.com", "session-123")

	// Add a session sanction
	testSanction := sanction{
		reason:           "test sanction",
		sessionServiceID: "test-service",
	}
	store.addSessionSanction(testEndpoint, testSanction)

	// Verify the endpoint is sanctioned immediately
	isSanctioned, reason := store.isSanctioned(testEndpoint)
	require.True(t, isSanctioned, "endpoint should be sanctioned immediately after adding sanction")
	require.Contains(t, reason, "session sanction")

	// Wait for the sanction to expire (add buffer for timing)
	time.Sleep(shortDuration + 100*time.Millisecond)

	// Verify the sanction has expired
	isSanctioned, _ = store.isSanctioned(testEndpoint)
	require.False(t, isSanctioned, "endpoint should no longer be sanctioned after duration expires")
}

// TestSessionSanction_DifferentDurationsForDifferentStores verifies that different
// stores can have different sanction durations.
func TestSessionSanction_DifferentDurationsForDifferentStores(t *testing.T) {
	logger := polyzero.NewLogger()

	// Create two stores with different durations
	shortConfig := SanctionConfig{
		SessionSanctionDuration: 100 * time.Millisecond,
		CacheCleanupInterval:    50 * time.Millisecond,
	}
	longConfig := SanctionConfig{
		SessionSanctionDuration: 500 * time.Millisecond,
		CacheCleanupInterval:    50 * time.Millisecond,
	}

	shortStore := newSanctionedEndpointsStore(logger, shortConfig)
	longStore := newSanctionedEndpointsStore(logger, longConfig)

	// Create test endpoints
	endpoint1 := createTestEndpoint("supplier1", "https://endpoint1.example.com", "session-1")
	endpoint2 := createTestEndpoint("supplier2", "https://endpoint2.example.com", "session-2")

	testSanction := sanction{
		reason:           "test sanction",
		sessionServiceID: "test-service",
	}

	// Add sanctions to both stores
	shortStore.addSessionSanction(endpoint1, testSanction)
	longStore.addSessionSanction(endpoint2, testSanction)

	// Both should be sanctioned initially
	sanctioned1, _ := shortStore.isSanctioned(endpoint1)
	sanctioned2, _ := longStore.isSanctioned(endpoint2)
	require.True(t, sanctioned1, "endpoint1 should be sanctioned in short store")
	require.True(t, sanctioned2, "endpoint2 should be sanctioned in long store")

	// Wait for short duration to expire
	time.Sleep(200 * time.Millisecond)

	// Short store sanction should have expired, long store should still be active
	sanctioned1, _ = shortStore.isSanctioned(endpoint1)
	sanctioned2, _ = longStore.isSanctioned(endpoint2)
	require.False(t, sanctioned1, "endpoint1 sanction should have expired in short store")
	require.True(t, sanctioned2, "endpoint2 should still be sanctioned in long store")

	// Wait for long duration to expire
	time.Sleep(400 * time.Millisecond)

	// Both should now be expired
	sanctioned2, _ = longStore.isSanctioned(endpoint2)
	require.False(t, sanctioned2, "endpoint2 sanction should have expired in long store")
}

// TestFilterSanctionedEndpoints_RespectsConfiguredDuration verifies that
// FilterSanctionedEndpoints correctly filters based on active sanctions
// and respects the configured expiration duration.
func TestFilterSanctionedEndpoints_RespectsConfiguredDuration(t *testing.T) {
	logger := polyzero.NewLogger()

	config := SanctionConfig{
		SessionSanctionDuration: 100 * time.Millisecond,
		CacheCleanupInterval:    50 * time.Millisecond,
	}

	store := newSanctionedEndpointsStore(logger, config)

	// Create test endpoints
	endpoint1 := createTestEndpoint("supplier1", "https://endpoint1.example.com", "session-1")
	endpoint2 := createTestEndpoint("supplier2", "https://endpoint2.example.com", "session-1")

	allEndpoints := map[protocol.EndpointAddr]endpoint{
		endpoint1.Addr(): endpoint1,
		endpoint2.Addr(): endpoint2,
	}

	// Sanction endpoint1 only
	testSanction := sanction{
		reason:           "test sanction",
		sessionServiceID: "test-service",
	}
	store.addSessionSanction(endpoint1, testSanction)

	// Filter should return only endpoint2 (endpoint1 is sanctioned)
	filtered := store.FilterSanctionedEndpoints(allEndpoints)
	require.Len(t, filtered, 1, "should filter out sanctioned endpoint")
	_, hasEndpoint2 := filtered[endpoint2.Addr()]
	require.True(t, hasEndpoint2, "endpoint2 should be in filtered results")

	// Wait for sanction to expire
	time.Sleep(200 * time.Millisecond)

	// Now both endpoints should be returned
	filtered = store.FilterSanctionedEndpoints(allEndpoints)
	require.Len(t, filtered, 2, "should return both endpoints after sanction expires")
}

// createTestEndpoint creates a protocolEndpoint for testing purposes.
func createTestEndpoint(supplier, url, sessionID string) protocolEndpoint {
	return protocolEndpoint{
		supplier: supplier,
		url:      url,
		session: sessiontypes.Session{
			Header: &sessiontypes.SessionHeader{
				SessionId: sessionID,
			},
		},
	}
}
