package websockets

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
)

// EndpointReconnector re-establishes the endpoint side of a websocket bridge after a
// recoverable disconnect — a Shannon session rollover, where the relay miner closes
// the endpoint connection (typically close code 4000 "session expired") once the chain
// crosses the session boundary. It is injected by the protocol layer, which alone
// knows how to re-fetch the current session, re-select a supplier, and re-sign frames.
//
// A nil reconnector (the default) disables rebind: an endpoint disconnect cancels the
// whole bridge, the pre-rebind behavior.
type EndpointReconnector interface {
	// ReconnectEndpoint opens a fresh endpoint websocket connection bound to the
	// CURRENT session. Called after the previous endpoint connection dropped at a
	// session boundary; it must NOT reuse the expired session.
	//
	// avoidCurrentSupplier asks the reconnector to reselect a DIFFERENT supplier than
	// the one currently bound, skipping the seamless same-supplier (tier-1) path. The
	// staleness watchdog sets it: a silent stall means the current supplier is the
	// problem, so reconnecting to it would just stall again. An ordinary session-rollover
	// reconnect passes false to prefer supplier continuity.
	ReconnectEndpoint(ctx context.Context, avoidCurrentSupplier bool) (*websocket.Conn, error)

	// SubscriptionReplayFrames returns the wire frames to send over the new endpoint
	// connection to restore the client's active subscriptions, already signed for the
	// current session. Empty when there is nothing to replay. Called after
	// ReconnectEndpoint succeeds, so the frames are signed against the new session.
	SubscriptionReplayFrames() ([][]byte, error)

	// HasActiveSubscriptions reports whether the client currently holds at least one
	// ESTABLISHED subscription (one that a rebind could actually replay). The staleness
	// watchdog only arms when this is true: with nothing to keep flowing, a quiet
	// endpoint connection is legitimate and must not be rebound.
	HasActiveSubscriptions() bool

	// OnReconnectOutcome reports the terminal result of a rebind episode so the
	// implementer (which knows service/domain) can emit metrics/observability.
	// success=false means the bridge is giving up and closing the client.
	// replayedSubscriptions is the number of subscriptions replayed on success.
	// stage identifies where a failure occurred (ReconnectStageNone on success) so the
	// implementer can distinguish a selection/dial failure from a replay failure; the
	// implementer holds the finer-grained selection reason itself.
	OnReconnectOutcome(success bool, replayedSubscriptions int, stage ReconnectFailureStage)

	// OnEndpointStallDetected reports that the staleness watchdog fired for observability.
	// gaveUp=false: the watchdog is forcing a rebind onto a different supplier.
	// gaveUp=true: the endpoint stayed silent across repeated rebinds and the bridge is
	// closing the client (1012). The implementer knows service/domain for the metric.
	OnEndpointStallDetected(gaveUp bool)
}

// ReconnectFailureStage identifies where in a rebind episode a failure happened, so the
// reconnector can emit a precise failure-reason metric. The bridge knows only the stage;
// the reconnector (protocol layer) knows the specific selection/dial reason.
type ReconnectFailureStage string

const (
	// ReconnectStageNone is passed on a successful rebind.
	ReconnectStageNone ReconnectFailureStage = ""
	// ReconnectStageSelect means ReconnectEndpoint failed — session lookup, endpoint
	// selection, or the endpoint dial. The reconnector supplies the specific reason.
	ReconnectStageSelect ReconnectFailureStage = "select"
	// ReconnectStageReplay means the endpoint reconnected but building or writing the
	// subscription replay frames failed.
	ReconnectStageReplay ReconnectFailureStage = "replay"
)

// endpointDisconnect carries a recoverable endpoint disconnect to the bridge's start()
// loop, tagged with the endpoint generation it was raised for so a late signal from an
// already-replaced connection can be ignored.
type endpointDisconnect struct {
	gen int
	err error
}

// Reconnect retry/backoff bounds. Package-level vars (not consts) so tests can shrink
// the backoff; production never mutates them.
var (
	// reconnectMaxAttempts bounds reconnect tries per disconnect so a supplier that
	// always rejects cannot loop forever; on exhaustion the bridge falls back to
	// closing the client with 1012 (reconnect guidance), the pre-rebind behavior.
	reconnectMaxAttempts = 5
	// reconnectBaseBackoff is the initial delay between reconnect attempts; it doubles
	// up to reconnectMaxBackoff.
	reconnectBaseBackoff = 250 * time.Millisecond
	reconnectMaxBackoff  = 3 * time.Second
)

