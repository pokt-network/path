package solana

import (
    "encoding/json"
    "fmt"
    "io"
    "net/http"

    "github.com/pokt-network/poktroll/pkg/polylog"

    "github.com/pokt-network/path/gateway"
    qosobservations "github.com/pokt-network/path/observation/qos"
    "github.com/pokt-network/path/protocol"
    "github.com/pokt-network/path/qos"
    "github.com/pokt-network/path/qos/jsonrpc"
)

// maxRequestBodySize is the maximum allowed size for HTTP request bodies (100MB).
// This prevents OOM attacks from unbounded io.ReadAll calls.
const maxRequestBodySize = 100 * 1024 * 1024

// Maximum length for error messages stored in validation failure logs/observations.
// - Prevents overly verbose error messages in logs/metrics
// - Reduces memory usage and cost
const maxErrMessageLen = 1000

// TODO_MVP(@adshmh): Drop this struct once JUDGE framework is merged.
//
// requestValidator:
// - Handles request validation for Solana JSONRPC requests
// - Generates error contexts if validation fails (e.g. error parsing JSONRPC request)
// - Generates request context if validation succeeds
// - Used as the entry point for HTTP request validation
type requestValidator struct {
    logger        polylog.Logger
    chainID       string
    serviceID     protocol.ServiceID
    endpointStore *EndpointStore
}

// TODO_TECHDEBT(@adshmh): Add a JSON-RPC request validator to reject invalid/unsupported method calls early.
//
// validateHTTPRequest:
// - Validates an HTTP request for a Solana JSONRPC payload
// - Extracts and validates the JSONRPC request from the HTTP body
// - Returns (errorContext, false) if validation fails
// - Returns (requestContext, true) if validation succeeds
func (rv *requestValidator) validateHTTPRequest(req *http.Request) (gateway.RequestQoSContext, bool) {
    logger := rv.logger.With(
        "qos", "Solana",
        "method", "validateHTTPRequest",
    )

    // Read the HTTP request body with a size limit to prevent OOM attacks
    limitedBody := http.MaxBytesReader(nil, req.Body, maxRequestBodySize)
    body, err := io.ReadAll(limitedBody)
    if err != nil {
        // Check if the error is due to body size limit exceeded
        if err.Error() == "http: request body too large" {
            err = fmt.Errorf("request body exceeds %d bytes limit", maxRequestBodySize)
        }
        logger.Warn().Err(err).Msg("HTTP request body read failed - returning generic error response")
        return rv.createHTTPBodyReadFailureContext(err), false
    }

    // TODO_TECHDEBT(@adshmh): Distinguish malformed single and batch requests.
    // This is needed to provide a JSONRPC-compliant error response to user if e.g. a batch request is malformed.
    //
    // Parse and validate the JSONRPC request
    // 1. Attempt to parse as a batch of requests
    // Ref: https://www.jsonrpc.org/specification#batch
    //
    var jsonrpcBatchRequest jsonrpc.BatchRequest
    if err = json.Unmarshal(body, &jsonrpcBatchRequest); err == nil {
        return &batchJSONRPCRequestContext{
            logger:               rv.logger,
            chainID:              rv.chainID,
            serviceID:            rv.serviceID,
            requestPayloadLength: uint(len(body)),
            JSONRPCBatchRequest:  jsonrpcBatchRequest,
            // Set the origin of the request as USER (i.e. organic relay)
            // The request is from a user.
            requestOrigin: qosobservations.RequestOrigin_REQUEST_ORIGIN_ORGANIC,
            endpointStore: rv.endpointStore,
        }, true
    }

    // 2. Attempt to parse as a single JSONRPC request
    var jsonrpcRequest jsonrpc.Request
    if err = json.Unmarshal(body, &jsonrpcRequest); err == nil {
        // single JSONRPC request is valid, return a fully initialized requestContext
        return &requestContext{
            logger:               rv.logger,
            chainID:              rv.chainID,
            serviceID:            rv.serviceID,
            requestPayloadLength: uint(len(body)),
            JSONRPCReq:           jsonrpcRequest,
            // Set the origin of the request as USER (i.e. organic relay)
            // The request is from a user.
            requestOrigin: qosobservations.RequestOrigin_REQUEST_ORIGIN_ORGANIC,
            endpointStore: rv.endpointStore,
        }, true
    }

    // TODO_UPNEXT(@adshmh): Adjust the error response based on request type: single JSONRPC vs. batch JSONRPC.
    // Only log a preview of the request body (first 1000 bytes or less) to avoid excessive logging
    requestPreview := string(body[:min(maxErrMessageLen, len(body))])
    logger.Error().Err(err).Msgf("❌ Solana endpoint will fail QoS check because JSONRPC request could not be parsed. Request preview: %s", requestPreview)
    return rv.createRequestUnmarshalingFailureContext(jsonrpc.ID{}, err), false
}

