package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/pokt-network/path/protocol"
)

// ServiceReadinessReporter provides readiness information for services.
// Implemented by the protocol to report session and endpoint availability.
type ServiceReadinessReporter interface {
	// GetServiceReadiness returns readiness info for a specific service.
	// Returns endpoint count, whether sessions are available, and any error.
	GetServiceReadiness(serviceID protocol.ServiceID) (endpointCount int, hasSession bool, err error)

	// GetServiceEndpointDetails returns detailed endpoint information for a specific service.
	// Includes reputation scores, archival status, latency metrics, and more.
	GetServiceEndpointDetails(serviceID protocol.ServiceID) ([]protocol.EndpointDetails, error)

	// GetServicePerceivedBlockHeight returns the perceived block height for a service.
	// This is the highest block number observed across all endpoints for the service.
	// Returns 0 if no block number has been observed yet.
	GetServicePerceivedBlockHeight(serviceID protocol.ServiceID) uint64

	// GetServiceBlockConsensusStats returns block consensus statistics for a service.
	// Returns (medianBlock, observationCount) for observability.
	GetServiceBlockConsensusStats(serviceID protocol.ServiceID) (medianBlock uint64, observationCount int)

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
	Ready                bool                       `json:"ready"`
	EndpointCount        int                        `json:"endpoint_count"`
	HasSession           bool                       `json:"has_session"`
	PerceivedBlockHeight uint64                     `json:"perceived_block_height,omitempty"`
	MedianBlockHeight    uint64                     `json:"median_block_height,omitempty"`
	BlockObservations    int                        `json:"block_observations,omitempty"`
	Error                string                     `json:"error,omitempty"`
	Endpoints            []protocol.EndpointDetails `json:"endpoints,omitempty"`
}

// handleHealth is a minimal liveness probe endpoint.
// Returns 200 OK with no body for Kubernetes liveness probes.
// For detailed health info, use /healthz instead.
func (r *router) handleHealth(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// handleReady handles both /ready and /ready/{serviceId} endpoints.
// Returns 200 if ready, 503 if not ready.
// Supports optional query parameter ?detailed=true to include endpoint details.
func (r *router) handleReady(w http.ResponseWriter, req *http.Request) {
	// Extract service ID from path if present: /ready/{serviceId}
	path := strings.TrimPrefix(req.URL.Path, "/ready")
	path = strings.TrimPrefix(path, "/")
	serviceID := protocol.ServiceID(path)

	// Check if detailed endpoint info is requested
	includeDetails := req.URL.Query().Get("detailed") == "true"

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
		r.handleServiceReadiness(w, reporter, serviceID, includeDetails)
	} else {
		// All services readiness check
		r.handleAllServicesReadiness(w, reporter, includeDetails)
	}
}

