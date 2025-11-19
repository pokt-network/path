package shannon

import (
	"errors"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"google.golang.org/protobuf/types/known/timestamppb"

	protocolobservations "github.com/buildwithgrove/path/observation/protocol"
	"github.com/buildwithgrove/path/protocol"
)

// buildSuccessfulEndpointLookupObservation builds a minimum observation to indicate the endpoint lookup was successful.
// Used when endpoint lookup succeeds but endpoint selection fails.
func buildSuccessfulEndpointLookupObservation(
	serviceID protocol.ServiceID,
) protocolobservations.Observations {
	return protocolobservations.Observations{
		Shannon: &protocolobservations.ShannonObservationsList{
			Observations: []*protocolobservations.ShannonRequestObservations{
				{
					ServiceId: string(serviceID),
				},
			},
		},
	}
}

// buildProtocolContextSetupErrorObservation builds a protocol observation from the supplied error.
// Used if any steps of building a protocol context fails:
// - Getting available endpoints.
// - Setting up the request context for a specific endpoint.
func buildProtocolContextSetupErrorObservation(
	serviceID protocol.ServiceID,
	err error,
) protocolobservations.Observations {
	return protocolobservations.Observations{
		Shannon: &protocolobservations.ShannonObservationsList{
			Observations: []*protocolobservations.ShannonRequestObservations{
				{
					ServiceId: string(serviceID),
					RequestError: &protocolobservations.ShannonRequestError{
						ErrorType:    translateContextSetupErrorToRequestErrorType(err),
						ErrorDetails: err.Error(),
					},
				},
			},
		},
	}
}

// translateContextSetupErrorToRequestErrorType maps the supplied error to a request error type.
// Used to generate the request error field of the observation.
func translateContextSetupErrorToRequestErrorType(err error) protocolobservations.ShannonRequestErrorType {
	switch {
	// Centralized gateway mode: error fetching app
	case errors.Is(err, errProtocolContextSetupCentralizedAppFetchErr):
		return protocolobservations.ShannonRequestErrorType_SHANNON_REQUEST_ERROR_INTERNAL_CENTRALIZED_MODE_APP_FETCH_ERR

	// Centralized gateway mode: app does not delegate to the gateway
	case errors.Is(err, errProtocolContextSetupCentralizedAppDelegation):
		return protocolobservations.ShannonRequestErrorType_SHANNON_REQUEST_ERROR_INTERNAL_CENTRALIZED_MODE_APP_DELEGATION

	// Centralized gateway mode: no sessions found for service
	case errors.Is(err, errProtocolContextSetupCentralizedNoSessions):
		return protocolobservations.ShannonRequestErrorType_SHANNON_REQUEST_ERROR_INTERNAL_CENTRALIZED_MODE_NO_SESSIONS

	// Centralized gateway mode: no apps found for service
	case errors.Is(err, errProtocolContextSetupCentralizedNoAppsForService):
		return protocolobservations.ShannonRequestErrorType_SHANNON_REQUEST_ERROR_INTERNAL_CENTRALIZED_MODE_NO_APPS_FOR_SERVICE

	// Delegated gateway mode: could not extract app from HTTP request.
	case errors.Is(err, errProtocolContextSetupGetAppFromHTTPReq):
		return protocolobservations.ShannonRequestErrorType_SHANNON_REQUEST_ERROR_INTERNAL_DELEGATED_GET_APP_HTTP

	// Delegated gateway mode: error fetching onchain app data
	case errors.Is(err, errProtocolContextSetupFetchSession):
		return protocolobservations.ShannonRequestErrorType_SHANNON_REQUEST_ERROR_INTERNAL_DELEGATED_FETCH_APP

	// Delegated gateway mode: pp does not delegate to the gateway
	case errors.Is(err, errProtocolContextSetupAppDoesNotDelegate):
		return protocolobservations.ShannonRequestErrorType_SHANNON_REQUEST_ERROR_INTERNAL_DELEGATED_APP_DOES_NOT_DELEGATE

	// No endpoints available for the service
	// Due to one or more of the following:
	// - Any of the gateway mode errors above
	// - Error fetching a session for one or more apps.
	// - One or more available endpoints are sanctioned.
	case errors.Is(err, errProtocolContextSetupNoEndpoints):
		return protocolobservations.ShannonRequestErrorType_SHANNON_REQUEST_ERROR_INTERNAL_NO_ENDPOINTS_AVAILABLE

	case errors.Is(err, errRequestContextSetupErrSignerSetup):
		return protocolobservations.ShannonRequestErrorType_SHANNON_REQUEST_ERROR_INTERNAL_SIGNER_SETUP_ERROR

	// Should NOT happen: use the INTERNAL type to track and resolve via metrics.
	default:
		return protocolobservations.ShannonRequestErrorType_SHANNON_REQUEST_ERROR_INTERNAL
	}
}

