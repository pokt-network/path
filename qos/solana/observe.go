// solana package provides the support required for interacting
// with the Solana blockchain through the gateway.
package solana

import (
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
)

// UpdateEndpointsFromObservations CRUDs endpoint entries in the store based on the supplied observations.
// It returns the set of created/updated endpoints.
func (es *EndpointStore) UpdateEndpointsFromObservations(
	solanaObservations *qosobservations.SolanaRequestObservations,
) map[protocol.EndpointAddr]endpoint {
	es.endpointsMu.Lock()
	defer es.endpointsMu.Unlock()

	if es.endpoints == nil {
		es.endpoints = make(map[protocol.EndpointAddr]endpoint)
	}

	endpointObservations := solanaObservations.GetEndpointObservations()

	logger := es.logger.With(
		"qos_instance", "solana",
		"method", "UpdateEndpointsFromObservations",
	)
	logger.Info().Msgf("About to update endpoints from %d observations.", len(endpointObservations))

	updatedEndpoints := make(map[protocol.EndpointAddr]endpoint)
	for _, observation := range endpointObservations {
		if observation == nil {
			logger.Info().Msg("💡 Solana EndpointStore received a nil observation. SKIPPING...")
			continue
		}

		endpointAddr := protocol.EndpointAddr(observation.EndpointAddr)

		logger := logger.With("endpoint_addr", endpointAddr)
		logger.Info().Msg("processing observation for endpoint.")

		// It is a valid scenario for an endpoint to not be present in the store.
		// E.g. when the first observation(s) are received for an endpoint.
		endpoint := es.endpoints[endpointAddr]

		// Apply the observation to the endpoint.
		isEndpointMutatedByObservation := endpoint.applyObservation(observation)
		// If the observation did not mutate the endpoint, there is no need to update the stored endpoint entry.
		if !isEndpointMutatedByObservation {
			logger.Info().Msg("💡 Endpoint was not mutated by observations. SKIPPING update of internal endpoint store.")
			continue
		}

		es.endpoints[endpointAddr] = endpoint
		updatedEndpoints[endpointAddr] = endpoint
	}

	return updatedEndpoints
}
