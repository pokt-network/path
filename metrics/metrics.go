// Package metrics provides Prometheus metrics for PATH gateway observability.
// These metrics are designed based on how_metrics_should_work.md to provide
// domain-centric, actionable insights into gateway performance.
package metrics

import (
	"net/http"
	"strconv"
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
		Name:    MetricPrefix + "websocket_connection_duration_seconds",
		Help:    "WebSocket connection duration in seconds by domain and service_id.",
		Buckets: []float64{1, 5, 10, 30, 60, 300, 600, 1800, 3600}, // 1s to 1h
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

// RecordBatchSize records a batch request with latency
func RecordBatchSize(rpcType, serviceID string, batchCount int, latencySeconds float64) {
	batchCountStr := strconv.Itoa(batchCount)
	BatchSizeTotal.WithLabelValues(rpcType, serviceID, batchCountStr).Inc()
	BatchSizeLatency.WithLabelValues(rpcType, serviceID, batchCountStr).Observe(latencySeconds)
	// Record for average calculation: avg = items_total / requests_total
	BatchRequestsTotal.WithLabelValues(serviceID).Inc()
	BatchItemsTotal.WithLabelValues(serviceID).Add(float64(batchCount))
}

// RecordRequestSize records request and response sizes
func RecordRequestSize(rpcType, serviceID string, bytesReceived, bytesSent int64) {
	RequestBytesReceived.WithLabelValues(rpcType, serviceID).Add(float64(bytesReceived))
	ResponseBytesSent.WithLabelValues(rpcType, serviceID).Add(float64(bytesSent))
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
