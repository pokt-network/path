// Package metrics provides Prometheus metrics for PATH gateway observability.
// These metrics are designed based on how_metrics_should_work.md to provide
// domain-centric, actionable insights into gateway performance.
package metrics

import (
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	// MetricPrefix prefix for all PATH metrics
	MetricPrefix = "path_"

	// --- Label names used across metrics

	LabelDomain             = "domain"
	LabelRPCType            = "rpc_type"
	LabelServiceID          = "service_id"
	LabelTierThreshold      = "tier_threshold"
	LabelSessionStartHeight = "session_start_height"
	LabelHealthCheckName    = "health_check_name"
	LabelReputationSignal   = "reputation_signal"
	LabelNetworkType        = "network_type"
	LabelMethod             = "method"
	LabelLatencySignal      = "latency_signal"
	LabelStatusCode         = "status_code"
	LabelRetryReason        = "reason"
	LabelRetryCount         = "retry_count"
	LabelResult             = "result"
	LabelBatchCount         = "batch_count"
	LabelSupplier           = "supplier"
	LabelSignalType         = "signal_type"
	LabelSeverity           = "severity"

	// --- Latency signal values

	LatencySignalCheetah = "Cheetah"
	LatencySignalGazelle = "Gazelle"
	LatencySignalRabbit  = "Rabbit"
	LatencySignalTurtle  = "Turtle"
	LatencySignalSnail   = "Snail"

	// --- Reputation signal values

	SignalOK            = "ok"
	SignalSlow          = "slow"
	SignalSlowASF       = "slow_asf"
	SignalMinorError    = "minor_error"
	SignalMajorError    = "major_error"
	SignalCriticalError = "critical_error"
	SignalFatalError    = "fatal_error"

	// --- Network types

	NetworkTypeEVM         = "evm"
	NetworkTypeCosmos      = "cosmos"
	NetworkTypeSolana      = "solana"
	NetworkTypePassthrough = "passthrough"
	// NetworkTypeGeneric     = "generic" // needs proper implementation as a possible network type.

	// --- Retry reasons

	RetryReason5xx        = "retry_on_5xx"
	RetryReasonTimeout    = "retry_on_timeout"
	RetryReasonConnection = "retry_on_connection"
	RetryReasonHeuristic  = "retry_on_heuristic"

	// --- Retry results

	RetryResultSuccess = "success"
	RetryResultFailure = "failure"

	// --- Hedge results

	HedgeResultPrimaryWon   = "primary_won"   // Primary request won the race
	HedgeResultHedgeWon     = "hedge_won"     // Hedge request won the race
	HedgeResultPrimaryOnly  = "primary_only"  // Primary responded before hedge delay
	HedgeResultBothFailed   = "both_failed"   // Both primary and hedge failed
	HedgeResultNoHedge      = "no_hedge"      // No hedge started (no alternative endpoint)
	HedgeReasonDelayElapsed = "delay_elapsed" // Hedge started because delay elapsed

	// --- Probation events

	LabelProbationEvent = "event"

	ProbationEventEntered = "entered"
	ProbationEventExited  = "exited"
	ProbationEventRouted  = "routed"
)

// NormalizeRPCType converts an RPC type string to lowercase format for consistent metric labels.
// Accepts both protobuf enum string format ("JSON_RPC") and snake_case format ("json_rpc").
// Returns lowercase snake_case (e.g., "json_rpc", "rest", "comet_bft", "websocket").
func NormalizeRPCType(rpcType string) string {
	return strings.ToLower(rpcType)
}

// =============================================================================
// Reputation Endpoint Leaderboard (Gauge, published every 10s via cron)
// Labels: domain, rpc_type, service_id, tier_threshold, session_start_height
// Value: endpoint count
// Purpose: Show how endpoints are grouped/distributed as a "leaderboard"
// =============================================================================

var ReputationEndpointLeaderboard = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: MetricPrefix + "reputation_endpoint_leaderboard",
		Help: "Number of endpoints grouped by domain, rpc_type, service_id, tier_threshold, and session_start_height. Published every 10s as a leaderboard snapshot.",
	},
	[]string{LabelDomain, LabelRPCType, LabelServiceID, LabelTierThreshold, LabelSessionStartHeight},
)

// =============================================================================
// Health Check Status (Counter)
// Labels: domain, supplier, rpc_type, service_id, health_check_name, reputation_signal
// Value: count
// Purpose: Track health check results per supplier for filtering visibility
// =============================================================================

var HealthCheckStatus = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "health_check_status_total",
		Help: "Health check results by domain, supplier, rpc_type, service_id, health_check_name, and reputation_signal.",
	},
	[]string{LabelDomain, LabelSupplier, LabelRPCType, LabelServiceID, LabelHealthCheckName, LabelReputationSignal},
)

// =============================================================================
// Observation Pipeline (Counter)
// Labels: domain, rpc_type, service_id, network_type, method, reputation_signal
// Value: count
// Purpose: Track observation events with method-level granularity
// =============================================================================

var ObservationPipeline = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "observation_pipeline_total",
		Help: "Observation pipeline events by domain, rpc_type, service_id, network_type, method, and reputation_signal.",
	},
	[]string{LabelDomain, LabelRPCType, LabelServiceID, LabelNetworkType, LabelMethod, LabelReputationSignal},
)

// =============================================================================
// Latency Reputation (Counter)
// Labels: domain, rpc_type, service_id, latency_signal
// Value: count
// Purpose: Categorize response latency into buckets (Cheetah/Gazelle/Rabbit/Turtle/Snail)
// =============================================================================

var LatencyReputation = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "latency_reputation_total",
		Help: "Latency categorization by domain, rpc_type, service_id, and latency_signal (Cheetah/Gazelle/Rabbit/Turtle/Snail).",
	},
	[]string{LabelDomain, LabelRPCType, LabelServiceID, LabelLatencySignal},
)

// =============================================================================
// Requests Per Second (Counter + Histogram)
// Labels: domain, rpc_type, service_id, status_code
// Value: count (counter), latency in seconds (histogram)
// Purpose: Track request throughput and latency by status code
// =============================================================================

var RequestsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "requests_total",
		Help: "Total requests by domain, rpc_type, service_id, and status_code.",
	},
	[]string{LabelDomain, LabelRPCType, LabelServiceID, LabelStatusCode},
)

var RequestLatency = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    MetricPrefix + "request_latency_seconds",
		Help:    "Request latency in seconds by domain, rpc_type, service_id, and status_code.",
		Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	},
	[]string{LabelDomain, LabelRPCType, LabelServiceID, LabelStatusCode},
)

