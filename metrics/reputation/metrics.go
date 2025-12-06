// Package reputation provides functionality for exporting reputation system metrics to Prometheus.
package reputation

import (
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// The POSIX process that emits metrics
	pathProcess = "path"

	// Reputation signal metrics
	reputationSignalsTotalMetric = "shannon_reputation_signals_total"

	// Reputation filtering metrics
	reputationEndpointsFilteredMetric = "shannon_reputation_endpoints_filtered_total"

	// Reputation score metrics
	reputationScoreDistributionMetric = "shannon_reputation_score_distribution"

	// Reputation service health metrics
	reputationErrorsTotalMetric = "shannon_reputation_errors_total"

	// Probation metrics
	probationEndpointsGaugeMetric     = "shannon_probation_endpoints"
	probationTransitionsTotalMetric   = "shannon_probation_transitions_total"
	probationTrafficRoutedTotalMetric = "shannon_probation_traffic_routed_total"

	// Tier selection metrics
	tierDistributionGaugeMetric = "shannon_reputation_tier_endpoints"
	tierSelectionTotalMetric    = "shannon_reputation_tier_selection_total"
)

func init() {
	prometheus.MustRegister(reputationSignalsTotal)
	prometheus.MustRegister(reputationEndpointsFiltered)
	prometheus.MustRegister(reputationScoreDistribution)
	prometheus.MustRegister(reputationErrorsTotal)
	prometheus.MustRegister(probationEndpointsGauge)
	prometheus.MustRegister(probationTransitionsTotal)
	prometheus.MustRegister(probationTrafficRoutedTotal)
	prometheus.MustRegister(tierDistributionGauge)
	prometheus.MustRegister(tierSelectionTotal)
}

var (
	// reputationSignalsTotal tracks the total reputation signals recorded.
	// Labels:
	//   - service_id: Target service identifier
	//   - signal_type: Type of signal (success, minor_error, major_error, critical_error, fatal_error)
	//   - endpoint_type: Type of endpoint (http, websocket, unknown)
	//   - endpoint_domain: Effective TLD+1 domain extracted from endpoint URL
	//
	// Use to analyze:
	//   - Signal distribution by type and service
	//   - Endpoint reliability patterns
	//   - Error rate trends over time
	//   - HTTP vs WebSocket reliability differences
	reputationSignalsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      reputationSignalsTotalMetric,
			Help:      "Total number of reputation signals recorded by type",
		},
		[]string{"service_id", "signal_type", "endpoint_type", "endpoint_domain"},
	)

	// reputationEndpointsFiltered tracks endpoints filtered due to low reputation.
	// Labels:
	//   - service_id: Target service identifier
	//   - action: "filtered" (below threshold) or "allowed" (above threshold or new)
	//   - endpoint_domain: Effective TLD+1 domain extracted from endpoint URL
	//
	// Use to analyze:
	//   - How many endpoints are being excluded due to poor reputation
	//   - Filter effectiveness per service
	//   - Domain-level reliability issues
	reputationEndpointsFiltered = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      reputationEndpointsFilteredMetric,
			Help:      "Total endpoints filtered or allowed by reputation system",
		},
		[]string{"service_id", "action", "endpoint_domain"},
	)

	// reputationScoreDistribution tracks the distribution of endpoint reputation scores.
	// Labels:
	//   - service_id: Target service identifier
	//
	// Buckets are designed to show:
	//   - Critical zone (0-30): Endpoints likely to be filtered
	//   - Warning zone (30-50): Endpoints at risk
	//   - Healthy zone (50-80): Normal endpoints
	//   - Excellent zone (80-100): High-performing endpoints
	//
	// Use to analyze:
	//   - Overall health of endpoint pool
	//   - Score distribution patterns
	//   - Threshold effectiveness
	reputationScoreDistribution = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: pathProcess,
			Name:      reputationScoreDistributionMetric,
			Help:      "Distribution of endpoint reputation scores",
			Buckets:   []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100},
		},
		[]string{"service_id"},
	)

	// reputationErrorsTotal tracks errors in the reputation system itself.
	// Labels:
	//   - operation: The operation that failed (record_signal, get_score, get_scores, filter)
	//   - error_type: Type of error encountered
	//
	// Use to analyze:
	//   - Reputation system health
	//   - Storage issues
	//   - Unexpected failures
	reputationErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      reputationErrorsTotalMetric,
			Help:      "Total errors in the reputation system",
		},
		[]string{"operation", "error_type"},
	)

	// probationEndpointsGauge tracks the current number of endpoints in probation.
	// Labels:
	//   - service_id: Target service identifier
	//
	// Use to analyze:
	//   - Current probation pool size per service
	//   - Trends in endpoint health
	probationEndpointsGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: pathProcess,
			Name:      probationEndpointsGaugeMetric,
			Help:      "Current number of endpoints in probation per service",
		},
		[]string{"service_id"},
	)

	// probationTransitionsTotal tracks probation state transitions.
	// Labels:
	//   - service_id: Target service identifier
	//   - endpoint_domain: Effective TLD+1 domain extracted from endpoint URL
	//   - transition: Type of transition (entered, exited, recovered, demoted)
	//
	// Use to analyze:
	//   - How often endpoints enter/exit probation
	//   - Recovery success rates
	//   - Domain-level reliability patterns
	probationTransitionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      probationTransitionsTotalMetric,
			Help:      "Total probation state transitions by type",
		},
		[]string{"service_id", "endpoint_domain", "transition"},
	)

	// probationTrafficRoutedTotal tracks traffic routed to probation endpoints.
	// Labels:
	//   - service_id: Target service identifier
	//   - endpoint_domain: Effective TLD+1 domain extracted from endpoint URL
	//   - success: Whether the request was successful (true/false)
	//
	// Use to analyze:
	//   - Success rate of probation traffic
	//   - Whether probation endpoints are recovering
	probationTrafficRoutedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      probationTrafficRoutedTotalMetric,
			Help:      "Total traffic routed to endpoints in probation",
		},
		[]string{"service_id", "endpoint_domain", "success"},
	)

	// tierDistributionGauge tracks the current number of endpoints in each tier.
	// Labels:
	//   - service_id: Target service identifier
	//   - tier: Tier number (1, 2, or 3)
	tierDistributionGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: pathProcess,
			Name:      tierDistributionGaugeMetric,
			Help:      "Current number of endpoints in each reputation tier",
		},
		[]string{"service_id", "tier"},
	)

	// tierSelectionTotal tracks tier selections during endpoint filtering.
	// Labels:
	//   - service_id: Target service identifier
	//   - tier: Selected tier (0 = no tier available, 1, 2, or 3)
	tierSelectionTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      tierSelectionTotalMetric,
			Help:      "Total tier selections by tier number",
		},
		[]string{"service_id", "tier"},
	)
)

