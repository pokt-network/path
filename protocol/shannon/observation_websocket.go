package shannon

import (
	"fmt"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"google.golang.org/protobuf/types/known/timestamppb"

	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
)

// ---------- Message-Level Observations ----------

// getWebsocketMessageSuccessObservation updates the observations for the current message
// if the message handler does not return an error.
func getWebsocketMessageSuccessObservation(
	logger polylog.Logger,
	serviceID protocol.ServiceID,
	selectedEndpoint endpoint,
	msgData []byte,
) protocolobservations.Observations {
	logger.With("method", "getWebsocketMessageSuccessObservation")

	// Create a new Websocket message observation for success
	wsMessageObs := buildWebsocketMessageSuccessObservation(
		logger,
		selectedEndpoint,
		int64(len(msgData)),
	)

	// Update the observations to use the Websocket message observation
	return protocolobservations.Observations{
		Shannon: &protocolobservations.ShannonObservationsList{
			Observations: []*protocolobservations.ShannonRequestObservations{
				{
					ServiceId:    string(serviceID),
					RequestError: nil, // WS messages do not have request errors
					ObservationData: &protocolobservations.ShannonRequestObservations_WebsocketMessageObservation{
						WebsocketMessageObservation: wsMessageObs,
					},
				},
			},
		},
	}
}

// getWebsocketMessageErrorObservation updates the observations for the current message
// if the message handler returns an error.
func getWebsocketMessageErrorObservation(
	logger polylog.Logger,
	serviceID protocol.ServiceID,
	selectedEndpoint endpoint,
	msgData []byte,
	messageError error,
) protocolobservations.Observations {
	// Classify error to get error type
	// Reputation signals are handled separately in websocket_context.go
	endpointErrorType, _ := classifyErrorAsSignal(logger, messageError, 0)

	// Create a new Websocket message observation for error
	wsMessageObs := buildWebsocketMessageErrorObservation(
		selectedEndpoint,
		int64(len(msgData)),
		endpointErrorType,
		fmt.Sprintf("websocket message error: %v", messageError),
	)

	return protocolobservations.Observations{
		Shannon: &protocolobservations.ShannonObservationsList{
			Observations: []*protocolobservations.ShannonRequestObservations{
				{
					ServiceId:    string(serviceID),
					RequestError: nil, // WS messages do not have request errors
					ObservationData: &protocolobservations.ShannonRequestObservations_WebsocketMessageObservation{
						WebsocketMessageObservation: wsMessageObs,
					},
				},
			},
		},
	}
}

// ---------- Connection-Level Observations ----------

// getWebsocketConnectionSuccessObservation builds observations for successful Websocket connection establishment.
func getWebsocketConnectionEstablishedObservation(
	logger polylog.Logger,
	serviceID protocol.ServiceID,
	selectedEndpoint endpoint,
) *protocolobservations.Observations {
	return &protocolobservations.Observations{
		Shannon: &protocolobservations.ShannonObservationsList{
			Observations: []*protocolobservations.ShannonRequestObservations{
				{
					ServiceId: string(serviceID),
					ObservationData: &protocolobservations.ShannonRequestObservations_WebsocketConnectionObservation{
						WebsocketConnectionObservation: buildWebsocketConnectionObservation(
							logger,
							selectedEndpoint,
							protocolobservations.ShannonWebsocketConnectionObservation_CONNECTION_ESTABLISHED,
						),
					},
				},
			},
		},
	}
}

func getWebsocketConnectionClosedObservation(
	logger polylog.Logger,
	serviceID protocol.ServiceID,
	selectedEndpoint endpoint,
) *protocolobservations.Observations {
	return &protocolobservations.Observations{
		Shannon: &protocolobservations.ShannonObservationsList{
			Observations: []*protocolobservations.ShannonRequestObservations{
				{
					ServiceId: string(serviceID),
					ObservationData: &protocolobservations.ShannonRequestObservations_WebsocketConnectionObservation{
						WebsocketConnectionObservation: buildWebsocketConnectionObservation(
							logger,
							selectedEndpoint,
							protocolobservations.ShannonWebsocketConnectionObservation_CONNECTION_CLOSED,
						),
					},
				},
			},
		},
	}
}

// getWebsocketConnectionErrorObservation builds observations for failed Websocket connection establishment.
func getWebsocketConnectionErrorObservation(
	logger polylog.Logger,
	serviceID protocol.ServiceID,
	selectedEndpoint endpoint,
	err error,
) *protocolobservations.Observations {
	// Classify error to get error type
	// Reputation signals are handled separately in websocket_context.go
	endpointErrorType, _ := classifyErrorAsSignal(logger, err, 0)

	return &protocolobservations.Observations{
		Shannon: &protocolobservations.ShannonObservationsList{
			Observations: []*protocolobservations.ShannonRequestObservations{
				{
					ServiceId: string(serviceID),
					RequestError: &protocolobservations.ShannonRequestError{
						ErrorType:    protocolobservations.ShannonRequestErrorType_SHANNON_REQUEST_ERROR_INTERNAL,
						ErrorDetails: err.Error(),
					},
					ObservationData: &protocolobservations.ShannonRequestObservations_WebsocketConnectionObservation{
						WebsocketConnectionObservation: buildWebsocketConnectionErrorObservation(
							logger,
							selectedEndpoint,
							endpointErrorType,
							err.Error(),
							protocolobservations.ShannonWebsocketConnectionObservation_CONNECTION_ESTABLISHMENT_FAILED,
						),
					},
				},
			},
		},
	}
}

