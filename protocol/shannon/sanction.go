package shannon

import (
	"time"

	"github.com/pokt-network/path/metrics/devtools"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
)

// TODO_FUTURE:
// - Consider expanding sanctions to apply across PATH instances and persist across gateway restarts.
// - The prior version of the Gateway worked this way, requiring a shared pubsub queue or database.
// - Only design and implement this after all basic quality-of-service checks are done.
//
// sanction represents a penalty applied to an endpoint based on observed behavior.
// Sanctions can be temporary (session-based) or permanent (gateway restart),
// depending on the severity of the observed issue.
type sanction struct {
	// reason: human-readable explanation for this sanction.
	reason string

	// errorType: the ErrorType that triggered the sanction.
	errorType protocolobservations.ShannonEndpointErrorType

	// createdAt: timestamp when the sanction was created.
	createdAt time.Time

	// sessionServiceID, sessionStartHeight: onchain session info at sanction creation (if available).
	sessionServiceID   string
	sessionStartHeight int64
}

// buildSanctionFromObservation creates a sanction struct from an endpoint observation.
func buildSanctionFromObservation(observation *protocolobservations.ShannonEndpointObservation) sanction {
	return sanction{
		reason:             observation.GetErrorDetails(),
		errorType:          observation.GetErrorType(),
		createdAt:          time.Now(),
		sessionServiceID:   observation.GetSessionServiceId(),
		sessionStartHeight: observation.GetSessionStartHeight(),
	}
}

// buildSanctionFromWebSocketConnectionObservation creates a sanction struct from a websocket connection observation.
func buildSanctionFromWebSocketConnectionObservation(observation *protocolobservations.ShannonWebsocketConnectionObservation) sanction {
	return sanction{
		reason:             observation.GetErrorDetails(),
		errorType:          observation.GetErrorType(),
		createdAt:          time.Now(),
		sessionServiceID:   observation.GetSessionServiceId(),
		sessionStartHeight: observation.GetSessionStartHeight(),
	}
}

// permanentSanctionToDetails converts a permanent sanction to a devtools.SanctionedEndpoint struct.
// It does not include the session ID as permanent sanction is not associated with a specific session.
func (s sanction) permanentSanctionToDetails(
	endpointAddr protocol.EndpointAddr,
	sanctionType protocolobservations.ShannonSanctionType,
) devtools.SanctionedEndpoint {
	return devtools.SanctionedEndpoint{
		EndpointAddr:  endpointAddr,
		ServiceID:     protocol.ServiceID(s.sessionServiceID),
		Reason:        s.reason,
		SanctionType:  protocolobservations.ShannonSanctionType_name[int32(sanctionType)],
		ErrorType:     protocolobservations.ShannonEndpointErrorType_name[int32(s.errorType)],
		SessionHeight: s.sessionStartHeight,
		CreatedAt:     s.createdAt,
	}
}

// sessionSanctionToDetails converts a session sanction to a devtools.SanctionedEndpoint struct.
// It includes the session ID as session sanction is associated with a specific session.
func (s sanction) sessionSanctionToDetails(
	endpointAddr protocol.EndpointAddr,
	sessionID string,
	sanctionType protocolobservations.ShannonSanctionType,
) devtools.SanctionedEndpoint {
	return devtools.SanctionedEndpoint{
		EndpointAddr:  endpointAddr,
		SessionID:     sessionID,
		ServiceID:     protocol.ServiceID(s.sessionServiceID),
		Reason:        s.reason,
		SanctionType:  protocolobservations.ShannonSanctionType_name[int32(sanctionType)],
		ErrorType:     protocolobservations.ShannonEndpointErrorType_name[int32(s.errorType)],
		SessionHeight: s.sessionStartHeight,
		CreatedAt:     s.createdAt,
	}
}
