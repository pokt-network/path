# Reputation Package

The reputation package provides endpoint reputation tracking and scoring for PATH.
It replaces binary sanctions with a gradual scoring system that tracks endpoint
reliability over time.

## Overview

The reputation system tracks endpoint performance through signals (success/error events)
and maintains a score for each endpoint. Endpoints with scores below the threshold are
excluded from selection for requests.

### Key Features

- **Performance-optimized design**: All reads from local cache (<1μs latency)
- **Async writes**: Updates local cache immediately, syncs to backend asynchronously
- **Background refresh**: Periodically syncs from shared storage (for multi-instance deployments)
- **Configurable thresholds**: Tune initial scores, minimum thresholds, and impact values

## Configuration

```yaml
reputation:
  enabled: true
  initial_score: 80        # Starting score for new endpoints
  min_threshold: 30        # Minimum score to be eligible for selection
  recovery_timeout: 5m     # Time before low-scoring endpoints recover
  storage_type: memory     # Storage backend: "memory" or "redis"
  sync_config:
    refresh_interval: 5s   # How often to refresh from backend
    write_buffer_size: 1000  # Max pending writes before blocking
    flush_interval: 100ms  # How often to flush writes to backend
```

## Score Range and Impacts

Scores range from 0 (worst) to 100 (best):

| Signal Type      | Impact | Description                           |
|------------------|--------|---------------------------------------|
| Success          | +1     | Request completed successfully        |
| Recovery Success | +15    | Success from low-scoring endpoint (probation) |
| Minor Error      | -3     | Validation or format errors           |
| Major Error      | -10    | Timeouts, connection failures         |
| Critical Error   | -25    | HTTP 5xx errors, service unavailable  |
| Fatal Error      | -50    | Configuration errors, invalid service |

Default values:
- **Initial Score**: 80 (new endpoints start here)
- **Minimum Threshold**: 30 (below this, endpoint is excluded)
- **Recovery Timeout**: 5m (low-scoring endpoints recover after this time)

## Recovery Mechanism

When an endpoint's score falls below the threshold, it gets excluded from selection.
To prevent endpoints from being "jailed" forever, the system automatically recovers
endpoints that have been below threshold for longer than `recovery_timeout`:

1. Endpoint fails repeatedly → score drops below threshold
2. Endpoint is excluded from request selection
3. After `recovery_timeout` (default 5m) with no signals
4. Next time the endpoint is checked, score resets to `initial_score`
5. Endpoint becomes eligible for selection again

This allows endpoints to recover from temporary issues (crashes, network problems, etc.)
without manual intervention.

## Usage

### Recording Signals

```go
key := reputation.NewEndpointKey("eth", "https://endpoint.example.com")

// On success
svc.RecordSignal(ctx, key, reputation.NewSuccessSignal(latency))

// On error
svc.RecordSignal(ctx, key, reputation.NewMajorErrorSignal("timeout", latency))
```

### Filtering Endpoints

```go
// Get endpoints above threshold
eligible, err := svc.FilterByScore(ctx, endpoints, config.MinThreshold)
```

### Getting Scores

```go
score, err := svc.GetScore(ctx, key)
fmt.Printf("Score: %.1f, Success: %d, Errors: %d\n",
    score.Value, score.SuccessCount, score.ErrorCount)
```

## Architecture

```
┌─────────────────┐     ┌──────────────┐     ┌─────────────┐
│  Hot Path       │────▶│ Local Cache  │────▶│ Read <1μs   │
│  (Requests)     │     │ (sync.Map)   │     └─────────────┘
└─────────────────┘     └──────────────┘
                               │
                               │ Async Write
                               ▼
                        ┌──────────────┐     ┌─────────────┐
                        │ Write Buffer │────▶│   Storage   │
                        │   Channel    │     │ (Memory/    │
                        └──────────────┘     │  Redis)     │
                               ▲             └─────────────┘
                               │                    │
                        ┌──────────────┐            │
                        │  Background  │◀───────────┘
                        │   Refresh    │  Periodic Sync
                        └──────────────┘
```

## Storage Backends

### Memory (Default)

- Single-instance only
- Data lost on restart
- Fastest performance

### Redis (Future PR)

- Shared across instances
- Persistent storage
- Required for multi-instance deployments

## Thread Safety

The ReputationService is safe for concurrent use. All methods can be called from
multiple goroutines without external synchronization.
