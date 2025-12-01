package reputation

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// mockStorage is a simple in-memory storage for testing.
type mockStorage struct {
	mu     sync.RWMutex
	scores map[string]Score
	closed bool
}

func newMockStorage() *mockStorage {
	return &mockStorage{
		scores: make(map[string]Score),
	}
}

func (m *mockStorage) Get(_ context.Context, key EndpointKey) (Score, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return Score{}, ErrStorageClosed
	}
	score, ok := m.scores[key.String()]
	if !ok {
		return Score{}, ErrNotFound
	}
	return score, nil
}

func (m *mockStorage) GetMultiple(_ context.Context, keys []EndpointKey) (map[EndpointKey]Score, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, ErrStorageClosed
	}
	result := make(map[EndpointKey]Score, len(keys))
	for _, key := range keys {
		if score, ok := m.scores[key.String()]; ok {
			result[key] = score
		}
	}
	return result, nil
}

func (m *mockStorage) Set(_ context.Context, key EndpointKey, score Score) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrStorageClosed
	}
	m.scores[key.String()] = score
	return nil
}

func (m *mockStorage) SetMultiple(_ context.Context, scores map[EndpointKey]Score) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrStorageClosed
	}
	for key, score := range scores {
		m.scores[key.String()] = score
	}
	return nil
}

func (m *mockStorage) Delete(_ context.Context, key EndpointKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrStorageClosed
	}
	delete(m.scores, key.String())
	return nil
}

func (m *mockStorage) List(_ context.Context, _ string) ([]EndpointKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, ErrStorageClosed
	}
	// Return all keys - parse the string keys back to EndpointKey
	var keys []EndpointKey
	for keyStr := range m.scores {
		// Keys are stored as "serviceID:endpointAddr"
		idx := strings.IndexByte(keyStr, ':')
		if idx >= 0 {
			keys = append(keys, NewEndpointKey(
				protocol.ServiceID(keyStr[:idx]),
				protocol.EndpointAddr(keyStr[idx+1:]),
			))
		}
	}
	return keys, nil
}

func (m *mockStorage) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func TestService_RecordSignal(t *testing.T) {
	ctx := context.Background()
	store := newMockStorage()
	defer store.Close()

	config := Config{
		Enabled:      true,
		InitialScore: 80,
		MinThreshold: 30,
	}
	config.HydrateDefaults()

	svc := NewService(config, store)
	err := svc.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = svc.Stop() }()

	key := NewEndpointKey("eth", "endpoint1")

	// Record success signal
	err = svc.RecordSignal(ctx, key, NewSuccessSignal(100*time.Millisecond))
	require.NoError(t, err)

	// Verify score increased
	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, 81.0, score.Value) // 80 + 1
	require.Equal(t, int64(1), score.SuccessCount)
	require.Equal(t, int64(0), score.ErrorCount)

	// Record error signal
	err = svc.RecordSignal(ctx, key, NewMajorErrorSignal("timeout", 5*time.Second))
	require.NoError(t, err)

	score, err = svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, 71.0, score.Value) // 81 - 10
	require.Equal(t, int64(1), score.SuccessCount)
	require.Equal(t, int64(1), score.ErrorCount)
}

func TestService_ScoreClamping(t *testing.T) {
	ctx := context.Background()
	store := newMockStorage()
	defer store.Close()

	config := Config{
		Enabled:      true,
		InitialScore: 80,
	}
	config.HydrateDefaults()

	svc := NewService(config, store)
	err := svc.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = svc.Stop() }()

	key := NewEndpointKey("eth", "endpoint1")

	// Record many successes - should clamp at MaxScore
	for i := 0; i < 30; i++ {
		err = svc.RecordSignal(ctx, key, NewSuccessSignal(100*time.Millisecond))
		require.NoError(t, err)
	}

	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, MaxScore, score.Value)

	// Record many fatal errors - should clamp at MinScore
	for i := 0; i < 10; i++ {
		err = svc.RecordSignal(ctx, key, NewFatalErrorSignal("critical failure"))
		require.NoError(t, err)
	}

	score, err = svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, MinScore, score.Value)
}

