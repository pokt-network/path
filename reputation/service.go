package reputation

import (
	"context"
	"sync"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"

	reputationmetrics "github.com/pokt-network/path/metrics/reputation"
	"github.com/pokt-network/path/protocol"
)

// service implements ReputationService with local cache + async backend sync.
// All reads are served from local cache (<1μs), writes update cache immediately
// and queue async writes to backend storage.
type service struct {
	logger            polylog.Logger
	config            Config
	storage           Storage
	defaultKeyBuilder KeyBuilder

	// serviceKeyBuilders caches KeyBuilders for services with overrides
	serviceKeyBuilders map[string]KeyBuilder

	// serviceConfigs stores per-service reputation config overrides
	// These override global InitialScore, MinThreshold for specific services
	serviceConfigs   map[string]ServiceConfig
	serviceConfigsMu sync.RWMutex

	// serviceLatencyProfiles stores per-service latency profile configurations
	// These override global latency thresholds for specific services
	serviceLatencyProfiles   map[string]LatencyConfig
	serviceLatencyProfilesMu sync.RWMutex

	// Local cache for fast reads
	mu    sync.RWMutex
	cache map[string]Score

	// Async write handling
	writeCh   chan writeRequest
	stopCh    chan struct{}
	stoppedCh chan struct{}

	// State tracking
	started bool
}

// writeRequest represents a pending write to backend storage.
type writeRequest struct {
	key   EndpointKey
	score Score
}

// NewService creates a new ReputationService with the given configuration.
// The service must be started with Start() before use.
func NewService(config Config, store Storage) ReputationService {
	config.HydrateDefaults()

	// Build service-specific key builders from overrides
	serviceKeyBuilders := make(map[string]KeyBuilder)
	for serviceID, svcConfig := range config.ServiceOverrides {
		if svcConfig.KeyGranularity != "" {
			serviceKeyBuilders[serviceID] = NewKeyBuilder(svcConfig.KeyGranularity)
		}
	}

	return &service{
		config:                 config,
		storage:                store,
		defaultKeyBuilder:      NewKeyBuilder(config.KeyGranularity),
		serviceKeyBuilders:     serviceKeyBuilders,
		serviceConfigs:         make(map[string]ServiceConfig),
		serviceLatencyProfiles: make(map[string]LatencyConfig),
		cache:                  make(map[string]Score),
		writeCh:                make(chan writeRequest, config.SyncConfig.WriteBufferSize),
		stopCh:                 make(chan struct{}),
		stoppedCh:              make(chan struct{}),
	}
}

// RecordSignal records a signal event for an endpoint.
// Updates local cache immediately and queues async write to backend storage.
func (s *service) RecordSignal(ctx context.Context, key EndpointKey, signal Signal) error {
	if !s.config.Enabled {
		return nil
	}

	// Calculate impact using latency-aware scoring if applicable
	impact := s.calculateImpact(key.ServiceID, signal)

	s.mu.Lock()
	score, exists := s.cache[key.String()]
	if !exists {
		// Use per-service InitialScore if configured, otherwise global default
		initialScore := s.GetInitialScoreForService(key.ServiceID)
		score = Score{
			Value:       initialScore,
			LastUpdated: time.Now(),
		}
	}

	// Apply impact and clamp to valid range
	score.Value = clamp(score.Value+impact, MinScore, MaxScore)
	score.LastUpdated = time.Now()

	if signal.IsPositive() {
		score.SuccessCount++
	} else if signal.IsNegative() {
		score.ErrorCount++
	}

	// Update latency metrics if signal includes latency data
	if signal.Latency > 0 {
		score.LatencyMetrics.UpdateLatency(signal.Latency)
	}

	s.cache[key.String()] = score
	s.mu.Unlock()

	// Record signal metrics for observability
	signalType := "success"
	if signal.IsNegative() {
		signalType = string(signal.Type)
	}
	reputationmetrics.RecordSignal(
		string(key.ServiceID),
		signalType,
		reputationmetrics.EndpointTypeUnknown, // EndpointKey doesn't track type
		string(key.EndpointAddr),              // Use full address as domain identifier
	)

	// Queue async write (non-blocking if buffer is full)
	select {
	case s.writeCh <- writeRequest{key: key, score: score}:
	default:
		// Buffer full, write will be picked up on next flush from cache
	}

	return nil
}