// builds a Shannon endpoint success observation to include:
// - endpoint details: address, url, app
// - endpoint query and response timestamps.
// - relay miner error if present: for tracking/cross referencing against endpoint errors.
func buildEndpointSuccessObservation(
	logger polylog.Logger,
	endpoint endpoint,
	endpointQueryTimestamp time.Time,
	endpointResponseTimestamp time.Time,
	endpointResponse *protocol.Response,
	relayMinerError *protocolobservations.ShannonRelayMinerError,
	rpcType sharedtypes.RPCType,
) *protocolobservations.ShannonEndpointObservation {
	// initialize an observation with endpoint details: URL, app, etc.
	endpointObs := buildEndpointObservation(logger, endpoint, endpointResponse, rpcType)

	// Update the observation with endpoint query and response timestamps.
	endpointObs.EndpointQueryTimestamp = timestamppb.New(endpointQueryTimestamp)
	endpointObs.EndpointResponseTimestamp = timestamppb.New(endpointResponseTimestamp)
	// Track RelayMiner error.
	endpointObs.RelayMinerError = relayMinerError

	return endpointObs
}

// builds a Shannon endpoint error observation to include:
// - endpoint details
// - the encountered error
// - any sanctions resulting from the error.
// - relay miner error if present: for tracking/cross referencing against endpoint errors.
func buildEndpointErrorObservation(
	logger polylog.Logger,
	endpoint endpoint,
	endpointQueryTimestamp time.Time,
	endpointResponseTimestamp time.Time,
	errorType protocolobservations.ShannonEndpointErrorType,
	errorDetails string,
	sanctionType protocolobservations.ShannonSanctionType,
	relayMinerError *protocolobservations.ShannonRelayMinerError,
	rpcType sharedtypes.RPCType,
) *protocolobservations.ShannonEndpointObservation {
	// initialize an observation with endpoint details: URL, app, etc.
	endpointObs := buildEndpointObservation(logger, endpoint, nil, rpcType)

	// Update the observation with endpoint query/response timestamps.
	endpointObs.EndpointQueryTimestamp = timestamppb.New(endpointQueryTimestamp)
	endpointObs.EndpointResponseTimestamp = timestamppb.New(endpointResponseTimestamp)

	// Update the observation with error details and any resulting sanctions
	endpointObs.ErrorType = &errorType
	endpointObs.ErrorDetails = &errorDetails
	endpointObs.RecommendedSanction = &sanctionType
	// Track RelayMiner error
	endpointObs.RelayMinerError = relayMinerError

	return endpointObs
}

