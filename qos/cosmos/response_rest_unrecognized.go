package cosmos

import (
	"github.com/pokt-network/poktroll/pkg/polylog"

	pathhttp "github.com/pokt-network/path/network/http"
	qosobservations "github.com/pokt-network/path/observation/qos"
)

// responseRESTUnrecognized handles unrecognized REST endpoint responses
// Implements the response interface for REST endpoints that don't have specific validators
// Returns the endpoint payload as-is without any processing
type responseRESTUnrecognized struct {
	logger             polylog.Logger
	endpointResponseBz []byte
}

// GetHTTPResponse builds the HTTP response to return to the client
// Returns the endpoint response payload as-is with HTTP 200 status
func (r responseRESTUnrecognized) GetHTTPResponse() pathhttp.HTTPResponse {
	// TODO_UPNEXT(@adshmh): Propagate endpoint HTTP response code
	return httpResponse{
		responsePayload: r.endpointResponseBz,
		httpStatusCode:  200,
	}
}

// GetObservation returns the QoS observation for this unrecognized REST response
func (r responseRESTUnrecognized) GetObservation() qosobservations.CosmosEndpointObservation {
	return qosobservations.CosmosEndpointObservation{
		EndpointResponseValidationResult: &qosobservations.CosmosEndpointResponseValidationResult{
			ResponseValidationType: qosobservations.CosmosResponseValidationType_COSMOS_RESPONSE_VALIDATION_TYPE_UNSTRUCTURED,
			HttpStatusCode:         200, // TODO_UPNEXT(@adshmh): Propagate endpoint HTTP response code
			ValidationError:        nil, // No validation performed for unrecognized responses
			ParsedResponse: &qosobservations.CosmosEndpointResponseValidationResult_ResponseUnrecognized{
				ResponseUnrecognized: &qosobservations.UnrecognizedResponse{
					EndpointPayloadLength: uint32(len(r.endpointResponseBz)),
				},
			},
		},
	}
}