// GetScore retrieves the current reputation score for an endpoint.
// Always reads from local cache for minimal latency.
// If the endpoint's score is below threshold and hasn't received signals
// for RecoveryTimeout, it is automatically reset to InitialScore.
func (s *service) GetScore(ctx context.Context, key EndpointKey) (Score, error) {
	if !s.config.Enabled {
		// Use per-service InitialScore if configured, otherwise global default
		return Score{Value: s.GetInitialScoreForService(key.ServiceID)}, nil
	}

	s.mu.RLock()
	score, exists := s.cache[key.String()]
	s.mu.RUnlock()

	if !exists {
		return Score{}, ErrNotFound
	}

	// Check if score should be recovered (using per-service MinThreshold)
	if s.shouldRecover(score, key.ServiceID) {
		score = s.recoverScore(ctx, key)
	}

	return score, nil
}

// shouldRecover returns true if the score is below threshold and
// hasn't received signals for RecoveryTimeout.
// Uses per-service MinThreshold and RecoveryTimeout if configured, otherwise global defaults.
//
// IMPORTANT: Time-based recovery is only applied when BOTH health checks AND probation
// are disabled for the service. If either is enabled, those systems handle recovery instead.
func (s *service) shouldRecover(score Score, serviceID protocol.ServiceID) bool {
	minThreshold := s.GetMinThresholdForService(serviceID)
	if score.Value >= minThreshold {
		return false // Score is above threshold, no recovery needed
	}

	// Check if health checks or probation are enabled for this service.
	// If either is enabled, they handle recovery - skip time-based recovery.
	s.serviceConfigsMu.RLock()
	svcConfig, exists := s.serviceConfigs[string(serviceID)]
	s.serviceConfigsMu.RUnlock()

	if exists {
		if svcConfig.HealthChecksEnabled || svcConfig.ProbationEnabled {
			return false // Health checks or probation will handle recovery
		}
	}

	recoveryTimeout := s.GetRecoveryTimeoutForService(serviceID)
	return time.Since(score.LastUpdated) >= recoveryTimeout
}

// recoverScore resets an endpoint's score to InitialScore.
// Called when an endpoint is eligible for recovery.
// Uses per-service InitialScore if configured, otherwise global default.
func (s *service) recoverScore(ctx context.Context, key EndpointKey) Score {
	// Use per-service InitialScore if configured
	initialScore := s.GetInitialScoreForService(key.ServiceID)
	score := Score{
		Value:       initialScore,
		LastUpdated: time.Now(),
		// Reset counts to indicate fresh start
		SuccessCount: 0,
		ErrorCount:   0,
	}

	s.mu.Lock()
	s.cache[key.String()] = score
	s.mu.Unlock()

	// Queue async write
	select {
	case s.writeCh <- writeRequest{key: key, score: score}:
	default:
	}

	return score
}

// GetScores retrieves reputation scores for multiple endpoints.
// Always reads from local cache for minimal latency.
// Uses per-service InitialScore if configured, otherwise global default.
func (s *service) GetScores(ctx context.Context, keys []EndpointKey) (map[EndpointKey]Score, error) {
	if !s.config.Enabled {
		result := make(map[EndpointKey]Score, len(keys))
		for _, key := range keys {
			// Use per-service InitialScore if configured
			result[key] = Score{Value: s.GetInitialScoreForService(key.ServiceID)}
		}
		return result, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[EndpointKey]Score, len(keys))
	for _, key := range keys {
		if score, exists := s.cache[key.String()]; exists {
			result[key] = score
		}
	}

	return result, nil
}