// handleServiceReadiness checks readiness for a specific service.
func (r *router) handleServiceReadiness(w http.ResponseWriter, reporter ServiceReadinessReporter, serviceID protocol.ServiceID, includeDetails bool) {
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

	// Include endpoint details and block consensus stats if requested
	if includeDetails {
		details, detailsErr := reporter.GetServiceEndpointDetails(serviceID)
		if detailsErr != nil {
			r.logger.Warn().Err(detailsErr).Str("service_id", string(serviceID)).Msg("Failed to get endpoint details")
		} else {
			info.Endpoints = details
		}

		// Include block consensus stats
		info.PerceivedBlockHeight = reporter.GetServicePerceivedBlockHeight(serviceID)
		info.MedianBlockHeight, info.BlockObservations = reporter.GetServiceBlockConsensusStats(serviceID)
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
func (r *router) handleAllServicesReadiness(w http.ResponseWriter, reporter ServiceReadinessReporter, includeDetails bool) {
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

		// Include endpoint details and block consensus stats if requested
		if includeDetails {
			details, detailsErr := reporter.GetServiceEndpointDetails(serviceID)
			if detailsErr != nil {
				r.logger.Warn().Err(detailsErr).Str("service_id", string(serviceID)).Msg("Failed to get endpoint details")
			} else {
				info.Endpoints = details
			}

			// Include block consensus stats
			info.PerceivedBlockHeight = reporter.GetServicePerceivedBlockHeight(serviceID)
			info.MedianBlockHeight, info.BlockObservations = reporter.GetServiceBlockConsensusStats(serviceID)
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

// handleCircuitBreakerClear handles POST /admin/circuit-breaker/clear/{serviceId}
// Clears both in-memory and Redis circuit breaker state for the given service.
func (r *router) handleCircuitBreakerClear(w http.ResponseWriter, req *http.Request) {
	if r.circuitBreakerAdmin == nil {
		http.Error(w, `{"error":"circuit breaker not configured"}`, http.StatusServiceUnavailable)
		return
	}

	serviceID := strings.TrimPrefix(req.URL.Path, "/admin/circuit-breaker/clear/")
	if serviceID == "" {
		http.Error(w, `{"error":"service ID required: POST /admin/circuit-breaker/clear/{serviceId}"}`, http.StatusBadRequest)
		return
	}

	count := r.circuitBreakerAdmin.ClearService(req.Context(), serviceID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"service_id":      serviceID,
		"cleared_domains": count,
		"message":         "circuit breaker state cleared (in-memory + Redis)",
	})
}

// handleChainStateClear handles POST /admin/chain-state/clear/{serviceId}
// Resets the perceived block height (in-memory + Redis) for the given service so it
// rebuilds from fresh endpoint observations. This is the only way to recover a
// stuck/too-high perceived height: the max-based consensus and the external floor only
// ever RAISE perceived, so a poisoned value cannot self-correct.
//
// Like the circuit-breaker clear, this must be called on EACH pod, since the perceived
// floor is per-pod in-memory state.
func (r *router) handleChainStateClear(w http.ResponseWriter, req *http.Request) {
	if r.chainStateAdmin == nil {
		http.Error(w, `{"error":"chain state admin not configured"}`, http.StatusServiceUnavailable)
		return
	}

	serviceID := strings.TrimPrefix(req.URL.Path, "/admin/chain-state/clear/")
	if serviceID == "" {
		http.Error(w, `{"error":"service ID required: POST /admin/chain-state/clear/{serviceId}"}`, http.StatusBadRequest)
		return
	}

	found, err := r.chainStateAdmin.ResetChainState(req.Context(), serviceID)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, fmt.Sprintf(`{"error":"service %q not found or has no perceived block height to reset"}`, serviceID), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"service_id": serviceID,
		"message":    "chain state cleared (perceived block height reset, in-memory + Redis)",
	})
}

// handleWebsocketTumble handles POST /admin/websocket/tumble/{serviceId}
//
// Forces live websocket connections for the service to rebind onto DIFFERENT suppliers.
// Clients stay connected throughout — the bridge re-dials an endpoint and replays the
// client's subscriptions, the same machinery a session rollover or a stall rebind uses.
//
// Why this exists: a websocket connection binds one endpoint for its entire lifetime and
// only moves at a session rollover or a stall. A long-lived high-volume subscriber
// therefore pins itself to whichever operator it first landed on, and no change to
// endpoint selection can move it — selection only governs where NEW connections go. The
// alternative to this endpoint is restarting the pod, which drops every client and
// resets unrelated in-memory state.
//
// Like the other admin endpoints, this operates on PER-POD in-memory state and must be
// issued to each pod separately.
//
// Query parameters (all optional):
//
//	domain=<eTLD+1>  only move connections currently bound to this operator
//	max=<n>          move at most n connections, most-concentrated operators first
//	dry_run=true     report what would move without moving anything
func (r *router) handleWebsocketTumble(w http.ResponseWriter, req *http.Request) {
	if r.websocketAdmin == nil {
		http.Error(w, `{"error":"websocket admin not configured"}`, http.StatusServiceUnavailable)
		return
	}

	serviceID := strings.TrimPrefix(req.URL.Path, "/admin/websocket/tumble/")
	if serviceID == "" {
		http.Error(w, `{"error":"service ID required: POST /admin/websocket/tumble/{serviceId}"}`, http.StatusBadRequest)
		return
	}

	query := req.URL.Query()

	// max must be a non-negative integer; a malformed value is rejected rather than
	// silently treated as "no cap", which would tumble every connection.
	maxConns := 0
	if raw := query.Get("max"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			http.Error(w, `{"error":"max must be a non-negative integer"}`, http.StatusBadRequest)
			return
		}
		maxConns = parsed
	}

	// order_by controls how a capped tumble ranks candidates. Defaults to throughput —
	// socket count is a poor proxy for load, so ordering by it spends the cap moving
	// connections that were not the problem. Rejected rather than silently defaulted, so a
	// typo does not quietly give the operator the ordering they were trying to avoid.
	orderBy := protocol.TumbleOrderThroughput
	switch raw := query.Get("order_by"); raw {
	case "", string(protocol.TumbleOrderThroughput):
	case string(protocol.TumbleOrderConnections):
		orderBy = protocol.TumbleOrderConnections
	default:
		http.Error(w, `{"error":"order_by must be one of: throughput, connections"}`, http.StatusBadRequest)
		return
	}

	result := r.websocketAdmin.TumbleWebsockets(protocol.WebsocketTumbleRequest{
		ServiceID: serviceID,
		Domain:    query.Get("domain"),
		Max:       maxConns,
		OrderBy:   orderBy,
		DryRun:    query.Get("dry_run") == "true",
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}
