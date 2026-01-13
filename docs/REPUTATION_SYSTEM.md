# PATH Unified QoS: Reputation System

This document provides comprehensive documentation of the PATH reputation system, covering the architecture before and after the unified QoS implementation.

## Table of Contents

- [Executive Summary](#executive-summary)
- [Architecture Overview](#architecture-overview)
- [Before: The Old System](#before-the-old-system)
- [After: The Unified QoS System](#after-the-unified-qos-system)
- [Core Components](#core-components)
  - [Reputation Scoring](#reputation-scoring)
  - [Signal Types](#signal-types)
  - [Tiered Selection](#tiered-selection)
  - [Probation System](#probation-system)
  - [Latency-Aware Scoring](#latency-aware-scoring)
  - [Health Checks](#health-checks)
  - [Observation Pipeline](#observation-pipeline)
- [Metrics & Observability](#metrics--observability)
- [Configuration Reference](#configuration-reference)
- [Recovery Mechanisms](#recovery-mechanisms)
- [Implementation Details](#implementation-details)

---

## Executive Summary

The PATH Unified QoS system transforms how endpoint quality is managed:

| Aspect | Before | After |
|--------|--------|-------|
| **Quality Tracking** | Separate hydrator + sanctions | Unified reputation scoring |
| **Health Checks** | Hardcoded per-chain logic | Configurable YAML-based checks |
| **Endpoint Recovery** | Permanent sanctions (never recover) | Score-based with recovery mechanisms |
| **Observability** | Limited metrics | Full Prometheus metrics suite |
| **Configuration** | Code changes required | YAML configuration + external URLs |
| **Multi-Instance** | No coordination | Redis storage + leader election |

---

## Architecture Overview

```
                    ┌─────────────────────────────────────────────────────────────┐
                    │                      PATH Gateway                            │
                    │                                                              │
  User Request ────▶│  ┌──────────────────┐    ┌──────────────────────────────┐  │
                    │  │   Gateway Layer  │    │      Reputation System       │  │
                    │  │                  │    │                              │  │
                    │  │  • Parse Request │───▶│  • Score Lookup (<1μs)       │  │
                    │  │  • Select QoS    │    │  • Filter by Threshold       │  │
                    │  │  • Route Traffic │◀───│  • Tiered Selection          │  │
                    │  └──────────────────┘    │  • Probation Sampling        │  │
                    │           │              └──────────────────────────────┘  │
                    │           ▼                          ▲                      │
                    │  ┌──────────────────┐               │                      │
                    │  │  Protocol Layer  │               │                      │
                    │  │                  │               │                      │
                    │  │  • Build Context │    ┌──────────┴───────────────────┐  │
                    │  │  • Send Relay    │───▶│      Signal Recording        │  │
                    │  │  • Get Response  │    │                              │  │
                    │  └──────────────────┘    │  • Success: +1               │  │
                    │           │              │  • Minor Error: -3           │  │
                    │           ▼              │  • Major Error: -10          │  │
  User Response ◀───│  ┌──────────────────┐    │  • Critical Error: -25       │  │
                    │  │   QoS Layer      │    │  • Fatal Error: -50          │  │
                    │  │                  │    │  • Recovery Success: +15     │  │
                    │  │  • Validate Resp │───▶│  • Latency Penalties         │  │
                    │  │  • Extract Data  │    └──────────────────────────────┘  │
                    │  └──────────────────┘                                      │
                    │                                                              │
                    │  ┌──────────────────────────────────────────────────────┐  │
                    │  │              Health Check Executor                    │  │
                    │  │                                                       │  │
                    │  │  • Runs via Protocol Layer (synthetic relays)         │  │
                    │  │  • Configurable per-service checks                    │  │
                    │  │  • Records signals to reputation system               │  │
                    │  │  • External + Local configuration                     │  │
                    │  └──────────────────────────────────────────────────────┘  │
                    └─────────────────────────────────────────────────────────────┘
```

---

## Before: The Old System

### The Hydrator (Legacy)

The old system used a **Hydrator** component that:

1. **Ran Direct HTTP Calls** - Health checks bypassed the protocol layer
2. **Hardcoded Logic** - Each chain type (EVM, Cosmos, Solana) had hardcoded checks in Go
3. **Permanent Sanctions** - Failed endpoints were permanently banned
4. **No Recovery** - Once sanctioned, an endpoint could never serve traffic again
5. **Limited Visibility** - Minimal metrics, hard to debug

```
OLD ARCHITECTURE:

┌──────────────────────────────────────────────────────┐
│                     Hydrator                          │
│                                                       │
│  ┌─────────────────┐    ┌─────────────────────────┐  │
│  │  Direct HTTP    │───▶│  Hardcoded Checks        │  │
│  │  Client         │    │                          │  │
│  │                 │    │  • EVM: eth_blockNumber  │  │
│  │  (bypasses      │    │  • EVM: archival check   │  │
│  │   protocol)     │    │  • Solana: getHealth     │  │
│  └─────────────────┘    │  • Cosmos: status        │  │
│          │              └─────────────────────────┘  │
│          ▼                                           │
│  ┌─────────────────────────────────────────────────┐ │
│  │          Sanction Store                          │ │
│  │                                                  │ │
│  │  • SanctionEndpoint(addr, PERMANENT)            │ │
│  │  • isSanctioned(addr) → true (forever)          │ │
│  │  • NO recovery mechanism                         │ │
│  └─────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────┘
```

### Problems with the Old System

| Problem | Impact |
|---------|--------|
| **Permanent sanctions** | Good endpoints that had temporary issues were banned forever |
| **No configurability** | Adding new health checks required code changes |
| **Direct HTTP calls** | Didn't test the full relay path (relay miners not tested) |
| **Per-chain hardcoding** | Each chain type needed separate Go implementation |
| **No tiered selection** | All endpoints treated equally regardless of quality |
| **Limited metrics** | Hard to diagnose why endpoints were excluded |
| **Single instance only** | No support for multi-instance deployments |

### Old Code Files (Removed)

```
protocol/shannon/sanction.go                    # Permanent sanction logic
protocol/shannon/sanctioned_endpoints_store.go  # In-memory sanction storage
protocol/shannon/sanctioned_endpoints_store_test.go
qos/evm/hydrator.go                             # EVM-specific health checks
qos/cosmos/hydrator.go                          # Cosmos-specific health checks
qos/solana/hydrator.go                          # Solana-specific health checks
```

---

## After: The Unified QoS System

### Key Improvements

1. **Reputation-Based Scoring** - Numeric scores (0-100) instead of binary sanctions
2. **Recovery Mechanisms** - Endpoints can recover via probation, health checks, or time
3. **YAML Configuration** - Health checks defined in config, not code
4. **Protocol-Based Checks** - Health checks go through the relay path (tests relay miners)
5. **Rich Metrics** - Full Prometheus metrics for all components
6. **Multi-Instance Support** - Redis storage and leader election for coordination

```
NEW ARCHITECTURE:

┌───────────────────────────────────────────────────────────────────────────┐
│                       Unified Reputation System                            │
│                                                                            │
│  ┌────────────────────────────────────────────────────────────────────┐   │
│  │                    Reputation Service                               │   │
│  │                                                                     │   │
│  │  Score: 0-100 (not binary sanctioned/not-sanctioned)               │   │
│  │                                                                     │   │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐                │   │
│  │  │  Tier 1     │  │  Tier 2     │  │  Tier 3     │  ┌──────────┐  │   │
│  │  │  Score ≥70  │  │  Score ≥50  │  │  Score ≥30  │  │ Probation │  │   │
│  │  │  (Primary)  │  │  (Backup)   │  │  (Last Res) │  │ Score <30 │  │   │
│  │  └─────────────┘  └─────────────┘  └─────────────┘  └──────────┘  │   │
│  │                                                                     │   │
│  │  Recovery Mechanisms:                                              │   │
│  │  ├─ Probation: 10% traffic sampling → recovery_success (+15)      │   │
│  │  ├─ Health Checks: Synthetic relays → success (+1)                │   │
│  │  └─ Time-based: RecoveryTimeout (when no signal-based recovery)   │   │
│  └────────────────────────────────────────────────────────────────────┘   │
│                                                                            │
│  ┌────────────────────────────────────────────────────────────────────┐   │
│  │                  Health Check Executor                              │   │
│  │                                                                     │   │
│  │  • YAML-configured checks (not hardcoded)                          │   │
│  │  • Runs through Protocol layer (tests full relay path)             │   │
│  │  • External URL support for centralized check definitions          │   │
│  │  • Per-service check intervals and configurations                  │   │
│  │  • Leader election for multi-instance deployments                  │   │
│  └────────────────────────────────────────────────────────────────────┘   │
│                                                                            │
│  ┌────────────────────────────────────────────────────────────────────┐   │
│  │                  Observation Pipeline                               │   │
│  │                                                                     │   │
│  │  • Async processing (non-blocking response path)                   │   │
│  │  • Deep response parsing (block height, chain ID)                  │   │
│  │  • Health check observations always processed                      │   │
│  │  • User requests sampled at configurable rate                      │   │
│  └────────────────────────────────────────────────────────────────────┘   │
└───────────────────────────────────────────────────────────────────────────┘
```

---

## Core Components

### Reputation Scoring

The reputation system maintains a score (0-100) for each endpoint:

```go
// Score represents an endpoint's reputation score at a point in time.
type Score struct {
    Value        float64       // Current score (0-100)
    LastUpdated  time.Time     // When the score was last modified
    SuccessCount int64         // Total successful requests
    ErrorCount   int64         // Total failed requests
    LatencyMetrics LatencyMetrics // Response latency statistics
}
```

**Score Lifecycle:**

```
New Endpoint → InitialScore (80)
     │
     ▼
┌─────────────────────────────────────────────┐
│            Score Changes                     │
│                                              │
│  Success:        +1  (fast: +2, slow: +0.5) │
│  Minor Error:    -3                         │
│  Major Error:    -10                        │
│  Critical Error: -25                        │
│  Fatal Error:    -50                        │
│  Recovery:       +15                        │
└─────────────────────────────────────────────┘
     │
     ▼
Score < MinThreshold (30)?
     │
     ├─ No → Continue serving traffic
     │
     └─ Yes → Enter Probation
              │
              ▼
         10% traffic sampling
              │
              ├─ Success → RecoverySuccess (+15)
              │            Climb back above threshold
              │
              └─ Failure → Stay in probation
                          Continue sampling
```

### Signal Types

Signals are events that affect an endpoint's reputation:

| Signal Type | Score Impact | When Generated |
|-------------|--------------|----------------|
| `success` | +1 | Successful request/response |
| `minor_error` | -3 | Validation issues, unknown errors |
| `major_error` | -10 | Timeout, connection issues |
| `critical_error` | -25 | HTTP 5xx, transport errors |
| `fatal_error` | -50 | Service misconfiguration |
| `recovery_success` | +15 | Successful request from probation/health check |
| `slow_response` | -1 | Response > PenaltyThreshold (2s) |
| `very_slow_response` | -3 | Response > SevereThreshold (5s) |

### Tiered Selection

Endpoints are grouped into tiers based on their scores:

```
┌─────────────────────────────────────────────────────────────────┐
│                    Endpoint Selection Flow                       │
│                                                                  │
│  Session Endpoints                                               │
│  ┌─────────────────────────────────────────────────────────────┐│
│  │ Endpoint A: Score 85  ┐                                     ││
│  │ Endpoint B: Score 72  ┼─ Tier 1 (≥70) → Selected First     ││
│  │ Endpoint C: Score 75  ┘                                     ││
│  │ Endpoint D: Score 55  ┐                                     ││
│  │ Endpoint E: Score 51  ┼─ Tier 2 (≥50) → Fallback           ││
│  │ Endpoint F: Score 35  ─ Tier 3 (≥30) → Last Resort          ││
│  │ Endpoint G: Score 15  ─ Probation (<30) → 10% sampling      ││
│  │ Endpoint H: Score 5   ─ Probation (<30) → 10% sampling      ││
│  └─────────────────────────────────────────────────────────────┘│
│                                                                  │
│  Selection Priority:                                             │
│  1. Try Tier 1 endpoints (by lowest latency)                    │
│  2. If none available, try Tier 2                               │
│  3. If none available, try Tier 3                               │
│  4. 10% of requests sample from Probation pool                  │
└─────────────────────────────────────────────────────────────────┘
```

### Probation System

The probation system enables endpoint recovery:

```yaml
probation:
  enabled: true
  threshold: 30           # Score below which endpoints enter probation
  traffic_percent: 10     # Percentage of traffic to sample
  recovery_multiplier: 2.0 # Multiplier for recovery success signals
```

**How Probation Works:**

1. Endpoint score drops below threshold (30)
2. Endpoint enters probation pool
3. 10% of requests are randomly routed to probation endpoints
4. Successful response → `recovery_success` signal (+15)
5. After several successes, endpoint climbs above threshold
6. Endpoint exits probation, rejoins normal selection

### Latency-Aware Scoring

Response latency affects reputation scoring:

```
Response Time Analysis:
────────────────────────────────────────────────────────────────────

0ms        100ms       500ms       1000ms      2000ms      5000ms
 │          │           │           │           │           │
 │ FAST     │  NORMAL   │   SLOW    │  PENALTY  │  SEVERE   │
 │ Bonus    │  Standard │  Reduced  │  Signal   │  Signal   │
 │ +2       │  +1       │  +0.5     │  -1       │  -3       │
 ▼          ▼           ▼           ▼           ▼           ▼

Fast responses (< 100ms):
  - Success impact: +1 × FastBonus (2.0) = +2

Normal responses (100-500ms):
  - Success impact: +1 (standard)

Slow responses (500-1000ms):
  - Success impact: +1 × SlowPenalty (0.5) = +0.5

Penalty responses (1000-2000ms):
  - Success impact: +1 × VerySlowPenalty (0.0) = 0
  - Additional: slow_response signal (-1)

Severe responses (> 2000ms):
  - Success impact: 0
  - Additional: very_slow_response signal (-3)
```

**Latency Profiles for Different Service Types:**

| Profile | Fast | Normal | Slow | Penalty | Severe |
|---------|------|--------|------|---------|--------|
| EVM | 50ms | 200ms | 500ms | 1000ms | 3000ms |
| Cosmos | 100ms | 500ms | 1000ms | 2000ms | 5000ms |
| Solana | 100ms | 300ms | 800ms | 1500ms | 4000ms |
| LLM | 2s | 10s | 30s | 60s | 120s |
| Generic | 500ms | 2s | 5s | 10s | 30s |

### Health Checks

Health checks are configurable probes that test endpoint health:

#### Configuration (YAML)

```yaml
active_health_checks:
  enabled: true

  # External configuration (fetched from URL)
  external:
    url: "https://example.com/health-checks.yaml"
    refresh_interval: "1h"
    timeout: "30s"

  # Local configuration (overrides external)
  local:
    - service_id: eth
      check_interval: "30s"
      enabled: true
      checks:
        - name: "eth_blockNumber"
          type: "jsonrpc"
          method: "POST"
          path: "/"
          headers:
            Content-Type: "application/json"
          body: '{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber"}'
          expected_status_code: 200
          timeout: "5s"
          reputation_signal: "minor_error"  # Signal on failure
```

#### Supported Check Types

| Type | Method | Use Case |
|------|--------|----------|
| `jsonrpc` | HTTP POST | JSON-RPC endpoints (EVM, Cosmos CometBFT) |
| `rest` | HTTP GET/POST | REST API endpoints (Cosmos LCD) |
| `websocket` | WebSocket | WebSocket connectivity tests |
| `grpc` | gRPC | gRPC endpoints (future) |

#### Health Check Flow

```
Health Check Execution:
──────────────────────────────────────────────────────────────────

1. Gateway (Leader) initiates health check cycle
     │
     ▼
2. For each service with health checks configured:
     │
     ├─ Get all session endpoints for service
     │
     └─ For each endpoint:
          │
          ▼
3. Build Protocol Request Context
   (Same as user requests - tests full relay path)
     │
     ▼
4. Send synthetic relay through protocol:
   Protocol.HandleServiceRequest(healthCheckPayload)
     │
     ├─ Request goes through relay miner
     │
     └─ Response returns through relay miner
     │
     ▼
5. Validate response:
   ├─ Check status code (expected_status_code)
   │
   └─ Check response body (expected_response_contains)
     │
     ├─ PASS → Record success signal (+1)
     │
     └─ FAIL → Record configured signal (minor/major/critical)
     │
     ▼
6. Publish observations to metrics
```

### Observation Pipeline

The observation pipeline enables async processing of request/response data:

```
Observation Pipeline Flow:
──────────────────────────────────────────────────────────────────

User Request
     │
     ▼
┌─────────────────────────────────────────────────────────────┐
│                    Request Processing                        │
│                                                              │
│  1. Gateway receives request                                 │
│  2. QoS parses and validates                                │
│  3. Protocol sends relay                                    │
│  4. Response received                                       │
│  5. Response returned to user immediately (non-blocking)    │
└─────────────────────────────────────────────────────────────┘
     │
     ├───────────────────┐
     │                   │
     ▼                   ▼
┌──────────────┐   ┌─────────────────────────────────────────┐
│ User gets    │   │       Observation Queue (Async)          │
│ response     │   │                                          │
│ immediately  │   │  Submit observation for deep parsing:    │
└──────────────┘   │  • Block height extraction               │
                   │  • Chain ID validation                   │
                   │  • Error classification                  │
                   │  • Additional reputation signals         │
                   │                                          │
                   │  Worker Pool (configurable):             │
                   │  • 4 workers (default)                   │
                   │  • 1000 queue size (default)             │
                   │  • 10% sample rate for user requests     │
                   │  • 100% for health checks                │
                   └─────────────────────────────────────────┘
```

---

## Metrics & Observability

### Reputation Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `shannon_reputation_signals_total` | Counter | service_id, signal_type, endpoint_type, endpoint_domain | Total reputation signals recorded |
| `shannon_reputation_endpoints_filtered_total` | Counter | service_id, action, endpoint_domain | Endpoints filtered/allowed by reputation |
| `shannon_reputation_score_distribution` | Histogram | service_id | Distribution of endpoint reputation scores |
| `shannon_reputation_errors_total` | Counter | operation, error_type | Errors in the reputation system itself |

### Probation Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `shannon_probation_endpoints` | Gauge | service_id | Current number of endpoints in probation |
| `shannon_probation_transitions_total` | Counter | service_id, endpoint_domain, transition | Probation state transitions |
| `shannon_probation_traffic_routed_total` | Counter | service_id, endpoint_domain, success | Traffic routed to probation endpoints |

### Health Check Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `shannon_health_check_total` | Counter | service_id, endpoint_domain, check_name, check_type, success, error_type | Health check results |
| `shannon_health_check_duration_seconds` | Histogram | service_id, endpoint_domain, check_name, check_type | Health check execution time |
| `shannon_health_check_cycles_total` | Counter | - | Total health check cycles completed |

### Session Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `shannon_active_sessions` | Gauge | service_id | Current number of active sessions |
| `shannon_session_endpoints` | Gauge | service_id, endpoint_domain | Endpoints per service by domain |
| `shannon_session_refreshes_total` | Counter | service_id, status | Session refresh events |
| `shannon_session_rollovers_total` | Counter | service_id, used_fallback | Session rollover events |

### Example Prometheus Queries

```promql
# Error rate by endpoint domain
rate(shannon_reputation_signals_total{signal_type=~".*_error"}[5m])
by (endpoint_domain)

# Endpoints currently in probation
shannon_probation_endpoints

# Health check success rate
rate(shannon_health_check_total{success="true"}[5m])
/
rate(shannon_health_check_total[5m])

# Score distribution (median)
histogram_quantile(0.5, shannon_reputation_score_distribution_bucket)
```

---

## Configuration Reference

### Reputation Configuration

```yaml
reputation_config:
  # Enable/disable (always enabled in unified system)
  enabled: true

  # Storage backend: "memory" or "redis"
  storage_type: "memory"

  # Starting score for new endpoints (0-100)
  initial_score: 80

  # Minimum score for endpoint selection
  min_threshold: 30

  # Time-based recovery (ignored when probation/health_checks enabled)
  recovery_timeout: 5m

  # Latency configuration
  latency:
    enabled: true
    fast_threshold: 100ms
    normal_threshold: 500ms
    slow_threshold: 1000ms
    penalty_threshold: 2000ms
    severe_threshold: 5000ms
    fast_bonus: 2.0
    slow_penalty: 0.5
    very_slow_penalty: 0.0

  # Tiered selection
  tiered_selection:
    enabled: true
    tier1_threshold: 70
    tier2_threshold: 50
    probation:
      enabled: true
      threshold: 30
      traffic_percent: 10
      recovery_multiplier: 2.0
```

### Health Check Configuration

```yaml
active_health_checks:
  enabled: true

  # Leader election (for multi-instance)
  coordination:
    type: "leader_election"
    lease_duration: "15s"
    renew_interval: "5s"
    key: "path:health:leader"

  # External config URL
  external:
    url: "https://example.com/health-checks.yaml"
    refresh_interval: "1h"
    timeout: "30s"

  # Local config (overrides external)
  local:
    - service_id: eth
      check_interval: "30s"
      enabled: true
      checks:
        - name: "eth_blockNumber"
          type: "jsonrpc"
          method: "POST"
          path: "/"
          body: '{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber"}'
          expected_status_code: 200
          timeout: "5s"
          reputation_signal: "minor_error"
```

---

## Recovery Mechanisms

The system provides three recovery mechanisms (in order of precedence):

### 1. Probation-Based Recovery (Highest Priority)

```
When: probation.enabled = true
How: 10% of traffic sampled to probation endpoints
Signal: recovery_success (+15) on success
Result: Fast recovery through successful production traffic
```

### 2. Health Check Recovery

```
When: active_health_checks.enabled = true
How: Periodic synthetic relays to all endpoints
Signal: success (+1) on passing health check
Result: Gradual recovery through background probes
```

### 3. Time-Based Recovery (Lowest Priority)

```
When: Neither probation nor health_checks enabled
How: After recovery_timeout (5m) with no signals
Action: Score reset to initial_score
Result: Automatic recovery after cooling off period
```

**Recovery Conflict Resolution:**

When multiple recovery mechanisms are configured:

```yaml
# This configuration has a conflict:
reputation_config:
  recovery_timeout: 5m  # Time-based recovery
  tiered_selection:
    probation:
      enabled: true     # Signal-based recovery

# Resolution: recovery_timeout is IGNORED
# Signal-based recovery takes precedence
# Set recovery_timeout: 0 to suppress warning
```

---

## Implementation Details

### Key Files

| File | Purpose |
|------|---------|
| `reputation/reputation.go` | Core types, config, interfaces |
| `reputation/service.go` | ReputationService implementation |
| `reputation/signals.go` | Signal types and score impacts |
| `gateway/health_check_executor.go` | Health check execution |
| `gateway/health_check_config.go` | Health check configuration types |
| `gateway/health_check_defaults.go` | Default values |
| `gateway/health_check_leader.go` | Leader election for multi-instance |
| `gateway/observation_queue.go` | Async observation processing |
| `metrics/reputation/metrics.go` | Prometheus metrics |
| `metrics/session/metrics.go` | Session metrics |
| `metrics/healthcheck/metrics.go` | Health check metrics |
| `protocol/shannon/observation.go` | Protocol-level observation recording |

### Performance Characteristics

| Operation | Latency | Notes |
|-----------|---------|-------|
| Score lookup | <1μs | In-memory cache |
| Score update | <1μs | Cache update + async write |
| FilterByScore | <10μs | In-memory iteration |
| Health check | 1-5s | Network round-trip through relay |
| Observation processing | Async | Non-blocking, background workers |

### Thread Safety

All reputation operations are thread-safe:

- `sync.RWMutex` for cache access
- Buffered channels for async writes
- Leader election prevents duplicate health checks

---

## Migration Guide

### From Old Hydrator/Sanction System

1. **Remove sanction-related code** (already done in this PR)
2. **Configure reputation system** in YAML
3. **Define health checks** in config (not code)
4. **Enable probation** for recovery
5. **Set up metrics dashboards**

### Recommended Production Configuration

```yaml
gateway_config:
  reputation_config:
    enabled: true
    storage_type: "redis"  # For multi-instance
    initial_score: 80
    min_threshold: 30
    recovery_timeout: 0    # Disabled (using probation)
    latency:
      enabled: true
    tiered_selection:
      enabled: true
      tier1_threshold: 70
      tier2_threshold: 50
      probation:
        enabled: true
        threshold: 30
        traffic_percent: 10
        recovery_multiplier: 2.0

  active_health_checks:
    enabled: true
    coordination:
      type: "leader_election"
      lease_duration: "15s"
      renew_interval: "5s"
    external:
      url: "https://your-org/health-checks.yaml"
      refresh_interval: "1h"

  observation_pipeline:
    enabled: true
    sample_rate: 0.1
    worker_count: 4
    queue_size: 1000
```

---

## Summary: Before vs After

| Feature | Before (Hydrator + Sanctions) | After (Unified QoS) |
|---------|-------------------------------|---------------------|
| Endpoint State | Binary (sanctioned/not) | Numeric score (0-100) |
| Recovery | Never (permanent ban) | Multiple mechanisms |
| Health Checks | Hardcoded Go code | YAML configuration |
| Check Execution | Direct HTTP | Through Protocol (relay) |
| Multi-Instance | Not supported | Redis + leader election |
| Metrics | Limited | Full Prometheus suite |
| Observability | Minimal | Rich labels and dashboards |
| Configuration | Code changes | YAML + external URL |
| Latency Awareness | None | Integrated scoring |
| Tiered Selection | None | 3 tiers + probation |

The unified QoS system transforms PATH from a simple relay gateway into an intelligent, self-healing system that continuously monitors and optimizes endpoint quality.