// Endpoint data-staleness watchdog bounds. Package-level vars (not consts) so tests can
// shrink them; production never mutates them.
//
// The bridge's ping/pong liveness (connection.go) is transport-level: it only detects a
// dead socket. A supplier can keep answering pings while its upstream subscription feed
// goes silent (stops pushing eth_subscription notifications). These bounds add
// application-level liveness: if an endpoint connection with ≥1 established subscription
// delivers no data frame for endpointStalenessThreshold, the watchdog forces a rebind to
// a different supplier — up to maxConsecutiveStallRebinds times before giving up.
var (
	// endpointStalenessThreshold is the maximum silence (no endpoint→client data frame)
	// tolerated on a connection with an active subscription before a rebind is forced.
	// 60s is 2× pongWaitDuration — comfortably beyond transport-level liveness and beyond
	// the newHeads cadence of every high-throughput WS chain (poly ~2s, bsc ~3s, eth ~12s),
	// so a live feed never trips it. Flat for now; a per-service / adaptive threshold
	// (k × observed inter-notification interval) is a planned refinement.
	endpointStalenessThreshold = 60 * time.Second
	// stalenessCheckInterval is how often the watchdog evaluates staleness. Worst-case
	// detection latency is endpointStalenessThreshold + stalenessCheckInterval.
	stalenessCheckInterval = 15 * time.Second
	// maxConsecutiveStallRebinds caps rebinds triggered by staleness with no intervening
	// data. Reaching it means the replacement suppliers are also silent (or the chain is
	// quiet network-wide); the bridge then closes the client (1012) rather than churn
	// forever. Reset to 0 by any endpoint data frame.
	maxConsecutiveStallRebinds = 3
)

// endpointDisconnectFunc returns the onDisconnect callback for an endpoint connection
// of the given generation. It signals the start() loop to attempt a reconnect rather
// than cancelling the bridge. Non-blocking: the buffered endpointDown channel plus the
// generation tag mean a duplicate signal (connLoop and pingLoop both failing) collapses
// to at most one live reconnect.
func (b *bridge) endpointDisconnectFunc(gen int) func(error) {
	return func(err error) {
		select {
		case b.endpointDown <- endpointDisconnect{gen: gen, err: err}:
		default:
			// A disconnect for this generation is already queued; drop the duplicate.
		}
	}
}

