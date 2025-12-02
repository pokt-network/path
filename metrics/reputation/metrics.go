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

	// Tiered selection metrics
	reputationTierSelectionMetric = "shannon_reputation_tier_selection_total"

	// Tier distribution metrics (gauge showing endpoints per tier)
	reputationTierDistributionMetric = "shannon_reputation_tier_distribution"
)

func init() {
	prometheus.MustRegister(reputationSignalsTotal)
	prometheus.MustRegister(reputationEndpointsFiltered)
	prometheus.MustRegister(reputationScoreDistribution)
	prometheus.MustRegister(reputationErrorsTotal)
	prometheus.MustRegister(reputationTierSelection)
	prometheus.MustRegister(reputationTierDistribution)
}

var (
	// reputationSignalsTotal tracks the total reputation signals recorded.
	// Labels:
	//   - service_id: Target service identifier
	//   - signal_type: Type of signal (success, minor_error, major_error, critical_error, fatal_error)
	//   - endpoint_domain: Effective TLD+1 domain extracted from endpoint URL
	//
	// Use to analyze:
	//   - Signal distribution by type and service
	//   - Endpoint reliability patterns
	//   - Error rate trends over time
	reputationSignalsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      reputationSignalsTotalMetric,
			Help:      "Total number of reputation signals recorded by type",
		},
		[]string{"service_id", "signal_type", "endpoint_domain"},
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

	// reputationTierSelection tracks endpoint selections by tier.
	// Labels:
	//   - service_id: Target service identifier
	//   - tier: Selected tier (1=Premium, 2=Good, 3=Fair, 0=Random/disabled)
	//
	// Use to analyze:
	//   - Tier distribution across services
	//   - How often cascade-down occurs (tier 2/3 selections)
	//   - Effectiveness of tiered selection
	reputationTierSelection = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      reputationTierSelectionMetric,
			Help:      "Total endpoint selections by tier",
		},
		[]string{"service_id", "tier"},
	)

	// reputationTierDistribution tracks the current distribution of endpoints across tiers.
	// Labels:
	//   - service_id: Target service identifier
	//   - tier: Tier number (1=Premium, 2=Good, 3=Fair)
	//
	// Use to analyze:
	//   - Real-time health of endpoint pool
	//   - How endpoints are distributed across reputation tiers
	//   - Identify services with poor endpoint quality
	reputationTierDistribution = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: pathProcess,
			Name:      reputationTierDistributionMetric,
			Help:      "Current number of endpoints in each tier",
		},
		[]string{"service_id", "tier"},
	)
)

// RecordSignal records a reputation signal metric.
func RecordSignal(serviceID, signalType, endpointDomain string) {
	reputationSignalsTotal.With(prometheus.Labels{
		"service_id":      serviceID,
		"signal_type":     signalType,
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

// RecordTierSelection records which tier an endpoint was selected from.
func RecordTierSelection(serviceID string, tier int) {
	reputationTierSelection.With(prometheus.Labels{
		"service_id": serviceID,
		"tier":       tierToString(tier),
	}).Inc()
}

// RecordTierDistribution records the current distribution of endpoints across tiers.
// This should be called whenever tiered selection is performed to show real-time tier health.
func RecordTierDistribution(serviceID string, tier1Count, tier2Count, tier3Count int) {
	reputationTierDistribution.With(prometheus.Labels{
		"service_id": serviceID,
		"tier":       "1",
	}).Set(float64(tier1Count))

	reputationTierDistribution.With(prometheus.Labels{
		"service_id": serviceID,
		"tier":       "2",
	}).Set(float64(tier2Count))

	reputationTierDistribution.With(prometheus.Labels{
		"service_id": serviceID,
		"tier":       "3",
	}).Set(float64(tier3Count))
}

// tierToString converts tier number to string label.
func tierToString(tier int) string {
	switch tier {
	case 1:
		return "1"
	case 2:
		return "2"
	case 3:
		return "3"
	default:
		return "0"
	}
}