// FilterByScore filters endpoints that meet the minimum score threshold.
// Always reads from local cache for minimal latency.
// Endpoints eligible for recovery are automatically reset before filtering.
func (s *service) FilterByScore(ctx context.Context, keys []EndpointKey, minThreshold float64) ([]EndpointKey, error) {
	if !s.config.Enabled {
		// When disabled, all endpoints pass the filter
		return keys, nil
	}

	// First pass: identify scores and keys needing recovery
	s.mu.RLock()
	type keyScore struct {
		key          EndpointKey
		score        Score
		exists       bool
		needRecovery bool
	}
	keyScores := make([]keyScore, len(keys))
	for i, key := range keys {
		score, exists := s.cache[key.String()]
		keyScores[i] = keyScore{
			key:          key,
			score:        score,
			exists:       exists,
			needRecovery: exists && s.shouldRecover(score, key.ServiceID),
		}
	}
	s.mu.RUnlock()

	// Recover any endpoints that need it
	for i, ks := range keyScores {
		if ks.needRecovery {
			keyScores[i].score = s.recoverScore(ctx, ks.key)
		}
	}

	// Filter by score
	var result []EndpointKey
	for _, ks := range keyScores {
		if !ks.exists {
			// Unknown endpoints get the per-service initial score
			if s.GetInitialScoreForService(ks.key.ServiceID) >= minThreshold {
				result = append(result, ks.key)
			}
			continue
		}

		if ks.score.Value >= minThreshold {
			result = append(result, ks.key)
		}
	}

	return result, nil
}

// ResetScore resets an endpoint's score to the initial value.
// Uses per-service InitialScore if configured, otherwise global default.
func (s *service) ResetScore(ctx context.Context, key EndpointKey) error {
	if !s.config.Enabled {
		return nil
	}

	score := Score{
		Value:       s.GetInitialScoreForService(key.ServiceID),
		LastUpdated: time.Now(),
	}

	s.mu.Lock()
	s.cache[key.String()] = score
	s.mu.Unlock()

	// Queue async write
	select {
	case s.writeCh <- writeRequest{key: key, score: score}:
	default:
	}

	return nil
}

// KeyBuilderForService returns the KeyBuilder for the given service.
// Uses service-specific config if available, otherwise falls back to global default.
func (s *service) KeyBuilderForService(serviceID protocol.ServiceID) KeyBuilder {
	// Check for service-specific override
	if builder, ok := s.serviceKeyBuilders[string(serviceID)]; ok {
		return builder
	}
	// Fall back to default
	return s.defaultKeyBuilder
}

// SetServiceConfig sets per-service reputation configuration overrides.
// This allows different services to have different initial scores and min thresholds.
func (s *service) SetServiceConfig(serviceID protocol.ServiceID, config ServiceConfig) {
	s.serviceConfigsMu.Lock()
	defer s.serviceConfigsMu.Unlock()
	s.serviceConfigs[string(serviceID)] = config

	// Also update key builder if granularity is specified
	if config.KeyGranularity != "" {
		s.serviceKeyBuilders[string(serviceID)] = NewKeyBuilder(config.KeyGranularity)
	}
}

// GetInitialScoreForService returns the initial score for a service.
// Uses per-service config if set, otherwise falls back to global default.
func (s *service) GetInitialScoreForService(serviceID protocol.ServiceID) float64 {
	s.serviceConfigsMu.RLock()
	svcConfig, exists := s.serviceConfigs[string(serviceID)]
	s.serviceConfigsMu.RUnlock()

	if exists && svcConfig.InitialScore > 0 {
		return svcConfig.InitialScore
	}
	return s.config.InitialScore
}