// =============================================================================
// Retries Distribution (Counter)
// Labels: domain, rpc_type, service_id, reason
// Value: count
// Purpose: Track why retries happen (5xx, timeout, connection errors)
// =============================================================================

var RetriesDistribution = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "retries_distribution_total",
		Help: "Retry events by domain, rpc_type, service_id, and reason (retry_on_5xx/retry_on_timeout/retry_on_connection).",
	},
	[]string{LabelDomain, LabelRPCType, LabelServiceID, LabelRetryReason},
)

// =============================================================================
// Retry Results (Counter + Histogram)
// Labels: rpc_type, service_id, retry_count, result
// Value: count (counter), latency in seconds (histogram)
// Purpose: Track retry outcomes (success/failure) and their latency
// =============================================================================

var RetryResultsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "retry_results_total",
		Help: "Retry results by rpc_type, service_id, retry_count, and result (success/failure).",
	},
	[]string{LabelRPCType, LabelServiceID, LabelRetryCount, LabelResult},
)

var RetryResultsLatency = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    MetricPrefix + "retry_results_latency_seconds",
		Help:    "Retry result latency in seconds by rpc_type, service_id, retry_count, and result.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	},
	[]string{LabelRPCType, LabelServiceID, LabelRetryCount, LabelResult},
)

// =============================================================================
// Batch Size Distribution (Counter + Histogram)
// Labels: rpc_type, service_id, batch_count
// Value: count (counter), latency in seconds (histogram)
// Purpose: Track batch sizes (1 request could contain 100 relays)
// =============================================================================

var BatchSizeTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "batch_size_total",
		Help: "Batch requests by rpc_type, service_id, and batch_count.",
	},
	[]string{LabelRPCType, LabelServiceID, LabelBatchCount},
)

var BatchSizeLatency = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    MetricPrefix + "batch_size_latency_seconds",
		Help:    "Batch request latency in seconds by rpc_type, service_id, and batch_count.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
	},
	[]string{LabelRPCType, LabelServiceID, LabelBatchCount},
)

// BatchRequestsTotal counts the number of batch requests per service.
// Used with BatchItemsTotal to calculate average batch size: items/requests
var BatchRequestsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "batch_requests_total",
		Help: "Total number of batch requests by service.",
	},
	[]string{LabelServiceID},
)

// BatchItemsTotal counts the total items across all batch requests per service.
// Used with BatchRequestsTotal to calculate average batch size: items/requests
var BatchItemsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "batch_items_total",
		Help: "Total items across all batch requests by service.",
	},
	[]string{LabelServiceID},
)

// =============================================================================
// Request/Response Sizes (Counters)
// Labels: rpc_type, service_id
// Value: bytes
// Purpose: Track data volume received and returned
// =============================================================================

var RequestBytesReceived = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "request_bytes_received_total",
		Help: "Total bytes received in requests by rpc_type and service_id.",
	},
	[]string{LabelRPCType, LabelServiceID},
)

var ResponseBytesSent = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "response_bytes_sent_total",
		Help: "Total bytes sent in responses by rpc_type and service_id.",
	},
	[]string{LabelRPCType, LabelServiceID},
)

// RequestBodySizeBytes is the distribution of request body sizes, complementing
// the request_bytes_received_total counter (which only yields totals/averages).
// It exists to make p50/p99/max request sizes observable — e.g. to size the
// max_request_body_bytes limit against real traffic.
//
// Cardinality is bounded: it reuses the same fixed rpc_type/service_id labels as
// the counter (no new label dimensions) and a small, fixed bucket set. Buckets
// span from ~256B to 128MB so the common case (single-KB), the tail (single-digit
// MB), and anything approaching the request-size limit are all visible.
var RequestBodySizeBytes = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name: MetricPrefix + "request_body_size_bytes",
		Help: "Distribution of HTTP request body sizes in bytes by rpc_type and service_id.",
		Buckets: []float64{
			256, 1024, 4096, 16384, 65536, // 256B .. 64KB
			262144, 1048576, 4194304, 16777216, // 256KB .. 16MB
			67108864, 134217728, // 64MB, 128MB
		},
	},
	[]string{LabelRPCType, LabelServiceID},
)

// =============================================================================
// Probation Events (Counter)
// Labels: domain, rpc_type, service_id, event
// Value: count
// Purpose: Track probation system activity (entered, exited, routed)
// =============================================================================

var ProbationEventsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "probation_events_total",
		Help: "Probation events by domain, rpc_type, service_id, and event type (entered/exited/routed).",
	},
	[]string{LabelDomain, LabelRPCType, LabelServiceID, LabelProbationEvent},
)

// =============================================================================
// Domain Circuit Breaker State (Gauge)
// Labels: service_id, domain
// Value: 1 if domain is currently broken (circuit-breaker locked out), 0 otherwise
// Purpose: Surface circuit-breaker state to dashboards. Without this, broken-domain
// state lives only in Redis (`path:gw:circuit:{serviceID}`) and is invisible to
// operators inspecting Grafana.
// =============================================================================

var DomainCircuitBreakerState = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: MetricPrefix + "circuit_breaker_state",
		Help: "1 if the domain is currently circuit-broken for this service (locked out from selection), 0 otherwise. Set on MarkBroken / MarkRecovered transitions and refreshed from Redis on each Get.",
	},
	[]string{LabelServiceID, LabelDomain},
)

// SetCircuitBreakerState sets the per-domain circuit-breaker gauge.
//   - broken=true → 1 (domain is currently locked out)
//   - broken=false → 0 (domain is healthy / has been recovered)
//
// Skipped silently when domain is empty (which would happen for endpoints whose
// URL parse fails and only happens in error paths anyway).
func SetCircuitBreakerState(serviceID, domain string, broken bool) {
	if domain == "" {
		return
	}
	v := 0.0
	if broken {
		v = 1.0
	}
	DomainCircuitBreakerState.WithLabelValues(serviceID, domain).Set(v)
}

// =============================================================================
// Domain Circuit Breaker Events (Counter)
// Labels: service_id, domain, reason_category, event
// Purpose: Surface WHY domains break and at what rate. Pairs with
// path_circuit_breaker_state (current 0/1 view) — events_total gives the
// historical decomposition by reason and event type (broken vs recovered).
//
// reason_category is a low-cardinality bucket extracted from the free-text
// reason passed to MarkBroken (which itself contains response snippets and
// error messages — too high-cardinality for direct labelling). See
// CircuitBreakerReasonCategory.
//
// event ∈ {"broken", "recovered"} so a single counter captures both transitions.
// =============================================================================

const (
	LabelReasonCategory          = "reason_category"
	LabelCircuitBreakerEvent     = "event"
	CircuitBreakerEventBroken    = "broken"
	CircuitBreakerEventRecovered = "recovered"
)

