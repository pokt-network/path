package gateway

import (
	"fmt"

	"github.com/pokt-network/poktroll/pkg/polylog"

	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
)

// buildConnectionEstablishmentFailureObservation creates a connection establishment failure observation
// when the protocol context build and bridge start fails.
func buildConnectionEstablishmentFailureObservation(
	logger polylog.Logger,
	serviceID protocol.ServiceID,
	selectedEndpoint protocol.EndpointAddr,
	_ error,
) *protocolobservations.Observations {
	logger = logger.With("method", "buildConnectionEstablishmentFailureObservation")

	endpointURL, err := selectedEndpoint.GetURL()
	if err != nil {
		logger.Error().Err(err).Msg("⁉️ SHOULD NEVER HAPPEN: Failed to get URL for selected endpoint")
	}

	// Create a failure observation similar to what the protocol layer would create
	return &protocolobservations.Observations{
		Shannon: &protocolobservations.ShannonObservationsList{
			Observations: []*protocolobservations.ShannonRequestObservations{
				{
					ServiceId: string(serviceID),
					RequestError: &protocolobservations.ShannonRequestError{
						ErrorType:    protocolobservations.ShannonRequestErrorType_SHANNON_REQUEST_ERROR_INTERNAL,
						ErrorDetails: fmt.Sprintf("failed to build protocol context and start bridge: %v", err),
					},
					ObservationData: &protocolobservations.ShannonRequestObservations_WebsocketConnectionObservation{
						WebsocketConnectionObservation: &protocolobservations.ShannonWebsocketConnectionObservation{
							Supplier:     "", // Unknown at this point
							EndpointUrl:  endpointURL,
							EventType:    protocolobservations.ShannonWebsocketConnectionObservation_CONNECTION_ESTABLISHMENT_FAILED,
							ErrorType:    &[]protocolobservations.ShannonEndpointErrorType{protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNKNOWN}[0],
							ErrorDetails: &[]string{fmt.Sprintf("failed to build protocol context and start bridge: %v", err)}[0],
						},
					},
				},
			},
		},
	}
}