// createHTTPBodyReadFailureContext:
// - Creates an error context for HTTP body read failures
// - Used when the HTTP request body cannot be read
func (rv *requestValidator) createHTTPBodyReadFailureContext(err error) gateway.RequestQoSContext {
    // TODO_IMPROVE(@adshmh): Propagate a request ID parameter on internal errors
    // that occur after successful request parsing.
    // There are no such cases as of PR #186.
    //
    // Create the JSON-RPC error response
    response := jsonrpc.NewErrResponseInternalErr(jsonrpc.ID{}, err)

    // Create the observations object with the HTTP body read failure observation
    observations := rv.createHTTPBodyReadFailureObservation(err, response)

    // Build and return the error context
    return &qos.RequestErrorContext{
        Logger:   rv.logger,
        Response: response,
        Observations: &qosobservations.Observations{
            ServiceObservations: observations,
        },
    }
}

// createRequestUnmarshalingFailureContext:
// - Creates an error context for JSONRPC request unmarshaling failures
// - Used when the request body cannot be parsed into a valid JSONRPC request
func (rv *requestValidator) createRequestUnmarshalingFailureContext(id jsonrpc.ID, err error) gateway.RequestQoSContext {
    // Create the JSON-RPC error response
    response := jsonrpc.NewErrResponseInvalidRequest(id, err)

    // Create the observations object with the request unmarshaling failure observation
    observations := createRequestUnmarshalingFailureObservation(id, rv.serviceID, rv.chainID, err, response)

    // Build and return the error context
    return &qos.RequestErrorContext{
        Logger:   rv.logger,
        Response: response,
        Observations: &qosobservations.Observations{
            ServiceObservations: observations,
        },
    }
}

// createRequestUnmarshalingFailureObservation:
// - Creates an observation for a Solana request that failed to unmarshal from JSON
// - Captures details for metrics and debugging:
//   - Request ID
//   - Error message
//   - Chain ID
//   - HTTP status code
//
// Parameters:
//   - id: JSON-RPC request ID
//   - serviceID: Service identifier
//   - chainID: Solana chain identifier
//   - err: Error from unmarshaling
//   - jsonrpcResponse: Associated JSONRPC error response
//
// Returns:
//   - Structured observation with failure details
func createRequestUnmarshalingFailureObservation(
        _ jsonrpc.ID,
        serviceID protocol.ServiceID,
        chainID string,
        err error,
        jsonrpcResponse jsonrpc.Response,
) *qosobservations.Observations_Solana {
    return &qosobservations.Observations_Solana{
        Solana: &qosobservations.SolanaRequestObservations{
            ServiceId: string(serviceID),
            ChainId:   chainID,
            RequestError: &qosobservations.RequestError{
                ErrorKind:      qosobservations.RequestErrorKind_REQUEST_ERROR_USER_ERROR_JSONRPC_PARSE_ERROR,
                ErrorDetails:   err.Error(),
                HttpStatusCode: int32(jsonrpcResponse.GetRecommendedHTTPStatusCode()),
            },
        },
    }
}

// createHTTPBodyReadFailureObservation creates an observation for Solana HTTP request body read failures.
func (rv *requestValidator) createHTTPBodyReadFailureObservation(
        err error,
        jsonrpcResponse jsonrpc.Response,
) *qosobservations.Observations_Solana {
    return &qosobservations.Observations_Solana{
        Solana: &qosobservations.SolanaRequestObservations{
            ChainId:   rv.chainID,
            ServiceId: string(rv.serviceID),
            RequestError: &qosobservations.RequestError{
                ErrorKind:      qosobservations.RequestErrorKind_REQUEST_ERROR_USER_ERROR_JSONRPC_PARSE_ERROR,
                ErrorDetails:   err.Error(),
                HttpStatusCode: int32(jsonrpcResponse.GetRecommendedHTTPStatusCode()),
            },
        },
    }
}