func TestService_GetScores(t *testing.T) {
	ctx := context.Background()
	store := newMockStorage()
	defer store.Close()

	config := Config{
		Enabled:      true,
		InitialScore: 80,
	}
	config.HydrateDefaults()

	svc := NewService(config, store)
	err := svc.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = svc.Stop() }()

	keys := []EndpointKey{
		NewEndpointKey("eth", "endpoint1"),
		NewEndpointKey("eth", "endpoint2"),
		NewEndpointKey("eth", "endpoint3"),
	}

	// Record signals for first two endpoints
	err = svc.RecordSignal(ctx, keys[0], NewSuccessSignal(100*time.Millisecond))
	require.NoError(t, err)
	err = svc.RecordSignal(ctx, keys[1], NewMajorErrorSignal("error", time.Second))
	require.NoError(t, err)

	// Get all scores
	scores, err := svc.GetScores(ctx, keys)
	require.NoError(t, err)
	require.Len(t, scores, 2) // Only 2 have scores

	require.Equal(t, 81.0, scores[keys[0]].Value)
	require.Equal(t, 70.0, scores[keys[1]].Value)
	_, exists := scores[keys[2]]
	require.False(t, exists)
}

func TestService_FilterByScore(t *testing.T) {
	ctx := context.Background()
	store := newMockStorage()
	defer store.Close()

	config := Config{
		Enabled:      true,
		InitialScore: 50,
		MinThreshold: 30,
	}
	config.HydrateDefaults()

	svc := NewService(config, store)
	err := svc.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = svc.Stop() }()

	keys := []EndpointKey{
		NewEndpointKey("eth", "good"),
		NewEndpointKey("eth", "bad"),
		NewEndpointKey("eth", "unknown"),
	}

	// Set up scores: good=60, bad=20, unknown=not recorded (uses initial)
	// Good endpoint
	for i := 0; i < 10; i++ {
		err = svc.RecordSignal(ctx, keys[0], NewSuccessSignal(100*time.Millisecond))
		require.NoError(t, err)
	}
	// Bad endpoint
	for i := 0; i < 3; i++ {
		err = svc.RecordSignal(ctx, keys[1], NewFatalErrorSignal("failure"))
		require.NoError(t, err)
	}

	score, _ := svc.GetScore(ctx, keys[0])
	require.Equal(t, 60.0, score.Value) // 50 + 10

	score, _ = svc.GetScore(ctx, keys[1])
	require.Equal(t, MinScore, score.Value) // 50 - 150 = 0 (clamped)

	// Filter with threshold 30
	passed, err := svc.FilterByScore(ctx, keys, 30)
	require.NoError(t, err)
	require.Len(t, passed, 2) // good (60) and unknown (50 initial)

	// Verify the correct endpoints passed
	passedMap := make(map[string]bool)
	for _, k := range passed {
		passedMap[k.String()] = true
	}
	require.True(t, passedMap[keys[0].String()])  // good
	require.False(t, passedMap[keys[1].String()]) // bad
	require.True(t, passedMap[keys[2].String()])  // unknown gets initial score
}

func TestService_ResetScore(t *testing.T) {
	ctx := context.Background()
	store := newMockStorage()
	defer store.Close()

	config := Config{
		Enabled:      true,
		InitialScore: 80,
	}
	config.HydrateDefaults()

	svc := NewService(config, store)
	err := svc.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = svc.Stop() }()

	key := NewEndpointKey("eth", "endpoint1")

	// Lower the score
	for i := 0; i < 5; i++ {
		err = svc.RecordSignal(ctx, key, NewMajorErrorSignal("error", time.Second))
		require.NoError(t, err)
	}

	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, 30.0, score.Value) // 80 - 50

	// Reset
	err = svc.ResetScore(ctx, key)
	require.NoError(t, err)

	score, err = svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, 80.0, score.Value)
}

func TestService_DisabledMode(t *testing.T) {
	ctx := context.Background()
	store := newMockStorage()
	defer store.Close()

	config := Config{
		Enabled:      false,
		InitialScore: 80,
	}

	svc := NewService(config, store)
	key := NewEndpointKey("eth", "endpoint1")

	// All operations should succeed but be no-ops
	err := svc.RecordSignal(ctx, key, NewFatalErrorSignal("critical"))
	require.NoError(t, err)

	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, 80.0, score.Value) // Returns initial score

	// Filter should pass all endpoints when disabled
	keys := []EndpointKey{key}
	passed, err := svc.FilterByScore(ctx, keys, 100)
	require.NoError(t, err)
	require.Len(t, passed, 1) // All pass when disabled
}

