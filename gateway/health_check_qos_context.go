// Package gateway provides the health check QoS context for pluggable health checks.
//
// HealthCheckQoSContext implements RequestQoSContext for YAML-configurable health checks.
// It allows sending configured payloads through the protocol layer and validating responses.
package gateway

import (
	"strings"
	"sync"

	"github.com/pokt-network/poktroll/pkg/polylog"

	pathhttp "github.com/pokt-network/path/network/http"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
)

// HealthCheckQoSContext implements RequestQoSContext for health check synthetic requests.
// It wraps a configured health check payload and validates the response.
var _ RequestQoSContext = &HealthCheckQoSContext{}

// HealthCheckQoSContext represents a health check request context.
// It is created from YAML configuration and used to send synthetic requests through the protocol.
type HealthCheckQoSContext struct {
	logger polylog.Logger

	// serviceID is the service this health check is for.
	serviceID protocol.ServiceID

	// checkConfig is the health check configuration.
	checkConfig HealthCheckConfig

	// servicePayload is the payload to send to the endpoint.
	servicePayload protocol.Payload

	// Response tracking
	responseMu       sync.Mutex
	responseReceived bool
	responseSuccess  bool
	responseError    string
	responseBody     []byte
	httpStatusCode   int
	endpointAddr     protocol.EndpointAddr
}

// HealthCheckQoSContextConfig contains configuration for creating a HealthCheckQoSContext.
type HealthCheckQoSContextConfig struct {
	Logger         polylog.Logger
	ServiceID      protocol.ServiceID
	CheckConfig    HealthCheckConfig
	ServicePayload protocol.Payload
}

// NewHealthCheckQoSContext creates a new HealthCheckQoSContext from configuration.
func NewHealthCheckQoSContext(cfg HealthCheckQoSContextConfig) *HealthCheckQoSContext {
	return &HealthCheckQoSContext{
		logger:         cfg.Logger,
		serviceID:      cfg.ServiceID,
		checkConfig:    cfg.CheckConfig,
		servicePayload: cfg.ServicePayload,
	}
}

// GetServicePayloads returns the configured payload for the health check.
// Implements RequestQoSContext interface.
func (hc *HealthCheckQoSContext) GetServicePayloads() []protocol.Payload {
	return []protocol.Payload{hc.servicePayload}
}

// UpdateWithResponse processes the endpoint response for the health check.
// It validates the response against expected criteria (status code, body content).
// Implements RequestQoSContext interface.
func (hc *HealthCheckQoSContext) UpdateWithResponse(
	endpointAddr protocol.EndpointAddr,
	endpointSerializedResponse []byte,
	httpStatusCode int,
) {
	hc.responseMu.Lock()
	defer hc.responseMu.Unlock()

	hc.responseReceived = true
	hc.endpointAddr = endpointAddr
	hc.responseBody = endpointSerializedResponse
	hc.httpStatusCode = httpStatusCode

	// Validate response
	hc.responseSuccess = true
	hc.responseError = ""

	// Check HTTP status code
	if hc.checkConfig.ExpectedStatusCode != 0 && httpStatusCode != hc.checkConfig.ExpectedStatusCode {
		hc.responseSuccess = false
		hc.responseError = "unexpected status code"
		hc.logger.Debug().
			Int("expected", hc.checkConfig.ExpectedStatusCode).
			Int("actual", httpStatusCode).
			Str("endpoint", string(endpointAddr)).
			Msg("Health check failed: unexpected status code")
		return
	}

	// Check response body contains expected string
	if hc.checkConfig.ExpectedResponseContains != "" {
		if !strings.Contains(string(endpointSerializedResponse), hc.checkConfig.ExpectedResponseContains) {
			hc.responseSuccess = false
			hc.responseError = "response does not contain expected content"
			hc.logger.Debug().
				Str("expected_contains", hc.checkConfig.ExpectedResponseContains).
				Str("endpoint", string(endpointAddr)).
				Msg("Health check failed: response content mismatch")
			return
		}
	}

	hc.logger.Debug().
		Str("endpoint", string(endpointAddr)).
		Str("check_name", hc.checkConfig.Name).
		Int("status_code", httpStatusCode).
		Msg("Health check passed")
}

// GetHTTPResponse returns a minimal HTTP response for health checks.
// For synthetic requests, this is not typically used but required by the interface.
// Implements RequestQoSContext interface.
func (hc *HealthCheckQoSContext) GetHTTPResponse() pathhttp.HTTPResponse {
	hc.responseMu.Lock()
	defer hc.responseMu.Unlock()

	if !hc.responseReceived {
		return &simpleHTTPResponse{
			statusCode: 503,
			body:       []byte(`{"error": "no response received"}`),
		}
	}

	return &simpleHTTPResponse{
		statusCode: hc.httpStatusCode,
		body:       hc.responseBody,
	}
}

// GetObservations returns QoS-level observations for the health check.
// Implements RequestQoSContext interface.
func (hc *HealthCheckQoSContext) GetObservations() qosobservations.Observations {
	// Return empty observations - health check results are handled separately
	// via the reputation service integration in the executor.
	return qosobservations.Observations{}
}

// GetEndpointSelector returns a no-op selector since health checks
// use pre-selected endpoints (specified in the YAML config or by the protocol).
// Implements RequestQoSContext interface.
func (hc *HealthCheckQoSContext) GetEndpointSelector() protocol.EndpointSelector {
	// Return nil to use default selection - the hydrator flow pre-selects endpoints anyway
	return nil
}

// IsSuccess returns true if the health check passed all validation criteria.
func (hc *HealthCheckQoSContext) IsSuccess() bool {
	hc.responseMu.Lock()
	defer hc.responseMu.Unlock()
	return hc.responseReceived && hc.responseSuccess
}

// GetError returns the error message if the health check failed.
func (hc *HealthCheckQoSContext) GetError() string {
	hc.responseMu.Lock()
	defer hc.responseMu.Unlock()
	return hc.responseError
}

// GetEndpointAddr returns the endpoint address that was checked.
func (hc *HealthCheckQoSContext) GetEndpointAddr() protocol.EndpointAddr {
	hc.responseMu.Lock()
	defer hc.responseMu.Unlock()
	return hc.endpointAddr
}

// GetCheckConfig returns the health check configuration.
func (hc *HealthCheckQoSContext) GetCheckConfig() HealthCheckConfig {
	return hc.checkConfig
}

// simpleHTTPResponse is a minimal implementation of pathhttp.HTTPResponse.
type simpleHTTPResponse struct {
	statusCode int
	body       []byte
}

func (r *simpleHTTPResponse) GetPayload() []byte {
	return r.body
}

func (r *simpleHTTPResponse) GetHTTPStatusCode() int {
	return r.statusCode
}

func (r *simpleHTTPResponse) GetHTTPHeaders() map[string]string {
	return map[string]string{"Content-Type": "application/json"}
}