// handleEndpointDown reconnects the endpoint side of the bridge after a recoverable
// disconnect, keeping the client connection open. Runs inline on the start() goroutine,
// so it is serialized with message processing — no client or endpoint frame is handled
// while a reconnect is in flight.
//
// On success the endpoint connection is replaced (new generation) and the client's
// active subscriptions are replayed. On failure (exhausted retries, or the bridge is
// already shutting down) it falls back to shutting the bridge down, which sends the
// client a 1012 close asking it to reconnect — the pre-rebind outcome.
func (b *bridge) handleEndpointDown(down endpointDisconnect) {
	// Ignore a stale signal from an endpoint connection we have already replaced.
	if down.gen != b.endpointGen {
		b.logger.Debug().
			Int("signal_gen", down.gen).
			Int("current_gen", b.endpointGen).
			Msg("ignoring endpoint disconnect from a superseded connection")
		return
	}

	// If the bridge is already tearing down (client gone / shutdown), do not reconnect.
	if b.ctx.Err() != nil {
		b.shutdown(ErrBridgeContextCanceled)
		return
	}

	// A stall-triggered disconnect (raised by the staleness watchdog) means the current
	// supplier is the problem, so the reconnect must avoid reselecting it. An ordinary
	// session-rollover disconnect prefers supplier continuity (tier-1).
	avoidCurrentSupplier := errors.Is(down.err, ErrEndpointStalled)

	// NOTE: logged at Error level ON PURPOSE so rebind activity is visible on canary
	// (LOG_LEVEL=error). Downgrade or remove once the feature is validated.
	b.logger.Error().
		Err(down.err).
		Bool("avoid_current_supplier", avoidCurrentSupplier).
		Msg("🔁 [WS-REBIND] endpoint disconnected — attempting session rebind (client stays connected)")

	// Stop the old endpoint loops and close the old connection before dialing a new one.
	// Clear endpointConn so that, if the reconnect fails, shutdown's close-code logic
	// does not propagate the old connection's abnormal close (1006 → sanitized 1011) to
	// the client; instead it falls through to the ErrBridgeEndpointUnavailable → 1012
	// "please reconnect" guidance. A successful reconnect reassigns it below.
	if b.endpointCancel != nil {
		b.endpointCancel()
	}
	if b.endpointConn != nil {
		b.endpointConn.Close()
		b.endpointConn = nil
	}

	newConn, err := b.reconnectWithBackoff(avoidCurrentSupplier)
	if err != nil {
		b.logger.Error().Err(err).Msg("❌ [WS-REBIND] endpoint session rebind failed after retries — closing client")
		b.reconnector.OnReconnectOutcome(false, 0, ReconnectStageSelect)
		b.shutdown(fmt.Errorf("%w: endpoint reconnect failed: %w", ErrBridgeEndpointUnavailable, err))
		return
	}

	// Fetch the frames that restore the client's subscriptions BEFORE the new
	// connection's read loop starts, so we can write them without racing the reader.
	replayFrames, replayErr := b.reconnector.SubscriptionReplayFrames()
	if replayErr != nil {
		// Replay-frame construction failed (e.g. re-signing error). The new connection
		// is unusable without restored subscriptions; close the client to reconnect.
		newConn.Close()
		b.logger.Error().Err(replayErr).Msg("❌ [WS-REBIND] failed to build subscription replay frames — closing client")
		b.reconnector.OnReconnectOutcome(false, 0, ReconnectStageReplay)
		b.shutdown(fmt.Errorf("%w: subscription replay failed: %w", ErrBridgeEndpointUnavailable, replayErr))
		return
	}
	if err := writeReplayFrames(newConn, replayFrames); err != nil {
		newConn.Close()
		b.logger.Error().Err(err).Msg("❌ [WS-REBIND] failed to replay subscriptions onto new endpoint — closing client")
		b.reconnector.OnReconnectOutcome(false, 0, ReconnectStageReplay)
		b.shutdown(fmt.Errorf("%w: subscription replay write failed: %w", ErrBridgeEndpointUnavailable, err))
		return
	}

	// Swap in the new endpoint connection under a fresh generation and context. The
	// replay responses that follow arrive through the new connection's read loop and
	// are swallowed by the processor's subscription registry.
	b.endpointGen++
	b.endpointCtx, b.endpointCancel = context.WithCancel(b.ctx)
	b.endpointConn = newConnection(
		b.endpointCtx,
		b.logger.With("conn", "endpoint"),
		newConn,
		messageSourceEndpoint,
		b.msgChan,
		b.endpointDisconnectFunc(b.endpointGen),
	)

	// Give the freshly reconnected endpoint a full staleness window before the watchdog
	// can fire again, regardless of how long ago the OLD endpoint last sent data.
	b.lastEndpointDataAt = time.Now()

	// Error level ON PURPOSE for canary visibility (LOG_LEVEL=error). Temporary.
	b.logger.Error().
		Int("endpoint_gen", b.endpointGen).
		Int("replayed_subscriptions", len(replayFrames)).
		Msg("✅ [WS-REBIND] endpoint session rebind succeeded — client kept open")
	b.reconnector.OnReconnectOutcome(true, len(replayFrames), ReconnectStageNone)
}

// reconnectWithBackoff calls the reconnector with bounded retries and exponential
// backoff, aborting early if the bridge context is canceled.
func (b *bridge) reconnectWithBackoff(avoidCurrentSupplier bool) (*websocket.Conn, error) {
	backoff := reconnectBaseBackoff
	var lastErr error
	for attempt := 1; attempt <= reconnectMaxAttempts; attempt++ {
		if b.ctx.Err() != nil {
			return nil, b.ctx.Err()
		}

		conn, err := b.reconnector.ReconnectEndpoint(b.ctx, avoidCurrentSupplier)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		// Error level for canary visibility — a burst of these on poktroll suppliers
		// signals a PATH<->miner session-height skew (validation/session mismatch).
		b.logger.Error().
			Err(err).
			Int("attempt", attempt).
			Int("max_attempts", reconnectMaxAttempts).
			Msg("⚠️ [WS-REBIND] endpoint reconnect attempt failed")

		if attempt == reconnectMaxAttempts {
			break
		}
		select {
		case <-time.After(backoff):
		case <-b.ctx.Done():
			return nil, b.ctx.Err()
		}
		if backoff *= 2; backoff > reconnectMaxBackoff {
			backoff = reconnectMaxBackoff
		}
	}
	return nil, lastErr
}

// writeReplayFrames writes each subscription replay frame to a freshly dialed endpoint
// connection before its read loop is started. EVM subscription clients use text frames,
// and the signed relay payload rides the same frame type the client originally used.
func writeReplayFrames(conn *websocket.Conn, frames [][]byte) error {
	for i, frame := range frames {
		if err := conn.SetWriteDeadline(time.Now().Add(writeWaitDuration)); err != nil {
			return fmt.Errorf("set write deadline for replay frame %d: %w", i, err)
		}
		if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
			return fmt.Errorf("write replay frame %d: %w", i, err)
		}
	}
	return nil
}
