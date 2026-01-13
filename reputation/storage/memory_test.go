package storage

import (
	"context"
	"testing"
	"time"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
)

func TestMemoryStorage_GetSet(t *testing.T) {
	ctx := context.Background()
	storage := NewMemoryStorage(0) // No TTL
	defer storage.Close()

	key := reputation.NewEndpointKey("eth", "supplier1-https://endpoint.com", sharedtypes.RPCType_JSON_RPC)
	score := reputation.Score{
		Value:        85.5,
		LastUpdated:  time.Now(),
		SuccessCount: 100,
		ErrorCount:   5,
	}

	// Get non-existent key
	_, err := storage.Get(ctx, key)
	require.ErrorIs(t, err, reputation.ErrNotFound)

	// Set and get
	err = storage.Set(ctx, key, score)
	require.NoError(t, err)

	retrieved, err := storage.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, score.Value, retrieved.Value)
	require.Equal(t, score.SuccessCount, retrieved.SuccessCount)
	require.Equal(t, score.ErrorCount, retrieved.ErrorCount)
}

func TestMemoryStorage_GetMultiple(t *testing.T) {
	ctx := context.Background()
	storage := NewMemoryStorage(0)
	defer storage.Close()

	// Setup test data
	keys := []reputation.EndpointKey{
		reputation.NewEndpointKey("eth", "endpoint1", sharedtypes.RPCType_JSON_RPC),
		reputation.NewEndpointKey("eth", "endpoint2", sharedtypes.RPCType_JSON_RPC),
		reputation.NewEndpointKey("eth", "endpoint3", sharedtypes.RPCType_JSON_RPC),
	}

	scores := map[reputation.EndpointKey]reputation.Score{
		keys[0]: {Value: 80, LastUpdated: time.Now()},
		keys[1]: {Value: 90, LastUpdated: time.Now()},
		// keys[2] not set - should be missing from result
	}

	err := storage.SetMultiple(ctx, scores)
	require.NoError(t, err)

	// Get multiple - should only return keys[0] and keys[1]
	result, err := storage.GetMultiple(ctx, keys)
	require.NoError(t, err)
	require.Len(t, result, 2)
	require.Equal(t, float64(80), result[keys[0]].Value)
	require.Equal(t, float64(90), result[keys[1]].Value)
	_, exists := result[keys[2]]
	require.False(t, exists, "missing key should not be in result")
}

func TestMemoryStorage_Delete(t *testing.T) {
	ctx := context.Background()
	storage := NewMemoryStorage(0)
	defer storage.Close()

	key := reputation.NewEndpointKey("eth", "endpoint1", sharedtypes.RPCType_JSON_RPC)
	score := reputation.Score{Value: 85, LastUpdated: time.Now()}

	// Set and verify
	err := storage.Set(ctx, key, score)
	require.NoError(t, err)

	_, err = storage.Get(ctx, key)
	require.NoError(t, err)

	// Delete
	err = storage.Delete(ctx, key)
	require.NoError(t, err)

	// Verify deleted
	_, err = storage.Get(ctx, key)
	require.ErrorIs(t, err, reputation.ErrNotFound)

	// Delete non-existent key should not error
	err = storage.Delete(ctx, key)
	require.NoError(t, err)
}

func TestMemoryStorage_List(t *testing.T) {
	ctx := context.Background()
	storage := NewMemoryStorage(0)
	defer storage.Close()

	// Setup test data for multiple services
	scores := map[reputation.EndpointKey]reputation.Score{
		reputation.NewEndpointKey("eth", "endpoint1", sharedtypes.RPCType_JSON_RPC):  {Value: 80},
		reputation.NewEndpointKey("eth", "endpoint2", sharedtypes.RPCType_JSON_RPC):  {Value: 85},
		reputation.NewEndpointKey("poly", "endpoint1", sharedtypes.RPCType_JSON_RPC): {Value: 90},
	}

	err := storage.SetMultiple(ctx, scores)
	require.NoError(t, err)

	// List all
	allKeys, err := storage.List(ctx, "")
	require.NoError(t, err)
	require.Len(t, allKeys, 3)

	// List by service ID
	ethKeys, err := storage.List(ctx, "eth")
	require.NoError(t, err)
	require.Len(t, ethKeys, 2)
	for _, key := range ethKeys {
		require.Equal(t, protocol.ServiceID("eth"), key.ServiceID)
	}

	polyKeys, err := storage.List(ctx, "poly")
	require.NoError(t, err)
	require.Len(t, polyKeys, 1)
	require.Equal(t, protocol.ServiceID("poly"), polyKeys[0].ServiceID)
}

