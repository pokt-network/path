# Session Rollover Enhancement Design

## Problem Statement

The current session rollover system has a critical flaw:

**During session rollover, it uses FALLBACK endpoints instead of merging current + extended session endpoints.**

Current behavior (`protocol/shannon/context.go:420`):
```go
case rc.fullNode.IsInSessionRollover():
    return rc.sendRelayWithFallback(payload)  // ❌ WRONG: Uses fallback only
```

**Expected behavior**:
- During rollover: Merge endpoints from current session + extended (previous) session
- Fallback endpoints: Only when 0 endpoints available OR all endpoints in probation

**Additional limitations**:
1. **Polling-based block monitoring**: Checks block height every 15 seconds, causing delays in detecting session transitions
2. **Extended session disabled**: `extendedSessionEnabled = false` hardcoded (protocol/shannon/session.go:21)
3. **No endpoint merging**: Doesn't combine current + extended session endpoints during grace period

## Current Architecture

### Session Rollover State (`protocol/shannon/fullnode_session_rollover.go`)
- **Polling**: `blockHeightMonitorLoop()` checks every 15s
- **Rollover Window**: `[sessionEnd - 1, sessionEnd + sessionRolloverBlocks]`
- **State Tracking**: `isInSessionRollover` boolean flag

### Endpoint Selection (`protocol/shannon/protocol.go:693-886`)
```
getUniqueEndpoints()
  → getSessionsUniqueEndpoints()
    → RPC type filtering (strict)
    → Reputation filtering
    → Tiered selection
```

### Current Rollover Handling
**Session retrieval** (`protocol/shannon/session.go:38-42`):
```go
if extendedSessionEnabled {  // Currently HARDCODED to false
    session, err = p.GetSessionWithExtendedValidity(ctx, serviceID, appAddr)
} else {
    session, err = p.GetSession(ctx, serviceID, appAddr)
}
```

**Relay handling** (`protocol/shannon/context.go:420`):
```go
case rc.fullNode.IsInSessionRollover():
    return rc.sendRelayWithFallback(payload)  // ❌ Uses fallback instead of extended session
```

**Problems**:
1. Extended session support exists but is **disabled**
2. During rollover: uses fallback instead of merging current + extended sessions
3. Fallback used even when extended session endpoints are available

## Proposed Solution

### Core Fix: Enable Extended Sessions During Rollover

**Goal**: During session rollover, merge endpoints from **current session + extended (previous) session**

**Key Changes**:
1. Enable `GetSessionWithExtendedValidity()` during rollover periods
2. Fetch BOTH current and extended sessions
3. Merge endpoints from both sessions
4. Apply reputation + RPC type filtering to merged set
5. Only use fallback when 0 endpoints from both sessions

### 1. WebSocket Block Monitoring

**Replace**: Polling-based `blockHeightMonitorLoop()`
**With**: WebSocket subscription to block events

```go
// protocol/shannon/fullnode_websocket_monitor.go

type blockHeightMonitor struct {
    logger        polylog.Logger
    ctx           context.Context
    wsClient      *sdk.WebSocketClient
    heightUpdates chan int64
    errorChan     chan error
}

func (m *blockHeightMonitor) start() {
    // Subscribe to new block events
    m.wsClient.SubscribeToBlocks(func(block *Block) {
        m.heightUpdates <- block.Height
    })
}

func (srs *sessionRolloverState) blockHeightMonitorLoop() {
    monitor := newBlockHeightMonitor(srs.ctx, srs.logger, srs.blockClient)
    monitor.start()

    for {
        select {
        case <-srs.ctx.Done():
            return
        case height := <-monitor.heightUpdates:
            srs.updateWithBlockHeight(height)
        case err := <-monitor.errorChan:
            srs.logger.Error().Err(err).Msg("WebSocket error, falling back to polling")
            // Fallback to polling on WebSocket failure
            srs.startPollingFallback()
        }
    }
}
```

**Benefits**:
- Instant detection of session transitions (< 1s vs. up to 15s)
- Reduces unnecessary polls by ~99%
- Automatic fallback to polling if WebSocket fails

### 2. Fetch Both Current and Extended Sessions During Rollover

**Update**: `getCentralizedGatewayModeActiveSessions()` to fetch both sessions during rollover

```go
// protocol/shannon/gateway_mode.go

func (p *Protocol) getCentralizedGatewayModeActiveSessions(
    ctx context.Context,
    serviceID protocol.ServiceID,
) ([]sessiontypes.Session, error) {
    logger := p.logger.With(
        "protocol", "shannon",
        "method", "getCentralizedGatewayModeActiveSessions",
        "service_id", serviceID,
    )

    sessions := []sessiontypes.Session{}

    // Always get current session
    for _, appAddr := range p.appAddresses {
        currentSession, err := p.GetSession(ctx, serviceID, appAddr)
        if err != nil {
            logger.Error().Err(err).Msgf("Failed to get current session for app %s", appAddr)
            continue
        }
        sessions = append(sessions, currentSession)
    }

    // During rollover: ALSO get extended (previous) session
    if p.IsInSessionRollover() {
        logger.Info().Msg("In session rollover - fetching extended sessions")

        for _, appAddr := range p.appAddresses {
            extendedSession, err := p.GetSessionWithExtendedValidity(ctx, serviceID, appAddr)
            if err != nil {
                logger.Warn().Err(err).Msgf("Failed to get extended session for app %s", appAddr)
                continue
            }

            // Only add if it's different from current session (previous session)
            if extendedSession.SessionId != currentSession.SessionId {
                sessions = append(sessions, extendedSession)
                logger.Info().
                    Str("extended_session_id", extendedSession.SessionId).
                    Str("current_session_id", currentSession.SessionId).
                    Msg("Added extended session endpoints")
            }
        }
    }

    logger.Info().Msgf("Successfully fetched %d sessions for %d owned apps for service %s.",
        len(sessions), len(p.appAddresses), serviceID)

    return sessions, nil
}
```

