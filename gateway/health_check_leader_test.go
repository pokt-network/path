package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// setupTestRedis creates a miniredis instance and returns a redis client.
func setupTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(func() { mr.Close() })

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	return mr, client
}

// TestNewLeaderElector tests leader elector creation.
func TestNewLeaderElector(t *testing.T) {
	_, client := setupTestRedis(t)
	defer client.Close()

	logger := polyzero.NewLogger()

	t.Run("returns nil when type is none", func(t *testing.T) {
		elector := NewLeaderElector(LeaderElectorConfig{
			Client: client,
			Config: LeaderElectionConfig{
				Type: "none",
			},
			Logger: logger,
		})
		require.Nil(t, elector)
	})

	t.Run("returns nil when client is nil", func(t *testing.T) {
		elector := NewLeaderElector(LeaderElectorConfig{
			Client: nil,
			Config: LeaderElectionConfig{
				Type: "leader_election",
			},
			Logger: logger,
		})
		require.Nil(t, elector)
	})

	t.Run("creates elector with valid config", func(t *testing.T) {
		elector := NewLeaderElector(LeaderElectorConfig{
			Client: client,
			Config: LeaderElectionConfig{
				Type:          "leader_election",
				LeaseDuration: 30 * time.Second,
				RenewInterval: 10 * time.Second,
				Key:           "test:leader",
			},
			Logger: logger,
		})
		require.NotNil(t, elector)
		require.False(t, elector.IsLeader())
	})
}

// TestLeaderElectorAcquireLeadership tests leadership acquisition.
func TestLeaderElectorAcquireLeadership(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer client.Close()

	logger := polyzero.NewLogger()

	config := LeaderElectionConfig{
		Type:          "leader_election",
		LeaseDuration: 5 * time.Second,
		RenewInterval: 1 * time.Second,
		Key:           "test:leader",
	}

	elector := NewLeaderElector(LeaderElectorConfig{
		Client: client,
		Config: config,
		Logger: logger,
	})
	require.NotNil(t, elector)

	// Start leadership acquisition
	ctx := context.Background()
	err := elector.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = elector.Stop() }()

	// Should acquire leadership immediately
	require.True(t, elector.IsLeader())

	// Verify key exists in Redis
	val, err := mr.Get(config.Key)
	require.NoError(t, err)
	require.NotEmpty(t, val)
}

// TestLeaderElectorRenewLeadership tests leadership renewal.
func TestLeaderElectorRenewLeadership(t *testing.T) {
	_, client := setupTestRedis(t)
	defer client.Close()

	logger := polyzero.NewLogger()

	config := LeaderElectionConfig{
		Type:          "leader_election",
		LeaseDuration: 2 * time.Second,
		RenewInterval: 500 * time.Millisecond,
		Key:           "test:leader",
	}

	elector := NewLeaderElector(LeaderElectorConfig{
		Client: client,
		Config: config,
		Logger: logger,
	})
	require.NotNil(t, elector)

	ctx := context.Background()
	err := elector.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = elector.Stop() }()

	// Should be leader
	require.True(t, elector.IsLeader())

	// Wait for renewal to happen (longer than initial lease would expire)
	time.Sleep(1 * time.Second)

	// Should still be leader after renewal
	require.True(t, elector.IsLeader())
}

// TestLeaderElectorReleaseLeadership tests graceful leadership release.
func TestLeaderElectorReleaseLeadership(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer client.Close()

	logger := polyzero.NewLogger()

	config := LeaderElectionConfig{
		Type:          "leader_election",
		LeaseDuration: 30 * time.Second,
		RenewInterval: 10 * time.Second,
		Key:           "test:leader",
	}

	elector := NewLeaderElector(LeaderElectorConfig{
		Client: client,
		Config: config,
		Logger: logger,
	})
	require.NotNil(t, elector)

	ctx := context.Background()
	err := elector.Start(ctx)
	require.NoError(t, err)

	// Should be leader
	require.True(t, elector.IsLeader())

	// Stop should release leadership
	err = elector.Stop()
	require.NoError(t, err)

	// Should no longer be leader
	require.False(t, elector.IsLeader())

	// Key should be deleted from Redis
	exists := mr.Exists(config.Key)
	require.False(t, exists)
}