// EndpointType constants for metrics labeling.
// These match the RPC types used for endpoint filtering (sharedtypes.RPCType).
const (
	EndpointTypeJSONRPC   = "jsonrpc"
	EndpointTypeREST      = "rest"
	EndpointTypeWebSocket = "websocket"
	EndpointTypeGRPC      = "grpc"
	EndpointTypeUnknown   = "unknown"
)

// RecordSignal records a reputation signal metric.
func RecordSignal(serviceID, signalType, endpointType, endpointDomain string) {
	reputationSignalsTotal.With(prometheus.Labels{
		"service_id":      serviceID,
		"signal_type":     signalType,
		"endpoint_type":   endpointType,
		"endpoint_domain": endpointDomain,
	}).Inc()
}

// RecordEndpointFiltered records when an endpoint is filtered due to low reputation.
func RecordEndpointFiltered(serviceID, endpointDomain string) {
	reputationEndpointsFiltered.With(prometheus.Labels{
		"service_id":      serviceID,
		"action":          "filtered",
		"endpoint_domain": endpointDomain,
	}).Inc()
}

// RecordEndpointAllowed records when an endpoint passes the reputation filter.
func RecordEndpointAllowed(serviceID, endpointDomain string) {
	reputationEndpointsFiltered.With(prometheus.Labels{
		"service_id":      serviceID,
		"action":          "allowed",
		"endpoint_domain": endpointDomain,
	}).Inc()
}

// RecordScoreObservation records a score observation for histogram distribution.
func RecordScoreObservation(serviceID string, score float64) {
	reputationScoreDistribution.With(prometheus.Labels{
		"service_id": serviceID,
	}).Observe(score)
}

// RecordError records an error in the reputation system.
func RecordError(operation, errorType string) {
	reputationErrorsTotal.With(prometheus.Labels{
		"operation":  operation,
		"error_type": errorType,
	}).Inc()
}

// Probation transition types for metrics labeling.
const (
	ProbationTransitionEntered   = "entered"
	ProbationTransitionExited    = "exited"
	ProbationTransitionRecovered = "recovered"
	ProbationTransitionDemoted   = "demoted"
)

// SetProbationEndpointsCount sets the current count of endpoints in probation for a service.
func SetProbationEndpointsCount(serviceID string, count int) {
	probationEndpointsGauge.With(prometheus.Labels{
		"service_id": serviceID,
	}).Set(float64(count))
}

// RecordProbationTransition records a probation state transition.
func RecordProbationTransition(serviceID, endpointDomain, transition string) {
	probationTransitionsTotal.With(prometheus.Labels{
		"service_id":      serviceID,
		"endpoint_domain": endpointDomain,
		"transition":      transition,
	}).Inc()
}

// RecordProbationTraffic records traffic routed to probation endpoints.
func RecordProbationTraffic(serviceID, endpointDomain string, success bool) {
	successStr := "false"
	if success {
		successStr = "true"
	}
	probationTrafficRoutedTotal.With(prometheus.Labels{
		"service_id":      serviceID,
		"endpoint_domain": endpointDomain,
		"success":         successStr,
	}).Inc()
}

// RecordTierDistribution records the current endpoint distribution across tiers.
func RecordTierDistribution(serviceID string, tier1Count, tier2Count, tier3Count int) {
	tierDistributionGauge.With(prometheus.Labels{
		"service_id": serviceID,
		"tier":       "1",
	}).Set(float64(tier1Count))
	tierDistributionGauge.With(prometheus.Labels{
		"service_id": serviceID,
		"tier":       "2",
	}).Set(float64(tier2Count))
	tierDistributionGauge.With(prometheus.Labels{
		"service_id": serviceID,
		"tier":       "3",
	}).Set(float64(tier3Count))
}

// RecordTierSelection records which tier was selected for endpoint filtering.
func RecordTierSelection(serviceID string, tier int) {
	tierSelectionTotal.With(prometheus.Labels{
		"service_id": serviceID,
		"tier":       tierToString(tier),
	}).Inc()
}

// tierToString converts a tier number to its string representation.
func tierToString(tier int) string {
	switch tier {
	case 0:
		return "0"
	case 1:
		return "1"
	case 2:
		return "2"
	case 3:
		return "3"
	default:
		return "unknown"
	}
}