**Benefits**:
- Current session endpoints: Always available
- Extended session endpoints: Added during rollover for continuity
- Both sets go through normal RPC type + reputation filtering
- No special endpoint manager needed - just pass both sessions to existing logic

### 3. Remove Fallback-Only Rollover Handling

**Remove**: `context.go:420` fallback-only logic

```go
// OLD (protocol/shannon/context.go:420-423):
case rc.fullNode.IsInSessionRollover() && rc.serviceID != "hey":
    rc.logger.Debug().Msg("Executing protocol relay with fallback protection during session rollover periods")
    return rc.sendRelayWithFallback(payload)

// NEW: Remove this case entirely
// Normal relay handling will use merged session endpoints (current + extended)
// Fallback only used when getUniqueEndpoints() returns 0 endpoints
```

**Result**:
- Fallback used ONLY when: `len(getUniqueEndpoints()) == 0`
- During rollover: Merged endpoints from both sessions
- No special-casing for rollover periods

## Implementation Plan

### Phase 1: Fetch Both Sessions During Rollover
**Files**:
- `protocol/shannon/gateway_mode.go`
- `protocol/shannon/context.go`

**Changes**:
1. Update `getCentralizedGatewayModeActiveSessions()`:
   - Always fetch current session
   - During rollover: ALSO fetch extended session
   - Only add extended session if different from current
2. Remove special rollover case from `context.go:420`
3. Add logging for extended session usage

**Expected Behavior**:
- Normal: 1 session with N endpoints
- During rollover: 2 sessions with N+M endpoints (merged)
- Both go through existing RPC type + reputation filtering
- Fallback only when 0 endpoints remain after filtering

### Phase 2: WebSocket Block Monitoring (Optional Performance Enhancement)
**Files**:
- Create `protocol/shannon/fullnode_websocket_monitor.go`
- Update `protocol/shannon/fullnode_session_rollover.go`

**Changes**:
1. Implement `blockHeightMonitor` with WebSocket subscription
2. Add fallback to polling on WebSocket failure
3. Update `blockHeightMonitorLoop()` to use WebSocket
4. Add metrics for WebSocket vs. polling monitoring

**Benefits**:
- Faster rollover detection (< 1s vs. up to 15s)
- Not strictly required for correctness - just performance

### Phase 3: Testing
**Files**:
- Update `protocol/shannon/gateway_mode_test.go`
- Update `protocol/shannon/context_test.go`
- Create `e2e/session_rollover_test.go`

**Test Cases**:
1. **Normal operation**: 1 session, N endpoints
2. **During rollover**: 2 sessions, N+M merged endpoints
3. **Extended session deduplication**: Don't add if same as current
4. **Fallback only when needed**: 0 endpoints from both sessions
5. **Reputation filtering**: Works on merged endpoint set
6. **RPC type filtering**: Works on merged endpoint set

## Configuration

**Existing configuration** (`protocol/shannon/config.go`):

```yaml
full_node_config:
  # Grace period for session rollover (in blocks)
  # During this period, both current AND extended sessions are fetched
  session_rollover_blocks: 10  # Already exists, defaults to 10
```

**No new configuration needed** - we're just enabling existing extended session support during rollover periods.

## Metrics

**Add to `metrics/session`**:

```go
// Session rollover state
sessionRolloverActive{service_id="eth"} 0|1

// Session counts during rollover
activeSessions{service_id="eth", type="current|extended"} count

// Endpoint counts by source
sessionEndpoints{service_id="eth", source="current|extended"} count
```

## Success Criteria

✅ **Correctness** (Phase 1):
- During rollover: 2 sessions fetched (current + extended)
- Normal operation: 1 session fetched
- Fallback ONLY used when 0 endpoints from both sessions
- No service disruption during session transitions

✅ **Observability**:
- Logs show when extended session is added
- Metrics track session count (1 normal, 2 during rollover)
- Clear visibility into endpoint counts from each session

✅ **Performance** (Phase 2 - Optional):
- Session transitions detected < 1s (if WebSocket implemented)
- WebSocket monitoring stable with < 1% fallback to polling

## Migration Path

### Phase 1: Enable Extended Session Fetching (Core Fix)
1. **Implement**: Fetch both current + extended sessions during rollover
2. **Deploy**: With logging to track session counts
3. **Monitor**: Verify 2 sessions during rollover, 1 during normal operation
4. **Verify**: No service disruption during session transitions
5. **Cleanup**: Remove fallback-only rollover logic from `context.go:420`

**Timeline**: 1-2 days to implement + test, low risk

### Phase 2: WebSocket Monitoring (Optional Performance)
1. **Implement**: WebSocket block subscription with polling fallback
2. **Deploy**: With feature flag to enable gradually
3. **Monitor**: Verify WebSocket stability, measure latency improvement
4. **Enable**: Gradually increase percentage using WebSocket

**Timeline**: 2-3 days to implement + test, medium risk (can rollback to polling)

## Risks & Mitigations

**Risk**: Fetching 2 sessions doubles full node RPC calls during rollover
**Mitigation**: Only happens during rollover window (~10 blocks every ~60 blocks = 16% overhead), acceptable for reliability

**Risk**: Duplicate endpoints from both sessions cause issues
**Mitigation**: Endpoint deduplication via map keys (EndpointAddr already unique)

**Risk**: Extended session returns same as current session
**Mitigation**: Check `sessionId` before adding to avoid duplication

**Risk**: WebSocket connection instability (Phase 2)
**Mitigation**: Automatic fallback to polling, reconnection logic