func TestService_AsyncWriteToStorage(t *testing.T) {
	ctx := context.Background()
	store := newMockStorage()
	defer store.Close()

	config := Config{
		Enabled:      true,
		InitialScore: 80,
		SyncConfig: SyncConfig{
			FlushInterval:   10 * time.Millisecond, // Short interval for faster test
			WriteBufferSize: 100,
			RefreshInterval: time.Hour, // Don't refresh during test
		},
	}

	svc := NewService(config, store)
	err := svc.Start(ctx)
	require.NoError(t, err)

	key := NewEndpointKey("eth", "endpoint1")

	// Record signal - this updates local cache immediately but queues async write
	err = svc.RecordSignal(ctx, key, NewSuccessSignal(100*time.Millisecond))
	require.NoError(t, err)

	// Local cache should have the value immediately
	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, 81.0, score.Value, "local cache should be updated immediately")

	// Use Eventually to wait for async write to storage (more robust than sleep)
	require.Eventually(t, func() bool {
		storedScore, err := store.Get(ctx, key)
		return err == nil && storedScore.Value == 81.0
	}, 500*time.Millisecond, 10*time.Millisecond, "score should be written to storage async")

	err = svc.Stop()
	require.NoError(t, err)
}

func TestService_StopFlushesWrites(t *testing.T) {
	ctx := context.Background()
	store := newMockStorage()
	defer store.Close()

	config := Config{
		Enabled:      true,
		InitialScore: 80,
		SyncConfig: SyncConfig{
			FlushInterval:   time.Hour, // Very long interval - won't flush automatically
			WriteBufferSize: 100,
			RefreshInterval: time.Hour,
		},
	}

	svc := NewService(config, store)
	err := svc.Start(ctx)
	require.NoError(t, err)

	key := NewEndpointKey("eth", "endpoint1")

	// Record signal
	err = svc.RecordSignal(ctx, key, NewSuccessSignal(100*time.Millisecond))
	require.NoError(t, err)

	// Storage should NOT have the value yet (flush interval is 1 hour)
	_, err = store.Get(ctx, key)
	require.ErrorIs(t, err, ErrNotFound, "storage should not have value before Stop")

	// Stop should flush pending writes
	err = svc.Stop()
	require.NoError(t, err)

	// Now storage should have the value
	storedScore, err := store.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, 81.0, storedScore.Value, "Stop should flush pending writes")
}

func TestService_RefreshFromStorage(t *testing.T) {
	ctx := context.Background()
	store := newMockStorage()
	defer store.Close()

	// Pre-populate storage
	key := NewEndpointKey("eth", "endpoint1")
	existingScore := Score{
		Value:        95,
		LastUpdated:  time.Now(),
		SuccessCount: 50,
		ErrorCount:   2,
	}
	err := store.Set(ctx, key, existingScore)
	require.NoError(t, err)

	config := Config{
		Enabled:      true,
		InitialScore: 80,
	}
	config.HydrateDefaults()

	svc := NewService(config, store)
	err = svc.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = svc.Stop() }()

	// Should have loaded from storage
	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, 95.0, score.Value)
	require.Equal(t, int64(50), score.SuccessCount)
}

func TestClamp(t *testing.T) {
	tests := []struct {
		value, min, max, expected float64
	}{
		{50, 0, 100, 50},   // Within range
		{-10, 0, 100, 0},   // Below min
		{150, 0, 100, 100}, // Above max
		{0, 0, 100, 0},     // At min
		{100, 0, 100, 100}, // At max
	}

	for _, tt := range tests {
		result := clamp(tt.value, tt.min, tt.max)
		require.Equal(t, tt.expected, result)
	}
}

func TestService_Recovery_GetScore(t *testing.T) {
	ctx := context.Background()
	store := newMockStorage()
	defer store.Close()

	// Use short recovery timeout for testing
	config := Config{
		Enabled:         true,
		InitialScore:    80,
		MinThreshold:    30,
		RecoveryTimeout: 100 * time.Millisecond, // Short timeout for test
	}
	config.HydrateDefaults()

	svc := NewService(config, store)
	err := svc.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = svc.Stop() }()

	key := NewEndpointKey("eth", "endpoint1")

	// Lower the score below threshold
	for i := 0; i < 10; i++ {
		err = svc.RecordSignal(ctx, key, NewFatalErrorSignal("failure"))
		require.NoError(t, err)
	}

	// Verify score is at minimum (below threshold)
	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, MinScore, score.Value)
	require.Less(t, score.Value, config.MinThreshold, "score should be below threshold")

	// Wait for recovery timeout
	time.Sleep(150 * time.Millisecond)

	// Get score again - should trigger recovery
	score, err = svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, config.InitialScore, score.Value, "score should be recovered to initial")
	require.Equal(t, int64(0), score.SuccessCount, "success count should be reset")
	require.Equal(t, int64(0), score.ErrorCount, "error count should be reset")
}

