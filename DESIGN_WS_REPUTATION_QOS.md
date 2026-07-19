# Design: WebSocket Reputation / QoS Layer

**Status:** Stage 0 + Stage 1 implemented (branch `feat/ws-reputation-s0-s1`, off `f5b14d65`); Stages 2–3 planned
**Author:** PATH team
**Related:** `feat/ws-session-rebind`, `feat/ws-stall-watchdog` (`f5b14d65`), `websocket_relay_instrumentation_gap`

---

## 1. Problem

Some suppliers consistently fail to connect or serve data over WebSocket, yet WS
endpoint selection keeps picking them. The HTTP/JSON-RPC path has a full reputation
loop (score, cooldown, critical strikes, tiered selection, circuit breaker); the
WebSocket path does not consume any of it. The failure signals we *do* collect are
either never recorded as reputation, or recorded and then ignored at selection time.

Call it **double amnesia**:

| WS failure signal | → metric | → reputation | consumed at selection |
|---|---|---|---|
| initial `ws_connection_failed` | — | ✅ `websocket_context.go:590` | ❌ `filterByReputation=false` |
| `ws_headers_failed` / `ws_signature_failed` | — | ✅ `:541` / `:555` | ❌ |
| `ws_message_validation_failed` | — | ✅ `:1011` | ❌ |
| silent stall (watchdog) | ✅ `path_websocket_endpoint_stall_total` | ❌ none | ❌ |
| rebind dial-fail | ✅ `path_websocket_rebind_total{result=failed_dial}` | ❌ none | ❌ |

Consequence: the stall watchdog shipped in `f5b14d65` makes the *current*
connection escape a stalling supplier (`avoidCurrentSupplier=true`), but nothing
persists that judgment. The next fresh WS client for the same service can be
selected straight back onto the same bad supplier — the system has no memory
between connections.

---

## 2. What already exists (and works in our favor)

1. **Reputation keys are already RPC-type-scoped.** `reputation/reputation.go:32`
   — `EndpointKey{ServiceID, EndpointAddr, RPCType}`; key format
   `serviceID:supplier-url:rpcType` (`reputation/key.go:36`). A `:websocket` score
   is tracked *separately* from `:json_rpc` for the same endpoint. WS signals that
   are recorded today already land on `:websocket` keys — they are simply never read.

2. **The threshold filter already tolerates cold start.** `filterByReputation`
   (`protocol/shannon/reputation.go`) passes through any endpoint with no score
   (`!exists → include`); `InitialScore = 80` is above `MinThreshold`. It only
   *removes* endpoints whose score fell below threshold or that are `IsInCooldown()`
   (strike system). So enabling it for WS would **not** over-filter never-tried
   endpoints — it would drop only proven-bad ones.

3. **Cooldown / strike machinery is generic.** `CriticalStrikes >=
   DefaultStrikeThreshold` → cooldown (`reputation/service.go:141-144`);
   `IsInCooldown()` gates the filter. Nothing about it is HTTP-specific.

4. **New per-domain WS health telemetry now flows.** From the rebind + watchdog
   work: `path_websocket_rebind_total{domain,service_id,result}` and
   `path_websocket_endpoint_stall_total{domain,service_id,result}`. These are the
   raw signals a WS QoS loop needs — currently metrics only.

### ⚠️ Metric caveat — which per-domain series reflect the *current* supplier

`wrc.selectedEndpoint` is **immutable** (zero write sites); rebind writes a separate
`wrc.reconnectEndpoint`, and `signingEndpoint()` returns reconnect-or-original
(`websocket_context.go:102-105, :758`). Consequences for keying WS health off metrics:

- **`path_websocket_connections_active{domain}` attributes a connection's whole
  lifetime to its ORIGINAL supplier**, not whoever it rebound onto. It reads
  "started on X, still alive" — NOT "currently talking to X." Do **not** use it as a
  per-supplier current-load or current-health signal.
- **Use the `signingEndpoint()`-based series for current-supplier health:**
  `path_websocket_rebind_total`, `path_websocket_endpoint_stall_total`, and the
  reputation signals from `recordWebsocketSignal` — these all key off the *current*
  endpoint. Stage 0 must record its new signals via `recordWebsocketSignal` /
  `signingEndpoint()` for the same reason, so the `:websocket` score reflects the
  supplier actually serving the connection at failure time.
