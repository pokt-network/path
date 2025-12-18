package main

import (
	"context"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/metrics"
)

// setupLeaderboardPublisher creates and starts the leaderboard publisher.
// The publisher periodically collects endpoint distribution data from the protocol
// and publishes it to the Prometheus metrics endpoint.
//
// Returns nil if the protocol does not implement LeaderboardDataProvider.
func setupLeaderboardPublisher(
	ctx context.Context,
	logger polylog.Logger,
	protocol gateway.Protocol,
) *metrics.LeaderboardPublisher {
	// Cast protocol to LeaderboardDataProvider
	provider, ok := protocol.(metrics.LeaderboardDataProvider)
	if !ok {
		logger.Warn().Msg("Protocol does not implement LeaderboardDataProvider, leaderboard metrics disabled")
		return nil
	}

	publisher := metrics.NewLeaderboardPublisher(logger, provider)
	if err := publisher.Start(ctx); err != nil {
		logger.Error().Err(err).Msg("Failed to start leaderboard publisher")
		return nil
	}

	logger.Info().Msg("Leaderboard publisher started")
	return publisher
}
