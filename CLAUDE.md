# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

PATH (Path API & Toolkit Harness) is an open-source Go framework for enabling access to a decentralized supply network. It serves as a gateway that handles service requests and relays them through the Shannon protocol to blockchain endpoints.

## Development Commands

### Building and Running

- `make path_build` - Build the PATH binary locally
- `make path_run` - Run PATH as a standalone binary (requires CONFIG_PATH)
- `make path_up` - Start local Tilt development environment with dependencies
- `make path_down` - Tear down local Tilt development environment

### Testing

- `make test_unit` - Run all unit tests (`go test ./... -short -count=1`)
- `make test_all` - Run unit tests plus E2E tests for key services
- `make e2e_test SERVICE_IDS` - Run E2E tests for specific Shannon service IDs (e.g., `make e2e_test eth,poly`)
- `make load_test SERVICE_IDS` - Run Shannon load tests
- `make go_lint` - Run Go linters (`golangci-lint run --timeout 5m --build-tags test`)

### Configuration

- `make config_prepare_shannon_e2e` - Prepare Shannon E2E configuration

## Architecture Overview

PATH operates as a multi-layered gateway system:

### Core Components

- **Gateway** (`gateway/`) - Main entry point that handles HTTP requests and coordinates request processing
- **Protocol** (`protocol/`) - Protocol implementations (currently only Shannon) that manage endpoint communication
- **QoS** (`qos/`) - Quality of Service implementations for different blockchain services (EVM, Solana, CosmosSDK)
- **Router** (`router/`) - HTTP routing and API endpoint management
- **Config** (`config/`) - Configuration management for different protocol modes

### Protocol Implementations

- **Shannon** (`protocol/shannon/`) - Main protocol implementation with gRPC communication

### QoS Services

- **EVM** (`qos/evm/`) - Ethereum-compatible blockchain QoS with archival data checks
- **Solana** (`qos/solana/`) - Solana blockchain QoS
- **CosmosSDK** (`qos/cosmos/`) - Cosmos SDK blockchain QoS with support for REST, CometBFT, and JSON-RPC
- **JSONRPC** (`qos/jsonrpc/`) - Generic JSON-RPC handling
- **NoOp** (`qos/noop/`) - Pass-through QoS for unsupported services

### Data Flow

1. HTTP requests arrive at the Gateway
2. Request Parser maps requests to appropriate QoS services
3. QoS services validate requests and select optimal endpoints
4. Protocol implementations relay requests to blockchain endpoints
5. Responses are processed through QoS validation
6. Metrics and observations are collected throughout the pipeline

### Configuration

PATH uses YAML configuration files that support the Shannon protocol. Configuration includes:

- Protocol-specific settings (gRPC endpoints, signing keys)
- Service definitions and endpoint mappings
- QoS parameters and validation rules
- Gateway routing and middleware settings

## Key Files and Directories

- `cmd/main.go` - Application entry point and initialization
- `config/config.go` - Configuration loading and management
- `gateway/gateway.go` - Main gateway implementation
- `protocol/protocol.go` - Protocol interface definitions
- `Makefile` - Build and development commands
- `makefiles/` - Modular Makefile components for different tasks
- `e2e/` - End-to-end tests and configuration
- `local/` - Local development configuration for Kubernetes/Tilt
- `proto/` - Protocol buffer definitions
- `observation/` - Generated protobuf code for metrics and observations

## Development Environment

PATH uses Tilt for local development with Kubernetes (kind). The development stack includes:

- PATH gateway
- Envoy Proxy for load balancing
- Prometheus for metrics
- Grafana for observability
- Rate limiting and authentication services

## API Usage

### Making Requests to PATH Gateway

PATH requires the service ID to be specified via the `Target-Service-Id` HTTP header, not in the URL path.

**Correct format:**
```bash
curl -X POST http://localhost:3069/v1 \
  -H "Content-Type: application/json" \
  -H "Target-Service-Id: eth" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```

**Common services:**
- `eth` - Ethereum mainnet (supports: json_rpc, websocket)
- `solana` - Solana mainnet (supports: json_rpc)
- `poly` - Polygon (supports: json_rpc, websocket)
- `xrplevm` - XRPL EVM (supports: json_rpc, rest, comet_bft, websocket)

