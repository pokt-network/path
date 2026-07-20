package metrics

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
)

const (
	// LeaderboardPublishInterval is how often the endpoint leaderboard is published
	LeaderboardPublishInterval = 10 * time.Second
)

// EndpointLeaderboardEntry represents a single entry in the leaderboard snapshot
type EndpointLeaderboardEntry struct {
	Domain             string
	RPCType            string
	ServiceID          string
	TierThreshold      int   // The tier threshold (e.g., 70, 50, 30)
	SessionStartHeight int64 // The session start height
	EndpointCount      int   // Number of endpoints in this group
}

// MeanScoreEntry represents mean score for a domain/service/rpc_type combination
type MeanScoreEntry struct {
	Domain    string
	ServiceID string
	RPCType   string
	MeanScore float64 // Average score across all endpoints for this combination
}

// SupplierScoreEntry represents the per-(supplier, service_id, rpc_type) reputation score.
// One value per triple — a supplier serving multiple RPC types (e.g. json_rpc + websocket)
// gets one entry per type, since reputation is tracked and acted on per rpc_type.
type SupplierScoreEntry struct {
	Supplier  string
	ServiceID string
	RPCType   string
	Score     float64
}

// CooldownCountEntry represents per-domain count of endpoints currently in strike
// cooldown for a given service / rpc_type.
type CooldownCountEntry struct {
	Domain    string
	RPCType   string
	ServiceID string
	Count     int
}

// LeaderboardDataProvider is an interface for getting endpoint distribution data
type LeaderboardDataProvider interface {
	// GetEndpointLeaderboardData returns all endpoint entries grouped by the required dimensions
	GetEndpointLeaderboardData(ctx context.Context) ([]EndpointLeaderboardEntry, error)
	// GetMeanScoreData returns mean reputation scores per domain/service/rpc_type
	GetMeanScoreData(ctx context.Context) ([]MeanScoreEntry, error)
	// GetSupplierScoreData returns per-(supplier, service_id) reputation scores.
	// Optional: implementations may return nil if per-supplier scoring is not supported.
	GetSupplierScoreData(ctx context.Context) ([]SupplierScoreEntry, error)
	// GetCooldownCountData returns per-(domain, service_id, rpc_type) counts of
	// endpoints currently in strike cooldown. Optional: implementations may return
	// nil if cooldown tracking is not supported.
	GetCooldownCountData(ctx context.Context) ([]CooldownCountEntry, error)
}

// LeaderboardPublisher publishes endpoint leaderboard metrics every 10 seconds
type LeaderboardPublisher struct {
	logger    polylog.Logger
	provider  LeaderboardDataProvider
	stopCh    chan struct{}
	stoppedCh chan struct{}
	mu        sync.Mutex
	running   bool
}

// NewLeaderboardPublisher creates a new leaderboard publisher
func NewLeaderboardPublisher(logger polylog.Logger, provider LeaderboardDataProvider) *LeaderboardPublisher {
	return &LeaderboardPublisher{
		logger:    logger.With("component", "leaderboard_publisher"),
		provider:  provider,
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
	}
}

// Start begins the periodic leaderboard publishing
func (lp *LeaderboardPublisher) Start(ctx context.Context) error {
	lp.mu.Lock()
	if lp.running {
		lp.mu.Unlock()
		return fmt.Errorf("leaderboard publisher already running")
	}
	lp.running = true
	lp.mu.Unlock()

	go lp.run(ctx)
	lp.logger.Info().Msg("Leaderboard publisher started")
	return nil
}

// Stop stops the leaderboard publisher
func (lp *LeaderboardPublisher) Stop() {
	lp.mu.Lock()
	if !lp.running {
		lp.mu.Unlock()
		return
	}
	lp.mu.Unlock()

	close(lp.stopCh)
	<-lp.stoppedCh
	lp.logger.Info().Msg("Leaderboard publisher stopped")
}

func (lp *LeaderboardPublisher) run(ctx context.Context) {
	defer close(lp.stoppedCh)

	ticker := time.NewTicker(LeaderboardPublishInterval)
	defer ticker.Stop()

	// Publish immediately on the start
	lp.publishLeaderboard(ctx)

	for {
		select {
		case <-ticker.C:
			lp.publishLeaderboard(ctx)
		case <-lp.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (lp *LeaderboardPublisher) publishLeaderboard(ctx context.Context) {
	if lp.provider == nil {
		lp.logger.Debug().Msg("No leaderboard data provider configured, skipping publish")
		return
	}

	// Publish endpoint tier distribution
	entries, err := lp.provider.GetEndpointLeaderboardData(ctx)
	if err != nil {
		lp.logger.Warn().Err(err).Msg("Failed to get endpoint leaderboard data")
	} else {
		// Reset all previous values to avoid stale data
		ReputationEndpointLeaderboard.Reset()

		if len(entries) > 0 {
			// Publish each entry
			for _, entry := range entries {
				ReputationEndpointLeaderboard.WithLabelValues(
					entry.Domain,
					entry.RPCType,
					entry.ServiceID,
					fmt.Sprintf("%d", entry.TierThreshold),
					fmt.Sprintf("%d", entry.SessionStartHeight),
				).Set(float64(entry.EndpointCount))
			}
			lp.logger.Debug().Int("entries", len(entries)).Msg("Published endpoint leaderboard")
		}
	}

	// Publish mean scores per domain/service/rpc_type
	meanScores, err := lp.provider.GetMeanScoreData(ctx)
	if err != nil {
		lp.logger.Warn().Err(err).Msg("Failed to get mean score data")
	} else {
		// Reset mean score metric to avoid stale data
		ReputationMeanScore.Reset()

		if len(meanScores) > 0 {
			for _, entry := range meanScores {
				SetMeanScore(entry.Domain, entry.ServiceID, entry.RPCType, entry.MeanScore)
			}
			lp.logger.Debug().Int("entries", len(meanScores)).Msg("Published mean scores")
		}
	}

	// Publish per-(supplier, service_id) reputation scores. Reset between
	// snapshots — suppliers may rotate out of sessions and stale series
	// would persist forever otherwise.
	supplierScores, err := lp.provider.GetSupplierScoreData(ctx)
	if err != nil {
		lp.logger.Warn().Err(err).Msg("Failed to get supplier score data")
		return
	}

	SupplierReputationScore.Reset()

	if len(supplierScores) > 0 {
		for _, entry := range supplierScores {
			SetSupplierReputationScore(entry.Supplier, entry.ServiceID, entry.RPCType, entry.Score)
		}
		lp.logger.Debug().Int("entries", len(supplierScores)).Msg("Published supplier scores")
	}

	// Publish per-domain endpoint cooldown counts. Reset between snapshots so a
	// domain that drops to zero cooldown'd endpoints actually shows zero (instead
	// of sticking at its last value via Prometheus' 5-min staleness window).
	cooldownCounts, err := lp.provider.GetCooldownCountData(ctx)
	if err != nil {
		lp.logger.Warn().Err(err).Msg("Failed to get cooldown count data")
		return
	}

	EndpointsInCooldown.Reset()

	if len(cooldownCounts) > 0 {
		for _, entry := range cooldownCounts {
			EndpointsInCooldown.WithLabelValues(entry.Domain, entry.RPCType, entry.ServiceID).Set(float64(entry.Count))
		}
		lp.logger.Debug().Int("entries", len(cooldownCounts)).Msg("Published cooldown counts")
	}
}

// PublishOnce can be called to manually trigger a leaderboard publish (for testing)
func (lp *LeaderboardPublisher) PublishOnce(ctx context.Context) {
	lp.publishLeaderboard(ctx)
}