func TestService_Recovery_FilterByScore(t *testing.T) {
	ctx := context.Background()
	store := newMockStorage()
	defer store.Close()

	config := Config{
		Enabled:         true,
		InitialScore:    80,
		MinThreshold:    30,
		RecoveryTimeout: 100 * time.Millisecond,
	}
	config.HydrateDefaults()

	svc := NewService(config, store)
	err := svc.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = svc.Stop() }()

	key := NewEndpointKey("eth", "endpoint1")

	// Lower the score below threshold
	for i := 0; i < 10; i++ {
		err = svc.RecordSignal(ctx, key, NewFatalErrorSignal("failure"))
		require.NoError(t, err)
	}

	// Endpoint should be filtered out (below threshold)
	keys := []EndpointKey{key}
	passed, err := svc.FilterByScore(ctx, keys, config.MinThreshold)
	require.NoError(t, err)
	require.Len(t, passed, 0, "endpoint should be filtered out before recovery")

	// Wait for recovery timeout
	time.Sleep(150 * time.Millisecond)

	// Now endpoint should pass filter after recovery
	passed, err = svc.FilterByScore(ctx, keys, config.MinThreshold)
	require.NoError(t, err)
	require.Len(t, passed, 1, "endpoint should pass filter after recovery")
	require.Equal(t, key, passed[0])
}

func TestService_NoRecovery_AboveThreshold(t *testing.T) {
	ctx := context.Background()
	store := newMockStorage()
	defer store.Close()

	config := Config{
		Enabled:         true,
		InitialScore:    80,
		MinThreshold:    30,
		RecoveryTimeout: 50 * time.Millisecond,
	}
	config.HydrateDefaults()

	svc := NewService(config, store)
	err := svc.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = svc.Stop() }()

	key := NewEndpointKey("eth", "endpoint1")

	// Record some errors but stay above threshold
	for i := 0; i < 3; i++ {
		err = svc.RecordSignal(ctx, key, NewMajorErrorSignal("error", time.Second))
		require.NoError(t, err)
	}

	score, _ := svc.GetScore(ctx, key)
	require.Equal(t, 50.0, score.Value) // 80 - 30
	require.GreaterOrEqual(t, score.Value, config.MinThreshold, "score should be above threshold")

	// Wait for recovery timeout
	time.Sleep(100 * time.Millisecond)

	// Score should NOT be reset because it's above threshold
	score, err = svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, 50.0, score.Value, "score should NOT be reset when above threshold")
	require.Equal(t, int64(3), score.ErrorCount, "error count should be preserved")
}

func TestService_NoRecovery_BeforeTimeout(t *testing.T) {
	ctx := context.Background()
	store := newMockStorage()
	defer store.Close()

	config := Config{
		Enabled:         true,
		InitialScore:    80,
		MinThreshold:    30,
		RecoveryTimeout: 1 * time.Hour, // Very long timeout
	}
	config.HydrateDefaults()

	svc := NewService(config, store)
	err := svc.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = svc.Stop() }()

	key := NewEndpointKey("eth", "endpoint1")

	// Lower the score below threshold
	for i := 0; i < 10; i++ {
		err = svc.RecordSignal(ctx, key, NewFatalErrorSignal("failure"))
		require.NoError(t, err)
	}

	// Score should NOT recover - timeout hasn't passed
	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, MinScore, score.Value, "score should NOT recover before timeout")
}

func TestService_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	store := newMockStorage()
	defer store.Close()

	config := Config{
		Enabled:      true,
		InitialScore: 80,
	}
	config.HydrateDefaults()

	svc := NewService(config, store)
	err := svc.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = svc.Stop() }()

	key := NewEndpointKey("eth", "endpoint1")
	const goroutines = 10
	const signalsPerGoroutine = 100

	// Run concurrent signals on the same endpoint
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < signalsPerGoroutine; j++ {
				if j%2 == 0 {
					_ = svc.RecordSignal(ctx, key, NewSuccessSignal(100*time.Millisecond))
				} else {
					_ = svc.RecordSignal(ctx, key, NewMinorErrorSignal("error"))
				}
				_, _ = svc.GetScore(ctx, key)
			}
		}()
	}
	wg.Wait()

	// Service should still be functional after concurrent access
	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.True(t, score.IsValid(), "score should be within valid range after concurrent access")

	// Total signals should match
	totalSignals := int64(goroutines * signalsPerGoroutine)
	require.Equal(t, totalSignals, score.SuccessCount+score.ErrorCount, "all signals should be counted")
}
