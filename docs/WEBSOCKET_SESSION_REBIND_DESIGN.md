# Design: WebSocket Session Rebind (survive Shannon session rollover)

Status: **Phase 0 (grace tuning) + Phase 1 (subscription registry) + Phase 2 (bridge
reconnect engine + Shannon provider/registry wiring) implemented, gated OFF by env
`PATH_WEBSOCKET_SESSION_REBIND`.** Phase 0 grace tuning measurably lengthened WS
connection lifetime and reduced boundary closes with no rise in HTTP boundary-error rate.
Remaining: enable the rebind path and validate the live reconnect (session re-fetch,
dial, handshake) under real traffic.

Origin: investigation of WebSocket teardown at Shannon session boundaries.

## Implementation status

- ✅ **Phase 1 — `websockets/subscription_registry.go`**. Pure JSON-RPC
  subscription tracker: records eth_subscribe for replay, freezes the client-facing
  subscription id, remaps notifications (current→client id), rewrites eth_unsubscribe,
  swallows replay responses. 12 unit tests.
- ✅ **Phase 2 seam — `websockets/connection.go`**. Connection disconnect
  is an injected `onDisconnect(error)` callback (was a hardcoded `cancelCtx()`). Pure
  refactor, behavior identical.
- ✅ **Phase 2 engine — `websockets/bridge.go` + `bridge_reconnect.go`**.
  Optional `EndpointReconnector`: endpoint disconnect → dedicated child-ctx endpoint loops
  → generation-tagged reconnect with bounded retries+backoff → replay subscriptions → client
  stays open. Falls back to 1012 on exhaustion. Bridge also drops a swallowed (nil) endpoint
  message. `reconnector == nil` everywhere in production today, so no behavior change.
  Reconnect / retry-exhaustion / swallow paths race-tested with in-process WS servers.

