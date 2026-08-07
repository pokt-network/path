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
  - Skip **all** reputation-derived filtering — both the score-threshold/cooldown filter and
    tiered (highest-tier-only) selection. A score-0, fully-cooled-down supplier is reachable.
  - Still apply RPC type filtering (only endpoints supporting the requested RPC type)
  - Still apply the config `blocked_suppliers` list, the endpoint policy (`require_https` /
    `require_domain`), and the supplier blacklist (signature/validation failures). None of these
    are reputation, and the header does not override them.
  - Log filtered supplier list and endpoint counts
- If none of the specified suppliers are available in the current session, the request will fail
- Header takes precedence over load testing configuration (if any)

Tiered selection used to run *after* the supplier allowlist, so pinning a supplier whose
endpoints were all below `min_threshold` returned `no valid endpoints available for service` —
the header failed exactly when it was most needed, on a supplier you were trying to diagnose.
Health checks were unaffected throughout (they pass `filterByReputation=false`), which is why a
dark supplier still shows health-check traffic while serving zero user traffic.

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

Query parameters (all optional): `domain=<eTLD+1>` restricts to connections currently bound to that operator; `max=<n>` caps how many move; `order_by=throughput|connections` picks how a capped tumble ranks candidates (default `throughput`); `dry_run=true` reports without moving.

**`max` is spent by traffic, not by socket count.** Connection count is a poor proxy for load — a single firehose subscriber routinely carries more than a dozen idle sockets on another operator (measured on live bsc: 232 frames/s on one connection vs 2.2 frames/s on another). Ordering by throughput moves the operator actually carrying the service, and within it that operator's busiest connections first. `order_by=connections` restores the old socket-count ordering for when the goal is evening out socket counts irrespective of how busy they are.

Response includes `connections_by_domain` and `throughput_by_domain_msgs_per_sec` (the pre-tumble distributions), `tumbled_by_domain` and `tumbled_throughput_by_domain_msgs_per_sec` (what moved), `order_by`, and `matched`/`tumbled`/`skipped` counts. `skipped` means the connection could not accept a tumble right now — rebind disabled for it, or one already queued.

A `dry_run=true` call is the cheapest way to answer **"who is actually carrying this service"** — the throughput distribution routinely contradicts the connection distribution.

Rebinds land on `path_websocket_rebind_total{trigger="admin"}`, distinct from `rollover`, `stall`, and `session_expired`.

**Automatic rebind triggers** (no admin action needed):
- `rollover` — the supplier closed the socket at session expiry (close 4000). The healthy path.
- `stall` — the staleness watchdog saw no subscription data past the threshold; escapes the bound **backend URL**.
- `session_expired` — PATH noticed the bound session had ended while the supplier kept streaming. Without this a connection is stranded outside the session indefinitely: unsigned endpoint→client frames need no session so data keeps flowing, and the staleness watchdog stays quiet *because* it is flowing. Measured at 21% of live connections before the fix. Watch for `Endpoints = 0` with a blank Mean Score but nonzero WS msg/s on the supplier-quality panel — that combination is the tell.

**Idle-connection reaper** (`websocket_idle_timeout`, default `30m`)

A connection that has **never established a subscription** and has **sent no client frame** for the threshold is closed with **1000 (Normal Closure)**. Nothing else reaped it: ping/pong keeps a socket alive for as long as the peer answers, and the staleness watchdog arms only on connections that *have* a subscription — a quiet subscription-less connection was assumed to be a WebSocket JSON-RPC client between requests.

Measured 2026-08-04: five services (`eth-sepolia-testnet` 134 conns, `blast` 40, `sei` 23, `moonbeam` 22, `bera` 22) held **241 connections with zero subscriptions between them**, costing **~1370 rebinds/hour that replayed nothing**. The tell is `path_websocket_rebind_total` high with `path_websocket_subscriptions_replayed_total` at **exactly 0** for the same `service_id` — compare against `eth` (188 rebinds → 182 replays). At the edge these were four WebSocket-only client IPs making no HTTP at all, holding sockets 4-6.5h and receiving ~1.5 KB each.

