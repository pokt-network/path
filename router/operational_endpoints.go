package router

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pokt-network/path/protocol"
)

// ServiceReadinessReporter provides readiness information for services.
// Implemented by the protocol to report session and endpoint availability.
type ServiceReadinessReporter interface {
	// GetServiceReadiness returns readiness info for a specific service.
	// Returns endpoint count, whether sessions are available, and any error.
	GetServiceReadiness(serviceID protocol.ServiceID) (endpointCount int, hasSession bool, err error)

	// ConfiguredServiceIDs returns all configured service IDs.
	ConfiguredServiceIDs() map[protocol.ServiceID]struct{}
}

// ConfigReporter provides sanitized configuration information.
// Implemented by components that can report their active configuration.
type ConfigReporter interface {
	// GetSanitizedConfig returns a sanitized view of the active configuration.
	// All sensitive information (private keys, passwords) MUST be redacted.
	GetSanitizedConfig() map[string]interface{}
}

// ServiceReadinessResponse is the JSON response for /ready endpoints.
type ServiceReadinessResponse struct {
	Ready    bool                        `json:"ready"`
	Services map[string]ServiceReadyInfo `json:"services,omitempty"`
	Message  string                      `json:"message,omitempty"`
}

// ServiceReadyInfo contains readiness info for a single service.
type ServiceReadyInfo struct {
	Ready         bool   `json:"ready"`
	EndpointCount int    `json:"endpoint_count"`
	HasSession    bool   `json:"has_session"`
	Error         string `json:"error,omitempty"`
}

// handleHealth is a minimal liveness probe endpoint.
// Returns 200 OK with no body for Kubernetes liveness probes.
// For detailed health info, use /healthz instead.
func (r *router) handleHealth(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// handleReady handles both /ready and /ready/{serviceId} endpoints.
// Returns 200 if ready, 503 if not ready.
func (r *router) handleReady(w http.ResponseWriter, req *http.Request) {
	// Extract service ID from path if present: /ready/{serviceId}
	path := strings.TrimPrefix(req.URL.Path, "/ready")
	path = strings.TrimPrefix(path, "/")
	serviceID := protocol.ServiceID(path)

	// Check if we have a readiness reporter
	reporter, ok := r.readinessReporter()
	if !ok {
		response := ServiceReadinessResponse{
			Ready:   false,
			Message: "readiness reporting not available",
		}
		r.writeReadinessResponse(w, response, http.StatusServiceUnavailable)
		return
	}

	if serviceID != "" {
		// Single service readiness check
		r.handleServiceReadiness(w, reporter, serviceID)
	} else {
		// All services readiness check
		r.handleAllServicesReadiness(w, reporter)
	}
}

// handleServiceReadiness checks readiness for a specific service.
func (r *router) handleServiceReadiness(w http.ResponseWriter, reporter ServiceReadinessReporter, serviceID protocol.ServiceID) {
	endpointCount, hasSession, err := reporter.GetServiceReadiness(serviceID)

	info := ServiceReadyInfo{
		EndpointCount: endpointCount,
		HasSession:    hasSession,
	}

	if err != nil {
		info.Error = err.Error()
		info.Ready = false
	} else {
		// Ready if we have at least one endpoint and a session
		info.Ready = endpointCount > 0 && hasSession
	}

	response := ServiceReadinessResponse{
		Ready: info.Ready,
		Services: map[string]ServiceReadyInfo{
			string(serviceID): info,
		},
	}

	status := http.StatusOK
	if !response.Ready {
		status = http.StatusServiceUnavailable
	}
	r.writeReadinessResponse(w, response, status)
}

// handleAllServicesReadiness checks readiness for all configured services.
func (r *router) handleAllServicesReadiness(w http.ResponseWriter, reporter ServiceReadinessReporter) {
	configuredServices := reporter.ConfiguredServiceIDs()
	if len(configuredServices) == 0 {
		response := ServiceReadinessResponse{
			Ready:   false,
			Message: "no services configured",
		}
		r.writeReadinessResponse(w, response, http.StatusServiceUnavailable)
		return
	}

	services := make(map[string]ServiceReadyInfo)
	allReady := true

	for serviceID := range configuredServices {
		endpointCount, hasSession, err := reporter.GetServiceReadiness(serviceID)

		info := ServiceReadyInfo{
			EndpointCount: endpointCount,
			HasSession:    hasSession,
		}

		if err != nil {
			info.Error = err.Error()
			info.Ready = false
			allReady = false
		} else {
			info.Ready = endpointCount > 0 && hasSession
			if !info.Ready {
				allReady = false
			}
		}

		services[string(serviceID)] = info
	}

	response := ServiceReadinessResponse{
		Ready:    allReady,
		Services: services,
	}

	status := http.StatusOK
	if !response.Ready {
		status = http.StatusServiceUnavailable
	}
	r.writeReadinessResponse(w, response, status)
}

// writeReadinessResponse writes the readiness response as JSON.
func (r *router) writeReadinessResponse(w http.ResponseWriter, response ServiceReadinessResponse, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		r.logger.Error().Err(err).Msg("failed to encode readiness response")
	}
}

// handleConfig returns a sanitized view of the active configuration.
func (r *router) handleConfig(w http.ResponseWriter, req *http.Request) {
	reporter, ok := r.configReporter()
	if !ok {
		http.Error(w, `{"error": "config reporting not available"}`, http.StatusServiceUnavailable)
		return
	}

	config := reporter.GetSanitizedConfig()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(config); err != nil {
		r.logger.Error().Err(err).Msg("failed to encode config response")
	}
}

// readinessReporter returns the ServiceReadinessReporter if available.
// This is a type assertion helper that checks if the health checker's
// service ID reporter also implements ServiceReadinessReporter.
func (r *router) readinessReporter() (ServiceReadinessReporter, bool) {
	if r.healthChecker == nil || r.healthChecker.ServiceIDReporter == nil {
		return nil, false
	}
	reporter, ok := r.healthChecker.ServiceIDReporter.(ServiceReadinessReporter)
	return reporter, ok
}

// configReporter returns the ConfigReporter if available.
func (r *router) configReporter() (ConfigReporter, bool) {
	if r.healthChecker == nil || r.healthChecker.ServiceIDReporter == nil {
		return nil, false
	}
	reporter, ok := r.healthChecker.ServiceIDReporter.(ConfigReporter)
	return reporter, ok
}
