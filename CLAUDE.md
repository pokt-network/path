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

## Testing Strategy

- **Unit Tests** - Standard Go tests with `-short` flag
- **E2E Tests** - Full integration tests against live blockchain endpoints
- **Load Tests** - Performance testing using Vegeta load testing tool
- **Protocol Tests** - test suites for Shannon protocol
