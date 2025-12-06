# How to Run PATH Locally

This guide provides step-by-step instructions for running PATH (Path API & Toolkit Harness) locally with the Shannon protocol.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Full Configuration Reference](#full-configuration-reference)
- [Running PATH](#running-path)
- [Verifying PATH is Running](#verifying-path-is-running)
- [Troubleshooting](#troubleshooting)

---

## Prerequisites

1. **Go 1.21+** installed
2. **Shannon Account** with:
   - Gateway address and private key
   - Application(s) staked for desired services
3. **Access to Shannon Full Node** (gRPC and RPC endpoints)

## Quick Start

```bash
# 1. Clone the repository
git clone https://github.com/pokt-network/path.git
cd path

# 2. Build PATH
make path_build

# 3. Create your configuration file
cp config/examples/config.shannon_example.yaml my_config.yaml
# Edit my_config.yaml with your credentials

# 4. Run PATH
./bin/path -config ./my_config.yaml

# Or using make:
CONFIG_PATH=./my_config.yaml make path_run
```

## Configuration

PATH uses a YAML configuration file. Below is a minimal configuration to get started:

### Minimal Configuration

```yaml
# Minimal PATH configuration
full_node_config:
  rpc_url: https://shannon-grove-rpc.mainnet.poktroll.com
  grpc_config:
    host_port: shannon-grove-grpc.mainnet.poktroll.com:443
  lazy_mode: false
  session_rollover_blocks: 10
  cache_config:
    session_ttl: 30s

gateway_config:
  gateway_mode: "centralized"
  gateway_address: pokt1yourgatewayaddrhere
  gateway_private_key_hex: your_gateway_private_key_64_hex_chars
  owned_apps_private_keys_hex:
    - your_app_private_key_64_hex_chars

logger_config:
  level: "info"
```

---

## Full Configuration Reference

Below is a comprehensive configuration with all available options and detailed explanations:

```yaml
# yaml-language-server: $schema=../config/config.schema.yaml
# PATH Gateway Configuration - Full Reference

#################################################
### Full Node Configuration
#################################################
full_node_config:
  # HTTP URL for the Shannon full node RPC endpoint
  # Used for blockchain queries and session management
  rpc_url: https://shannon-grove-rpc.mainnet.poktroll.com

  # gRPC configuration for the Shannon full node
  grpc_config:
    # Host and port for gRPC connections (format: host:port)
    host_port: shannon-grove-grpc.mainnet.poktroll.com:443

    # Set to true if the gRPC connection doesn't use TLS
    # Default: false
    insecure: false

    # Retry configuration for gRPC connections
    base_delay: "1s"       # Base delay between retries
    max_delay: "30s"       # Maximum delay between retries
    min_connect_timeout: "5s"  # Minimum connection timeout

    # Keep-alive settings for long-lived connections
    keep_alive_time: "30s"     # How often to send keep-alive pings
    keep_alive_timeout: "10s"  # How long to wait for keep-alive response

  # Lazy mode disables all caching of full node data
  # Set to true for development/debugging
  # Default: true
  lazy_mode: false

  # Grace period after session end where rollover issues may occur
  # Helps handle session transitions smoothly
  # Default: none (required when lazy_mode is false)
  session_rollover_blocks: 10

  # Cache configuration (only used when lazy_mode is false)
  cache_config:
    # TTL for application cache
    app_ttl: 5m
    # TTL for session cache
    session_ttl: 30s

#################################################
### Gateway Configuration
#################################################
gateway_config:
  # Gateway operation mode
  # Options: "centralized", "delegated", "permissionless"
  # - centralized: Gateway signs all requests
  # - delegated: Gateway delegates to applications
  # - permissionless: Open access mode
  gateway_mode: "centralized"

  # Your Shannon gateway address (starts with pokt1)
  gateway_address: pokt1yourgatewayaddrhere

  # Private key for the gateway (64 hex characters)
  # SECURITY: Keep this secret! Use environment variables in production
  gateway_private_key_hex: your_gateway_private_key_64_hex_chars

  # Private keys for applications owned by the gateway
  # Each application should be staked for specific services
  owned_apps_private_keys_hex:
    - app1_private_key_64_hex_chars  # e.g., staked for eth
    - app2_private_key_64_hex_chars  # e.g., staked for solana

  #################################################
  ### Reputation System Configuration
  #################################################
  # The reputation system tracks endpoint reliability via scores (0-100).
  # Endpoints that fail requests get penalized; successful requests improve scores.
  # Endpoints below min_threshold are filtered out of endpoint selection.
  reputation_config:
    # Enable/disable the reputation system
    # When disabled, PATH operates in simple relay mode without quality filtering
    # Default: true
    enabled: true

    # Storage backend for reputation data
    # Options: "memory" (single instance) or "redis" (multi-instance deployments)
    # Default: "memory"
    storage_type: "memory"

    # Starting score for new endpoints (0-100 scale)
    # Higher values give new endpoints more chances before filtering
    # Default: 80
    initial_score: 80

    # Minimum score required for endpoint selection
    # Endpoints below this threshold are filtered out
    # Default: 30
    min_threshold: 30

    # Time after which inactive low-scoring endpoint scores can recover
    # IGNORED when probation or health_checks are enabled (signal-based recovery takes over)
    # Default: 5m
    recovery_timeout: 5m

    # Latency-aware scoring configuration
    # Fast endpoints get bonuses; slow endpoints get penalties
    latency:
      enabled: true
      # Thresholds for latency classification (service-type dependent)
      fast_threshold: 100ms    # Responses faster than this get bonus
      normal_threshold: 500ms  # Normal response time
      slow_threshold: 1000ms   # Slow but acceptable
      penalty_threshold: 2000ms # Triggers slow_response penalty signal
      severe_threshold: 5000ms  # Triggers very_slow_response penalty signal
      # Score multipliers
      fast_bonus: 2.0          # Fast success = +2 instead of +1
      slow_penalty: 0.5        # Slow success = +0.5 instead of +1
      very_slow_penalty: 0.0   # Very slow success = no reputation gain

    # Tiered endpoint selection based on reputation scores
    tiered_selection:
      enabled: true
      # Minimum score for tier 1 (highest priority, selected first)
      tier1_threshold: 70
      # Minimum score for tier 2 (selected if no tier 1 available)
      tier2_threshold: 50

      # Probation system for recovering low-scoring endpoints
      # Gives filtered-out endpoints a small percentage of traffic to prove recovery
      probation:
        enabled: true
        # Score threshold below which endpoints enter probation
        threshold: 30
        # Percentage of traffic routed to probation endpoints (0-100)
        traffic_percent: 10
        # Multiplier for score recovery during successful probation requests
        recovery_multiplier: 2.0

  #################################################
  ### Retry Configuration
  #################################################
  # Automatic retry on transient errors
  retry_config:
    enabled: true
    # Maximum number of retry attempts per request
    max_retries: 1
    # Retry on HTTP 5xx server errors
    retry_on_5xx: true
    # Retry on timeout errors
    retry_on_timeout: true
    # Retry on connection errors
    retry_on_connection: true

  #################################################
  ### Observation Pipeline Configuration
  #################################################
  # Async observation processing for deep response parsing
  # Extracts quality data (block height, chain ID) without blocking responses
  observation_pipeline:
    # Enable/disable async processing
    enabled: false
    # Percentage of requests to sample for deep parsing (0.0-1.0)
    # Health checks are always processed (not sampled)
    sample_rate: 0.1
    # Number of async parser workers
    worker_count: 4
    # Max pending observations before dropping (non-blocking)
    queue_size: 1000

  #################################################
  ### Active Health Checks Configuration
  #################################################
  # Proactive endpoint monitoring - detects issues before user traffic
  # Health checks are sent through the protocol layer (like real requests)
  active_health_checks:
    enabled: true

    # Leader election for multi-instance deployments
    # Only the leader runs health checks to avoid duplicate work
    # coordination:
    #   type: "leader_election"  # Options: "none", "leader_election"
    #   lease_duration: "15s"
    #   renew_interval: "5s"
    #   key: "path:health:leader"

    # External health check rules (fetched from URL, e.g., GitHub)
    # Local rules override external rules with the same service_id + check name
    external:
      url: "https://raw.githubusercontent.com/your-org/health-checks/main/checks.yaml"
      refresh_interval: "1h"  # Re-fetch interval (0 = only at startup)
      timeout: "30s"

    # Local health check configurations per service
    local:
      #-----------------------------------------
      # EVM Service (Ethereum)
      #-----------------------------------------
      - service_id: eth
        check_interval: "30s"
        enabled: true
        checks:
          # Basic connectivity check
          - name: "eth_blockNumber"
            type: "jsonrpc"
            method: "POST"
            path: "/"
            headers:
              Content-Type: "application/json"
            body: '{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber"}'
            expected_status_code: 200
            timeout: "5s"
            reputation_signal: "minor_error"  # -3 on failure

          # Chain ID check (validates correct chain)
          - name: "eth_chainId"
            type: "jsonrpc"
            method: "POST"
            path: "/"
            headers:
              Content-Type: "application/json"
            body: '{"jsonrpc":"2.0","id":1,"method":"eth_chainId"}'
            expected_status_code: 200
            timeout: "5s"
            reputation_signal: "major_error"  # -10 on failure

          # Archival check - queries historical data
          # Non-archival nodes will fail with "missing trie node"
          - name: "eth_archival"
            type: "jsonrpc"
            method: "POST"
            path: "/"
            headers:
              Content-Type: "application/json"
            body: '{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x28C6c06298d514Db089934071355E5743bf21d60","0xe4e1c0"]}'
            expected_status_code: 200
            expected_response_contains: "0x314214a541a8e719f516"
            timeout: "10s"
            reputation_signal: "critical_error"  # -25 on failure

      #-----------------------------------------
      # Solana Service
      #-----------------------------------------
      - service_id: solana
        check_interval: "30s"
        enabled: true
        checks:
          - name: "solana_getHealth"
            type: "jsonrpc"
            method: "POST"
            path: "/"
            body: '{"jsonrpc":"2.0","id":1,"method":"getHealth"}'
            expected_status_code: 200
            timeout: "5s"
            reputation_signal: "minor_error"

          - name: "solana_getEpochInfo"
            type: "jsonrpc"
            method: "POST"
            path: "/"
            body: '{"jsonrpc":"2.0","id":1,"method":"getEpochInfo"}'
            expected_status_code: 200
            timeout: "5s"
            reputation_signal: "major_error"

      #-----------------------------------------
      # Cosmos Service (e.g., Stargaze)
      #-----------------------------------------
      - service_id: stargaze
        check_interval: "30s"
        enabled: true
        checks:
          # CometBFT JSON-RPC health check
          - name: "cometbft_health"
            type: "jsonrpc"
            method: "POST"
            path: "/"
            body: '{"jsonrpc":"2.0","id":1,"method":"health"}'
            expected_status_code: 200
            timeout: "5s"
            reputation_signal: "minor_error"

          # REST API health check
          - name: "cosmos_status"
            type: "rest"
            method: "GET"
            path: "/cosmos/base/node/v1beta1/status"
            expected_status_code: 200
            timeout: "5s"
            reputation_signal: "minor_error"

  #################################################
  ### Service Fallback Configuration
  #################################################
  # Fallback endpoints used when no healthy session endpoints are available
  service_fallback:
    - service_id: eth
      # Send all traffic to fallback (bypass session endpoints)
      send_all_traffic: false
      fallback_endpoints:
        - default_url: "https://eth.backup.provider.io"

    # Multi-RPC-type service example (Cosmos)
    - service_id: xrplevm
      send_all_traffic: false
      fallback_endpoints:
        - default_url: "http://backup.node.io"
          json_rpc: "http://backup.node.io:8545"
          rest: "http://backup.node.io:1317"
          comet_bft: "http://backup.node.io:26657"
          websocket: "ws://backup.node.io:8546"

#################################################
### Logger Configuration
#################################################
logger_config:
  # Log level: debug, info, warn, error
  # Use "debug" for development, "info" or "warn" for production
  level: "info"

#################################################
### Router Configuration (Optional)
#################################################
router_config:
  # Port for the HTTP server
  port: 3069
  # Maximum request header size
  max_request_header_bytes: 8192
  # Timeouts
  read_timeout: "30s"
  write_timeout: "30s"
  idle_timeout: "120s"
  # WebSocket message buffer size
  websocket_message_buffer_size: 4096

#################################################
### Redis Configuration (Optional)
#################################################
# Required when reputation storage_type is "redis" or
# when using leader_election for health checks
redis_config:
  address: "localhost:6379"
  password: ""
  db: 0
  pool_size: 10
  dial_timeout: "5s"
  read_timeout: "3s"
  write_timeout: "3s"
```

---

## Running PATH

### Using Binary Directly

```bash
# Build first
make path_build

# Run with config file
./bin/path -config ./my_config.yaml
```

### Using Make

```bash
# Set config path and run
CONFIG_PATH=./my_config.yaml make path_run
```

### Using Docker (Tilt)

For local development with full stack (Prometheus, Grafana, etc.):

```bash
# Start the development environment
make path_up

# Tear down when done
make path_down
```

---

## Verifying PATH is Running

### 1. Check Health Endpoint

```bash
curl http://localhost:3069/healthz
```

### 2. Check Metrics

```bash
# View all metrics
curl http://localhost:3070/metrics

# Check reputation metrics
curl -s http://localhost:3070/metrics | grep shannon_reputation

# Check health check metrics
curl -s http://localhost:3070/metrics | grep shannon_health_check
```

### 3. Send a Test Request

```bash
# Example: eth_blockNumber request
curl -X POST http://localhost:3069/v1/eth \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber"}'
```

### 4. Check Logs

PATH logs are written to stdout. Key log messages to look for:

```
Starting health check cycle        # Health checks are running
Health check relay response received  # Health checks completing successfully
Session refreshed successfully     # Sessions are being maintained
```

---

## Troubleshooting

### "No endpoints available"

1. Verify your application is staked for the requested service
2. Check that the full node RPC/gRPC endpoints are accessible
3. Verify gateway credentials are correct

### Health checks failing

1. Check the specific check's configuration
2. Verify the `expected_status_code` and `expected_response_contains` values
3. Review logs for detailed error messages

### "recovery_timeout ignored" warning

This is informational. When probation or health_checks are enabled, signal-based recovery takes precedence over time-based recovery. Set `recovery_timeout: 0` to suppress.

### High memory usage

1. Reduce `queue_size` in observation_pipeline
2. Use Redis storage for reputation in multi-instance deployments
3. Reduce the number of concurrent health check workers

---

## Environment Variables

PATH supports environment variable substitution in config files:

```yaml
gateway_config:
  gateway_private_key_hex: ${GATEWAY_PRIVATE_KEY}
```

Run with:

```bash
GATEWAY_PRIVATE_KEY=your_key_here ./bin/path -config ./config.yaml
```

---

## Next Steps

1. **Production Deployment**: See the deployment documentation
2. **Monitoring**: Set up Prometheus and Grafana dashboards
3. **Scaling**: Configure Redis for multi-instance deployments
4. **Custom Health Checks**: Add service-specific health checks via external URL