**The two conditions are ANDed, and each alone is wrong.** Silence alone reaps a subscriber watching a rare event (legitimately quiet for hours). No-subscription alone reaps a WebSocket JSON-RPC client between requests. Neither at once, for half an hour, is neither shape.

Not an endpoint fault, so it **never touches reputation** — the supplier was never asked for anything. Reaps land on `path_websocket_idle_reaped_total{service_id, domain}`, deliberately a separate counter: the shared close path already emits `event="closed"`, and folding reaps in would break established/closed reconciliation, which is the check that separates a gauge leak from real accumulation.

```yaml
router_config:
  websocket_idle_timeout: 30m   # negative disables reaping entirely
```
```bash
PATH_WEBSOCKET_IDLE_TIMEOUT=45m   # pod restart instead of a config-map edit
```

**Reputation Drain** (`POST /admin/reputation/drain/{serviceId}`)

Temporarily benches every **scored** endpoint of one operator (eTLD+1) for a service by
writing a cooldown expiry onto its score. Selection already excludes endpoints in cooldown
regardless of score, so this reuses a filter every selection path is guaranteed to consult
rather than adding a second exclusion some path could miss.

Answers "where would this traffic go if operator X were unavailable" without waiting for X
to fail.

**Tumble is not a substitute.** A tumble re-dials but leaves every operator eligible, so the
connection can land straight back where it started. Measured on gnosis: **8 consecutive
tumbles failed to move a ~500 frames/s subscription** off the two operators already carrying
it, because each rebind could reselect them. Drain first, *then* tumble — the rebind then has
nowhere else to go.

```bash
# what would be benched (always do this first — the response lists domains_seen, so a typo
# reads as "matched 0, and here is what actually exists" rather than a silent no-op)
curl -X POST "http://localhost:13069/admin/reputation/drain/gnosis?domain=spacebelt.xyz&rpc_type=websocket&dry_run=true"

# bench for 20m, websocket only — the operator's json_rpc / rest traffic is untouched
curl -X POST "http://localhost:13069/admin/reputation/drain/gnosis?domain=spacebelt.xyz&rpc_type=websocket&duration=20m"

# then move the live connections that are already bound
curl -X POST "http://localhost:13069/admin/websocket/tumble/gnosis"

# release early
curl -X POST "http://localhost:13069/admin/reputation/drain/gnosis?domain=spacebelt.xyz&rpc_type=websocket&duration=0"
```

Query parameters: `domain=<eTLD+1|hostname|url>` (**required**, `url=` is an alias — a drain
with no target would bench the whole service, which is never what anyone meant to type) ·
`duration=<go duration>` (default `15m`; `0` releases) · `rpc_type=<websocket|json_rpc|rest|…>`
(default all) · `dry_run=true`.

**Target by URL/domain, never by node id.** The handler resolves the target against live
endpoint details into *every* identifier a reputation key could carry — full endpoint address,
supplier address, URL, hostname, eTLD+1 — because key granularity is per-service config. The
same operator is a hostname on one service and a `pokt1…` supplier address on another. An
eTLD+1-only filter returns `matched: 0` on a supplier-keyed service while looking like it
worked; check `identifiers_resolved` and `matched_endpoints` in the response to tell "target
names nothing" apart from "names endpoints that carry no score yet".

**The bench is an overlay, not a score write.** It lives in `drainedKeys` and is applied when
scores are read. This is load-bearing: an earlier version wrote `CooldownUntil` onto the Score,
and `refreshFromStorage` — which overwrites the local cache from Redis unconditionally —
erased every drain within a refresh cycle while the endpoint still reported `drained=N`.
**Anything that must outlive a storage refresh cannot live on the score.** Same trap as the
circuit breaker's `refreshFromRedis`, approached from the other direction.

