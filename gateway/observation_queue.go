// Package gateway provides async observation processing for QoS data extraction.
//
// The ObservationQueue enables non-blocking response processing by separating
// the hot path (storing bytes + recording reputation signals) from the heavy
// parsing work (extracting block_height, chain_id, etc.).
//
// Architecture:
//
//	Response from Endpoint
//	         │
//	         ▼
//	┌─────────────────────────────────────┐
//	│     UpdateWithResponse (FAST)       │
//	├─────────────────────────────────────┤
//	│ 1. Store raw bytes (always)         │ ← for client write-back
//	│ 2. Record reputation (always)       │ ← status + latency (no parsing)
//	│ 3. Queue for parsing (sampled)      │ ← async, non-blocking
//	└─────────────────────────────────────┘
//	         │                    │
//	         ▼                    ▼ (async worker)
//	   Write to Client      Deep parse response
//	   (immediate)          Update endpointStore
//
// This design ensures:
//   - Client latency is minimal (no parsing in hot path)
//   - Reputation gets 100% of status/latency signals
//   - Heavy parsing (JSON decode, validation) is sampled and async
package gateway

import (
	"math/rand"
	"sync"
	"time"

	"github.com/alitto/pond/v2"
	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/protocol"
	qostypes "github.com/pokt-network/path/qos/types"
)

// ObservationQueueConfig configures the async observation processing.
type ObservationQueueConfig struct {
	// Enabled enables/disables async observation processing.
	// When disabled, all parsing happens synchronously (legacy behavior).
	Enabled bool `yaml:"enabled,omitempty"`

	// SampleRate is the fraction of requests to deep-parse (0.0 to 1.0).
	// Default: 0.1 (10% of requests get deep parsing)
	// Note: Reputation signals are recorded for 100% of requests regardless.
	SampleRate float64 `yaml:"sample_rate,omitempty"`

	// WorkerCount is the number of worker goroutines for parsing.
	// Default: 4
	WorkerCount int `yaml:"worker_count,omitempty"`

	// QueueSize is the max number of pending observations.
	// If queue is full, new observations are dropped (non-blocking).
	// Default: 1000
	QueueSize int `yaml:"queue_size,omitempty"`
}

// Default configuration values.
const (
	DefaultObservationSampleRate  = 0.1  // 10% of requests get deep parsing
	DefaultObservationWorkerCount = 4    // 4 worker goroutines
	DefaultObservationQueueSize   = 1000 // 1000 pending observations max
)

// HydrateDefaults applies default values to ObservationQueueConfig.
func (c *ObservationQueueConfig) HydrateDefaults() {
	if c.SampleRate == 0 {
		c.SampleRate = DefaultObservationSampleRate
	}
	if c.WorkerCount == 0 {
		c.WorkerCount = DefaultObservationWorkerCount
	}
	if c.QueueSize == 0 {
		c.QueueSize = DefaultObservationQueueSize
	}
}

// ObservationSource indicates where the observation came from.
type ObservationSource string

const (
	// SourceUserRequest indicates the observation is from a sampled user request.
	SourceUserRequest ObservationSource = "user_request"
	// SourceHealthCheck indicates the observation is from a background health check.
	SourceHealthCheck ObservationSource = "health_check"
)

// QueuedObservation represents a sampled request/response to be parsed async.
// Contains EVERYTHING needed for parsing - all the heavy work happens in the worker.
type QueuedObservation struct {
	// === Key Components (for unique identification) ===

	// ServiceID identifies the service (e.g., "eth", "base", "cosmos").
	ServiceID protocol.ServiceID

	// EndpointAddr identifies the endpoint that responded.
	EndpointAddr protocol.EndpointAddr

	// === Source & Timing ===

	// Source indicates where this observation came from.
	Source ObservationSource

	// Timestamp when the response was received.
	Timestamp time.Time

	// Latency of the request-response cycle.
	Latency time.Duration

	// === Request Context (raw - parse in worker) ===

	// RequestPath is the URL path (e.g., "/", "/v1/completions").
	RequestPath string

	// RequestHTTPMethod is the HTTP method (GET, POST, etc.).
	RequestHTTPMethod string

	// RequestHeaders are the request headers (optional, for context).
	RequestHeaders map[string]string

	// RequestBody is the raw request payload (for determining RPC method, etc.).
	RequestBody []byte

	// === Response Data (raw - parse in worker) ===

	// ResponseStatusCode is the HTTP status code from the endpoint.
	ResponseStatusCode int

	// ResponseHeaders are the response headers (optional, for context).
	ResponseHeaders map[string]string

	// ResponseBody is the raw endpoint response to parse.
	ResponseBody []byte
}