// CircuitBreakerReasonCategory enumerates the bounded set of reason buckets
// surfaced via the events counter. Keeps cardinality bounded regardless of
// how variable the underlying reason strings are. Add new categories here
// when MarkBroken call sites grow new reason prefixes.
const (
	CircuitBreakReasonRetry          = "retry"           // failure during retry path
	CircuitBreakReasonBatchTransport = "batch_transport" // batch path: transport-level error
	CircuitBreakReasonBatchHeuristic = "batch_heuristic" // batch path: heuristic flagged response
	CircuitBreakReasonParallelRetry  = "parallel_retry"  // parallel-retry path failure
	CircuitBreakReasonHeuristic      = "heuristic"       // top-level heuristic break
	CircuitBreakReasonUnknown        = "unknown"         // anything we can't classify
)

var DomainCircuitBreakerEventsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "circuit_breaker_events_total",
		Help: "Domain circuit-breaker transitions over time. event ∈ {broken, recovered}. reason_category is a bounded prefix bucket extracted from the raw MarkBroken reason. Pairs with path_circuit_breaker_state for current snapshot.",
	},
	[]string{LabelServiceID, LabelDomain, LabelReasonCategory, LabelCircuitBreakerEvent},
)

// RecordCircuitBreakerEvent increments the per-(service, domain, reason, event)
// counter. Skipped silently when domain is empty.
func RecordCircuitBreakerEvent(serviceID, domain, reasonCategory, event string) {
	if domain == "" {
		return
	}
	if reasonCategory == "" {
		reasonCategory = CircuitBreakReasonUnknown
	}
	DomainCircuitBreakerEventsTotal.WithLabelValues(serviceID, domain, reasonCategory, event).Inc()
}

// =============================================================================
// Endpoints In Cooldown (Gauge, published every 10s via leaderboard publisher)
// Labels: domain, rpc_type, service_id
// Value: count of endpoints currently in a strike cooldown period (Score.IsInCooldown
// returns true — i.e. CooldownUntil is in the future).
// Purpose: Cooldown is a TEMPORARY state imposed by accumulated critical strikes,
// independent from "score below minThreshold". Pre-this-metric, cooldown state was
// only visible via /ready/<svc>?detailed=true on a single pod. Now operators can
// see at a glance: "how many endpoints does this domain have in cooldown right now?"
// (For "below minThreshold / excluded" use path_reputation_endpoint_leaderboard
// with tier_threshold="0" — that's already covered by the existing leaderboard.)
// =============================================================================

var EndpointsInCooldown = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: MetricPrefix + "endpoints_in_cooldown",
		Help: "Number of endpoints currently in strike cooldown (Score.CooldownUntil in the future). Published every 10s. Cooldown is independent from score-below-threshold — an endpoint can be cooldown'd even with a high score after critical strikes.",
	},
	[]string{LabelDomain, LabelRPCType, LabelServiceID},
)

// =============================================================================
// Supplier Blacklist Events (Counter)
// Labels: domain, supplier, service_id, reason
// Value: count
// Purpose: Track suppliers blacklisted for validation/signature errors
// =============================================================================

var SupplierBlacklistTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "supplier_blacklist_total",
		Help: "Suppliers blacklisted by domain, supplier address, service_id, and reason.",
	},
	[]string{LabelDomain, LabelSupplier, LabelServiceID, "reason"},
)

// Blacklist reason constants
const (
	BlacklistReasonSignatureError  = "signature_error"
	BlacklistReasonValidationError = "validation_error"
	BlacklistReasonUnmarshalError  = "unmarshal_error"
	BlacklistReasonPubKeyError     = "pubkey_error"
	BlacklistReasonNilPubKey       = "nil_pubkey"
)

// =============================================================================
// Supplier Nil Pubkey Events (Counter)
// Labels: supplier
// Value: count
// Purpose: Track suppliers with nil public keys (haven't signed first tx)
// This helps identify suppliers that need to sign a transaction before they can be used
// =============================================================================

var SupplierNilPubkeyTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "supplier_nil_pubkey_total",
		Help: "Count of times a supplier was found with nil public key (hasn't signed first tx).",
	},
	[]string{LabelSupplier},
)

// =============================================================================
// Supplier Pubkey Cache Events (Counter)
// Labels: supplier, event
// Value: count
// Purpose: Track cache invalidation and recovery events for supplier pubkeys
// =============================================================================

var SupplierPubkeyCacheEvents = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "supplier_pubkey_cache_events_total",
		Help: "Supplier pubkey cache events: invalidated (on signature failure), recovered (nil -> valid).",
	},
	[]string{LabelSupplier, "event"},
)

// Pubkey cache event constants
const (
	PubkeyCacheEventInvalidated = "invalidated"
	PubkeyCacheEventRecovered   = "recovered"
)

// =============================================================================
// RPC Type Fallback (Counter)
// Labels: domain, supplier, service_id, requested_rpc_type, fallback_rpc_type
// Purpose: Track when suppliers don't support the requested RPC type and fallback is used
// =============================================================================

var RPCTypeFallbackTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "rpc_type_fallback_total",
		Help: "Count of RPC type fallbacks when supplier doesn't support requested RPC type.",
	},
	[]string{LabelDomain, LabelSupplier, LabelServiceID, "requested_rpc_type", "fallback_rpc_type"},
)

// =============================================================================
// Mean Reputation Score (Gauge)
// Labels: domain, service_id, rpc_type
// Value: mean score (0-100)
// Purpose: Track average reputation score per domain/service/rpc_type
// =============================================================================

var ReputationMeanScore = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: MetricPrefix + "reputation_mean_score",
		Help: "Mean reputation score by domain, service_id, and rpc_type.",
	},
	[]string{LabelDomain, LabelServiceID, LabelRPCType},
)

// =============================================================================
// Supplier Exhausted (Counter)
// Labels: supplier, service_id
// Purpose: Track relay-miner-reported "session stake exhausted" / "over-serviced"
// rejections per supplier. These are NOT supplier faults — they mean the app
// has consumed its per-(supplier, session) stake allocation and the supplier
// is correctly refusing further relays. We use this metric to:
//   1. Confirm we are not penalizing reputation for these (operator visibility)
//   2. Surface which suppliers/services frequently saturate, suggesting either
//      app under-staking or supplier over-allocation.
// =============================================================================

var SupplierExhaustedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "supplier_exhausted_total",
		Help: "Relay rejected by supplier's relay-miner because the application's per-session stake allocation is exhausted (over-servicing). NOT counted against reputation.",
	},
	[]string{LabelSupplier, LabelServiceID},
)