**RPC Type Detection:**
PATH automatically detects the RPC type from the request:
- **JSON-RPC**: POST with `{"jsonrpc":"2.0",...}` body
  ```bash
  curl -X POST http://localhost:3069/v1 \
    -H "Target-Service-Id: eth" \
    -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
  ```
- **REST** (Cosmos SDK): GET/POST to Cosmos REST API paths
  ```bash
  # Cosmos SDK REST API (gRPC-gateway)
  curl -X GET http://localhost:3069/v1/cosmos/base/tendermint/v1beta1/blocks/latest \
    -H "Target-Service-Id: xrplevm"
  ```
- **CometBFT RPC**: GET/POST to CometBFT RPC paths
  ```bash
  # CometBFT JSON-RPC over HTTP
  curl -X GET http://localhost:3069/v1/status \
    -H "Target-Service-Id: xrplevm"
  ```
- **WebSocket**: WebSocket upgrade requests (for subscriptions)

### Advanced Headers

**Target-Suppliers** (Optional)
Restricts relay requests to a specific list of supplier addresses, bypassing reputation and endpoint selection logic.

Format: Comma-separated list of supplier addresses
```bash
# Send request only to specific suppliers
curl -X POST http://localhost:3069/v1 \
  -H "Target-Service-Id: eth" \
  -H "Target-Suppliers: pokt1abc123...,pokt1def456...,pokt1ghi789..." \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```

**Use cases:**
- Testing specific supplier endpoints
- Debugging supplier-specific issues
- Directing traffic to trusted suppliers for sensitive operations
- Load testing specific infrastructure providers

**Behavior:**
- When `Target-Suppliers` header is present, PATH will:
  - Filter available endpoints to only those from the specified suppliers
  - Skip reputation-based filtering (allows targeting suppliers with low reputation)
  - Still apply RPC type filtering (only endpoints supporting the requested RPC type)
  - Log filtered supplier list and endpoint counts
- If none of the specified suppliers are available in the current session, the request will fail
- Header takes precedence over load testing configuration (if any)

**App-Address** (Delegated Mode Only)
Specifies the target application address when PATH is running in delegated mode.
```bash
curl -X POST http://localhost:3069/v1 \
  -H "Target-Service-Id: eth" \
  -H "App-Address: pokt1app..." \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```

### Operational Endpoints

**Health Check** (`/health`)
Returns overall gateway health status. Note: `/healthz` is deprecated, use `/health` instead.
```bash
curl http://localhost:3069/health
```

**Service Readiness** (`/ready/<service>`)
Check if a specific service is ready to handle requests.
```bash
# Basic readiness check
curl http://localhost:3069/ready/eth
# Response: {"ready":true,"endpoint_count":49,"has_session":true}

# Detailed endpoint information (includes reputation, archival status, latency)
curl "http://localhost:3069/ready/eth?detailed=true"
```

**Detailed Response Fields:**
- `endpoints[]` - Array of endpoint details:
  - `address` - Unique endpoint identifier (supplier-url format)
  - `supplier_address` - Supplier's POKT address
  - `url` - Backend endpoint URL
  - `is_fallback` - Whether this is a fallback endpoint
  - `reputation` - Reputation metrics:
    - `score` - Current reputation score (0-100)
    - `success_count` / `error_count` - Request counters
    - `latency` - Latency metrics (avg, min, max, last in ms)
    - `critical_strikes` - Number of critical failures
  - `archival` - Archival capability (EVM services only):
    - `is_archival` - Whether endpoint can serve historical data
    - `expires_at` - When archival status expires (needs re-validation)
  - `tier` - Reputation tier (1=best, 2=good, 3=probation)
  - `in_cooldown` - Whether endpoint is in cooldown period
  - `cooldown_remaining` - Time remaining in cooldown

**All Services Readiness** (`/ready`)
Check readiness of all configured services.
```bash
curl http://localhost:3069/ready
# With detailed endpoint info for all services
curl "http://localhost:3069/ready?detailed=true"
```

### Admin Endpoints

