package gateway

import (
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
	qostypes "github.com/pokt-network/path/qos/types"
)

// TestNewObservationQueue tests queue creation with config.
func TestNewObservationQueue(t *testing.T) {
	config := ObservationQueueConfig{
		Enabled:     true,
		SampleRate:  0.5,
		WorkerCount: 2,
		QueueSize:   100,
	}

	logger := polyzero.NewLogger()
	queue := NewObservationQueue(config, logger)

	require.NotNil(t, queue)
	require.True(t, queue.IsEnabled())
	require.Equal(t, 0.5, queue.config.SampleRate)
	require.Equal(t, 2, queue.config.WorkerCount)
	require.Equal(t, 100, queue.config.QueueSize)
}

// TestObservationQueueConfigDefaults tests that defaults are applied.
func TestObservationQueueConfigDefaults(t *testing.T) {
	config := ObservationQueueConfig{
		Enabled: true,
		// Leave other fields empty to test defaults
	}
	config.HydrateDefaults()

	require.Equal(t, DefaultObservationSampleRate, config.SampleRate)
	require.Equal(t, DefaultObservationWorkerCount, config.WorkerCount)
	require.Equal(t, DefaultObservationQueueSize, config.QueueSize)
}

// TestObservationQueueDisabled tests that disabled queue returns false.
func TestObservationQueueDisabled(t *testing.T) {
	config := ObservationQueueConfig{
		Enabled:     false,
		SampleRate:  1.0, // 100% sample rate
		WorkerCount: 2,
		QueueSize:   100,
	}

	logger := polyzero.NewLogger()
	queue := NewObservationQueue(config, logger)

	require.False(t, queue.IsEnabled())

	// Try to queue - should return false because queue is disabled
	obs := &QueuedObservation{
		ServiceID:    "eth",
		EndpointAddr: "http://example.com",
		Source:       SourceUserRequest,
		Timestamp:    time.Now(),
	}
	require.False(t, queue.TryQueue(obs))
	require.False(t, queue.Submit(obs))
}

// TestObservationQueueSubmitAlwaysQueues tests that Submit bypasses sampling.
func TestObservationQueueSubmitAlwaysQueues(t *testing.T) {
	config := ObservationQueueConfig{
		Enabled:     true,
		SampleRate:  0.0, // 0% sample rate - TryQueue should always skip
		WorkerCount: 2,
		QueueSize:   100,
	}

	logger := polyzero.NewLogger()
	queue := NewObservationQueue(config, logger)

	obs := &QueuedObservation{
		ServiceID:    "eth",
		EndpointAddr: "http://example.com",
		Source:       SourceHealthCheck,
		Timestamp:    time.Now(),
	}

	// Submit should bypass sampling and queue
	require.True(t, queue.Submit(obs))

	// Wait for processing
	time.Sleep(50 * time.Millisecond)

	queued, _, _, _ := queue.GetMetrics()
	require.Equal(t, int64(1), queued)
}

// TestObservationQueuePerServiceRate tests per-service sample rate overrides.
func TestObservationQueuePerServiceRate(t *testing.T) {
	config := ObservationQueueConfig{
		Enabled:     true,
		SampleRate:  0.1, // 10% default
		WorkerCount: 2,
		QueueSize:   100,
	}

	logger := polyzero.NewLogger()
	queue := NewObservationQueue(config, logger)

	// Set 100% rate for eth service
	queue.SetPerServiceRate("eth", 1.0)

	// Verify rate was set
	rate := queue.getSampleRate("eth")
	require.Equal(t, 1.0, rate)

	// Other services should use default
	rate = queue.getSampleRate("solana")
	require.Equal(t, 0.1, rate)
}

// TestObservationQueueStats tests stats tracking.
func TestObservationQueueStats(t *testing.T) {
	config := ObservationQueueConfig{
		Enabled:     true,
		SampleRate:  1.0, // 100% sample rate
		WorkerCount: 2,
		QueueSize:   100,
	}

	logger := polyzero.NewLogger()
	queue := NewObservationQueue(config, logger)

	// Queue an observation via Submit (always queues)
	obs := &QueuedObservation{
		ServiceID:    "eth",
		EndpointAddr: "http://example.com",
		Source:       SourceHealthCheck,
		Timestamp:    time.Now(),
	}
	queue.Submit(obs)

	// Wait for processing
	time.Sleep(50 * time.Millisecond)

	queued, processed, _, _ := queue.GetMetrics()
	require.Equal(t, int64(1), queued)
	require.GreaterOrEqual(t, processed, int64(0)) // May or may not be processed yet
}

// TestObservationQueueStop tests graceful shutdown.
func TestObservationQueueStop(t *testing.T) {
	config := ObservationQueueConfig{
		Enabled:     true,
		SampleRate:  1.0,
		WorkerCount: 2,
		QueueSize:   100,
	}

	logger := polyzero.NewLogger()
	queue := NewObservationQueue(config, logger)

	// Queue a few observations
	for i := 0; i < 5; i++ {
		queue.Submit(&QueuedObservation{
			ServiceID:    "eth",
			EndpointAddr: protocol.EndpointAddr("http://example.com"),
			Source:       SourceHealthCheck,
			Timestamp:    time.Now(),
		})
	}

	// Stop should not panic
	queue.Stop()
}

// TestExtractorRegistryBasics tests the extractor registry.
func TestExtractorRegistryBasics(t *testing.T) {
	registry := qostypes.NewExtractorRegistry()

	require.NotNil(t, registry)
	require.Equal(t, 0, registry.Count())

	// Getting non-existent extractor returns NoOp fallback (not nil)
	extractor := registry.Get("nonexistent")
	require.NotNil(t, extractor)
	// It should be a NoOp extractor
	_, isNoOp := extractor.(*qostypes.NoOpDataExtractor)
	require.True(t, isNoOp, "Expected NoOpDataExtractor for non-existent service")
}