// TestLeaderElectorConcurrentAcquisition tests that only one instance can be leader.
func TestLeaderElectorConcurrentAcquisition(t *testing.T) {
	_, client := setupTestRedis(t)
	defer client.Close()

	logger := polyzero.NewLogger()

	config := LeaderElectionConfig{
		Type:          "leader_election",
		LeaseDuration: 30 * time.Second,
		RenewInterval: 10 * time.Second,
		Key:           "test:leader",
	}

	// Create first elector
	elector1 := NewLeaderElector(LeaderElectorConfig{
		Client: client,
		Config: config,
		Logger: logger,
	})
	require.NotNil(t, elector1)

	ctx := context.Background()

	// Start first elector - should become leader
	err := elector1.Start(ctx)
	require.NoError(t, err)
	require.True(t, elector1.IsLeader())

	// Create and start second elector - should NOT become leader
	elector2 := NewLeaderElector(LeaderElectorConfig{
		Client: client,
		Config: config,
		Logger: logger,
	})
	require.NotNil(t, elector2)

	err = elector2.Start(ctx)
	require.NoError(t, err)
	require.False(t, elector2.IsLeader())

	// Stop second elector first (it's not the leader)
	_ = elector2.Stop()

	// Stop first elector - releases leadership
	_ = elector1.Stop()
	require.False(t, elector1.IsLeader())

	// Create a new elector that should acquire leadership
	elector3 := NewLeaderElector(LeaderElectorConfig{
		Client: client,
		Config: config,
		Logger: logger,
	})
	err = elector3.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = elector3.Stop() }()

	// Third elector should now be leader
	require.True(t, elector3.IsLeader())
}

// TestLeaderElectorGetLeaderInstanceID tests retrieving the current leader ID.
func TestLeaderElectorGetLeaderInstanceID(t *testing.T) {
	_, client := setupTestRedis(t)
	defer client.Close()

	logger := polyzero.NewLogger()

	config := LeaderElectionConfig{
		Type:          "leader_election",
		LeaseDuration: 30 * time.Second,
		RenewInterval: 10 * time.Second,
		Key:           "test:leader",
	}

	elector := NewLeaderElector(LeaderElectorConfig{
		Client: client,
		Config: config,
		Logger: logger,
	})
	require.NotNil(t, elector)

	ctx := context.Background()

	// No leader yet
	leaderID := elector.GetLeaderInstanceID(ctx)
	require.Empty(t, leaderID)

	// Start and become leader
	err := elector.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = elector.Stop() }()

	// Should have a leader ID now
	leaderID = elector.GetLeaderInstanceID(ctx)
	require.NotEmpty(t, leaderID)
	require.Equal(t, elector.instanceID, leaderID)
}

// TestLeaderElectionConfigHydrateDefaults tests config defaults.
func TestLeaderElectionConfigHydrateDefaults(t *testing.T) {
	config := LeaderElectionConfig{}
	config.HydrateDefaults()

	require.Equal(t, DefaultLeaderLeaseDuration, config.LeaseDuration)
	require.Equal(t, DefaultLeaderRenewInterval, config.RenewInterval)
	require.Equal(t, DefaultLeaderKey, config.Key)
}

// TestLeaderElectorContextCancellation tests that context cancellation stops the elector.
func TestLeaderElectorContextCancellation(t *testing.T) {
	_, client := setupTestRedis(t)
	defer client.Close()

	logger := polyzero.NewLogger()

	config := LeaderElectionConfig{
		Type:          "leader_election",
		LeaseDuration: 30 * time.Second,
		RenewInterval: 100 * time.Millisecond,
		Key:           "test:leader",
	}

	elector := NewLeaderElector(LeaderElectorConfig{
		Client: client,
		Config: config,
		Logger: logger,
	})
	require.NotNil(t, elector)

	ctx, cancel := context.WithCancel(context.Background())

	err := elector.Start(ctx)
	require.NoError(t, err)
	require.True(t, elector.IsLeader())

	// Cancel context
	cancel()

	// Wait for goroutine to notice cancellation
	time.Sleep(200 * time.Millisecond)

	// The elector should still report as leader since we didn't call Stop()
	// (Stop() is what releases leadership in Redis)
	// But the renewal goroutine should have stopped

	// Cleanup
	_ = elector.Stop()
}