- ✅ **Phase 2 Shannon wiring — `protocol/shannon/websocket_context.go` + `protocol.go`**.
  wrc implements `EndpointReconnector` (ReconnectEndpoint via
  `getPreSelectedEndpoint` for the current session + re-signed handshake dial;
  SubscriptionReplayFrames re-signs the registry's active subscribes). Registry driven in
  `ProcessProtocolClientWebsocketMessage` (track/rewrite before signing) and
  `ProcessProtocolEndpointWebsocketMessage` (remap notification ids, swallow replay
  responses via nil body). **The `selectedEndpoint` race was avoided WITHOUT the mutex
  refactor** (see resolved note below). Gated off by env `PATH_WEBSOCKET_SESSION_REBIND`;
  nil-by-default means byte-for-byte unchanged behavior. Builds + vet + race green.

### Resolved — the `selectedEndpoint` race (WS-only field, no mutex refactor)

The earlier "~20-site RWMutex" plan was an overcount: ~18 of those reads run on the
bridge's single message-processing goroutine (serialized with reconnect). The ONLY
concurrent reader is the connection-observation goroutine, and it needs only cosmetic
endpoint info. Solution: keep `selectedEndpoint` IMMUTABLE (write-once → the observation
goroutine reads it race-free) and add a `reconnectEndpoint` field that reconnect mutates.
A `signingEndpoint()` helper returns `reconnectEndpoint` if set else `selectedEndpoint`;
only the 4 signing/validation reads use it, and those + the reconnect writer are all on
the one bridge goroutine → no mutex. ~4 localized edits instead of a repo-wide refactor.

### Remaining — enable + validate on canary

Set `PATH_WEBSOCKET_SESSION_REBIND=true` on canary. Unit tests cover the registry and the
bridge reconnect/replay/swallow; what only canary can prove is the LIVE reconnect:
`getPreSelectedEndpoint` re-fetching a real session, dialing the miner, and the re-signed
handshake being accepted. Watch `path_websocket_connection_events_total{event}` (fewer
forced reconnects), messages-per-connection (up), and reconnect-failure fallbacks.

### Wiring detail

The `websocketRequestContext` (wrc) becomes the `EndpointReconnector` and drives the registry:

1. **`ReconnectEndpoint(ctx)`**: call a `reconnectProvider` closure (captured at build:
   `p.getPreSelectedEndpoint(ctx, serviceID, selectedEndpointAddr, httpReq, WEBSOCKET)` — this
   already re-fetches the CURRENT session and returns the same-supplier endpoint bound to it,
   erroring if that supplier is not in the new session → reconnect fails → 1012 fallback), set
   the current endpoint, rebuild URL+headers+handshake sig (reuse `getWebsocketEndpointURL` /
   `getWebsocketConnectionHeaders` / `generateHandshakeSignature`), dial via
   `websockets.ConnectWebsocketEndpoint`.
2. **`SubscriptionReplayFrames()`**: `registry.ActiveSubscribeFrames()` → sign each with
   `signClientWebsocketMessage` (uses the now-current session) → return.
3. **Drive registry**: in `ProcessProtocolClientWebsocketMessage`, `registry.TrackClientMessage`
   BEFORE signing (tracks subscribe, rewrites unsubscribe). In
   `ProcessProtocolEndpointWebsocketMessage` (2xx path), `registry.TranslateEndpointMessage` on
   the decoded body; if `forward == false`, return a nil body so the bridge swallows the replay
   response.
4. **Wire**: pass `wrc` (not nil) as the reconnector to `StartBridge` when enabled; build the
   registry + reconnectProvider in `BuildWebsocketRequestContextForEndpoint`.
5. **Flag**: gate on a Protocol bool (env `PATH_WEBSOCKET_SESSION_REBIND=true` for the canary
   toggle; promote to YAML once validated). Default off → registry/reconnector nil → today's
   behavior.

(The `selectedEndpoint` mutation race that this step first appeared to require a wide mutex
refactor for was instead solved by the WS-only `reconnectEndpoint` field + `signingEndpoint()`
helper — see the "Resolved" note above.)

## Problem (mechanical, evidence-locked)

A PATH WebSocket connection is **pinned to one Shannon session for its entire life** and
never rebinds:

- The session is fetched once at connect (`getPreSelectedEndpoint` → `getActiveGatewaySessions`
  → `getSession` → `GetSession`), baked into `selectedEndpoint`, and every client frame is
  signed with `selectedEndpoint.Session().GetHeader()` forever (`signClientWebsocketMessage`).
- When the chain crosses that session's end (+ the supplier's on-chain grace), the relay miner
  **proactively** closes the endpoint WS with HTTP 410 `{"error":"session expired"}` + close
  code **4000** (observed: a fresh connection received the error unprompted, 0 client frames
  sent — so the miner ties the WS to session N and closes at N's expiry). PATH's bridge shares
  one `cancelCtx` between the client and endpoint connections, so the endpoint drop tears the
  client down too.

One hypothesis is **refuted by evidence**, one is a **real contributor**:
- **REFUTED — session-math / grid-anchor miscalc.** HTTP and WS share the identical session
  machinery, and HTTP runs at orders of magnitude higher relay volume. If the grid were wrong
  HTTP would fail too. It doesn't.
- **CONTRIBUTOR — the rollover grace (amplified by 60→20).** `extendedSessionEnabled=false`
  (session.go) only disables the `getSession` path; but `getCentralizedGatewayModeActiveSessions`
  (mode_centralized.go:67 — prod is centralized) and `getDelegatedGatewayModeActiveSession`
  (mode_delegated.go:70) call `GetSessionWithExtendedValidity` **directly** when
  `IsInSessionRollover()` is true. The rollover window is `[sessionEnd-1,
  sessionEnd+session_rollover_blocks]` ≈ 11 blocks; at 20-block sessions that is ~55% of every
  session (was ~18% at 60 blocks). During rollover, `GetSessionWithExtendedValidity` serves the
  **previous** session for the first 80% of grace (`gracePeriodScaleDownFactor=0.8`). A WS
  connecting during rollover binds the previous session → the supplier may reject it → 4000. So
  short sessions + a now-oversized rollover window + a generous scale-down together raise the
  probability that a new WS connection is born on a soon-rejected session.

### Phase 0 — quick tuning mitigation (config + constant; canary)

Independent of the rebind, reduce how long/often PATH hands out the previous session:
- **`session_rollover_blocks`** 10 → ~3-5 (config; also `defaultSessionRolloverBlocks`) so the
  rollover window is a small fraction of a 20-block session, not half of it.
- **`gracePeriodScaleDownFactor`** 0.8 → **0.2** (fullnode_cache.go:76). On-chain
  `grace_period_end_offset_blocks=10`, so this is 8 → **2 blocks** of serving the previous
  session. The rollover grace existed for an era when suppliers were slow to materialize the new
  session; that is resolved, so switch to the new session as soon as possible. The floor is
  supplier new-session adoption speed — too small and PATH signs the new session before a lagging
  supplier has it ("session not found/not active", affects HTTP too).
- **Cleanest, WS-scoped:** make the WS path always request the CURRENT session (bypass
  extended-validity). WS is long-lived, so binding the previous session is always wrong;
  HTTP (ms-lived) legitimately benefits from grace and is untouched. Requires a
  force-current/websocket flag threaded through `getActiveGatewaySessions` →
  `getCentralizedGatewayModeActiveSessions`.

CAVEAT: grace exists so in-flight relays signed with the old session survive the boundary
(HTTP resilience). Too small → the opposite failure (PATH on the new session before the supplier
is). Tune against the supplier's real grace and CANARY: WS 4000 rate should fall while HTTP
boundary-failure rate must not rise. Phase 0 reduces frequency; only the rebind (below) removes
the teardown.

**Impact.** Teardown is intermittent, not catastrophic: a reconnecting client still observes a
healthy newHeads stream with sub-1% downtime. But shortening the session length (60→20 blocks)
tripled boundary frequency, and near-boundary connects die in seconds. A client that does not
reconnect promptly loses its stream; the churn is invisible in `path_relays_total` (WS is a
separate metric family).

## Goal

Keep the **client** WebSocket open across Shannon session boundaries. When the endpoint
connection is torn down by a session roll, PATH transparently:
1. re-fetches the current session and selects an endpoint (supplier) valid for it,
2. establishes a new endpoint connection,
3. replays the client's active subscriptions to the new supplier,
4. remaps subscription IDs so the client keeps the IDs it already holds,
5. resumes bridging — the client observes at most a brief gap, never a close.

Non-goal (Phase 1): zero-gap failover. A short gap (one reconnect RTT) during rollover is
acceptable; correctness (no lost subscriptions, stable IDs) is the bar.

## Why re-signing on the same connection is NOT enough

The miner closes the endpoint WS at session-N expiry regardless of what session subsequent
frames carry (it spoke first, before any client frame). So the connection itself must be
re-established against a supplier in the new session. If the same supplier is in the new
session we may reconnect to the same URL; if not, we reconnect to a different supplier — either
way the endpoint socket is new, so subscriptions (server-side state) must be replayed.

## Architecture

Three concerns, cleanly separated by layer:

### 1. Subscription registry + ID remapper — `websockets/subscription_registry.go` (specified, not yet built)

JSON-RPC subscription state is the only per-connection server-side state we must reconstruct.
The registry (pure, no I/O, unit-tested) tracks, per bridge:

- **client→endpoint**: an `eth_subscribe` request (by its JSON-RPC `id`) and its params, so it
  can be replayed verbatim after a reconnect.
- **endpoint→client**: the subscribe *response* whose `result` is the supplier's subscription
  id. The FIRST id returned is the **client-facing id** — the value the client now holds and
  will use forever. Subsequent (post-reconnect) ids are internal only.
- **remap outbound notifications**: `{"method":"eth_subscription","params":{"subscription":
  "<currentSupplierId>", ...}}` → rewrite `subscription` to the client-facing id.
- **remap inbound unsubscribe**: `eth_unsubscribe(<clientFacingId>)` → rewrite the param to the
  current supplier id before forwarding.
- **replay set**: on reconnect, emit each active subscribe request; on each new response, update
  `currentSupplierId → clientFacingId`.

Layering note: subscription semantics are JSON-RPC/EVM-specific, so the registry is invoked by
the subscription-aware message path (gateway/QoS), not baked into the protocol-agnostic frame
pump. The bridge only asks "does the current message need rewriting?" through an interface.

Scope: single (non-batch) `eth_subscribe`/`eth_unsubscribe`, which is the near-universal shape.
Batch-embedded subscribes are logged and left un-tracked (they degrade to today's behavior:
lost on rollover) — rare enough to defer.

### 2. Endpoint reconnect hook — `websockets/bridge.go` (staged)

Decouple the endpoint connection lifecycle from the client/bridge lifecycle:

- Give the endpoint connection its **own** cancel scope, distinct from the client's. An endpoint
  drop no longer cancels the client.
- On endpoint disconnect, classify: **recoverable** (session-expiry: close 4000 / "session
  expired", or a clean endpoint EOF while the client is still live) vs **fatal** (client closed,
  context canceled, repeated reconnect failure).
- On recoverable: call a `ReconnectEndpoint(ctx) (*websocketConnection, error)` callback
  (injected by the protocol layer), then replay the registry's subscriptions over the new
  connection and resume the pump. Bounded retries with backoff; on exhaustion, fall back to the
  current behavior (close the client with 1012 "service restarting, please reconnect").

### 3. Reconnect provider — `protocol/shannon/websocket_context.go` (staged)

Implements `ReconnectEndpoint`: re-runs `getPreSelectedEndpoint` for the **current** session
(fresh `getActiveGatewaySessions`), re-selects an endpoint (preferring the same supplier if it
is in the new session, else a new one), opens a new endpoint WS, and returns it. Re-signing uses
the already-per-frame signer, now bound to the refreshed session.

## Proactive vs reactive rebind

Phase 1 is **reactive** — reconnect after the miner closes (simplest, provably correct). A later
enhancement is **proactive**: PATH knows `SessionEndBlockHeight`; it can open the next endpoint
connection ~1 block before expiry and cut over with near-zero gap. Deferred; reactive first.

## Risks

- **Duplicate notifications across the cutover** — a head delivered on both the old and new
  connection. Acceptable (clients dedupe by block); document it.
- **Subscribe replay storms** — bound reconnects (retry/backoff cap) so a supplier that always
  rejects can't loop.
- **Registry memory** — bounded by subscriptions per connection; unsubscribe + connection close
  evict.
- **Batch/unknown subscription shapes** — degrade to today's behavior, never crash.

## Test / rollout plan

- Unit: subscription registry — subscribe/response/notification remap, unsubscribe translation,
  replay set, id stability across reconnect, batch-degrade, eviction. (Implemented.)
- Integration (staged): a mock endpoint that closes 4000 mid-stream; assert the client stays up,
  subscriptions replay, and notification ids are stable.
- Canary: enable behind a config flag (`websocket_session_rebind`), watch
  `path_websocket_connection_events_total{event="established"}` (should drop — fewer forced
  reconnects), messages-per-connection (should rise), and reconnect-failure fallbacks.

## Phasing

1. **Core (this PR):** subscription registry + ID remapper + unit tests. Zero behavior change
   (not yet wired).
2. **Wiring (next PR, canary):** bridge endpoint-cancel decoupling + `ReconnectEndpoint` hook +
   protocol reconnect provider + integration test + config flag.
3. **Proactive cutover (later):** pre-expiry reconnect for near-zero gap.
