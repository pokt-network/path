package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
)

// hedgeResult contains the result from a hedged request attempt.
type hedgeResult struct {
	responses     []protocol.Response
	err           error
	endpointAddr  protocol.EndpointAddr
	supplierAddr  string
	duration      time.Duration
	isHedge       bool // true if this was the hedge request, false if primary
	attemptNumber int  // 1 for primary, 2 for hedge
}

// hedgeRacer manages racing between primary and hedge requests.
// It ensures both requests are properly tracked for reputation and only one response is returned.
type hedgeRacer struct {
	rc             *requestContext
	logger         polylog.Logger
	rpcType        sharedtypes.RPCType
	hedgeDelay     time.Duration
	connectTimeout time.Duration
	serviceID      string // For metrics

	// Results tracking
	resultChan chan hedgeResult
	once       sync.Once
	winner     *hedgeResult

	// Endpoints tracking (for reputation)
	primaryEndpoint protocol.EndpointAddr
	hedgeEndpoint   protocol.EndpointAddr

	// Timing tracking (for metrics)
	raceStartTime    time.Time
	hedgeStarted     bool
	primaryCompleted bool

	// raceResult stores the outcome for headers (primary_only, primary_won, hedge_won, etc.)
	raceResult string

	// payload is the single payload to send for this hedge race.
	// For batch requests, this is the individual batch item being processed.
	payload protocol.Payload
}

// newHedgeRacer creates a new hedge racer for a request.
func newHedgeRacer(
	rc *requestContext,
	logger polylog.Logger,
	rpcType sharedtypes.RPCType,
	hedgeDelay time.Duration,
	connectTimeout time.Duration,
) *hedgeRacer {
	return &hedgeRacer{
		rc:             rc,
		logger:         logger,
		rpcType:        rpcType,
		hedgeDelay:     hedgeDelay,
		connectTimeout: connectTimeout,
		serviceID:      string(rc.serviceID),
		resultChan:     make(chan hedgeResult, 2), // Buffer for primary and hedge
	}
}