- The active gauge itself is provably correct for counting (per-domain Inc/Dec
  balanced, `established − closed == active`, no leak) — the caveat is only about
  *semantic attribution* after a rebind, not accounting.

---

## 3. Why WS selection is unfiltered today

`protocol/shannon/protocol.go:281`:
```go
filterByReputation := rpcType != sharedtypes.RPCType_WEBSOCKET
```
and the reconnect path hardcodes `const filterByReputation = false`
(`protocol/shannon/websocket_context.go:352`).

Original rationale (comments at `protocol.go:279-281`, `:654`): WS has **no active
health checks**. The hydrator probes HTTP endpoints to keep scores fresh even while
idle; it does not probe WS. So the worry was that filtering on scores nobody keeps
current would exclude endpoints based on absent/stale data.

That rationale is now **partly outdated**: WS connect/handshake/message signals
*are* recorded on live client traffic (§1), and the filter already passes
unscored endpoints (§2.2). The genuinely-missing piece is active probing for
*idle* endpoints (Stage 3) — not a reason to ignore the scores we do have.

---

## 4. Key structural constraint

`filterByReputation` is a **single bool that gates four subsystems** in
`getUniqueEndpoints` / selection:

| Line | Subsystem gated |
|---|---|
| `protocol.go:1188` | `supplierBlacklist` skip |
| `protocol.go:1220` | `sessionExhaustion` skip |
| `protocol.go:1256` | reputation threshold + cooldown filter |
| `protocol.go:1290` | `tieredSelector` (tier 1/2/3 ranking) |

**Implication:** flipping the existing bool to `true` for WS turns on *all four* at
once — that is the full HTTP treatment, not a conservative first step. A staged
rollout that wants "exclude the known-bad but don't tier yet" needs finer control
than the current bool (see Stage 1).

---

## 5. Staged design

Ordered low → high risk. Each stage is independently shippable and observable.

### Stage 0 — Make WS scores truthful (data only, no behavior change) — ✅ IMPLEMENTED

Route the two strongest WS-failure signals into reputation, so the `:websocket`
score reflects reality:

- **Silent stall** — `OnEndpointStallDetected` records
  `recordWebsocketSignal(reputation.NewMajorErrorSignal("ws_endpoint_stalled", 0))`
  alongside the metric. Attribution is `signingEndpoint()` = the stalling supplier
  (the watchdog fires before the rebind swaps in a replacement). Chose **Major** on
  every stall (not Critical-on-giveup) for consistency — each stalling supplier gets
  one Major as it stalls (see open question 1).
- **Rebind dial-fail** — at the reconnect dial failure (`websocket_context.go:785`,
  `WSRebindFailedDial`), records
  `NewMajorErrorSignal("ws_reconnect_dial_failed", 0)`. `reconnectEndpoint` is
  already set to the failed `ep`, so `signingEndpoint()` attributes to the endpoint
  that could not be reached.

No selection change. Ship, then watch `:websocket` scores for known-bad suppliers
diverge downward before trusting them. This de-risks every later stage.

**Risk:** near-zero. Only adds signal recording (already fire-and-forget).

### Stage 1 — Hard-stop the known-bad (disqualify-only) — ✅ IMPLEMENTED

Exclude WS endpoints that are **in cooldown** or **below threshold**, but do **not**
tier/rank yet. No code enum was needed: of the four gates (§4), **only tiering must
stay off for WS** — blacklist, session-exhaustion, and threshold/cooldown are all
desirable for WS. So the implementation is surgical:

1. **Flip the two WS entry points on** — `filterByReputation = true` at
   `websocket_context.go:281` (initial connect) and `:352` (reconnect).
2. **Guard tiering off for WS at the gate** — add `filterByRPCType !=
   RPCType_WEBSOCKET` to the `tieredSelector` condition (`protocol.go:1290`). This
   is more precise than option (b)'s global-config approach and needs no new flag —
   the gate itself knows the rpc type.
3. **WS-only safety net** on the reputation filter (`protocol.go:1256`): if the
   filter would leave the WS pool empty, keep the pre-filter set as last resort
   (mirrors the session-exhaustion net at `:1233`). Disqualify is best-effort for WS
   — a transient score dip must never sever connectivity while active WS health
   checks don't exist.