// GetMinThresholdForService returns the min threshold for a service.
// Uses per-service config if set, otherwise falls back to global default.
func (s *service) GetMinThresholdForService(serviceID protocol.ServiceID) float64 {
	s.serviceConfigsMu.RLock()
	svcConfig, exists := s.serviceConfigs[string(serviceID)]
	s.serviceConfigsMu.RUnlock()

	if exists && svcConfig.MinThreshold > 0 {
		return svcConfig.MinThreshold
	}
	return s.config.MinThreshold
}

// GetRecoveryTimeoutForService returns the recovery timeout for a service.
// Uses per-service config if set, otherwise falls back to global default.
func (s *service) GetRecoveryTimeoutForService(serviceID protocol.ServiceID) time.Duration {
	s.serviceConfigsMu.RLock()
	svcConfig, exists := s.serviceConfigs[string(serviceID)]
	s.serviceConfigsMu.RUnlock()

	if exists && svcConfig.RecoveryTimeout > 0 {
		return svcConfig.RecoveryTimeout
	}
	return s.config.RecoveryTimeout
}

// SetLatencyProfile sets per-service latency profile configuration.
// This allows different services to have different latency thresholds and bonuses/penalties.
func (s *service) SetLatencyProfile(serviceID protocol.ServiceID, latencyConfig LatencyConfig) {
	s.serviceLatencyProfilesMu.Lock()
	defer s.serviceLatencyProfilesMu.Unlock()
	s.serviceLatencyProfiles[string(serviceID)] = latencyConfig
}

// GetLatencyConfigForService returns the latency config for a service.
// Uses per-service latency profile if set, otherwise falls back to global default.
// This method is exported to satisfy the ReputationService interface.
func (s *service) GetLatencyConfigForService(serviceID protocol.ServiceID) LatencyConfig {
	s.serviceLatencyProfilesMu.RLock()
	latencyConfig, exists := s.serviceLatencyProfiles[string(serviceID)]
	s.serviceLatencyProfilesMu.RUnlock()

	if exists {
		return latencyConfig
	}
	return s.config.Latency
}

// getLatencyConfigForService is a convenience wrapper for GetLatencyConfigForService.
// Kept for backward compatibility with internal callers.
func (s *service) getLatencyConfigForService(serviceID protocol.ServiceID) LatencyConfig {
	return s.GetLatencyConfigForService(serviceID)
}

// calculateImpact calculates the score impact for a signal, applying latency-aware
// adjustments if the signal has latency data and latency scoring is enabled.
// This respects per-service latency configuration.
func (s *service) calculateImpact(serviceID protocol.ServiceID, signal Signal) float64 {
	// Get latency config for the service (handles per-service overrides)
	latencyConfig := s.getLatencyConfigForService(serviceID)

	var impact float64
	// If latency is disabled or signal has no latency, use base impact
	if !latencyConfig.Enabled || signal.Latency == 0 {
		impact = signal.GetDefaultImpact()
	} else {
		// Apply latency-aware impact calculation for success signals
		result := signal.CalculateLatencyAwareImpactWithDetails(latencyConfig)
		impact = result.FinalImpact

		// Log latency scoring details
		if s.logger != nil && result.LatencyCategory != "skipped" {
			s.logger.Debug().
				Str("service_id", string(serviceID)).
				Str("signal_type", string(signal.Type)).
				Str("latency_category", result.LatencyCategory).
				Dur("latency", result.Latency).
				Float64("base_impact", result.BaseImpact).
				Float64("modifier", result.Modifier).
				Float64("final_impact", result.FinalImpact).
				Dur("fast_threshold", result.Config.FastThreshold).
				Dur("normal_threshold", result.Config.NormalThreshold).
				Dur("slow_threshold", result.Config.SlowThreshold).
				Float64("fast_bonus", result.Config.FastBonus).
				Float64("slow_penalty", result.Config.SlowPenalty).
				Msg("[LATENCY_SCORE] Applied latency scoring")
		}
	}

	// Apply recovery multiplier if set (used by probation system to boost recovery)
	// Only applies to positive signals (success, recovery_success)
	if impact > 0 {
		multiplier := signal.GetRecoveryMultiplier()
		if multiplier != 1.0 && s.logger != nil {
			s.logger.Debug().
				Str("service_id", string(serviceID)).
				Float64("original_impact", impact).
				Float64("recovery_multiplier", multiplier).
				Float64("boosted_impact", impact*multiplier).
				Msg("[RECOVERY_MULTIPLIER] Applied recovery multiplier")
		}
		impact *= multiplier
	}

	return impact
}