// race executes the primary request and optionally starts a hedge request if the primary
// doesn't respond within hedgeDelay. Returns the winning response.
//
// The flow is:
// 1. Start primary request to initially selected endpoint
// 2. Wait for hedgeDelay
// 3. If no response yet, start hedge request to TOP-ranked different endpoint
// 4. Return first successful response (or first error if both fail)
// 5. Track reputation for both endpoints (winner gets reward, loser gets recorded)
//
// The payload parameter specifies the single payload to send. For batch requests,
// this should be the individual batch item being processed, not all batch items.
func (hr *hedgeRacer) race(
	ctx context.Context,
	primaryCtx ProtocolRequestContext,
	primaryEndpoint protocol.EndpointAddr,
	availableEndpoints protocol.EndpointAddrList,
	payload protocol.Payload,
) ([]protocol.Response, error) {
	hr.primaryEndpoint = primaryEndpoint
	hr.payload = payload
	hr.raceStartTime = time.Now()
	rpcTypeStr := metrics.NormalizeRPCType(hr.rpcType.String())

	// Extract supplier from primary endpoint for tracking
	primarySupplier := extractSupplierFromEndpoint(primaryEndpoint)

	hr.logger.Info().
		Str("service_id", hr.serviceID).
		Str("primary_supplier", primarySupplier).
		Dur("hedge_delay", hr.hedgeDelay).
		Int("available_endpoints", len(availableEndpoints)).
		Msg("🏁 Starting hedged request race")

	// Start primary request
	go hr.executeRequest(ctx, primaryCtx, primaryEndpoint, primarySupplier, false, 1, hr.raceStartTime)

	// Wait for hedge delay or primary response
	select {
	case result := <-hr.resultChan:
		// Primary responded before hedge delay - no hedge needed
		hr.primaryCompleted = true
		hr.recordWinner(result)
		hr.raceResult = metrics.HedgeResultPrimaryOnly

		winningLatency := result.duration.Seconds()
		hr.logger.Info().
			Str("service_id", hr.serviceID).
			Str("supplier", result.supplierAddr).
			Str("primary_supplier", primarySupplier).
			Dur("latency", result.duration).
			Bool("success", result.err == nil).
			Int("suppliers_tracked_before", len(hr.rc.suppliersTried)).
			Msg("⚡ Primary responded before hedge delay (no hedge needed)")

		// Record metric: primary_only (no hedge was started)
		metrics.RecordHedgeRequest(rpcTypeStr, hr.serviceID, metrics.HedgeResultPrimaryOnly, winningLatency)

		return hr.handleResult(result)

	case <-time.After(hr.hedgeDelay):
		// Hedge delay elapsed - start hedge request to different endpoint
		hr.logger.Info().
			Str("service_id", hr.serviceID).
			Str("primary_supplier", primarySupplier).
			Dur("hedge_delay", hr.hedgeDelay).
			Msg("🏃 Hedge delay elapsed, starting hedge request to TOP-ranked endpoint")

		// Select a different endpoint for hedge (TOP-ranked, excluding primary)
		hedgeEndpoint := hr.selectHedgeEndpoint(availableEndpoints, primaryEndpoint)
		if hedgeEndpoint != "" {
			hr.hedgeEndpoint = hedgeEndpoint
			hr.hedgeStarted = true
			hedgeSupplier := extractSupplierFromEndpoint(hedgeEndpoint)

			hr.logger.Info().
				Str("service_id", hr.serviceID).
				Str("primary_supplier", primarySupplier).
				Str("hedge_supplier", hedgeSupplier).
				Msg("🏃 Hedge request started - racing primary and hedge")

			// Pre-register both suppliers for X-Suppliers-Tried header
			// This ensures both are tracked even if one is cancelled before responding
			hr.rc.suppliersTried = append(hr.rc.suppliersTried, primarySupplier, hedgeSupplier)

			// Build protocol context for hedge endpoint
			hedgeCtx, _, err := hr.rc.protocol.BuildHTTPRequestContextForEndpoint(
				ctx, hr.rc.serviceID, hedgeEndpoint, hr.rpcType, hr.rc.originalHTTPRequest, true)
			if err != nil {
				hr.logger.Warn().Err(err).
					Str("hedge_endpoint", string(hedgeEndpoint)).
					Msg("Failed to build hedge protocol context")
				hr.hedgeStarted = false
			} else {
				// Start hedge request
				go hr.executeRequest(ctx, hedgeCtx, hedgeEndpoint, hedgeSupplier, true, 2, time.Now())
			}
		} else {
			hr.logger.Warn().
				Str("service_id", hr.serviceID).
				Int("available_endpoints", len(availableEndpoints)).
				Msg("⚠️ No alternative endpoint for hedge request")

			// Record metric: no_hedge (wanted to hedge but couldn't)
			// We'll record the final latency when primary completes
		}

		// Wait for either request to complete
		return hr.waitForWinner(ctx)

	case <-ctx.Done():
		hr.logger.Warn().
			Str("service_id", hr.serviceID).
			Dur("elapsed", time.Since(hr.raceStartTime)).
			Msg("Hedge race canceled by context")
		return nil, ctx.Err()
	}
}

