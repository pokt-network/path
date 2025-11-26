// Package qos handles exporting of all qos-related metrics.
package qos

import (
	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/metrics/qos/cosmos"
	"github.com/pokt-network/path/metrics/qos/evm"
	"github.com/pokt-network/path/metrics/qos/solana"
	"github.com/pokt-network/path/observation/qos"
)

// PublishMetrics builds and exports all qos-related metrics using qos-level observations.
func PublishQoSMetrics(
	logger polylog.Logger,
	qosObservations *qos.Observations,
) {
	hydratedLogger := logger.With("method", "PublishQoSMetrics")

	if qosObservations == nil {
		hydratedLogger.Warn().Msg("received nil set of QoS observations.")
		return
	}

	// Publish EVM metrics.
	if evmObservations := qosObservations.GetEvm(); evmObservations != nil {
		evm.PublishMetrics(hydratedLogger, evmObservations)
		hydratedLogger.Debug().Msg("published EVM metrics.")
		return
	}

	// Publish CometBFT metrics.
	if cosmosObservations := qosObservations.GetCosmos(); cosmosObservations != nil {
		cosmos.PublishMetrics(hydratedLogger, cosmosObservations)
		hydratedLogger.Debug().Msg("published Cosmos SDK metrics.")
		return
	}

	// Publish Solana metrics.
	if solanaObservations := qosObservations.GetSolana(); solanaObservations != nil {
		solana.PublishMetrics(hydratedLogger, solanaObservations)
		hydratedLogger.Debug().Msg("published Solana metrics.")
		return
	}

	// Log warning if no matching observation types were found
	hydratedLogger.Warn().Msgf("SHOULD RARELY HAPPEN: supplied observations do not match any known QoS service: '%+v'", qosObservations)
}