**The gate is `GetScores` → `IsInCooldown()`** in `protocol/shannon/reputation.go`, not
`FilterByScore` — that one only compares `Value` against the threshold and ignores cooldown
entirely. A test asserting on `Score.CooldownUntil` proves nothing about whether selection
will honour a drain; assert through `GetScores`.

**Not a penalty.** `Value`, `CriticalStrikes` and `RecentCriticalRate` are left untouched, so
the quality signal stays readable *while* the drain is in effect — which matters, because
reading it is usually the entire point of draining. A drain that rewrote the score would
destroy the measurement it exists to enable.

**Release is deliberately narrow:** it lifts only cooldowns the drain itself wrote and that
nothing has overwritten since. A cooldown earned for real while the drain was up survives —
otherwise "undo my experiment" would silently un-bench a legitimately failing endpoint.

**Known limit — a drain is not airtight.** The cooldown lives on the score, so an endpoint the
reputation service has never observed is treated by selection as "initial score, not in
cooldown" and stays selectable. The response carries `unscored_warning` rather than letting a
partial drain read as complete. On a service with health checks running everything is scored,
so it is usually complete.

Per-pod in-memory state like the other admin endpoints — issue it to **each pod**. Expires on
its own; does not survive a restart.

**Circuit Breaker — when to use:**
- After deploying a fix for a bug that caused false positive circuit breaker lockouts
- When a domain is stuck in circuit breaker state due to a transient issue that has resolved
- Rolling restarts alone don't work because `refreshFromRedis` repopulates in-memory state from Redis

## WebSocket Frames Are Reward-Eligible Relays

**Every endpoint→client WebSocket frame is signed by the relay miner and mined as a
reward-eligible relay**, paired with the *most recent* request. poktroll
`pkg/relayer/proxy/websockets/bridge.go`:

> Each message (inbound or outbound) is treated as a reward-eligible relay. For example, with
> eth_subscribe, both the initial subscription request and each received event would be
> eligible for rewards. […] Currently, the RelayMiner is paid for each incoming and outgoing
> message transmitted.

PATH validates that signature in `validateEndpointWebsocketMessage` → `ValidateRelayResponse`
(`protocol/shannon/websocket_context.go`).

**The asymmetry:** HTTP is 1 client request = 1 relay, client-driven. A WS subscription is 1
signed request = **unbounded** relays, and the push rate is chosen by **the supplier being
paid**. PATH signs the anchoring subscribe with its *own* application key, so the gateway's app
stake funds it. The only brake is `relayMeter.IsOverServicing(...)` — the application's
per-session allowance — and `path_supplier_exhausted_total` was **0 fleetwide** when checked,
so that brake is dormant.

Consequence: a per-domain frames/s number is closer to a **settlement-volume** meter than a
demand meter. Do not reason about it as load on the supplier.

### `path_websocket_connection_frame_rate` — the distribution, not the sum

Histogram `{service_id, domain}`, every live connection observed every 15s, buckets
`0.1 … 2500`. Emitted from the same `sampleRates` pass that feeds the tumble ranking, so the
two can never disagree.

**Why it exists:** `path_websocket_messages_total` is a per-domain SUM, and a sum cannot tell
*one firehose plus a hundred idle sockets* apart from *a hundred ordinary subscribers*. Those
have opposite explanations and the difference is not academic — measured fleetwide, one
operator held **16.9% of WS connections and earned 66.1% of WS relays** (3.9× over-index),
while on gnosis **2 connections out of ~70 carried ~97% of frames**. A handful of connections
can produce that entire fleetwide number without the operator doing anything.

```promql
histogram_quantile(0.5,  sum by (le, domain) (rate(path_websocket_connection_frame_rate_bucket{service_id="gnosis"}[10m])))
histogram_quantile(0.99, sum by (le, domain) (rate(path_websocket_connection_frame_rate_bucket{service_id="gnosis"}[10m])))
```

**Read it as:** p50 ≈ 0 with p99 in the hundreds → a few firehoses landed there, a placement
artifact, nobody is doing anything. p50 materially above other operators → systematic across
that operator's whole connection population.