// RecordSupplierExhausted increments the per-supplier exhaustion counter.
// Skipped when supplier is empty.
func RecordSupplierExhausted(supplier, serviceID string) {
	if supplier == "" {
		return
	}
	SupplierExhaustedTotal.WithLabelValues(supplier, serviceID).Inc()
}

// =============================================================================
// QoS Filter Rejections (Counter)
// Labels: supplier, service_id, reason
// Purpose: Per-supplier visibility into why QoS dropped an endpoint pre-relay.
// These rejections are silent today — operators can't tell whether their
// endpoint was filtered for being block-behind, missing chain_id, lacking
// archival capability, etc.
// =============================================================================

const (
	QoSFilterReasonBlockHeightLag     = "block_height_lag"
	QoSFilterReasonBlockHeightUnknown = "block_height_unknown"
	QoSFilterReasonChainIDMismatch    = "chain_id_mismatch"
	QoSFilterReasonArchivalRequired   = "archival_required"
	QoSFilterReasonInvalidResponse    = "invalid_response"
	QoSFilterReasonEmptyResponse      = "empty_response"
	QoSFilterReasonCapabilityLimited  = "capability_limitation"
)

var QoSFilterRejectionTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "qos_filter_rejection_total",
		Help: "QoS filter rejections by supplier, service_id, and reason. Reasons: block_height_lag, block_height_unknown, chain_id_mismatch, archival_required, invalid_response, empty_response, capability_limitation.",
	},
	[]string{LabelSupplier, LabelServiceID, "reason"},
)

// RecordQoSFilterRejection counts a per-supplier QoS filter rejection.
// Skipped when supplier is empty (e.g., missing endpoint context) or when
// the cardinality guard has tripped for this metric.
func RecordQoSFilterRejection(supplier, serviceID, reason string) {
	if supplier == "" {
		return
	}
	if !qosFilterRejectionGuard.allow(supplier, serviceID, reason) {
		return
	}
	QoSFilterRejectionTotal.WithLabelValues(supplier, serviceID, reason).Inc()
}

// =============================================================================
// Per-supplier reputation observability (Gauge + Counter)
// Labels: supplier, service_id (+ signal_type on counter)
// Purpose: Give operators of relay miners a metric-level view of why PATH is
// or isn't routing to them. Supplier already implies domain — no domain label.
// Signals are emitted from reputation/service.go::RecordSignal, so any QoS
// code that produces a Signal automatically feeds this counter.
// =============================================================================

var SupplierReputationScore = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: MetricPrefix + "supplier_reputation_score",
		Help: "Per-supplier reputation score (0-100) by supplier, service_id, and rpc_type. Snapshotted every 10s. The rpc_type split keeps a supplier's websocket score from colliding with its json_rpc score (they are tracked and acted on separately).",
	},
	[]string{LabelSupplier, LabelServiceID, LabelRPCType},
)

// Reasons an endpoint is dropped by the reputation filter, used as the `reason`
// label on ReputationDisqualifiedTotal.
const (
	ReputationDisqualifyReasonBelowThreshold = "below_threshold" // score < per-service MinThreshold
	ReputationDisqualifyReasonCooldown       = "cooldown"        // in strike cooldown (critical strikes)
)

// ReputationDisqualifiedTotal counts endpoint exclusions by the reputation filter,
// split by service_id, rpc_type, and reason. This makes S1-style disqualification
// (below-threshold or cooldown) visible on a dashboard instead of requiring a Redis
// read. It counts each drop OCCURRENCE per endpoint-selection pass, not unique
// endpoints — a high rate on a (service_id, rpc_type=websocket) pair means the filter
// is actively steering traffic away from proven-bad endpoints for that stream.
var ReputationDisqualifiedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "reputation_disqualified_total",
		Help: "Endpoints excluded by the reputation filter, by service_id, rpc_type, and reason (below_threshold/cooldown). Counts each drop occurrence per selection pass, not unique endpoints.",
	},
	[]string{LabelServiceID, LabelRPCType, "reason"},
)

// HedgeSelfOperatorAvoidedTotal counts hedge candidates skipped because they
// share the primary endpoint's registrable domain (eTLD+1) — i.e. a different
// subdomain of the SAME operator. This is the live, self-measurable signal for
// the eTLD+1 hedge-exclusion fix: pre-fix these siblings could win the hedge
// against their own operator (no resilience benefit); post-fix each such skip
// increments here. A nonzero rate on a service_id means that service has an
// operator sharding relay miners across subdomains, and the hedge is correctly
// steering to a genuinely different operator. Counts each skipped sibling
// endpoint per hedge selection pass, not unique operators.
var HedgeSelfOperatorAvoidedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "hedge_self_operator_avoided_total",
		Help: "Hedge candidates skipped for sharing the primary's registrable domain (same operator, different subdomain), by service_id. The live signal for the eTLD+1 hedge-exclusion fix.",
	},
	[]string{LabelServiceID},
)

// ConcentrationCapReshapedTotal counts endpoint selections whose distribution the
// per-operator (eTLD+1) concentration cap actually altered — either an operator's share
// exceeded the cap and its excess was water-filled to other operators, or the pool was
// too concentrated to satisfy the cap and selection fell back to uniform-over-operators.
// Selections where no operator exceeded the cap (the cap is a no-op) are NOT counted, so a
// nonzero rate on a service_id is the live signal that the cap is bounding a real
// concentration — the operational proof the shipped-on default is doing something.
var ConcentrationCapReshapedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "concentration_cap_reshaped_total",
		Help: "Endpoint selections reshaped by the per-operator (eTLD+1) concentration cap, by service_id. Nonzero = the cap is actively bounding a dominant operator's selection share.",
	},
	[]string{LabelServiceID},
)

// RecordConcentrationCapReshaped increments the concentration-cap reshape counter for a
// service. Called once per selection whose distribution the cap actually altered.
func RecordConcentrationCapReshaped(serviceID string) {
	ConcentrationCapReshapedTotal.WithLabelValues(serviceID).Inc()
}

// Severity classes for supplier_signal_total. The reputation layer emits 8
// distinct signal-type strings; carrying all 8 as a label multiplies this
// counter's cardinality 8× on top of the (supplier × service_id) base — the
// base already accumulates toward the full network supplier set as sessions
// rotate, so the extra 8× is what pushes the metric into the 25K guard within
// ~15 min of pod start. Collapsing to 3 severity classes cuts the fan to 3×
// while preserving the only distinction this per-supplier view needs: is the
// supplier working, degraded-but-serving, or failing. Full error taxonomy
// (5xx vs timeout vs config) remains available per-domain on relays_total
// (status_code + reputation_signal).
const (
	SupplierSeverityOK    = "ok"    // success, recovery_success
	SupplierSeveritySlow  = "slow"  // slow_response, very_slow_response
	SupplierSeverityError = "error" // minor/major/critical/fatal error
)

var SupplierSignalTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "supplier_signal_total",
		Help: "Reputation signals emitted by supplier and service_id, collapsed to a severity class (ok/slow/error). Full error taxonomy is available per-domain on relays_total.",
	},
	[]string{LabelSupplier, LabelServiceID, LabelSeverity},
)

// supplierSignalSeverity collapses a reputation signal-type string (the 8
// reputation.SignalType wire values) into one of three severity classes.
// Unknown/new signal types fall through to "error" so a mis-added type shows
// up loudly rather than silently vanishing, and cardinality stays bounded.
func supplierSignalSeverity(signalType string) string {
	switch signalType {
	case "success", "recovery_success":
		return SupplierSeverityOK
	case "slow_response", "very_slow_response":
		return SupplierSeveritySlow
	default: // minor_error, major_error, critical_error, fatal_error, unknown
		return SupplierSeverityError
	}
}

// =============================================================================
// Relays (Counter + Histogram)
// Labels: domain, rpc_type, service_id, status_code, reputation_signal, request_type
// Purpose: Track ALL outgoing relays from PATH to supplier endpoints
// Includes: normal user requests, health checks, probation traffic
// =============================================================================

const (
	// --- Request type labels for relays

	RelayTypeNormal      = "normal"
	RelayTypeHealthCheck = "health_check"
	RelayTypeProbation   = "probation"
	// RelayTypeHedge tags relays issued as the hedge branch of a hedge race —
	// a backup attempt to a different endpoint when the primary is slow. These
	// inflate per-domain RPS metrics if grouped with normal traffic, because
	// fast endpoints are picked as hedge again and again. Dashboards filter to
	// request_type="normal" by default to show fair primary-traffic distribution.
	RelayTypeHedge = "hedge"
	// RelayTypeRetry tags relays issued as a sequential retry to a different
	// endpoint after a prior attempt failed. Like hedge, retry overflow otherwise
	// hides inside "normal" and inflates a single endpoint's apparent RPS during
	// upstream degradation — its own label makes the overflow measurable.
	RelayTypeRetry = "retry"
)

var RelaysTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "relays_total",
		Help: "Total outgoing relays to suppliers by domain, rpc_type, service_id, status_code, reputation_signal, and request_type.",
	},
	[]string{LabelDomain, LabelRPCType, LabelServiceID, LabelStatusCode, LabelReputationSignal, "request_type"},
)

var RelayLatency = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    MetricPrefix + "relay_latency_seconds",
		Help:    "Outgoing relay latency in seconds by domain, rpc_type, service_id, status_code, reputation_signal, and request_type.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	},
	[]string{LabelDomain, LabelRPCType, LabelServiceID, LabelStatusCode, LabelReputationSignal, "request_type"},
)

// =============================================================================
// WebSocket Connections (Gauge + Counter + Histogram)
// Labels: domain, service_id for active connections
// Purpose: Track WebSocket connection lifecycle and duration
// =============================================================================

const (
	// --- WebSocket connection event types

	WSEventEstablished = "established"
	WSEventClosed      = "closed"
	WSEventFailed      = "failed"

	// --- WebSocket message direction

	WSDirectionClientToEndpoint = "client_to_endpoint"
	WSDirectionEndpointToClient = "endpoint_to_client"

	// --- WebSocket session-rebind result labels (experimental; see PATH_WEBSOCKET_SESSION_REBIND)
	// The `result` dimension of WebsocketRebindTotal. Split by supplier-continuity and
	// failure stage so canary directly measures how often the original supplier persists
	// across a Shannon session boundary (persistence rate = same / (same + different)) and
	// why a rebind fell back to closing the client.
	//
	// Successes:
	WSRebindSuccessSameSupplier      = "success_same_supplier"      // tier-1: original supplier+URL still in the new session (seamless)
	WSRebindSuccessDifferentSupplier = "success_different_supplier" // tier-2: original supplier rotated out; rebound to best-available endpoint
	// Failures (bridge gave up → client closed with 1012):
	WSRebindFailedNoEndpoints  = "failed_no_endpoints"  // new session had zero usable websocket endpoints
	WSRebindFailedSessionError = "failed_session_error" // could not fetch/build the new session
	WSRebindFailedDial         = "failed_dial"          // endpoint selected but the websocket dial failed after retries
	WSRebindFailedReplay       = "failed_replay"        // reconnected, but building/writing subscription replay frames failed
	WSRebindFailedSelect       = "failed_select"        // generic selection failure with no more specific reason

	// --- WebSocket endpoint-staleness watchdog result labels (experimental)
	// The `result` dimension of WebsocketEndpointStallTotal. A stall is a silent supplier
	// (endpoint transport alive, no subscription data past the staleness threshold) that
	// ping/pong liveness cannot detect.
	WSStallRebind = "rebind" // watchdog forced a rebind onto a different supplier
	WSStallGaveUp = "giveup" // endpoint stayed silent across repeated rebinds; client closed (1012)
)

var WebsocketConnectionsActive = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: MetricPrefix + "websocket_connections_active",
		Help: "Current active WebSocket connections by domain and service_id.",
	},
	[]string{LabelDomain, LabelServiceID},
)

var WebsocketConnectionEventsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "websocket_connection_events_total",
		Help: "WebSocket connection events by domain, service_id, and event type (established/closed/failed).",
	},
	[]string{LabelDomain, LabelServiceID, "event"},
)

var WebsocketConnectionDuration = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name: MetricPrefix + "websocket_connection_duration_seconds",
		Help: "WebSocket connection duration in seconds by domain and service_id.",
		// Sub-second buckets (0.1/0.25/0.5) make immediate supplier resets — a
		// connection that establishes then dies in well under a second — visible
		// and distinguishable from healthy long-lived subscriptions.
		Buckets: []float64{0.1, 0.25, 0.5, 1, 5, 10, 30, 60, 300, 600, 1800, 3600}, // 100ms to 1h
	},
	[]string{LabelDomain, LabelServiceID},
)

var WebsocketMessagesTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "websocket_messages_total",
		Help: "WebSocket messages by domain, service_id, direction (client_to_endpoint/endpoint_to_client), and reputation_signal.",
	},
	[]string{LabelDomain, LabelServiceID, "direction", LabelReputationSignal},
)

