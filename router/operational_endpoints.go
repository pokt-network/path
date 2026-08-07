package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
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

// handleReputationDrain handles POST /admin/reputation/drain/{serviceId}
//
// Temporarily benches every scored endpoint belonging to one operator (eTLD+1) for the
// service, by writing a cooldown expiry onto its score. Selection already excludes
// endpoints in cooldown regardless of score, so this reuses a filter every selection path
// is guaranteed to consult.
//
// Why this exists: questions of the form "where would this traffic go if operator X were
// not available" are otherwise only answerable by waiting for X to fail. Tumbling a
// websocket connection is not a substitute — a tumble re-dials but leaves every operator
// eligible, so the connection can and does land straight back where it started.
//
// This is NOT a penalty. Value, CriticalStrikes and RecentCriticalRate are left alone, so
// the quality signal stays readable while the drain is in effect — which matters, because
// reading it is usually the entire point of draining.
//
// Per-pod in-memory state like the other admin endpoints; issue it to each pod.
//
// Query parameters:
//
//	domain=<eTLD+1>   operator to bench (REQUIRED)
//	duration=<dur>    how long, Go duration (default 15m). 0 releases this pod's drain.
//	rpc_type=<type>   narrow to one protocol (websocket, json_rpc, …); default all
//	dry_run=true      report what would be benched without writing
//
// A drain expires on its own. It does not survive a pod restart.
func (r *router) handleReputationDrain(w http.ResponseWriter, req *http.Request) {
	if r.reputationAdmin == nil {
		http.Error(w, `{"error":"reputation admin not configured"}`, http.StatusServiceUnavailable)
		return
	}

	serviceID := strings.TrimPrefix(req.URL.Path, "/admin/reputation/drain/")
	if serviceID == "" {
		http.Error(w, `{"error":"service ID required: POST /admin/reputation/drain/{serviceId}"}`, http.StatusBadRequest)
		return
	}

	query := req.URL.Query()

	// Required rather than defaulted: a drain with no target would bench the whole
	// service, which is never what anyone meant to type.
	target := query.Get("domain")
	if target == "" {
		target = query.Get("url")
	}
	if target == "" {
		http.Error(w, `{"error":"target required: ?domain=<eTLD+1|hostname|url>"}`, http.StatusBadRequest)
		return
	}

	// Default 15m — long enough to collect a clean rate window, short enough that a
	// forgotten drain heals itself well inside a shift.
	duration := 15 * time.Minute
	if raw := query.Get("duration"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < 0 {
			http.Error(w, `{"error":"duration must be a non-negative Go duration (e.g. 15m); 0 releases"}`, http.StatusBadRequest)
			return
		}
		duration = parsed
	}

	// Resolve the human-facing target (an operator domain, hostname, or URL) into the
	// concrete identifiers a reputation key can carry. This MUST happen here rather than
	// inside the reputation service: key granularity is per-service config — endpoint
	// address, URL, domain, or supplier address — so the same operator is a hostname on
	// one service and a pokt1… supplier address on another, and only the protocol layer
	// holds the supplier→URL mapping that bridges them.
	reporter, ok := r.readinessReporter()
	if !ok {
		http.Error(w, `{"error":"endpoint details unavailable; cannot resolve target"}`, http.StatusServiceUnavailable)
		return
	}
	details, err := reporter.GetServiceEndpointDetails(protocol.ServiceID(serviceID))
	if err != nil {
		http.Error(w, `{"error":"failed to list endpoints for service"}`, http.StatusInternalServerError)
		return
	}

	identifiers, matchedEndpoints := resolveDrainIdentifiers(details, target)

	result := r.reputationAdmin.DrainDomain(req.Context(), reputation.DrainRequest{
		ServiceID:   protocol.ServiceID(serviceID),
		Identifiers: identifiers,
		Label:       target,
		Duration:    duration,
		RPCType:     query.Get("rpc_type"),
		DryRun:      query.Get("dry_run") == "true",
	})
	result.MatchedEndpoints = matchedEndpoints

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}

// resolveDrainIdentifiers maps a human-facing target — an eTLD+1, a hostname, or a full
// URL — onto every reputation-key identifier the matching endpoints could be keyed under,
// and reports how many endpoints matched.
//
// Every granularity is emitted for each matching endpoint (full address, supplier address,
// URL, hostname, eTLD+1) rather than trying to detect which one the service uses. Emitting
// a superset is safe because matching is exact string equality against keys that already
// belong to the requested service, and it means the drain does not silently bench nothing
// when a service's granularity is not what the caller assumed — which is exactly how the
// supplier-address-keyed services defeated an eTLD+1-only filter.
func resolveDrainIdentifiers(details []protocol.EndpointDetails, target string) ([]string, int) {
	target = strings.ToLower(strings.TrimSpace(target))
	// Accept a full URL as the target by reducing it to its host.
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		target = strings.ToLower(u.Hostname())
	}

	set := make(map[string]struct{})
	matched := 0
	for _, d := range details {
		host := ""
		if u, err := url.Parse(d.URL); err == nil {
			host = strings.ToLower(u.Hostname())
		}
		if host == "" {
			continue
		}
		if host != target && registrableDomain(host) != target {
			continue
		}
		matched++
		for _, id := range []string{d.Address, d.SupplierAddress, d.URL, host, registrableDomain(host)} {
			if id != "" {
				set[id] = struct{}{}
			}
		}
	}

	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, matched
}

// registrableDomain returns the last two labels of a host ("rm-01.eu.example.com" →
// "example.com"). Deliberately the same naive rule the metrics layer uses for the `domain`
// label, so an operator identified from a dashboard resolves to the same thing here.
func registrableDomain(host string) string {
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return host
	}
	return strings.Join(parts[len(parts)-2:], ".")
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