// SetLogger sets the logger for the reputation service.
func (s *service) SetLogger(logger polylog.Logger) {
	s.logger = logger.With("component", "reputation_service")
}

// Start begins background sync processes.
func (s *service) Start(ctx context.Context) error {
	if !s.config.Enabled {
		return nil
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	s.started = true
	s.mu.Unlock()

	// Load initial data from storage
	if err := s.refreshFromStorage(ctx); err != nil {
		// Log but don't fail - cache will build up from signals
		_ = err
	}

	// Start background goroutines
	go s.writeLoop()
	go s.refreshLoop()
	go s.cleanupLoop()

	return nil
}

// Stop gracefully shuts down background processes and flushes pending writes.
func (s *service) Stop() error {
	if !s.config.Enabled {
		return nil
	}

	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	close(s.stopCh)
	<-s.stoppedCh

	return nil
}

// writeLoop processes pending writes to backend storage.
func (s *service) writeLoop() {
	ticker := time.NewTicker(s.config.SyncConfig.FlushInterval)
	defer ticker.Stop()

	pending := make(map[string]writeRequest)

	flush := func() {
		if len(pending) == 0 {
			return
		}

		scores := make(map[EndpointKey]Score, len(pending))
		for _, req := range pending {
			scores[req.key] = req.score
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.storage.SetMultiple(ctx, scores)
		cancel()

		// Clear pending after flush
		pending = make(map[string]writeRequest)
	}

	for {
		select {
		case req := <-s.writeCh:
			// Coalesce writes - keep latest score for each key
			pending[req.key.String()] = req

		case <-ticker.C:
			flush()

		case <-s.stopCh:
			// Drain remaining writes
			for {
				select {
				case req := <-s.writeCh:
					pending[req.key.String()] = req
				default:
					flush()
					close(s.stoppedCh)
					return
				}
			}
		}
	}
}

// refreshLoop periodically refreshes cache from backend storage.
func (s *service) refreshLoop() {
	ticker := time.NewTicker(s.config.SyncConfig.RefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = s.refreshFromStorage(ctx)
			cancel()

		case <-s.stopCh:
			return
		}
	}
}

// cleanupLoop periodically removes expired entries from storage.
// Only runs if the storage backend implements the Cleaner interface.
func (s *service) cleanupLoop() {
	cleaner, ok := s.storage.(Cleaner)
	if !ok {
		// Storage doesn't support cleanup, nothing to do
		return
	}

	// Use RecoveryTimeout as cleanup interval (reasonable default)
	ticker := time.NewTicker(s.config.RecoveryTimeout)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cleaner.Cleanup()

		case <-s.stopCh:
			return
		}
	}
}

// refreshFromStorage loads all scores from backend storage into cache.
func (s *service) refreshFromStorage(ctx context.Context) error {
	keys, err := s.storage.List(ctx, "")
	if err != nil {
		return err
	}

	if len(keys) == 0 {
		return nil
	}

	scores, err := s.storage.GetMultiple(ctx, keys)
	if err != nil {
		return err
	}

	s.mu.Lock()
	for key, score := range scores {
		s.cache[key.String()] = score
	}
	s.mu.Unlock()

	return nil
}

// clamp constrains a value between min and max.
func clamp(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
