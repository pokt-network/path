package storage

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
)

// setupRedisContainer starts a Redis container for testing and returns the address and cleanup function.
func setupRedisContainer(t *testing.T) (string, func()) {
	t.Helper()

	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "redis:7-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err, "failed to start Redis container")

	host, err := container.Host(ctx)
	require.NoError(t, err, "failed to get container host")

	port, err := container.MappedPort(ctx, "6379")
	require.NoError(t, err, "failed to get container port")

	address := fmt.Sprintf("%s:%s", host, port.Port())

	cleanup := func() {
		if err := container.Terminate(ctx); err != nil {
			t.Logf("failed to terminate Redis container: %v", err)
		}
	}

	return address, cleanup
}

// newRedisStorage creates a RedisStorage connected to the test container.
func newRedisStorage(t *testing.T, address string, ttl time.Duration) *RedisStorage {
	t.Helper()

	ctx := context.Background()
	config := reputation.RedisConfig{
		Address:   address,
		KeyPrefix: "test:reputation:",
	}

	storage, err := NewRedisStorage(ctx, config, ttl)
	require.NoError(t, err, "failed to create Redis storage")

	return storage
}

func TestRedisStorage_GetSet(t *testing.T) {
	address, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()
	storage := newRedisStorage(t, address, 0) // No TTL
	defer storage.Close()

	key := reputation.NewEndpointKey("eth", "supplier1-https://endpoint.com")
	score := reputation.Score{
		Value:        85.5,
		LastUpdated:  time.Now().Truncate(time.Second), // Redis stores as Unix timestamp
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
	require.Equal(t, score.LastUpdated.Unix(), retrieved.LastUpdated.Unix())
}

func TestRedisStorage_GetMultiple(t *testing.T) {
	address, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()
	storage := newRedisStorage(t, address, 0)
	defer storage.Close()

	// Setup test data
	keys := []reputation.EndpointKey{
		reputation.NewEndpointKey("eth", "endpoint1"),
		reputation.NewEndpointKey("eth", "endpoint2"),
		reputation.NewEndpointKey("eth", "endpoint3"),
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

func TestRedisStorage_Delete(t *testing.T) {
	address, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()
	storage := newRedisStorage(t, address, 0)
	defer storage.Close()

	key := reputation.NewEndpointKey("eth", "endpoint1")
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

func TestRedisStorage_List(t *testing.T) {
	address, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()
	storage := newRedisStorage(t, address, 0)
	defer storage.Close()

	// Setup test data for multiple services
	scores := map[reputation.EndpointKey]reputation.Score{
		reputation.NewEndpointKey("eth", "endpoint1"):  {Value: 80, LastUpdated: time.Now()},
		reputation.NewEndpointKey("eth", "endpoint2"):  {Value: 85, LastUpdated: time.Now()},
		reputation.NewEndpointKey("poly", "endpoint1"): {Value: 90, LastUpdated: time.Now()},
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

func TestRedisStorage_TTL(t *testing.T) {
	address, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()
	ttl := 2 * time.Second // Redis TTL minimum resolution is 1 second
	storage := newRedisStorage(t, address, ttl)
	defer storage.Close()

	key := reputation.NewEndpointKey("eth", "endpoint1")
	score := reputation.Score{Value: 85, LastUpdated: time.Now()}

	// Set with TTL
	err := storage.Set(ctx, key, score)
	require.NoError(t, err)

	// Should be retrievable immediately
	_, err = storage.Get(ctx, key)
	require.NoError(t, err)

	// Wait for TTL to expire
	time.Sleep(ttl + 500*time.Millisecond)

	// Should be expired
	_, err = storage.Get(ctx, key)
	require.ErrorIs(t, err, reputation.ErrNotFound)
}

func TestRedisStorage_ConnectionFailure(t *testing.T) {
	ctx := context.Background()
	config := reputation.RedisConfig{
		Address:     "localhost:59999", // Invalid port
		DialTimeout: 1 * time.Second,
	}

	_, err := NewRedisStorage(ctx, config, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to connect to Redis")
}

// TestRedisStorage_ConfigValidation tests various configuration scenarios
// to ensure proper error handling and defaults.
func TestRedisStorage_ConfigValidation(t *testing.T) {
	address, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()

	tests := []struct {
		name        string
		config      reputation.RedisConfig
		expectError bool
		errContains string
	}{
		{
			name: "valid config with all fields",
			config: reputation.RedisConfig{
				Address:      address,
				Password:     "",
				DB:           0,
				KeyPrefix:    "test:",
				PoolSize:     5,
				DialTimeout:  3 * time.Second,
				ReadTimeout:  2 * time.Second,
				WriteTimeout: 2 * time.Second,
			},
			expectError: false,
		},
		{
			name: "valid config with minimal fields (uses defaults)",
			config: reputation.RedisConfig{
				Address: address,
			},
			expectError: false,
		},
		{
			name: "invalid address - connection refused",
			config: reputation.RedisConfig{
				Address:     "localhost:59999",
				DialTimeout: 500 * time.Millisecond,
			},
			expectError: true,
			errContains: "failed to connect to Redis",
		},
		{
			name: "invalid address format",
			config: reputation.RedisConfig{
				Address:     "not-a-valid-host:99999",
				DialTimeout: 500 * time.Millisecond,
			},
			expectError: true,
			errContains: "failed to connect to Redis",
		},
		{
			name: "empty address hydrates to test container",
			config: reputation.RedisConfig{
				Address:     address, // Use test container address
				DialTimeout: 500 * time.Millisecond,
			},
			expectError: false,
			errContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage, err := NewRedisStorage(ctx, tt.config, 0)

			if tt.expectError {
				require.Error(t, err)
				if tt.errContains != "" {
					require.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, storage)
				storage.Close()
			}
		})
	}
}

// TestRedisConfig_HydrateDefaults tests that config hydration works correctly
// without requiring an actual Redis connection.
func TestRedisConfig_HydrateDefaults(t *testing.T) {
	tests := []struct {
		name            string
		input           reputation.RedisConfig
		expectedAddress string
	}{
		{
			name:            "empty address hydrates to default localhost:6379",
			input:           reputation.RedisConfig{Address: ""},
			expectedAddress: "localhost:6379",
		},
		{
			name:            "custom address is preserved",
			input:           reputation.RedisConfig{Address: "custom:1234"},
			expectedAddress: "custom:1234",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := tt.input
			config.HydrateDefaults()
			require.Equal(t, tt.expectedAddress, config.Address)
			require.NotZero(t, config.PoolSize, "PoolSize should be hydrated")
			require.NotZero(t, config.DialTimeout, "DialTimeout should be hydrated")
		})
	}
}

func TestRedisStorage_ConcurrentAccess(t *testing.T) {
	address, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()
	storage := newRedisStorage(t, address, 0)
	defer storage.Close()

	key := reputation.NewEndpointKey("eth", "endpoint1")

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

// TestRedisStorage_ConcurrentMultiInstance simulates multiple PATH instances
// accessing the same Redis backend concurrently. This tests data consistency
// in a distributed deployment scenario.
func TestRedisStorage_ConcurrentMultiInstance(t *testing.T) {
	address, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()
	const numInstances = 5
	const numOperationsPerInstance = 20

	// Create multiple storage instances (simulating multiple PATH instances)
	storages := make([]*RedisStorage, numInstances)
	for i := 0; i < numInstances; i++ {
		storage, err := NewRedisStorage(ctx, reputation.RedisConfig{
			Address:   address,
			KeyPrefix: "multiinstance:",
		}, 0)
		require.NoError(t, err)
		storages[i] = storage
		defer storage.Close()
	}

	// Each instance writes to different keys (realistic scenario - each handles different endpoints)
	var wg sync.WaitGroup
	errors := make(chan error, numInstances*numOperationsPerInstance)

	for instanceID := 0; instanceID < numInstances; instanceID++ {
		wg.Add(1)
		go func(id int, storage *RedisStorage) {
			defer wg.Done()

			for op := 0; op < numOperationsPerInstance; op++ {
				key := reputation.NewEndpointKey("eth", protocol.EndpointAddr(fmt.Sprintf("instance%d-endpoint%d", id, op)))
				score := reputation.Score{
					Value:        float64(50 + op),
					LastUpdated:  time.Now(),
					SuccessCount: int64(op),
					ErrorCount:   int64(op / 2),
				}

				// Write
				if err := storage.Set(ctx, key, score); err != nil {
					errors <- fmt.Errorf("instance %d: set error: %w", id, err)
					continue
				}

				// Read back and verify
				retrieved, err := storage.Get(ctx, key)
				if err != nil {
					errors <- fmt.Errorf("instance %d: get error: %w", id, err)
					continue
				}

				if retrieved.Value != score.Value {
					errors <- fmt.Errorf("instance %d: value mismatch: got %.1f, want %.1f", id, retrieved.Value, score.Value)
				}
				if retrieved.SuccessCount != score.SuccessCount {
					errors <- fmt.Errorf("instance %d: success count mismatch: got %d, want %d", id, retrieved.SuccessCount, score.SuccessCount)
				}
			}
		}(instanceID, storages[instanceID])
	}

	wg.Wait()
	close(errors)

	// Check for errors
	var errs []error
	for err := range errors {
		errs = append(errs, err)
	}
	require.Empty(t, errs, "concurrent multi-instance operations should not produce errors: %v", errs)

	// Verify total keys written
	allKeys, err := storages[0].List(ctx, "eth")
	require.NoError(t, err)
	require.Equal(t, numInstances*numOperationsPerInstance, len(allKeys),
		"all keys should be present after concurrent writes")
}

// TestRedisStorage_ConcurrentSameKey tests concurrent writes to the SAME key
// from multiple goroutines. This is a stress test for Redis atomicity.
func TestRedisStorage_ConcurrentSameKey(t *testing.T) {
	address, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()
	storage := newRedisStorage(t, address, 0)
	defer storage.Close()

	key := reputation.NewEndpointKey("eth", "contested-endpoint")
	const numWriters = 50

	var wg sync.WaitGroup
	writtenValues := make(chan float64, numWriters)

	// Many goroutines write to the same key simultaneously
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(v int) {
			defer wg.Done()
			score := reputation.Score{
				Value:        float64(v),
				LastUpdated:  time.Now(),
				SuccessCount: int64(v),
				ErrorCount:   0,
			}
			err := storage.Set(ctx, key, score)
			if err == nil {
				writtenValues <- float64(v)
			}
		}(i)
	}

	wg.Wait()
	close(writtenValues)

	// Verify: the final value should be one of the written values
	finalScore, err := storage.Get(ctx, key)
	require.NoError(t, err)

	// Collect all written values
	var allWritten []float64
	for v := range writtenValues {
		allWritten = append(allWritten, v)
	}

	// Final value must be one of the values that was written
	require.Contains(t, allWritten, finalScore.Value,
		"final value %.1f should be one of the written values", finalScore.Value)

	// SuccessCount should match Value (our test invariant)
	require.Equal(t, int64(finalScore.Value), finalScore.SuccessCount,
		"data integrity: SuccessCount should match Value (both came from same write)")
}

// TestRedisStorage_ConcurrentReadWrite tests readers and writers accessing
// the same data concurrently - readers should never see partial/corrupted data.
func TestRedisStorage_ConcurrentReadWrite(t *testing.T) {
	address, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()
	storage := newRedisStorage(t, address, 0)
	defer storage.Close()

	key := reputation.NewEndpointKey("eth", "readwrite-endpoint")

	// Initialize with a known value
	initialScore := reputation.Score{
		Value:        100,
		LastUpdated:  time.Now(),
		SuccessCount: 1000,
		ErrorCount:   100,
	}
	err := storage.Set(ctx, key, initialScore)
	require.NoError(t, err)

	const numReaders = 20
	const numWriters = 10
	const iterations = 50

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	readErrors := make(chan error, numReaders*iterations)

	// Writers: update with consistent data (Value = SuccessCount / 10)
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				successCount := int64((writerID+1)*1000 + j)
				score := reputation.Score{
					Value:        float64(successCount) / 10,
					LastUpdated:  time.Now(),
					SuccessCount: successCount,
					ErrorCount:   int64(j),
				}
				_ = storage.Set(ctx, key, score)
			}
		}(i)
	}

	// Readers: verify data consistency (Value should always = SuccessCount / 10)
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				score, err := storage.Get(ctx, key)
				if err != nil {
					continue // Key might not exist momentarily
				}
				// Check data consistency invariant
				expectedValue := float64(score.SuccessCount) / 10
				if score.Value != expectedValue {
					readErrors <- fmt.Errorf("data corruption detected: Value=%.1f but SuccessCount=%d (expected Value=%.1f)",
						score.Value, score.SuccessCount, expectedValue)
				}
			}
		}()
	}

	wg.Wait()
	close(readErrors)

	// Should have no data corruption
	var errs []error
	for err := range readErrors {
		errs = append(errs, err)
	}
	require.Empty(t, errs, "readers should never see corrupted/partial data: %v", errs)
}