// executeRequest runs a single request and sends result to channel.
func (hr *hedgeRacer) executeRequest(
	ctx context.Context,
	protocolCtx ProtocolRequestContext,
	endpoint protocol.EndpointAddr,
	supplier string,
	isHedge bool,
	attemptNum int,
	startTime time.Time,
) {
	requestType := "primary"
	if isHedge {
		requestType = "hedge"
	}

	hr.logger.Debug().
		Str("endpoint", string(endpoint)).
		Str("supplier", supplier).
		Str("request_type", requestType).
		Msg("Starting request")

	// Use the single payload stored in the racer, not all payloads from QoS context.
	// This is critical for batch requests where each item must be processed independently.
	responses, err := protocolCtx.HandleServiceRequest([]protocol.Payload{hr.payload})
	duration := time.Since(startTime)

	// Use supplier from response metadata if available, otherwise fall back to extracted supplier.
	// This ensures X-Suppliers-Tried matches X-Supplier-Address when both come from the same response.
	supplierFromResponse := supplier
	if len(responses) > 0 && responses[0].Metadata.SupplierAddress != "" {
		supplierFromResponse = responses[0].Metadata.SupplierAddress
	}

	result := hedgeResult{
		responses:     responses,
		err:           err,
		endpointAddr:  endpoint,
		supplierAddr:  supplierFromResponse,
		duration:      duration,
		isHedge:       isHedge,
		attemptNumber: attemptNum,
	}

	// Try to send result (may be ignored if race already decided)
	select {
	case hr.resultChan <- result:
		hr.logger.Debug().
			Str("request_type", requestType).
			Dur("duration", duration).
			Bool("success", err == nil && len(responses) > 0).
			Msg("Request completed")
	default:
		// Channel full or race already decided
		hr.logger.Debug().
			Str("request_type", requestType).
			Msg("Request completed but race already decided")
	}
}