// WebsocketRebindTotal counts websocket session-rebind episodes by outcome.
// EXPERIMENTAL / canary observability for the session-rebind feature
// (PATH_WEBSOCKET_SESSION_REBIND). result is one of the WSRebind* labels above:
// success_same_supplier / success_different_supplier on success (client kept open,
// subscriptions replayed), or failed_* when the bridge gave up and closed the client
// (1012). The same/different split measures supplier persistence across Shannon session
// boundaries. Safe to remove once rebind is validated and promoted.
var WebsocketRebindTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "websocket_rebind_total",
		Help: "WebSocket session-rebind episodes by domain, service_id, and result (success_same_supplier/success_different_supplier/failed_*). EXPERIMENTAL.",
	},
	[]string{LabelDomain, LabelServiceID, "result"},
)

// WebsocketSubscriptionsReplayedTotal counts subscriptions replayed onto a
// reconnected endpoint during rebind. EXPERIMENTAL / canary observability.
var WebsocketSubscriptionsReplayedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "websocket_subscriptions_replayed_total",
		Help: "Subscriptions replayed onto a reconnected websocket endpoint during rebind, by domain and service_id. EXPERIMENTAL.",
	},
	[]string{LabelDomain, LabelServiceID},
)

// WebsocketEndpointStallTotal counts endpoint-staleness watchdog firings by outcome.
// EXPERIMENTAL / canary observability for the silent-supplier-stall detector: a supplier
// that keeps answering pings but stops delivering subscription data (invisible to
// ping/pong liveness). result is WSStallRebind (forced a rebind onto a different supplier)
// or WSStallGaveUp (silent across repeated rebinds → client closed with 1012). The
// downstream rebind outcome is captured separately in WebsocketRebindTotal.
var WebsocketEndpointStallTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "websocket_endpoint_stall_total",
		Help: "WebSocket silent-endpoint-stall watchdog firings by domain, service_id, and result (rebind/giveup). EXPERIMENTAL.",
	},
	[]string{LabelDomain, LabelServiceID, "result"},
)

// =============================================================================
// Helper Functions for Recording Metrics
// =============================================================================

// RecordHealthCheck records a health check result per supplier
func RecordHealthCheck(domain, supplier, rpcType, serviceID, healthCheckName, reputationSignal string) {
	HealthCheckStatus.WithLabelValues(domain, supplier, rpcType, serviceID, healthCheckName, reputationSignal).Inc()
}

// RecordObservation records an observation pipeline event
func RecordObservation(domain, rpcType, serviceID, networkType, method, reputationSignal string) {
	ObservationPipeline.WithLabelValues(domain, rpcType, serviceID, networkType, method, reputationSignal).Inc()
}

// RecordLatencyReputation records a latency categorization
func RecordLatencyReputation(domain, rpcType, serviceID, latencySignal string) {
	LatencyReputation.WithLabelValues(domain, rpcType, serviceID, latencySignal).Inc()
}

// RecordRequest records a request with status code and latency
func RecordRequest(domain, rpcType, serviceID, statusCode string, latencySeconds float64) {
	RequestsTotal.WithLabelValues(domain, rpcType, serviceID, statusCode).Inc()
	RequestLatency.WithLabelValues(domain, rpcType, serviceID, statusCode).Observe(latencySeconds)
}

// RecordRetryDistribution records why a retry happened
func RecordRetryDistribution(domain, rpcType, serviceID, reason string) {
	RetriesDistribution.WithLabelValues(domain, rpcType, serviceID, reason).Inc()
}

// RecordRetryResult records a retry outcome with latency
func RecordRetryResult(rpcType, serviceID, retryCount, result string, latencySeconds float64) {
	RetryResultsTotal.WithLabelValues(rpcType, serviceID, retryCount, result).Inc()
	RetryResultsLatency.WithLabelValues(rpcType, serviceID, retryCount, result).Observe(latencySeconds)
}

// =============================================================================
// Hedge Request Metrics (Counter + Histogram)
// Labels: rpc_type, service_id, result
// Purpose: Track hedge request usage, outcomes, and latency benefits
// =============================================================================

var HedgeRequestsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "hedge_requests_total",
		Help: "Total hedge request events by rpc_type, service_id, and result (primary_won/hedge_won/primary_only/both_failed/no_hedge).",
	},
	[]string{LabelRPCType, LabelServiceID, LabelResult},
)

var HedgeLatencySavings = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    MetricPrefix + "hedge_latency_savings_seconds",
		Help:    "Latency saved by hedge requests (primary_latency - winning_latency) when hedge wins. Negative values mean hedge was slower.",
		Buckets: []float64{-1, -0.5, -0.1, 0, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	},
	[]string{LabelRPCType, LabelServiceID},
)

var HedgeWinningLatency = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    MetricPrefix + "hedge_winning_latency_seconds",
		Help:    "Latency of the winning request in a hedge race by result type.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	},
	[]string{LabelRPCType, LabelServiceID, LabelResult},
)

// =============================================================================
// Hedge per-supplier outcome (Counter) + role latency (Histogram)
// Purpose: Answers "am I losing races, and by how much?".
//
// Split into two metrics to bound cardinality. The per-supplier signal is a
// win-rate — a ratio of counts — which needs no buckets, so it lives on a plain
// COUNTER (supplier × role = 2 series/supplier, ~10K series network-wide,
// well under the 25K guard → complete, not first-seen-biased). The "by how
// much" is a latency distribution that does NOT need per-supplier resolution,
// so it lives on a HISTOGRAM keyed by role only (~2×12 series total).
//
// This replaces the earlier single per-supplier HistogramVec, which multiplied
// ~8K supplier×role tuples by ~12 bucket series each = the largest single
// metric family in the gateway (~585K series, ~39% of all gateway cardinality;
// with service_id it was 945K — audit 2026-04-28). Win-rate = winner /
// (winner + loser) on the counter; loser-vs-winner latency gap on the histogram.
// =============================================================================

const (
	HedgeRoleWinner = "winner"
	HedgeRoleLoser  = "loser"
)

// HedgeSupplierOutcomeTotal is the per-supplier win/loss counter. Bounded and
// complete: supplier × {winner,loser} stays well under the guard cap.
var HedgeSupplierOutcomeTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "hedge_supplier_outcome_total",
		Help: "Per-supplier hedge race outcomes. role=winner|loser. Win-rate = winner/(winner+loser).",
	},
	[]string{LabelSupplier, "role"},
)

// HedgeRoleLatency is the hedge outcome latency distribution by role, aggregated
// across all suppliers (no supplier label → tiny, fixed cardinality). Pairs
// with HedgeSupplierOutcomeTotal for the per-supplier win-rate.
var HedgeRoleLatency = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    MetricPrefix + "hedge_role_latency_seconds",
		Help:    "Hedge race outcome latency by role (winner|loser), aggregated across suppliers. Pairs with hedge_supplier_outcome_total for per-supplier win-rate.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	},
	[]string{"role"},
)