func TestMemoryStorage_TTL(t *testing.T) {
	ctx := context.Background()
	ttl := 100 * time.Millisecond
	storage := NewMemoryStorage(ttl)
	defer storage.Close()

	key := reputation.NewEndpointKey("eth", "endpoint1", sharedtypes.RPCType_JSON_RPC)
	score := reputation.Score{Value: 85, LastUpdated: time.Now()}

	// Set with TTL
	err := storage.Set(ctx, key, score)
	require.NoError(t, err)

	// Should be retrievable immediately
	_, err = storage.Get(ctx, key)
	require.NoError(t, err)

	// Wait for TTL to expire
	time.Sleep(ttl + 50*time.Millisecond)

	// Should be expired
	_, err = storage.Get(ctx, key)
	require.ErrorIs(t, err, reputation.ErrNotFound)
}

func TestMemoryStorage_Close(t *testing.T) {
	ctx := context.Background()
	storage := NewMemoryStorage(0)

	key := reputation.NewEndpointKey("eth", "endpoint1", sharedtypes.RPCType_JSON_RPC)
	score := reputation.Score{Value: 85, LastUpdated: time.Now()}

	// Set before close
	err := storage.Set(ctx, key, score)
	require.NoError(t, err)

	// Close storage
	err = storage.Close()
	require.NoError(t, err)

	// All operations should fail after close
	_, err = storage.Get(ctx, key)
	require.ErrorIs(t, err, reputation.ErrStorageClosed)

	err = storage.Set(ctx, key, score)
	require.ErrorIs(t, err, reputation.ErrStorageClosed)

	err = storage.Delete(ctx, key)
	require.ErrorIs(t, err, reputation.ErrStorageClosed)

	_, err = storage.List(ctx, "")
	require.ErrorIs(t, err, reputation.ErrStorageClosed)
}

func TestMemoryStorage_Cleanup(t *testing.T) {
	ctx := context.Background()
	ttl := 50 * time.Millisecond
	storage := NewMemoryStorage(ttl)
	defer storage.Close()

	// Add entries
	scores := map[reputation.EndpointKey]reputation.Score{
		reputation.NewEndpointKey("eth", "endpoint1", sharedtypes.RPCType_JSON_RPC): {Value: 80},
		reputation.NewEndpointKey("eth", "endpoint2", sharedtypes.RPCType_JSON_RPC): {Value: 85},
	}
	err := storage.SetMultiple(ctx, scores)
	require.NoError(t, err)

	require.Equal(t, 2, storage.Len())

	// Wait for TTL to expire
	time.Sleep(ttl + 20*time.Millisecond)

	// Cleanup should remove expired entries
	storage.Cleanup()

	require.Equal(t, 0, storage.Len())
}

func TestMemoryStorage_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	storage := NewMemoryStorage(0)
	defer storage.Close()

	key := reputation.NewEndpointKey("eth", "endpoint1", sharedtypes.RPCType_JSON_RPC)

	// Run concurrent reads and writes
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(v int) {
			score := reputation.Score{Value: float64(v), LastUpdated: time.Now()}
			_ = storage.Set(ctx, key, score)
			_, _ = storage.Get(ctx, key)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Should complete without race condition
	_, err := storage.Get(ctx, key)
	require.NoError(t, err)
}