// waitForWinner waits for the first response after hedge was started.
func (hr *hedgeRacer) waitForWinner(ctx context.Context) ([]protocol.Response, error) {
	var firstResult, secondResult *hedgeResult
	rpcTypeStr := metrics.NormalizeRPCType(hr.rpcType.String())

	// Wait for first response
	select {
	case result := <-hr.resultChan:
		firstResult = &result
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Check if first response is successful
	if firstResult.err == nil && len(firstResult.responses) > 0 {
		// First response is good - use it
		hr.recordWinner(*firstResult)

		winningLatency := firstResult.duration.Seconds()
		totalRaceTime := time.Since(hr.raceStartTime)

		// Determine result type and record metric
		if hr.hedgeStarted {
			if firstResult.isHedge {
				hr.raceResult = metrics.HedgeResultHedgeWon
				hr.logger.Info().
					Str("service_id", hr.serviceID).
					Str("winner", "hedge").
					Str("supplier", firstResult.supplierAddr).
					Dur("hedge_latency", firstResult.duration).
					Dur("total_race_time", totalRaceTime).
					Msg("🏆 HEDGE WON the race!")
				metrics.RecordHedgeRequest(rpcTypeStr, hr.serviceID, metrics.HedgeResultHedgeWon, winningLatency)
			} else {
				hr.raceResult = metrics.HedgeResultPrimaryWon
				hr.logger.Info().
					Str("service_id", hr.serviceID).
					Str("winner", "primary").
					Str("supplier", firstResult.supplierAddr).
					Dur("primary_latency", firstResult.duration).
					Dur("total_race_time", totalRaceTime).
					Msg("🏆 PRIMARY WON the race (hedge started but primary faster)")
				metrics.RecordHedgeRequest(rpcTypeStr, hr.serviceID, metrics.HedgeResultPrimaryWon, winningLatency)
			}
		} else {
			// Hedge was not started (no alternative endpoint)
			hr.raceResult = metrics.HedgeResultNoHedge
			hr.logger.Info().
				Str("service_id", hr.serviceID).
				Str("supplier", firstResult.supplierAddr).
				Dur("latency", firstResult.duration).
				Msg("📍 No hedge available - primary completed")
			metrics.RecordHedgeRequest(rpcTypeStr, hr.serviceID, metrics.HedgeResultNoHedge, winningLatency)
		}

		// Wait briefly for second result to record its status (for X-Suppliers-Tried header)
		// Must be synchronous so loser is recorded before response headers are sent
		hr.collectLoserSync()

		return hr.handleResult(*firstResult)
	}

	// First response failed - wait for second
	hr.logger.Warn().
		Str("service_id", hr.serviceID).
		Str("first_request_type", hr.getRequestType(firstResult)).
		Err(firstResult.err).
		Dur("first_duration", firstResult.duration).
		Msg("First response failed, waiting for second")

	select {
	case result := <-hr.resultChan:
		secondResult = &result
	case <-time.After(10 * time.Second): // Max wait for second
		// Second never came, use first result (even if error)
		hr.recordWinner(*firstResult)
		hr.logger.Error().
			Str("service_id", hr.serviceID).
			Err(firstResult.err).
			Msg("❌ Second request timed out, returning first (failed) result")

		winningLatency := firstResult.duration.Seconds()
		if hr.hedgeStarted {
			hr.raceResult = metrics.HedgeResultBothFailed
			metrics.RecordHedgeRequest(rpcTypeStr, hr.serviceID, metrics.HedgeResultBothFailed, winningLatency)
		} else {
			hr.raceResult = metrics.HedgeResultNoHedge
			metrics.RecordHedgeRequest(rpcTypeStr, hr.serviceID, metrics.HedgeResultNoHedge, winningLatency)
		}
		return hr.handleResult(*firstResult)
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Choose the better result
	if secondResult.err == nil && len(secondResult.responses) > 0 {
		hr.recordWinner(*secondResult)
		hr.recordLoser(*firstResult)

		winningLatency := secondResult.duration.Seconds()
		// Calculate latency savings: first response latency - winning latency
		// If first failed fast but second succeeded, savings might be negative
		latencySavings := firstResult.duration.Seconds() - secondResult.duration.Seconds()

		if secondResult.isHedge {
			hr.raceResult = metrics.HedgeResultHedgeWon
			hr.logger.Info().
				Str("service_id", hr.serviceID).
				Str("winner", "hedge").
				Str("hedge_supplier", secondResult.supplierAddr).
				Str("primary_supplier", firstResult.supplierAddr).
				Dur("hedge_latency", secondResult.duration).
				Dur("primary_latency", firstResult.duration).
				Err(firstResult.err).
				Msg("🏆 HEDGE WON - Primary failed, hedge succeeded!")
			metrics.RecordHedgeRequest(rpcTypeStr, hr.serviceID, metrics.HedgeResultHedgeWon, winningLatency)
		} else {
			hr.raceResult = metrics.HedgeResultPrimaryWon
			hr.logger.Info().
				Str("service_id", hr.serviceID).
				Str("winner", "primary").
				Str("primary_supplier", secondResult.supplierAddr).
				Str("hedge_supplier", firstResult.supplierAddr).
				Dur("primary_latency", secondResult.duration).
				Dur("hedge_latency", firstResult.duration).
				Err(firstResult.err).
				Msg("🏆 PRIMARY WON - Hedge failed, primary succeeded!")
			metrics.RecordHedgeRequest(rpcTypeStr, hr.serviceID, metrics.HedgeResultPrimaryWon, winningLatency)
		}

		// Record latency savings (can be negative if winner was slower)
		metrics.RecordHedgeLatencySavings(rpcTypeStr, hr.serviceID, latencySavings)

		return hr.handleResult(*secondResult)
	}

	// Both failed - use first
	hr.recordWinner(*firstResult)
	hr.recordLoser(*secondResult)
	hr.raceResult = metrics.HedgeResultBothFailed

	winningLatency := firstResult.duration.Seconds()
	hr.logger.Error().
		Str("service_id", hr.serviceID).
		Str("primary_supplier", extractSupplierFromEndpoint(hr.primaryEndpoint)).
		Str("hedge_supplier", extractSupplierFromEndpoint(hr.hedgeEndpoint)).
		Err(firstResult.err).
		Dur("primary_duration", firstResult.duration).
		Dur("hedge_duration", secondResult.duration).
		Msg("❌ BOTH FAILED - Returning first result")

	metrics.RecordHedgeRequest(rpcTypeStr, hr.serviceID, metrics.HedgeResultBothFailed, winningLatency)

	return hr.handleResult(*firstResult)
}

// selectHedgeEndpoint selects the best endpoint for hedge request.
// Uses TOP-ranked endpoint excluding the primary.
func (hr *hedgeRacer) selectHedgeEndpoint(
	endpoints protocol.EndpointAddrList,
	excludePrimary protocol.EndpointAddr,
) protocol.EndpointAddr {
	// Filter out primary endpoint
	filtered := make(protocol.EndpointAddrList, 0, len(endpoints)-1)
	for _, ep := range endpoints {
		if ep != excludePrimary {
			filtered = append(filtered, ep)
		}
	}

	if len(filtered) == 0 {
		hr.logger.Debug().Msg("No alternative endpoints for hedge request")
		return ""
	}

	// Select TOP-ranked from filtered list
	return hr.rc.selectTopRankedEndpoint(filtered, hr.rpcType)
}

// handleResult extracts response data and tracks suppliers.
func (hr *hedgeRacer) handleResult(result hedgeResult) ([]protocol.Response, error) {
	// Track supplier tried
	if result.supplierAddr != "" && len(hr.rc.suppliersTried) < 10 {
		alreadyTracked := false
		for _, s := range hr.rc.suppliersTried {
			if s == result.supplierAddr {
				alreadyTracked = true
				break
			}
		}
		if !alreadyTracked {
			hr.rc.suppliersTried = append(hr.rc.suppliersTried, result.supplierAddr)
			hr.logger.Debug().
				Str("supplier", result.supplierAddr).
				Bool("is_hedge", result.isHedge).
				Str("source", "handleResult").
				Int("total_suppliers_tried", len(hr.rc.suppliersTried)).
				Msg("🔍 TRACKED supplier in suppliersTried (winner)")
		}
	}

	return result.responses, result.err
}

// recordWinner records the winning request for metrics/logging.
func (hr *hedgeRacer) recordWinner(result hedgeResult) {
	hr.once.Do(func() {
		hr.winner = &result
		requestType := "primary"
		if result.isHedge {
			requestType = "hedge"
		}

		hr.logger.Debug().
			Str("winner", requestType).
			Str("supplier", result.supplierAddr).
			Dur("duration", result.duration).
			Bool("success", result.err == nil).
			Msg("🏆 Race winner determined")
	})
}

// recordLoser records the losing request for reputation tracking.
func (hr *hedgeRacer) recordLoser(result hedgeResult) {
	// Track loser's supplier for X-Suppliers-Tried header
	if result.supplierAddr != "" && len(hr.rc.suppliersTried) < 10 {
		alreadyTracked := false
		for _, s := range hr.rc.suppliersTried {
			if s == result.supplierAddr {
				alreadyTracked = true
				break
			}
		}
		if !alreadyTracked {
			hr.rc.suppliersTried = append(hr.rc.suppliersTried, result.supplierAddr)
			hr.logger.Debug().
				Str("supplier", result.supplierAddr).
				Bool("is_hedge", result.isHedge).
				Str("source", "recordLoser").
				Int("total_suppliers_tried", len(hr.rc.suppliersTried)).
				Msg("🔍 TRACKED supplier in suppliersTried (loser)")
		}
	}

	requestType := "primary"
	if result.isHedge {
		requestType = "hedge"
	}

	hr.logger.Debug().
		Str("loser", requestType).
		Str("supplier", result.supplierAddr).
		Dur("duration", result.duration).
		Err(result.err).
		Msg("Race loser recorded")
}

// collectLoserSync waits synchronously for the loser to record it in suppliersTried.
// Uses a short timeout to avoid adding latency when loser is slow.
func (hr *hedgeRacer) collectLoserSync() {
	select {
	case result := <-hr.resultChan:
		hr.recordLoser(result)
	case <-time.After(100 * time.Millisecond):
		// Loser didn't arrive in time - that's ok, we already have the winner
		hr.logger.Debug().Msg("Loser did not arrive within 100ms for X-Suppliers-Tried")
	}
}

// getRequestType returns "primary" or "hedge" for logging.
func (hr *hedgeRacer) getRequestType(result *hedgeResult) string {
	if result == nil {
		return "unknown"
	}
	if result.isHedge {
		return "hedge"
	}
	return "primary"
}
