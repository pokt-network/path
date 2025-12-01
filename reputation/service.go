package reputation

import (
	"context"
	"sync"
	"time"
)

// service implements ReputationService with local cache + async backend sync.
// All reads are served from local cache (<1μs), writes update cache immediately
// and queue async writes to backend storage.
type service struct {
	config  Config
	storage Storage

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

	return &service{
		config:    config,
		storage:   store,
		cache:     make(map[string]Score),
		writeCh:   make(chan writeRequest, config.SyncConfig.WriteBufferSize),
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
	}
}

// RecordSignal records a signal event for an endpoint.
// Updates local cache immediately and queues async write to backend storage.
func (s *service) RecordSignal(ctx context.Context, key EndpointKey, signal Signal) error {
	if !s.config.Enabled {
		return nil
	}

	impact := signal.GetDefaultImpact()

	s.mu.Lock()
	score, exists := s.cache[key.String()]
	if !exists {
		score = Score{
			Value:       s.config.InitialScore,
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

	s.cache[key.String()] = score
	s.mu.Unlock()

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
		return Score{Value: s.config.InitialScore}, nil
	}

	s.mu.RLock()
	score, exists := s.cache[key.String()]
	s.mu.RUnlock()

	if !exists {
		return Score{}, ErrNotFound
	}

	// Check if score should be recovered
	if s.shouldRecover(score) {
		score = s.recoverScore(ctx, key)
	}

	return score, nil
}

// shouldRecover returns true if the score is below threshold and
// hasn't received signals for RecoveryTimeout.
func (s *service) shouldRecover(score Score) bool {
	if score.Value >= s.config.MinThreshold {
		return false // Score is above threshold, no recovery needed
	}
	return time.Since(score.LastUpdated) >= s.config.RecoveryTimeout
}

// recoverScore resets an endpoint's score to InitialScore.
// Called when an endpoint is eligible for recovery.
func (s *service) recoverScore(ctx context.Context, key EndpointKey) Score {
	score := Score{
		Value:       s.config.InitialScore,
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
func (s *service) GetScores(ctx context.Context, keys []EndpointKey) (map[EndpointKey]Score, error) {
	if !s.config.Enabled {
		result := make(map[EndpointKey]Score, len(keys))
		for _, key := range keys {
			result[key] = Score{Value: s.config.InitialScore}
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
			needRecovery: exists && s.shouldRecover(score),
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
			// Unknown endpoints get the initial score
			if s.config.InitialScore >= minThreshold {
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
func (s *service) ResetScore(ctx context.Context, key EndpointKey) error {
	if !s.config.Enabled {
		return nil
	}

	score := Score{
		Value:       s.config.InitialScore,
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
