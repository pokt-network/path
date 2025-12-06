// Package session provides functionality for exporting session metrics to Prometheus.
package session

import (
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// The POSIX process that emits metrics
	pathProcess = "path"

	// Session metrics
	activeSessionsGaugeMetric   = "shannon_active_sessions"
	sessionEndpointsGaugeMetric = "shannon_session_endpoints"
	sessionRefreshesTotalMetric = "shannon_session_refreshes_total"
	sessionRolloversTotalMetric = "shannon_session_rollovers_total"
)

func init() {
	prometheus.MustRegister(activeSessionsGauge)
	prometheus.MustRegister(sessionEndpointsGauge)
	prometheus.MustRegister(sessionRefreshesTotal)
	prometheus.MustRegister(sessionRolloversTotal)
}

var (
	// activeSessionsGauge tracks the current number of active sessions.
	// Labels:
	//   - service_id: Target service identifier
	//
	// Use to analyze:
	//   - Session pool size per service
	//   - Session availability trends
	activeSessionsGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: pathProcess,
			Name:      activeSessionsGaugeMetric,
			Help:      "Current number of active sessions per service",
		},
		[]string{"service_id"},
	)

	// sessionEndpointsGauge tracks endpoints per session.
	// Labels:
	//   - service_id: Target service identifier
	//   - endpoint_domain: Effective TLD+1 domain extracted from endpoint URL
	//
	// Use to analyze:
	//   - Endpoint distribution across services
	//   - Domain concentration patterns
	sessionEndpointsGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: pathProcess,
			Name:      sessionEndpointsGaugeMetric,
			Help:      "Current number of endpoints per service by domain",
		},
		[]string{"service_id", "endpoint_domain"},
	)

	// sessionRefreshesTotal tracks session refresh events.
	// Labels:
	//   - service_id: Target service identifier
	//   - status: Status of refresh (success, error, timeout)
	//
	// Use to analyze:
	//   - Session refresh frequency
	//   - Refresh error rates
	//   - Service stability
	sessionRefreshesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      sessionRefreshesTotalMetric,
			Help:      "Total number of session refresh events",
		},
		[]string{"service_id", "status"},
	)

	// sessionRolloversTotal tracks session rollover events.
	// Labels:
	//   - service_id: Target service identifier
	//   - used_fallback: Whether fallback was used during rollover (true/false)
	//
	// Use to analyze:
	//   - Session rollover frequency
	//   - Rollover impact on traffic routing
	sessionRolloversTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      sessionRolloversTotalMetric,
			Help:      "Total number of session rollover events",
		},
		[]string{"service_id", "used_fallback"},
	)
)

// Session refresh status constants.
const (
	RefreshStatusSuccess = "success"
	RefreshStatusError   = "error"
	RefreshStatusTimeout = "timeout"
)

// SetActiveSessions sets the current count of active sessions for a service.
func SetActiveSessions(serviceID string, count int) {
	activeSessionsGauge.With(prometheus.Labels{
		"service_id": serviceID,
	}).Set(float64(count))
}

// SetSessionEndpoints sets the current count of endpoints by domain for a service.
func SetSessionEndpoints(serviceID, endpointDomain string, count int) {
	sessionEndpointsGauge.With(prometheus.Labels{
		"service_id":      serviceID,
		"endpoint_domain": endpointDomain,
	}).Set(float64(count))
}

// RecordSessionRefresh records a session refresh event.
func RecordSessionRefresh(serviceID, status string) {
	sessionRefreshesTotal.With(prometheus.Labels{
		"service_id": serviceID,
		"status":     status,
	}).Inc()
}

// RecordSessionRollover records a session rollover event.
func RecordSessionRollover(serviceID string, usedFallback bool) {
	usedFallbackStr := "false"
	if usedFallback {
		usedFallbackStr = "true"
	}
	sessionRolloversTotal.With(prometheus.Labels{
		"service_id":    serviceID,
		"used_fallback": usedFallbackStr,
	}).Inc()
}