**Idle connections are observed at 0 deliberately.** Dropping them would leave the quantiles
describing only the connections that carry traffic — exactly the population the metric exists
to be measured *against*.

**Trap:** clients do not choose their operator; selection assigns it. So which operator a
high-volume subscriber lands on is effectively a random draw, and any per-operator *average*
conflates "inflates every stream" with "the big streams landed here". Only the distribution —
or a same-client-different-operator comparison via a drain — separates them.

## Endpoint Selection — Registration-Weighted, Capped per Operator

A provider's share of a service follows the **supplier registrations** it holds, not the machines it runs. Each registration carries its own per-session service allowance, so registrations are both what a provider can actually serve and what the chain settles on. How a provider spreads its registrations across its own infrastructure is not a routing input.

Selection resolves to a concrete supplier — relays are signed against a supplier's session — and spreads across the registrations behind a chosen backend rather than pinning one, so allowance consumption is shared.

**This reversed an earlier design** that weighted by distinct backend URL. Measured across all 64 production pools, machine-weighting allocated **33.4% of traffic on average (worst 51.4%) beyond what the receiving provider's allowance could serve**, while starving providers who held the tickets: one provider holding 19 of 50 registrations on a service received 7.3% of its traffic, against 45% for a provider holding 15. Registration-weighting is 0% by construction.

**Per-operator cap: `max_operator_share`, default `0.50`.** This is the mechanism that stops one provider owning a session, and the only thing that should be tuned for that purpose. The largest provider holds ~71% of registrations fleet-wide and lands at ~51% of traffic under it.

**Displacement ceiling: 3× (`DefaultDisplacementCeilingMultiple`).** The cap moves a dominant provider's excess onto everyone under it — but a provider handed far more than its own registrations entitle it to cannot serve it. Receivers are capped at 3× their entitlement, and excess nobody can absorb **stays with the capped provider**: moving it anyway only produces 429s and a retry. Without this, a 49-vs-1 pool allocated the single-registration provider 17.5× its allowance.

**Two-operator pools sit at 0.65.** `0.50 × 2 = 1.0` is exactly the infeasibility boundary, so the tightened cap cannot apply to them; they keep the previous cap rather than being forced to an even split.

**Off-switches:**
```bash
PATH_OPERATOR_SHARE_BY_BACKEND_URL=false   # flat registration pick, no cap
PATH_BACKEND_REGISTRATION_WEIGHT_CAP=1     # machine-weighted (the reversed design)
PATH_BACKEND_REGISTRATION_WEIGHT_CAP=2     # bounded middle: min(registrations, 2) per backend
PATH_PRIMARY_PICK_OPERATOR_CAP=false       # basis only, no cap on the serving pick
PATH_MAX_OPERATOR_SHARE=0.65               # move the cap without a config rollout
```

**What to watch:** `path_supplier_exhausted_total` — over-servicing is opt-in for the supplier and a 429 just moves the request on, so a spike is inefficiency (wasted relays) rather than failure. Also `path_selection_pool_size{path="diversity"}`, which reports the pool in the weighting currency and is the quickest confirmation the basis in force is the one you think.

**Dry run before changing any of this:** `go test ./qos/selector/ -run Test_ProductionDryRun -v` replays the real pools through the shipped selector and gates on nobody dropped, nobody stranded, nobody allocated past both the cap and their own entitlement.

### Is the cap engaging on WebSocket? Read the `path` label

The cap covers WebSocket selection already — a WS connection reaches it through a QoS type's single-endpoint `Select` (`gateway/websocket_request_context.go` → `qos/*/…Select` → `SelectWithConcentrationCap`), while HTTP reaches it through `SelectMultipleWithArchival` → `SelectEndpointsWithDiversity`. **Do not add a second cap for WebSocket; it is the same cap.**

The two are separable on `path_concentration_cap_reshaped_total{service_id, path}`:

- `path="concentration_cap"` — the **WebSocket** selection path.
- `path="diversity"` — the HTTP serving pick.