// BuildEndpointObservation builds a Shannon endpoint observation to include:
// endpoint: supplier, URL
// session: app, service ID, session ID, session start and end heights (using `buildEndpointObservationFromSession`).
func buildEndpointObservation(
	logger polylog.Logger,
	endpoint endpoint,
	endpointResponse *protocol.Response,
	rpcType sharedtypes.RPCType,
) *protocolobservations.ShannonEndpointObservation {
	// Add session fields to the observation:
	// app, serviceID, session ID, session start and end heights
	observation := buildEndpointObservationFromSession(logger, *endpoint.Session())

	// Add endpoint-level details: supplier, URL, isFallback.
	observation.Supplier = endpoint.Supplier()
	observation.EndpointUrl = endpoint.GetURL(rpcType)
	observation.IsFallbackEndpoint = endpoint.IsFallback()

	// Add endpoint response details if not nil (i.e. success)
	if endpointResponse != nil {
		statusCode := int32(endpointResponse.HTTPStatusCode)
		payloadSize := int64(len(endpointResponse.Bytes))
		observation.EndpointBackendServiceHttpResponseStatusCode = &statusCode
		observation.EndpointBackendServiceHttpResponsePayloadSize = &payloadSize
	}

	return observation
}

// builds an endpoint observation using session's fields, to include:
// session: app, service ID, session ID, session start/end height.
func buildEndpointObservationFromSession(
	logger polylog.Logger,
	session sessiontypes.Session,
) *protocolobservations.ShannonEndpointObservation {
	defaultStatusCode := int32(0)
	defaultPayloadSize := int64(0)

	header := session.Header
	// Nil session: skip.
	if header == nil {
		logger.With("method", "buildEndpointObservationFromSession").ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD NEVER HAPPEN: received nil session header. Skip session fields.")
		// Initialize with empty values and nil pointers properly initialized
		return &protocolobservations.ShannonEndpointObservation{
			EndpointBackendServiceHttpResponseStatusCode:  &defaultStatusCode,
			EndpointBackendServiceHttpResponsePayloadSize: &defaultPayloadSize,
		}
	}

	// Build an endpoint observation using session fields.
	return &protocolobservations.ShannonEndpointObservation{
		EndpointAppAddress: header.ApplicationAddress,
		SessionServiceId:   header.ServiceId,
		SessionId:          header.SessionId,
		SessionStartHeight: header.SessionStartBlockHeight,
		SessionEndHeight:   header.SessionEndBlockHeight,
		EndpointBackendServiceHttpResponseStatusCode:  &defaultStatusCode,
		EndpointBackendServiceHttpResponsePayloadSize: &defaultPayloadSize,
	}
}

// builds a Shannon endpoint from an endpoint observation.
// Used to identify an endpoint for applying sanctions.
func buildEndpointFromObservation(
	observation *protocolobservations.ShannonEndpointObservation,
) endpoint {
	session := buildSessionFromObservation(observation)
	return &protocolEndpoint{
		session:  session,
		supplier: observation.GetSupplier(),
		url:      observation.GetEndpointUrl(),
	}
}

// builds the details of a session from an endpoint observation.
// Used to identify an endpoint for applying sanctions.
func buildSessionFromObservation(
	observation *protocolobservations.ShannonEndpointObservation,
) *sessiontypes.Session {
	return &sessiontypes.Session{
		// Only Session Header is required for processing observations.
		Header: &sessiontypes.SessionHeader{
			ApplicationAddress:      observation.GetEndpointAppAddress(),
			ServiceId:               observation.GetSessionServiceId(),
			SessionId:               observation.GetSessionId(),
			SessionStartBlockHeight: observation.GetSessionStartHeight(),
			SessionEndBlockHeight:   observation.GetSessionEndHeight(),
		},
	}
}

// builds and returns a request error observation for the supplied internal error.
func buildInternalRequestProcessingErrorObservation(internalErr error) *protocolobservations.ShannonRequestError {
	return &protocolobservations.ShannonRequestError{
		ErrorType: protocolobservations.ShannonRequestErrorType_SHANNON_REQUEST_ERROR_INTERNAL,
		// Use the error message as the request error details.
		ErrorDetails: internalErr.Error(),
	}
}