**Circuit Breaker Clear** (`POST /admin/circuit-breaker/clear/{serviceId}`)
Clears all circuit breaker state (in-memory + Redis) for a specific service. This is the only reliable way to reset circuit breaker state — Redis DEL alone is insufficient because `refreshFromRedis` merges local in-memory entries back.

Must be called on each pod individually since in-memory state is per-pod.
```bash
# Port-forward to a pod first
kubectl --context pnf -n <namespace> port-forward <pod> 13069:3069 &

# Clear circuit breaker state for a service
curl -X POST http://localhost:13069/admin/circuit-breaker/clear/near
# Response: {"service_id":"near","cleared_domains":3,"message":"circuit breaker state cleared (in-memory + Redis)"}
```

**WebSocket Tumble** (`POST /admin/websocket/tumble/{serviceId}`)
Forces live WebSocket connections to rebind onto different suppliers. Clients stay connected — the bridge re-dials an endpoint and replays the client's subscriptions, the same machinery a session rollover uses.

A WebSocket connection binds one endpoint for its entire lifetime and only moves at a session rollover or a stall. A long-lived high-volume subscriber therefore pins itself to whichever operator it first landed on, and no change to endpoint selection can move it — selection only governs where *new* connections go. The alternative is restarting the pod, which drops every client and resets unrelated in-memory state.

Must be called on each pod individually (per-pod in-memory registry).
```bash
# See the current distribution without moving anything
curl -X POST "http://localhost:13069/admin/websocket/tumble/bsc?dry_run=true"

# Move every connection currently bound to one operator
curl -X POST "http://localhost:13069/admin/websocket/tumble/bsc?domain=example.net"

# Move at most 5, most-concentrated operators first
curl -X POST "http://localhost:13069/admin/websocket/tumble/bsc?max=5"
```

Query parameters (all optional): `domain=<eTLD+1>` restricts to connections currently bound to that operator; `max=<n>` caps how many move (candidates ordered by descending per-domain concentration); `dry_run=true` reports without moving.

Response includes `connections_by_domain` (the pre-tumble distribution), `tumbled_by_domain`, and `matched`/`tumbled`/`skipped` counts. `skipped` means the connection could not accept a tumble right now — rebind disabled for it, or one already queued.

Rebinds land on `path_websocket_rebind_total{trigger="admin"}`, distinct from `rollover` and `stall`.

**Circuit Breaker — when to use:**
- After deploying a fix for a bug that caused false positive circuit breaker lockouts
- When a domain is stuck in circuit breaker state due to a transient issue that has resolved
- Rolling restarts alone don't work because `refreshFromRedis` repopulates in-memory state from Redis

## Endpoint Selection — Backend-URL Weighting

Several suppliers can register against the **same backend URL**. Selection weights an operator's share by **distinct backend URL**, not by supplier registration, so stacking registrations behind one machine does not buy that machine more traffic.

Selection is two-stage: pick a backend URL uniformly, then pick a supplier registration uniformly within it. The result is always a concrete supplier — relays are signed against a supplier's session and each supplier carries its own per-session service allowance.

Applies to every real decision path: `SelectWithConcentrationCap`, `SelectEndpointsWithDiversity` (including its first pick, which has no TLD-diversity logic), `selectTopRankedEndpoint` (retry/hedge band), and `SelectOperatorUniform` (WS rebind).

**Off-switch** — restores registration-counted behavior exactly:
```bash
PATH_OPERATOR_SHARE_BY_BACKEND_URL=false
```
No-op for operators that register one supplier per URL (distinct-URL count == registration count).

**What to watch after enabling:** `path_supplier_exhausted_total` for the **small** operators, not the large one. A solo-registration backend's share rises (on a 50-registration/17-backend service, roughly 3x) while it still has only one supplier's allowance. Exhaustion is self-correcting — an exhausted supplier is filtered out per-supplier and its share redistributes — but a spike there is the expected failure mode.

Related: `path_concentration_cap_reshaped_total` should **fall**, since deduped shares often land under the cap and need no water-filling.

## Testing Strategy

- **Unit Tests** - Standard Go tests with `-short` flag
- **E2E Tests** - Full integration tests against live blockchain endpoints
- **Load Tests** - Performance testing using Vegeta load testing tool
- **Protocol Tests** - test suites for Shannon protocol