Applies to **both** the initial connect path and `getReconnectEndpoint`. On the
reconnect path the preferred (original) supplier still gets the requestedEndpointAddr
race-protection pass even if in cooldown; the stall watchdog's `avoidPreferred` path
deletes it explicitly, so a proven-stalling supplier is still escaped.

**Risk:** low, bounded by the InitialScore=80 pass-through for unscored endpoints and
the safety net. Watch item: a service where *most* WS endpoints scored low would
trip the safety net often — add a counter/log on the net firing (TODO) and alert.

### Stage 2 — Rank, don't just exclude

Enable tiered / score-ordered selection for WS. `selectBestReconnectEndpoint`
currently ranks only by non-fallback + deterministic addr tiebreak, with an
explicit TODO to use reputation/head (`websocket_context.go:396-403`). Replace the
tiebreak with `:websocket` score (and, once available, head freshness). Requires
Stage 0 data to be meaningful.

**Risk:** medium — ranking amplifies score quality; only trustworthy after Stage 0
has been observed to produce sane scores.

### Stage 3 — Active WS health checks

Teach the hydrator to probe WS connectivity (dial + subscribe + expect ≥1
notification within N s), so *idle* endpoints carry fresh `:websocket` scores.
Removes the cold-start caveat entirely (§3) and makes full tiering safe for
endpoints that haven't recently had live client traffic.

**Risk:** highest effort. New probe type, cadence/cost tuning, and care not to hold
long-lived subscriptions per endpoint. Deferred until Stages 0–2 prove the loop.

---

## 6. Cold-start risk (the one real hazard)

The reason the flag was ever `false`. Handled across stages:

- Unscored endpoints already pass (`!exists → include`, InitialScore=80) — Stage 1
  never excludes a fresh endpoint.
- Stage 1 disqualifies only *proven*-bad (cooldown / below-threshold from real
  recorded failures) — no ranking on absent data.
- All-filtered fallback (mirror `STANDARD_FALLBACK_RANDOM`) prevents "empty pool →
  1012" when a whole service's WS endpoints score low.
- Stage 3 eliminates the caveat by keeping idle scores fresh.

---

## 7. Validation & metrics

- **Stage 0:** watch `:websocket` scores for suppliers that show up in
  `path_websocket_endpoint_stall_total` / `{result=failed_dial}` — confirm they
  degrade. No user-facing change expected.
- **Stage 1:** expect `path_websocket_rebind_total{result=failed_dial}` and
  `endpoint_stall_total` to *drop* (bad suppliers no longer selected). Watch for an
  all-filtered fallback counter (add one) — if it fires often, threshold is too
  aggressive for WS.
- **Stage 2:** same/(same+different) rebind ratio and stall rate should improve
  further; head-freshness gap narrows.
- **Stage 3:** idle-endpoint score coverage; probe cost.

Add a WS-specific counter for "all endpoints filtered → fell back to unfiltered"
(Stage 1) — the WS analogue of the HTTP fallback leak we already track.

---

## 8. Recommendation

Ship **Stage 0 + Stage 1(a)** as the next WS PR *after* the stall watchdog validates
on main. Together they directly kill the "consistently fail to connect/work" leak:
Stage 0 makes the score honest, Stage 1 stops selecting proven-bad suppliers — both
reuse the existing reputation infra (cooldown, strikes, Redis sync, per-service
config) with no new storage. Stages 2–3 follow once the score signal is trusted.

---

## 9. Open questions

1. ~~**Signal severity for stall.**~~ RESOLVED: Major on every stall (consistent —
   each stalling supplier accrues one Major as it stalls; no Critical-on-giveup
   special case). Revisit if stalls need to bench a supplier faster than −10/stall.
2. **Score sharing across RPC types.** Keys are `:websocket`-scoped today. Should a
   catastrophic HTTP failure (host unreachable) also suppress WS selection for the
   same endpoint, or keep them fully independent? A dead host is dead for both.
3. ~~**Stage 1 finer control.**~~ RESOLVED: no enum needed. Only tiering must skip
   WS, so a single `filterByRPCType != WEBSOCKET` guard at the tiering gate suffices;
   the other three gates (blacklist/exhaustion/threshold) run for WS as-is.
4. **All-filtered fallback semantics.** Stage 1 keeps the pre-filter set when the WS
   pool would empty (best-effort disqualify). TODO: add a counter/log when this net
   fires so an over-aggressive threshold is visible. Longer term, Stage 3 removes the
   need by keeping idle scores fresh.