// ObservationHandler processes extracted data from observations.
// This is called after the extractor runs to update endpoint state.
type ObservationHandler interface {
	// HandleExtractedData processes the extracted data from an observation.
	// Called by worker pool goroutines, must be thread-safe.
	HandleExtractedData(obs *QueuedObservation, data *qostypes.ExtractedData) error
}

// ObservationQueue handles async, non-blocking observation processing.
// It uses a worker pool to parse sampled responses without blocking the hot path.
//
// Architecture:
//   - Uses ExtractorRegistry to get the right DataExtractor for each service
//   - Runs extraction in worker goroutines (heavy parsing is async)
//   - Calls ObservationHandler with extracted data to update endpoint state
type ObservationQueue struct {
	config   ObservationQueueConfig
	pool     pond.Pool
	registry *qostypes.ExtractorRegistry
	handler  ObservationHandler
	logger   polylog.Logger

	// Per-service sample rate overrides
	perServiceRates   map[protocol.ServiceID]float64
	perServiceRatesMu sync.RWMutex

	// Metrics
	mu             sync.RWMutex
	totalQueued    int64
	totalDropped   int64
	totalProcessed int64
	totalSkipped   int64 // Not sampled
}

// NewObservationQueue creates a new observation queue with the given config.
// The registry and handler must be set before use via SetRegistry and SetHandler.
func NewObservationQueue(config ObservationQueueConfig, logger polylog.Logger) *ObservationQueue {
	config.HydrateDefaults()

	pool := pond.NewPool(config.WorkerCount, pond.WithQueueSize(config.QueueSize))

	return &ObservationQueue{
		config:          config,
		pool:            pool,
		registry:        qostypes.NewExtractorRegistry(), // Default empty registry
		perServiceRates: make(map[protocol.ServiceID]float64),
		logger:          logger.With("component", "observation_queue"),
	}
}

// SetRegistry sets the extractor registry for looking up service-specific extractors.
func (q *ObservationQueue) SetRegistry(registry *qostypes.ExtractorRegistry) {
	q.registry = registry
}

// SetHandler sets the handler for processing extracted data.
func (q *ObservationQueue) SetHandler(handler ObservationHandler) {
	q.handler = handler
}

// SetPerServiceRate sets a sample rate override for a specific service.
// Use this to sample different services at different rates.
// For example, high-traffic services might use a lower rate.
func (q *ObservationQueue) SetPerServiceRate(serviceID protocol.ServiceID, rate float64) {
	q.perServiceRatesMu.Lock()
	defer q.perServiceRatesMu.Unlock()
	q.perServiceRates[serviceID] = rate
}

// getSampleRate returns the sample rate for a service.
// Returns per-service rate if set, otherwise the default rate.
func (q *ObservationQueue) getSampleRate(serviceID protocol.ServiceID) float64 {
	q.perServiceRatesMu.RLock()
	defer q.perServiceRatesMu.RUnlock()

	if rate, exists := q.perServiceRates[serviceID]; exists {
		return rate
	}
	return q.config.SampleRate
}