// buildWebsocketMessageSuccessObservation creates a Shannon websocket message observation for successful message processing.
// It includes endpoint details, session information, and message-specific data.
// Used when websocket message handling succeeds.
func buildWebsocketMessageSuccessObservation(
	_ polylog.Logger,
	endpoint endpoint,
	msgSize int64,
) *protocolobservations.ShannonWebsocketMessageObservation {
	session := *endpoint.Session()
	sessionHeader := session.GetHeader()

	return &protocolobservations.ShannonWebsocketMessageObservation{
		// Endpoint information
		Supplier:           endpoint.Supplier(),
		EndpointUrl:        endpoint.PublicURL(),
		EndpointAppAddress: sessionHeader.ApplicationAddress,
		IsFallbackEndpoint: endpoint.IsFallback(),

		// Session information
		SessionServiceId:   sessionHeader.ServiceId,
		SessionId:          sessionHeader.SessionId,
		SessionStartHeight: sessionHeader.SessionStartBlockHeight,
		SessionEndHeight:   sessionHeader.SessionEndBlockHeight,

		// Message information
		MessageTimestamp:   timestamppb.New(time.Now()),
		MessagePayloadSize: msgSize,
	}
}

// buildWebsocketMessageErrorObservation creates a Shannon websocket message observation for failed message processing.
// It includes endpoint details, session information, message data, and error details.
// Used when websocket message handling fails.
func buildWebsocketMessageErrorObservation(
	endpoint endpoint,
	msgSize int64,
	errorType protocolobservations.ShannonEndpointErrorType,
	errorDetails string,
) *protocolobservations.ShannonWebsocketMessageObservation {
	session := *endpoint.Session()
	sessionHeader := session.GetHeader()

	return &protocolobservations.ShannonWebsocketMessageObservation{
		// Endpoint information
		Supplier:           endpoint.Supplier(),
		EndpointUrl:        endpoint.PublicURL(),
		EndpointAppAddress: sessionHeader.ApplicationAddress,
		IsFallbackEndpoint: endpoint.IsFallback(),

		// Session information
		SessionServiceId:   sessionHeader.ServiceId,
		SessionId:          sessionHeader.SessionId,
		SessionStartHeight: sessionHeader.SessionStartBlockHeight,
		SessionEndHeight:   sessionHeader.SessionEndBlockHeight,

		// Message information
		MessageTimestamp:   timestamppb.New(time.Now()),
		MessagePayloadSize: msgSize,

		// Error information
		ErrorType:    &errorType,
		ErrorDetails: &errorDetails,
	}
}

// buildWebsocketConnectionObservation creates a Shannon websocket connection observation for connection lifecycle events.
// It includes endpoint details and session information for connection-level tracking.
// Used when websocket connection setup succeeds or when connection closes.
func buildWebsocketConnectionObservation(
	_ polylog.Logger,
	endpoint endpoint,
	eventType protocolobservations.ShannonWebsocketConnectionObservation_ConnectionEventType,
) *protocolobservations.ShannonWebsocketConnectionObservation {
	session := *endpoint.Session()
	sessionHeader := session.GetHeader()

	return &protocolobservations.ShannonWebsocketConnectionObservation{
		// Endpoint information
		Supplier:           endpoint.Supplier(),
		EndpointUrl:        endpoint.PublicURL(),
		EndpointAppAddress: sessionHeader.ApplicationAddress,
		IsFallbackEndpoint: endpoint.IsFallback(),

		// Session information
		SessionServiceId:   sessionHeader.ServiceId,
		SessionId:          sessionHeader.SessionId,
		SessionStartHeight: sessionHeader.SessionStartBlockHeight,
		SessionEndHeight:   sessionHeader.SessionEndBlockHeight,

		// Connection lifecycle
		ConnectionEstablishedTimestamp: timestamppb.New(time.Now()),
		EventType:                      eventType,
	}
}

// buildWebsocketConnectionErrorObservation creates a Shannon websocket connection observation for failed connection events.
// It includes endpoint details, session information, and error details.
// Used when websocket connection setup fails or when connection closes with an error.
func buildWebsocketConnectionErrorObservation(
	_ polylog.Logger,
	endpoint endpoint,
	errorType protocolobservations.ShannonEndpointErrorType,
	errorDetails string,
	eventType protocolobservations.ShannonWebsocketConnectionObservation_ConnectionEventType,
) *protocolobservations.ShannonWebsocketConnectionObservation {
	return &protocolobservations.ShannonWebsocketConnectionObservation{
		// Endpoint information
		Supplier:           endpoint.Supplier(),
		EndpointUrl:        endpoint.PublicURL(),
		EndpointAppAddress: endpoint.Session().GetHeader().ApplicationAddress,
		IsFallbackEndpoint: endpoint.IsFallback(),

		// Session information
		SessionServiceId:   endpoint.Session().GetHeader().ServiceId,
		SessionId:          endpoint.Session().GetHeader().SessionId,
		SessionStartHeight: endpoint.Session().GetHeader().SessionStartBlockHeight,
		SessionEndHeight:   endpoint.Session().GetHeader().SessionEndBlockHeight,

		// Error information
		ErrorType:    &errorType,
		ErrorDetails: &errorDetails,

		// Connection lifecycle
		ConnectionEstablishedTimestamp: timestamppb.New(time.Now()),
		EventType:                      eventType,
	}
}
