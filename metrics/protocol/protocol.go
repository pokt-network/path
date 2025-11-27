// Package protocol handles exporting of all protocol-related observation based metrics.
package protocol

import (
	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/metrics/protocol/shannon"
	"github.com/pokt-network/path/observation/protocol"
)

// PublishMetrics builds and exports all protocol-related metrics using protocol-level observations.
func PublishMetrics(
	logger polylog.Logger,
	protocolObservations *protocol.Observations,
) {
	hydratedLogger := logger.With("method", "PublishMetrics")
	if protocolObservations == nil {
		hydratedLogger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: received nil set of Protocol observations.")
		return
	}

	// Publish Shannon metrics.
	if shannonObservations := protocolObservations.GetShannon(); shannonObservations != nil {
		shannon.PublishMetrics(logger, shannonObservations)
		return
	}

	// Log warning if no matching observation types were found
	hydratedLogger.Warn().Msgf("SHOULD NEVER HAPPEN: supplied observations do not match any known Protocol: %+v", protocolObservations)
}