// RecordHedgeRequest records a hedge request outcome
// result should be one of: HedgeResultPrimaryWon, HedgeResultHedgeWon, HedgeResultPrimaryOnly, HedgeResultBothFailed, HedgeResultNoHedge
func RecordHedgeRequest(rpcType, serviceID, result string, winningLatencySeconds float64) {
	HedgeRequestsTotal.WithLabelValues(rpcType, serviceID, result).Inc()
	HedgeWinningLatency.WithLabelValues(rpcType, serviceID, result).Observe(winningLatencySeconds)
}

// RecordHedgeLatencySavings records the latency benefit/cost of using hedged requests
// savings = what the primary would have taken - what the winner took
// positive = hedge saved time, negative = hedge was slower (but still won if primary failed)
func RecordHedgeLatencySavings(rpcType, serviceID string, savingsSeconds float64) {
	HedgeLatencySavings.WithLabelValues(rpcType, serviceID).Observe(savingsSeconds)
}

// RecordHedgeSupplierOutcome records a hedge race outcome. role must be
// HedgeRoleWinner or HedgeRoleLoser. The latency distribution is recorded per
// role (no supplier label, always). The per-supplier win/loss count is recorded
// only when supplier is known and the cardinality guard admits it.
func RecordHedgeSupplierOutcome(supplier, role string, latencySeconds float64) {
	// Role-only latency distribution: fixed, tiny cardinality — always recorded.
	HedgeRoleLatency.WithLabelValues(role).Observe(latencySeconds)

	// Per-supplier win/loss counter: 2 series/supplier, still guarded as a
	// backstop against a supplier-address label leak.
	if supplier == "" {
		return
	}
	if !hedgeSupplierGuard.allow(supplier, role) {
		return
	}
	HedgeSupplierOutcomeTotal.WithLabelValues(supplier, role).Inc()
}

// RecordBatchSize records a batch request with latency.
// batchCount is bucketed via batchCountBucket to bound label cardinality —
// max_batch_payloads defaults to 5500, so emitting raw counts would create
// an unbounded `batch_count` label. Buckets: 1, 2-10, 11-50, 51-500, 500+.
func RecordBatchSize(rpcType, serviceID string, batchCount int, latencySeconds float64) {
	bucket := batchCountBucket(batchCount)
	BatchSizeTotal.WithLabelValues(rpcType, serviceID, bucket).Inc()
	BatchSizeLatency.WithLabelValues(rpcType, serviceID, bucket).Observe(latencySeconds)
	// Record for average calculation: avg = items_total / requests_total
	BatchRequestsTotal.WithLabelValues(serviceID).Inc()
	BatchItemsTotal.WithLabelValues(serviceID).Add(float64(batchCount))
}

// batchCountBucket maps a raw batch count to one of five fixed bucket labels.
// Caps cardinality of the batch_count label at 5 values regardless of payload size.
func batchCountBucket(batchCount int) string {
	switch {
	case batchCount <= 1:
		return "1"
	case batchCount <= 10:
		return "2-10"
	case batchCount <= 50:
		return "11-50"
	case batchCount <= 500:
		return "51-500"
	default:
		return "500+"
	}
}

// RecordRequestSize records request and response sizes
func RecordRequestSize(rpcType, serviceID string, bytesReceived, bytesSent int64) {
	RequestBytesReceived.WithLabelValues(rpcType, serviceID).Add(float64(bytesReceived))
	ResponseBytesSent.WithLabelValues(rpcType, serviceID).Add(float64(bytesSent))

	// Also record the per-request size distribution (for p99/max visibility).
	// Only observe real request bodies; skip zero to avoid diluting the histogram
	// with sizeless requests that carry no signal.
	if bytesReceived > 0 {
		RequestBodySizeBytes.WithLabelValues(rpcType, serviceID).Observe(float64(bytesReceived))
	}
}

// RecordProbationEvent records a probation event (entered, exited, or routed)
func RecordProbationEvent(domain, rpcType, serviceID, event string) {
	ProbationEventsTotal.WithLabelValues(domain, rpcType, serviceID, event).Inc()
}

// RecordSupplierBlacklist records a supplier being blacklisted with the specific reason
// reason should be one of the BlacklistReason* constants
func RecordSupplierBlacklist(domain, supplier, serviceID, reason string) {
	SupplierBlacklistTotal.WithLabelValues(domain, supplier, serviceID, reason).Inc()
}

// RecordSupplierNilPubkey records when a supplier is found with a nil public key.
// This indicates the supplier account exists but hasn't signed its first transaction yet.
func RecordSupplierNilPubkey(supplier string) {
	SupplierNilPubkeyTotal.WithLabelValues(supplier).Inc()
}

// RecordSupplierPubkeyCacheInvalidated records when a supplier's pubkey cache entry
// is invalidated due to signature verification failure.
func RecordSupplierPubkeyCacheInvalidated(supplier string) {
	SupplierPubkeyCacheEvents.WithLabelValues(supplier, PubkeyCacheEventInvalidated).Inc()
}

// RecordSupplierPubkeyRecovered records when a supplier's pubkey transitions
// from nil to valid (they signed their first transaction).
func RecordSupplierPubkeyRecovered(supplier string) {
	SupplierPubkeyCacheEvents.WithLabelValues(supplier, PubkeyCacheEventRecovered).Inc()
}

// RecordRPCTypeFallback records when a supplier doesn't support the requested RPC type
// and a fallback RPC type is used instead
func RecordRPCTypeFallback(domain, supplier, serviceID, requestedRPCType, fallbackRPCType string) {
	RPCTypeFallbackTotal.WithLabelValues(domain, supplier, serviceID, requestedRPCType, fallbackRPCType).Inc()
}

// SetMeanScore sets the mean reputation score for a domain/service/rpc_type combination
func SetMeanScore(domain, serviceID, rpcType string, score float64) {
	ReputationMeanScore.WithLabelValues(domain, serviceID, rpcType).Set(score)
}

// SetSupplierReputationScore sets the per-(supplier, service_id, rpc_type) reputation gauge.
// Skipped silently when supplier is empty (e.g., per-domain reputation key)
// or when the cardinality guard has tripped for this metric.
func SetSupplierReputationScore(supplier, serviceID, rpcType string, score float64) {
	if supplier == "" {
		return
	}
	if !supplierReputationGuard.allow(supplier, serviceID) {
		return
	}
	SupplierReputationScore.WithLabelValues(supplier, serviceID, rpcType).Set(score)
}

