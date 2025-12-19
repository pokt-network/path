## Summary

Unified QoS system with per-service configuration, reputation-based endpoint selection, async observation pipeline, and comprehensive retry/fallback mechanisms including RPC type fallback with proper actualRPCType propagation.

## New Features

1. **Async Observation Pipeline** - Response parsing off critical path with configurable sampling
2. **Reputation System** - Score-based endpoint quality tracking (0-100 scale)
3. **Tiered Endpoint Selection** - 3-tier cascading selection (Tier 1 → Tier 2 → Tier 3)
4. **Probation System** - Recovery mechanism for low-scoring endpoints (10% traffic sampling)
5. **RPC Type Fallback** - Automatic fallback to alternative RPC types with actualRPCType propagation
6. **RPC-Type-Aware Reputation** - Separate scores per (endpoint, RPC type) tuple
7. **Configurable Cosmos QoS** - Custom RPC type support for hybrid chains (Cosmos+EVM)
8. **Session Rollover** - Automatic endpoint refresh on session transitions with configurable rollover blocks
9. **WebSocket Height Subscription** - Real-time blockchain height monitoring via WebSocket instead of polling
10. **Distributed Health Checks** - Proactive endpoint monitoring with leader election (only one instance runs checks)
11. **Latency-Aware Scoring** - Fast endpoints get bonuses, slow ones penalized
12. **Named Latency Profiles** - Reusable configurations: `fast`, `standard`, `slow`, `llm`
13. **Per-Service Configuration** - Override any global setting via `defaults` + `services[]`
14. **External Health Check Rules** - Fetch health check definitions from remote URLs
15. **Enhanced Retry System** - Endpoint rotation, latency budget, configurable conditions
16. **Retry Endpoint Rotation** - Never retry same failed endpoint within request
17. **Retry Latency Budget** - Skip retries on slow requests (configurable threshold)
18. **Concurrency Controls** - Configurable limits: parallel endpoints, concurrent relays, batch payloads
19. **Target-Suppliers Header** - Filter requests to specific suppliers via HTTP header
20. **Error Classification System** - Comprehensive error categorization with reputation signals
21. **WebSocket Monitoring** - Connection health tracking and failure detection
22. **RPC Type Detection** - Automatic detection and validation from HTTP requests
23. **Comprehensive Metrics** - Prometheus metrics for reputation, health checks, retries, sessions

## Removed Systems

1. **Hydrator Command** - Replaced by async observation pipeline
2. **Sanctions System** - Replaced by reputation-based filtering
3. **Hardcoded Service Configs** - Now fully YAML-driven configuration
4. **Synchronous QoS Validation** - Moved to async background processing

## Breaking Changes

1. **Reputation Storage Format**: Keys now include RPC type dimension (`serviceID:endpointAddr:rpcType`)
   - Existing reputation data invalidated
   - System rebuilds scores naturally (5-10 min settling period)

2. **Protocol Interface Changes**:
   - `AvailableHTTPEndpoints()` now requires `rpcType` parameter
   - `BuildHTTPRequestContextForEndpoint()` now requires `rpcType` parameter

3. **QoS Interface Changes**:
   - `ParseHTTPRequest()` now receives `detectedRPCType` parameter

4. **Configuration Structure**: New required sections
   - `reputation_config` (global)
   - `active_health_checks` (global)
   - `observation_pipeline` (global)
   - `defaults` (service defaults)
   - `services` (per-service array)

5. **Private Key Logging**: Now redacted (security fix)

## Configuration

See [`config/examples/config.shannon_example.yaml`](config/examples/config.shannon_example.yaml) for full configuration examples including:
- Global defaults and per-service overrides
- RPC type fallback mappings
- Reputation, retry, and health check settings
- Latency profiles and concurrency controls

## Test Results

**Pre-Commit Checks**:
- ✅ Unit Tests: All passing (26 packages)
- ✅ Lint: 0 issues
- ✅ Build: Successful

**E2E Results** (34 test runs):
- **Empty URL Errors**: 0
- **RPC Fallback Success**: 100%

**Cosmos Chains** (48-100%):
- juno: 100% ✅
- persistence: 100% ✅
- akash: 100% ✅
- stargaze: 99.74% ✅
- xrplevm: 100% ✅ (hybrid Cosmos+EVM)
- fetch: 92.31% ✅
- osmosis: 62%

**EVM Chains** (75-83%):
- eth, poly, avax, bsc, base: All passing

**Other Chains**:
- solana: 95.42% ✅

*Remaining failures are supplier quality issues (pruned state, missing trie nodes, 404s, timeouts).*

## New Prometheus Metrics

**Reputation**:
- `shannon_reputation_signals_total`
- `shannon_reputation_endpoints_filtered_total`
- `shannon_reputation_score_distribution`
- `shannon_probation_endpoints`

**Health Checks**:
- `shannon_health_check_total`
- `shannon_health_check_duration_seconds`

**Session**:
- `shannon_active_sessions`
- `shannon_session_endpoints`

**Retry**:
- `shannon_retries_total`
- `shannon_retry_success_total`
- `shannon_retry_latency_seconds`
- `shannon_retry_budget_skipped_total`
- `shannon_retry_endpoint_switches_total`
- `shannon_retry_endpoint_exhaustion_total`
