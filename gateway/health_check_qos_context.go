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

	// Log the validation criteria being applied
	hc.logger.Debug().
		Str("check_name", hc.checkConfig.Name).
		Str("endpoint", string(endpointAddr)).
		Int("expected_status_code", hc.checkConfig.ExpectedStatusCode).
		Str("expected_response_contains", hc.checkConfig.ExpectedResponseContains).
		Int("actual_status_code", httpStatusCode).
		Int("response_size", len(endpointSerializedResponse)).
		Msg("🔍 Health check validating response")

	// Log truncated response body for debugging (max 200 chars)
	responsePreview := string(endpointSerializedResponse)
	if len(responsePreview) > 200 {
		responsePreview = responsePreview[:200] + "..."
	}
	hc.logger.Debug().
		Str("check_name", hc.checkConfig.Name).
		Str("endpoint", string(endpointAddr)).
		Str("response_preview", responsePreview).
		Msg("🔍 Health check response body preview")

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
			Msg("❌ Health check failed: unexpected status code")
		return
	}

	// Check response body contains expected string
	if hc.checkConfig.ExpectedResponseContains != "" {
		containsExpected := strings.Contains(string(endpointSerializedResponse), hc.checkConfig.ExpectedResponseContains)
		hc.logger.Debug().
			Str("check_name", hc.checkConfig.Name).
			Str("endpoint", string(endpointAddr)).
			Str("expected_contains", hc.checkConfig.ExpectedResponseContains).
			Bool("contains_expected", containsExpected).
			Msg("🔍 Health check validating response content")

		if !containsExpected {
			hc.responseSuccess = false
			hc.responseError = "response does not contain expected content"
			hc.logger.Debug().
				Str("expected_contains", hc.checkConfig.ExpectedResponseContains).
				Str("endpoint", string(endpointAddr)).
				Msg("❌ Health check failed: response content mismatch")
			return
		}
	}

	hc.logger.Debug().
		Str("endpoint", string(endpointAddr)).
		Str("check_name", hc.checkConfig.Name).
		Int("status_code", httpStatusCode).
		Msg("✅ Health check validation passed")
}

// GetHTTPResponse returns a minimal HTTP response for health checks.
// This method is required by the RequestQoSContext interface but is not used for health checks.
func (hc *HealthCheckQoSContext) GetHTTPResponse() pathhttp.HTTPResponse {
	// Health checks don't generate user-facing HTTP responses.
	// Return a no-op implementation.
	return nil
}

// GetObservations returns QoS-level observations for the health check.
// This method is required by the RequestQoSContext interface but is not used for health checks.
func (hc *HealthCheckQoSContext) GetObservations() qosobservations.Observations {
	// Health check results are handled separately via the reputation service.
	return qosobservations.Observations{}
}

// GetEndpointSelector returns a no-op selector since health checks use pre-selected endpoints.
// This method is required by the RequestQoSContext interface but is not used for health checks.
func (hc *HealthCheckQoSContext) GetEndpointSelector() protocol.EndpointSelector {
	// Health checks use endpoints specified in YAML config or by the protocol.
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