// RecordReputationDisqualified increments the reputation-filter disqualification
// counter for one dropped endpoint. reason is one of the
// ReputationDisqualifyReason* constants.
func RecordReputationDisqualified(serviceID, rpcType, reason string) {
	ReputationDisqualifiedTotal.WithLabelValues(serviceID, rpcType, reason).Inc()
}

// RecordHedgeSelfOperatorAvoided increments the counter for one hedge candidate
// skipped because it shares the primary endpoint's registrable domain (same
// operator, different subdomain). See HedgeSelfOperatorAvoidedTotal.
func RecordHedgeSelfOperatorAvoided(serviceID string) {
	HedgeSelfOperatorAvoidedTotal.WithLabelValues(serviceID).Inc()
}

// RecordSupplierSignal increments the per-supplier signal counter, collapsing
// the reputation signal type to a severity class (ok/slow/error) to bound
// cardinality. Skipped silently when supplier is empty (e.g., per-domain
// reputation key) or when the cardinality guard has tripped for this metric.
func RecordSupplierSignal(supplier, serviceID, signalType string) {
	if supplier == "" {
		return
	}
	severity := supplierSignalSeverity(signalType)
	if !supplierSignalGuard.allow(supplier, serviceID, severity) {
		return
	}
	SupplierSignalTotal.WithLabelValues(supplier, serviceID, severity).Inc()
}

// RecordRelay records an outgoing relay to a supplier endpoint with latency
// relayType should be one of: RelayTypeNormal, RelayTypeHealthCheck, RelayTypeProbation
// statusCode should be the HTTP status code category (2xx, 4xx, 5xx, etc.)
// reputationSignal should be the signal recorded (ok, minor_error, major_error, etc.)
func RecordRelay(domain, rpcType, serviceID, statusCode, reputationSignal, relayType string, latencySeconds float64) {
	RelaysTotal.WithLabelValues(domain, rpcType, serviceID, statusCode, reputationSignal, relayType).Inc()
	RelayLatency.WithLabelValues(domain, rpcType, serviceID, statusCode, reputationSignal, relayType).Observe(latencySeconds)
}

// WebSocket Connection Metrics Helpers

// RecordWebsocketConnectionEstablished records a successful WebSocket connection establishment
// and increments the active connection count
func RecordWebsocketConnectionEstablished(domain, serviceID string) {
	WebsocketConnectionsActive.WithLabelValues(domain, serviceID).Inc()
	WebsocketConnectionEventsTotal.WithLabelValues(domain, serviceID, WSEventEstablished).Inc()
}

// RecordWebsocketConnectionClosed records a WebSocket connection closure
// and decrements the active connection count, recording the duration
func RecordWebsocketConnectionClosed(domain, serviceID string, durationSeconds float64) {
	WebsocketConnectionsActive.WithLabelValues(domain, serviceID).Dec()
	WebsocketConnectionEventsTotal.WithLabelValues(domain, serviceID, WSEventClosed).Inc()
	WebsocketConnectionDuration.WithLabelValues(domain, serviceID).Observe(durationSeconds)
}

// RecordWebsocketConnectionFailed records a WebSocket connection failure
func RecordWebsocketConnectionFailed(domain, serviceID string) {
	WebsocketConnectionEventsTotal.WithLabelValues(domain, serviceID, WSEventFailed).Inc()
}

// RecordWebsocketMessage records a WebSocket message
// direction should be WSDirectionClientToEndpoint or WSDirectionEndpointToClient
func RecordWebsocketMessage(domain, serviceID, direction, reputationSignal string) {
	WebsocketMessagesTotal.WithLabelValues(domain, serviceID, direction, reputationSignal).Inc()
}

// RecordWebsocketRebind records a websocket session-rebind episode outcome and, on
// success, the number of subscriptions replayed. EXPERIMENTAL / canary observability
// for the session-rebind feature. result should be one of the WSRebind* labels.
func RecordWebsocketRebind(domain, serviceID, result string, replayedSubscriptions int) {
	WebsocketRebindTotal.WithLabelValues(domain, serviceID, result).Inc()
	if replayedSubscriptions > 0 {
		WebsocketSubscriptionsReplayedTotal.WithLabelValues(domain, serviceID).Add(float64(replayedSubscriptions))
	}
}

// RecordWebsocketEndpointStall records a staleness-watchdog firing. gaveUp distinguishes a
// forced rebind (false) from giving up and closing the client (true).
func RecordWebsocketEndpointStall(domain, serviceID string, gaveUp bool) {
	result := WSStallRebind
	if gaveUp {
		result = WSStallGaveUp
	}
	WebsocketEndpointStallTotal.WithLabelValues(domain, serviceID, result).Inc()
}

// LatencyThresholds defines thresholds for latency signal categorization.
// These should be derived from per-service LatencyConfig.
type LatencyThresholds struct {
	FastMs   float64 // <= this = Cheetah
	NormalMs float64 // <= this = Gazelle
	SlowMs   float64 // <= this = Rabbit
	SevereMs float64 // <= this = Turtle
}

// DefaultLatencyThresholds returns default thresholds when no per-service config is available.
func DefaultLatencyThresholds() *LatencyThresholds {
	return &LatencyThresholds{
		FastMs:   100,
		NormalMs: 500,
		SlowMs:   1000,
		SevereMs: 3000,
	}
}

// GetLatencySignal converts latency in milliseconds to a latency signal category.
// Uses default fixed thresholds. Use GetLatencySignalWithThresholds for per-service thresholds.
func GetLatencySignal(latencyMs float64) string {
	return GetLatencySignalWithThresholds(latencyMs, nil)
}

// GetLatencySignalWithThresholds converts latency to a signal based on provided thresholds.
// If thresholds are nil, use default fixed thresholds.
func GetLatencySignalWithThresholds(latencyMs float64, thresholds *LatencyThresholds) string {
	if thresholds == nil {
		thresholds = DefaultLatencyThresholds()
	}

	switch {
	case latencyMs <= thresholds.FastMs:
		return LatencySignalCheetah
	case latencyMs <= thresholds.NormalMs:
		return LatencySignalGazelle
	case latencyMs <= thresholds.SlowMs:
		return LatencySignalRabbit
	case latencyMs <= thresholds.SevereMs:
		return LatencySignalTurtle
	default:
		return LatencySignalSnail
	}
}

// GetStatusCodeCategory returns the status code as a string, grouping 4xx and 5xx
func GetStatusCodeCategory(statusCode int) string {
	switch {
	case statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices:
		return "200"
	case statusCode >= http.StatusBadRequest && statusCode < http.StatusInternalServerError:
		return "4xx"
	case statusCode >= http.StatusInternalServerError:
		return "5xx"
	default:
		return "other"
	}
}
