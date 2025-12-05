// package noop implements a noop QoS module, enabling a gateway operator to support services
// which do not yet have a QoS implementation.
package noop

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/metrics/devtools"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	qostypes "github.com/pokt-network/path/qos/types"
)

// maxRequestBodySize is the maximum allowed size for HTTP request bodies (100MB).
// This prevents OOM attacks from unbounded io.ReadAll calls.
const maxRequestBodySize = 100 * 1024 * 1024

var _ gateway.QoSService = NoOpQoS{}

type NoOpQoS struct{}

// NewNoOpQoSService creates a new NoOp QoS service instance.
// The logger and serviceID parameters are accepted for interface consistency
// but are not used by NoOp QoS.
func NewNoOpQoSService(_ polylog.Logger, _ protocol.ServiceID) *NoOpQoS {
	return &NoOpQoS{}
}

// ParseHTTPRequest reads the supplied HTTP request's body and passes it on to a new requestContext instance.
// It intentionally avoids performing any validation on the request, as is the designed behavior of the noop QoS.
// Implements the gateway.QoSService interface.
func (NoOpQoS) ParseHTTPRequest(_ context.Context, httpRequest *http.Request) (gateway.RequestQoSContext, bool) {
	// Apply size limit to prevent OOM attacks from unbounded io.ReadAll calls
	limitedBody := http.MaxBytesReader(nil, httpRequest.Body, maxRequestBodySize)
	bz, err := io.ReadAll(limitedBody)
	if err != nil {
		// Check if the error is due to body size limit exceeded
		if err.Error() == "http: request body too large" {
			err = fmt.Errorf("request body exceeds %d bytes limit", maxRequestBodySize)
		}
		return requestContextFromError(fmt.Errorf("error reading the HTTP request body: %w", err)), false
	}

	return &requestContext{
		httpRequestBody:   bz,
		httpRequestMethod: httpRequest.Method,
		httpRequestPath:   httpRequest.URL.Path,
	}, true
}

// ParseWebsocketRequest builds a request context from the provided Websocket request.
// This method implements the gateway.QoSService interface.
func (q NoOpQoS) ParseWebsocketRequest(_ context.Context) (gateway.RequestQoSContext, bool) {
	return &requestContext{}, true
}

// ApplyObservations on noop QoS only fulfills the interface requirements and does not perform any actions.
// Implements the gateway.QoSService interface.
func (NoOpQoS) ApplyObservations(_ *qosobservations.Observations) error {
	return nil
}

// CheckWebsocketConnection returns true if the endpoint supports Websocket connections.
// NoOp QoS does not support Websocket connections.
func (NoOpQoS) CheckWebsocketConnection() bool {
	return false
}

// GetRequiredQualityChecks on noop QoS only fulfills the interface requirements and does not perform any actions.
// Implements the gateway.QoSService interface.
func (NoOpQoS) GetRequiredQualityChecks(_ protocol.EndpointAddr) []gateway.RequestQoSContext {
	return nil
}

// requestContextFromError constructs and returns a requestContext instance using the supplied error.
// The returned requestContext will returns a user-facing HTTP request with the supplied error when it GetHTTPResponse method is called.
func requestContextFromError(err error) *requestContext {
	return &requestContext{
		presetFailureResponse: getRequestProcessingError(err),
	}
}

// HydrateDisqualifiedEndpointsResponse is a no-op for the noop QoS.
func (NoOpQoS) HydrateDisqualifiedEndpointsResponse(_ protocol.ServiceID, _ *devtools.DisqualifiedEndpointResponse) {
}

// UpdateFromExtractedData is a no-op for the noop QoS.
// Implements gateway.QoSService interface.
func (NoOpQoS) UpdateFromExtractedData(_ protocol.EndpointAddr, _ *qostypes.ExtractedData) error {
	return nil
}