// TryQueue attempts to queue an observation for async parsing.
// Returns true if queued, false if skipped (not sampled) or dropped (queue full).
// This method is NON-BLOCKING - it never waits.
//
// Sampling logic:
//   - Uses per-service rate if configured, otherwise default rate
//   - If not sampled, observation is skipped (not an error)
//   - If queue is full, observation is dropped (logged as warning)
func (q *ObservationQueue) TryQueue(obs *QueuedObservation) bool {
	if !q.config.Enabled {
		return false
	}

	// Get sample rate for this service (per-service or default)
	sampleRate := q.getSampleRate(obs.ServiceID)

	// Random sampling - only parse a fraction of requests
	if rand.Float64() > sampleRate {
		q.mu.Lock()
		q.totalSkipped++
		q.mu.Unlock()
		return false
	}

	// Try to submit to pool (non-blocking)
	_, submitted := q.pool.TrySubmit(func() {
		q.processObservation(obs)
	})

	q.mu.Lock()
	if submitted {
		q.totalQueued++
	} else {
		q.totalDropped++
		q.logger.Warn().
			Str("service_id", string(obs.ServiceID)).
			Str("endpoint", string(obs.EndpointAddr)).
			Msg("Observation queue full, dropping observation")
	}
	q.mu.Unlock()

	return submitted
}

// Submit queues an observation without sampling (always queues if queue not full).
// Use this for health checks which should always be processed.
// Returns true if queued, false if dropped (queue full).
func (q *ObservationQueue) Submit(obs *QueuedObservation) bool {
	if !q.config.Enabled {
		return false
	}

	// Try to submit to pool (non-blocking)
	_, submitted := q.pool.TrySubmit(func() {
		q.processObservation(obs)
	})

	q.mu.Lock()
	if submitted {
		q.totalQueued++
	} else {
		q.totalDropped++
		q.logger.Warn().
			Str("service_id", string(obs.ServiceID)).
			Str("endpoint", string(obs.EndpointAddr)).
			Str("source", string(obs.Source)).
			Msg("Observation queue full, dropping observation")
	}
	q.mu.Unlock()

	return submitted
}

// processObservation runs in a worker goroutine to parse the response.
// This is where all the heavy parsing work happens - completely async.
func (q *ObservationQueue) processObservation(obs *QueuedObservation) {
	startTime := time.Now()

	// Get extractor for this service (falls back to NoOp if not registered)
	extractor := q.registry.Get(obs.ServiceID)

	// Create extracted data container
	data := qostypes.NewExtractedData(
		obs.EndpointAddr,
		obs.ResponseStatusCode,
		obs.ResponseBody,
		obs.Latency,
	)

	// Run all extractions (this is the heavy parsing work)
	data.ExtractAll(extractor)

	// Call handler if set
	if q.handler != nil {
		if err := q.handler.HandleExtractedData(obs, data); err != nil {
			q.logger.Debug().
				Err(err).
				Str("service_id", string(obs.ServiceID)).
				Str("endpoint", string(obs.EndpointAddr)).
				Dur("parse_duration", time.Since(startTime)).
				Msg("Handler failed to process extracted data")
		}
	}

	q.mu.Lock()
	q.totalProcessed++
	q.mu.Unlock()

	// Log successful parsing at debug level
	q.logger.Debug().
		Str("service_id", string(obs.ServiceID)).
		Str("endpoint", string(obs.EndpointAddr)).
		Str("source", string(obs.Source)).
		Int64("block_height", data.BlockHeight).
		Bool("is_syncing", data.IsSyncing).
		Bool("is_valid", data.IsValidResponse).
		Bool("has_errors", data.HasErrors()).
		Dur("parse_duration", time.Since(startTime)).
		Msg("Observation processed")
}

// Stop gracefully shuts down the observation queue.
// Waits for all pending observations to be processed.
func (q *ObservationQueue) Stop() {
	q.pool.StopAndWait()

	q.mu.RLock()
	defer q.mu.RUnlock()

	q.logger.Info().
		Int64("total_queued", q.totalQueued).
		Int64("total_processed", q.totalProcessed).
		Int64("total_dropped", q.totalDropped).
		Int64("total_skipped", q.totalSkipped).
		Msg("Observation queue stopped")
}

// GetMetrics returns current queue metrics.
func (q *ObservationQueue) GetMetrics() (queued, processed, dropped, skipped int64) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.totalQueued, q.totalProcessed, q.totalDropped, q.totalSkipped
}

// IsEnabled returns true if async observation processing is enabled.
func (q *ObservationQueue) IsEnabled() bool {
	return q.config.Enabled
}

// GetSampleRate returns the current sample rate.
func (q *ObservationQueue) GetSampleRate() float64 {
	return q.config.SampleRate
}