Same `path` vocabulary as `path_selection_pool_size` / `path_selection_selected_total`, so pool composition and reshape counts join on it. Before this label existed the two shared one series, and HTTP — running three to four orders of magnitude more selections per second — completely masked whether the cap ever engaged on a WebSocket selection.

**A near-zero WS series is not proof the cap is broken.** Three things concentrate WebSocket traffic that no cap can touch, and they should be excluded before touching selection:
1. **A WS connection binds one endpoint for its lifetime.** Selection governs only *new* connections; existing ones move only on rollover / stall / `session_expired` / `POST /admin/websocket/tumble/{svc}`.
2. **Reputation removes whole operators from the pool before the cap sees it** (`path_reputation_disqualified_total{rpc_type="websocket"}`). The cap water-fills across survivors; it cannot restore what the floor deleted.
3. **Supply** — some services have only one operator offering WS endpoints at all.

## Concentration Cap on the Retry / Hedge Paths

The per-operator (eTLD+1) cap governs **primary** selection. Retry, hedge and batch-item picks come from the top-reputation-score band, which was weighted within the band but **not capped by operator** — so an operator holding most of the band took most of the retries and hedges, on the two paths whose entire purpose is to reach different infrastructure than the attempt that just failed.

**Lowering `max_operator_share` does not close this.** The cap was never the binding constraint on the band paths; it simply did not run there.

**Size it honestly.** Measured 2026-07-30: primary **2184/s**, retries **209/s**, hedges that fired and won **25/s**. The cap-exempt paths are ~10% of selections, not the majority.

**Ships ON.** It reweights the band and never filters it, so a retry's reachable set is unchanged at any cap value — the risk is bounded by construction rather than by the flag. **Disable** for one service (or via `defaults:`):
```yaml
services:
  - id: <service>
    cap_retry_hedge_selection: true
```
**Process-wide override** (a pod restart instead of a config-map edit per service, for flipping while watching a dashboard):
```bash
PATH_CAP_RETRY_HEDGE_SELECTION=true   # or =false to force off everywhere
```
Unset leaves config in charge. The share value itself is still `max_operator_share`; this key only decides whether the band paths consult it.

**Metric** — a separate counter rather than another value in `path_concentration_cap_reshaped_total`'s `path` label, so the primary path's already-nonzero series stay a valid baseline. The two counters keep independent `path` vocabularies: `retry|hedge|batch` here, `diversity|concentration_cap` (HTTP vs WebSocket) there.
```
path_concentration_cap_band_total{service_id, path="retry|hedge|batch", outcome}
```
Recorded on **every** band pick, so `outcome="reshaped"` over the total is the real engagement rate. Retry and hedge differ by an order of magnitude in volume — never sum them.

`outcome` values: `reshaped` (distribution altered) · `no_op` (multi-operator band, none over cap) · `degraded_no_room` (band collapsed to one operator or one candidate — pick left **uncapped**, also logged at Debug) · `disabled` (the cap value itself is off) · `no_candidates` (empty band; the caller falls back to an uncapped pick and warns).

`degraded_no_room` is **expected, not an error** — a retry has already excluded the operators it tried. It is what separates "the cap is doing nothing" from "the cap had no room to do anything", and only the latter is a reason to change the cap value.

**Cannot starve a retry:** the cap reweights the band, it never filters it. The set of endpoints a retry can reach is bit-for-bit what it was before, at any cap value.

**What to watch after enabling:** `path_supplier_exhausted_total` for the **thin** operators the excess lands on, not the capped one — a solo-registration backend gains share while still holding one supplier's per-session allowance. Same failure mode as the backend-URL dedup, and self-correcting. Retry success rate — `path_relays_total{request_type="retry"}` split by `status_code` — must not fall; roughly 60% of retries already fail, so that pool is marginal to begin with.

## Testing Strategy

- **Unit Tests** - Standard Go tests with `-short` flag
- **E2E Tests** - Full integration tests against live blockchain endpoints
- **Load Tests** - Performance testing using Vegeta load testing tool
- **Protocol Tests** - test suites for Shannon protocol