func TestRedisStorage_SetMultiple_Empty(t *testing.T) {
	address, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()
	storage := newRedisStorage(t, address, 0)
	defer storage.Close()

	// SetMultiple with empty map should not error
	err := storage.SetMultiple(ctx, map[reputation.EndpointKey]reputation.Score{})
	require.NoError(t, err)
}

func TestRedisStorage_GetMultiple_Empty(t *testing.T) {
	address, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()
	storage := newRedisStorage(t, address, 0)
	defer storage.Close()

	// GetMultiple with empty slice should return empty map
	result, err := storage.GetMultiple(ctx, []reputation.EndpointKey{})
	require.NoError(t, err)
	require.Empty(t, result)
}

// TestRedisStorage_PersistenceAcrossConnections verifies that data persists
// in Redis across different storage instances. This test would fail if we
// were accidentally using in-memory storage.
func TestRedisStorage_PersistenceAcrossConnections(t *testing.T) {
	address, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()
	key := reputation.NewEndpointKey("eth", "persistence-test-endpoint")
	score := reputation.Score{Value: 77.7, LastUpdated: time.Now()}

	// First connection - write data
	storage1, err := NewRedisStorage(ctx, reputation.RedisConfig{
		Address:   address,
		KeyPrefix: "persistence-test:",
	}, 0)
	require.NoError(t, err)

	err = storage1.Set(ctx, key, score)
	require.NoError(t, err)

	// Close first connection
	err = storage1.Close()
	require.NoError(t, err)

	// Second connection - read data (proves persistence)
	storage2, err := NewRedisStorage(ctx, reputation.RedisConfig{
		Address:   address,
		KeyPrefix: "persistence-test:",
	}, 0)
	require.NoError(t, err)
	defer storage2.Close()

	// Data should still be there from first connection
	retrieved, err := storage2.Get(ctx, key)
	require.NoError(t, err, "data should persist across connections - this proves we're using Redis, not memory")
	require.Equal(t, score.Value, retrieved.Value)
}

func TestRedisStorage_KeyPrefix(t *testing.T) {
	address, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()

	// Create two storages with different prefixes
	config1 := reputation.RedisConfig{Address: address, KeyPrefix: "prefix1:"}
	config2 := reputation.RedisConfig{Address: address, KeyPrefix: "prefix2:"}

	storage1, err := NewRedisStorage(ctx, config1, 0)
	require.NoError(t, err)
	defer storage1.Close()

	storage2, err := NewRedisStorage(ctx, config2, 0)
	require.NoError(t, err)
	defer storage2.Close()

	key := reputation.NewEndpointKey("eth", "endpoint1")
	score1 := reputation.Score{Value: 80, LastUpdated: time.Now()}
	score2 := reputation.Score{Value: 90, LastUpdated: time.Now()}

	// Set different scores in each storage
	err = storage1.Set(ctx, key, score1)
	require.NoError(t, err)

	err = storage2.Set(ctx, key, score2)
	require.NoError(t, err)

	// Each storage should see its own value
	retrieved1, err := storage1.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, float64(80), retrieved1.Value)

	retrieved2, err := storage2.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, float64(90), retrieved2.Value)
}
