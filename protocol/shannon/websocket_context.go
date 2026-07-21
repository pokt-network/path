package shannon

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	gorillaws "github.com/gorilla/websocket"
	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/pokt-network/poktroll/pkg/relayer/proxy"
	servicetypes "github.com/pokt-network/poktroll/x/service/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	sdk "github.com/pokt-network/shannon-sdk"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/metrics"
	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	"github.com/pokt-network/path/observation"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/selector"
	"github.com/pokt-network/path/reputation"
	"github.com/pokt-network/path/request"
	"github.com/pokt-network/path/websockets"
)

// The requestContext implements the gateway.ProtocolRequestContextWebsocket interface.
// It handles protocol-level Websocket message processing for both client and endpoint messages.
// For example, client messages are signed and endpoint messages are validated.
var _ gateway.ProtocolRequestContextWebsocket = &websocketRequestContext{}

// It also implements websockets.EndpointReconnector so the bridge can rebind the
// endpoint connection across a Shannon session rollover (when session rebind is enabled).
var _ websockets.EndpointReconnector = &websocketRequestContext{}

type websocketRequestContext struct {
	logger polylog.Logger

	// context for timeout propagation and cancellation
	context context.Context

	// fullNode is used for retrieving onchain data.
	fullNode FullNode

	// serviceID is the service ID for the request.
	serviceID protocol.ServiceID

	// relayRequestSigner is used for signing relay requests.
	relayRequestSigner RelayRequestSigner

	// selectedEndpoint is the endpoint chosen at connect time. It is IMMUTABLE for the
	// connection's life (set once at construction, never mutated), so the concurrent
	// connection-observation goroutine reads it race-free. Session rebind does NOT touch
	// it — it updates reconnectEndpoint instead. Used for observations, metrics, and
	// logging.
	selectedEndpoint endpoint

	// reconnectEndpoint is the endpoint bound to the CURRENT session after a rebind. It
	// is nil until the first reconnect. Written only by ReconnectEndpoint and read only
	// by signingEndpoint() — both on the bridge's single message-processing goroutine —
	// so no mutex is needed. The signing/validation path uses signingEndpoint() so it
	// always operates on the live session.
	reconnectEndpoint endpoint

	// registry tracks JSON-RPC subscriptions so they can be replayed after a rollover.
	// Non-nil only when session rebind is enabled.
	registry *websockets.SubscriptionRegistry

	// reconnectProvider re-selects an endpoint for the CURRENT session on a rollover. It
	// prefers the original supplier+URL (seamless) and, if that supplier rotated out of
	// the new session, falls back to the best-available endpoint (tier-2) so the client's
	// connection survives regardless of Shannon supplier rotation. avoidCurrent forces a
	// different supplier than the one currently bound (used by the staleness watchdog,
	// where the current supplier is the one stalling). Returns the endpoint, whether a
	// different supplier was chosen, and (on failure) a metrics reason label. Non-nil only
	// when session rebind is enabled; its presence is what makes this context act as an
	// EndpointReconnector.
	reconnectProvider func(ctx context.Context, avoidCurrent bool) (ep endpoint, differentSupplier bool, failureReason string, err error)

	// lastReconnectDifferentSupplier records whether the most recent successful reconnect
	// fell back to a different supplier (tier-2). Read by OnReconnectOutcome to tag the
	// metric. Bridge-goroutine only (sole reader/writer), so it needs no synchronization.
	lastReconnectDifferentSupplier bool

	// lastReconnectReason holds the failure-reason metric label from the most recent
	// failed selection or dial, consumed by OnReconnectOutcome. Bridge-goroutine only.
	lastReconnectReason string

	// reputationService tracks endpoint reputation scores.
	// If non-nil, signals are recorded on success/error for gradual reputation tracking.
	reputationService reputation.ReputationService
}

// signingEndpoint returns the endpoint whose session must be used to sign/validate
// messages: the rebound endpoint if a reconnect has happened, else the original. Called
// only on the bridge's message-processing goroutine (the sole writer of
// reconnectEndpoint), so it needs no synchronization.
func (wrc *websocketRequestContext) signingEndpoint() endpoint {
	if wrc.reconnectEndpoint != nil {
		return wrc.reconnectEndpoint
	}
	return wrc.selectedEndpoint
}

// ---------- Websocket Request Context Setup  ----------

// BuildWebsocketRequestContextForEndpoint creates a new Websocket protocol request context for a specified service and endpoint.
// This method immediately establishes the Websocket connection and starts the bridge.
//
// Parameters:
//   - ctx: Context for cancellation, deadlines, and logging.
//   - serviceID: The unique identifier of the target service.
//   - selectedEndpointAddr: The address of the endpoint to use for the request.
//   - httpReq: HTTP request used for Websocket upgrade and delegated mode app extraction.
//   - httpResponseWriter: HTTP response writer for Websocket upgrade.
//   - messageObservationsChan: Channel for sending message-level observations to the gateway.
func (p *Protocol) BuildWebsocketRequestContextForEndpoint(
	ctx context.Context,
	serviceID protocol.ServiceID,
	selectedEndpointAddr protocol.EndpointAddr,
	websocketMessageProcessor websockets.WebsocketMessageProcessor,
	httpReq *http.Request,
	httpResponseWriter http.ResponseWriter,
	// TODO_TECHDEBT(@commoddity): this channel should be created here, not passed to it, as protocol is the producer side of the channel.
	messageObservationsChan chan *observation.RequestResponseObservations,
) (gateway.ProtocolRequestContextWebsocket, <-chan *protocolobservations.Observations, error) {
	logger := p.logger.With(
		"method", "BuildWebsocketRequestContextForEndpoint",
		"service_id", serviceID,
		"endpoint_addr", selectedEndpointAddr,
	)

	selectedEndpoint, err := p.getPreSelectedEndpoint(ctx, serviceID, selectedEndpointAddr, httpReq, sharedtypes.RPCType_WEBSOCKET)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get pre-selected endpoint")
		return nil, nil, err
	}

	// Retrieve the relay request signer for the current gateway mode.
	permittedSigner, err := p.getGatewayModePermittedRelaySigner(p.gatewayMode)
	if err != nil {
		// Wrap the context setup error.
		// Used to generate the observation.
		err = fmt.Errorf("%w: gateway mode %s: %w", errRequestContextSetupErrSignerSetup, p.gatewayMode, err)
		return nil, nil, err
	}

	// Create Websocket request context for the pre-selected endpoint.
	// The logger is hydrated once with all connection-level fields (supplier,
	// URL, app, etc.) — these are immutable for the connection's lifetime, so
	// re-adding them per message would just churn allocations.
	wrc := &websocketRequestContext{
		logger:             buildWebsocketConnectionLogger(p.logger, serviceID, selectedEndpoint),
		context:            ctx,
		fullNode:           p.FullNode,
		selectedEndpoint:   selectedEndpoint,
		serviceID:          serviceID,
		relayRequestSigner: permittedSigner,
		reputationService:  p.reputationService,
	}

	// Enable websocket session rebind: track subscriptions and give the bridge a way to
	// re-select an endpoint for the current session on a rollover. When disabled, both
	// stay nil and the bridge tears the connection down on a rollover (pre-rebind
	// behavior). getReconnectEndpoint re-fetches the current session and prefers the
	// original supplier, falling back to the best-available endpoint if that supplier
	// rotated out of the new session.
	if p.websocketSessionRebindEnabled {
		wrc.registry = websockets.NewSubscriptionRegistry()
		wrc.reconnectProvider = func(reconnectCtx context.Context, avoidCurrent bool) (endpoint, bool, string, error) {
			return p.getReconnectEndpoint(reconnectCtx, serviceID, selectedEndpointAddr, httpReq, avoidCurrent)
		}
	}

	// Create observation channel for connection-level observations only
	// Buffer size of 10 should be sufficient for connection lifecycle events
	connectionObservationChan := make(chan *protocolobservations.Observations, 10)

	// Start the Websocket bridge immediately
	// This handles connection establishment and message processing
	err = wrc.startWebSocketBridge(
		ctx,
		httpReq,
		httpResponseWriter,
		websocketMessageProcessor,
		messageObservationsChan,
		connectionObservationChan,
	)
	if err != nil {
		// Close the observation channel on error to prevent resource leaks
		close(connectionObservationChan)
		logger.Error().Err(err).Msg("Failed to start Websocket bridge")
		return nil, nil, fmt.Errorf("failed to start Websocket bridge: %w", err)
	}

	return wrc, connectionObservationChan, nil
}

// CheckWebsocketConnection checks if the websocket connection to the endpoint is established.
// This method is used by the websocket hydrator to check if the endpoint supports websocket RPC type.
// It uses a simplified version of the websocket bridge connection process to avoid unnecessary overhead.
func (p *Protocol) CheckWebsocketConnection(
	ctx context.Context,
	serviceID protocol.ServiceID,
	selectedEndpointAddr protocol.EndpointAddr,
) *protocolobservations.Observations {
	logger := p.logger.With("method", "CheckWebsocketConnection")

	// Get the pre-selected endpoint.
	selectedEndpoint, err := p.getPreSelectedEndpoint(ctx, serviceID, selectedEndpointAddr, nil, sharedtypes.RPCType_WEBSOCKET)
	if err != nil {
		err = fmt.Errorf("⁉️ SHOULD NEVER HAPPEN: failed to get pre-selected endpoint: %s", err.Error())
		// Will not lead to reputation penalty as this does not indicate a problem with the endpoint, nor should it ever happen.
		return getWebsocketConnectionErrorObservation(logger, serviceID, selectedEndpoint, err)
	}

	// Get the websocket-specific URL from the selected endpoint.
	websocketEndpointURL, err := getWebsocketEndpointURL(logger, selectedEndpoint)
	if err != nil {
		err = fmt.Errorf("%w: selected endpoint does not support websocket RPC type: %s", errCreatingWebSocketConnection, err.Error())
		logger.Debug().Err(err).Msg("❌ Selected endpoint does not support websocket RPC type")
		return getWebsocketConnectionErrorObservation(logger, serviceID, selectedEndpoint, err)
	}
	logger = logger.With("websocket_url", websocketEndpointURL)

	// Get the headers for the websocket connection that will be sent to the endpoint.
	endpointConnectionHeaders, err := getWebsocketConnectionHeaders(logger, selectedEndpoint)
	if err != nil {
		err = fmt.Errorf("%w: failed to get websocket connection headers: %s", errCreatingWebSocketConnection, err.Error())
		logger.Debug().Err(err).Msg("❌ Failed to get websocket connection headers")
		return getWebsocketConnectionErrorObservation(logger, serviceID, selectedEndpoint, err)
	}

	// Test the websocket connection to the endpoint.
	conn, err := websockets.ConnectWebsocketEndpoint(
		logger,
		websocketEndpointURL,
		endpointConnectionHeaders,
	)
	if err != nil {
		err = fmt.Errorf("%w: failed to connect to websocket endpoint: %s", errCreatingWebSocketConnection, err.Error())
		logger.Debug().Err(err).Msg("❌ Failed to connect to websocket endpoint")
		return getWebsocketConnectionErrorObservation(logger, serviceID, selectedEndpoint, err)
	}
	// Close the test connection immediately — this is only a connectivity probe.
	conn.Close()

	// A nil observation means no error occurred.
	return nil
}

func (p *Protocol) getPreSelectedEndpoint(
	ctx context.Context,
	serviceID protocol.ServiceID,
	selectedEndpointAddr protocol.EndpointAddr,
	httpReq *http.Request,
	rpcType sharedtypes.RPCType,
) (endpoint, error) {
	logger := p.logger.With("method", "getPreSelectedEndpoint")

	activeSessions, err := p.getActiveGatewaySessions(ctx, serviceID, httpReq)
	if err != nil {
		logger.Error().Err(err).Msgf("Relay request will fail due to error retrieving active sessions for service %s", serviceID)
		return nil, err
	}

	// Parse allowed suppliers from header (if present)
	allowedSuppliers := parseAllowedSuppliersHeader(httpReq)

	// Retrieve the list of endpoints (i.e. backend service URLs by external operators)
	// that can service RPC requests for the given service ID for the given apps.
	// This includes fallback logic if session endpoints are unavailable.
	// The final boolean parameter sets whether to filter by reputation.
	// The final slice parameter optionally restricts endpoints to specific allowed suppliers.
	//
	// S1 (disqualify-only): reputation filtering now applies to WebSocket too. WS records
	// reputation signals (connect/handshake/message + stall/dial from S0), so proven-bad
	// WS endpoints (cooldown / below threshold) are excluded here. Unscored endpoints still
	// pass (InitialScore), and a WS-only safety net in getSessionsUniqueEndpoints prevents an
	// empty pool. Tiered *ranking* stays OFF for WS until active WS health checks exist (S2/S3).
	const filterByReputation = true
	endpoints, actualRPCType, err := p.getUniqueEndpoints(ctx, serviceID, activeSessions, filterByReputation, rpcType, allowedSuppliers, selectedEndpointAddr)
	if err != nil {
		logger.Error().Err(err).Msg(err.Error())
		return nil, err
	}

	// Log if RPC type fallback occurred
	if actualRPCType != rpcType {
		logger.Debug().
			Str("requested_rpc_type", rpcType.String()).
			Str("actual_rpc_type", actualRPCType.String()).
			Msg("RPC type fallback was applied for websocket endpoint selection")
	}

	// Select the endpoint that matches the pre-selected address.
	// This ensures QoS checks are performed on the selected endpoint.
	selectedEndpoint, ok := endpoints[selectedEndpointAddr]
	if !ok {
		// Wrap the context setup error with the protocol package's error.
		// This allows the gateway package to detect and handle this specific case.
		err := fmt.Errorf("%w: service %s endpoint %s", protocol.ErrEndpointUnavailable, serviceID, selectedEndpointAddr)
		logger.Error().Err(err).Msg("Selected endpoint is not available.")
		return nil, err
	}

	return selectedEndpoint, nil
}

// getReconnectEndpoint re-selects a websocket endpoint for the CURRENT session on a
// session rollover. It is the tier-1/tier-2 rebind selector:
//
//   - Tier 1 (seamless): if the ORIGINAL supplier+URL is still in the new session, reuse
//     it. Same node → no data seam for the client's subscriptions.
//   - Tier 2 (survive rotation): if that supplier rotated out — the common case for
//     large-pool services, where the pinned supplier persists only ~NumSuppliersPerSession
//     / poolSize of the time — fall back to the best-available endpoint in the new session
//     so the client's connection survives regardless. Shannon supplier rotation is a
//     protocol detail; the client only wants uninterrupted RPC data.
//
// Only when the new session has zero usable endpoints (or session lookup fails) does it
// error, letting the bridge fall back to closing the client with reconnect guidance.
//
// Returns (endpoint, differentSupplier, failureReason, error): differentSupplier is true
// when tier-2 chose a new supplier; failureReason is a metrics label on error.
//
// avoidPreferred skips tier-1 and excludes preferredAddr from the candidate set: the
// staleness watchdog sets it because the currently bound supplier is the one stalling, so
// reselecting it would just stall again. When it is the only endpoint in the new session,
// selection fails with WSRebindFailedNoEndpoints and the bridge closes the client.
func (p *Protocol) getReconnectEndpoint(
	ctx context.Context,
	serviceID protocol.ServiceID,
	preferredAddr protocol.EndpointAddr,
	httpReq *http.Request,
	avoidPreferred bool,
) (endpoint, bool, string, error) {
	logger := p.logger.With("method", "getReconnectEndpoint", "service_id", serviceID)

	activeSessions, err := p.getActiveGatewaySessions(ctx, serviceID, httpReq)
	if err != nil {
		logger.Error().Err(err).Msg("rebind: failed to retrieve active sessions for the new session")
		return nil, false, metrics.WSRebindFailedSessionError, err
	}

	// Honor a Target-Suppliers pin if the original request carried one; tier-2 stays
	// within the same allowed set.
	allowedSuppliers := parseAllowedSuppliersHeader(httpReq)

	// S1 (disqualify-only): reputation filtering applies on rebind too, so a rollover does
	// not rebind onto a proven-bad WS endpoint (cooldown / below threshold). The preferred
	// (original) supplier is still kept if in cooldown via requestedEndpointAddr
	// race-protection in filterByReputation; the stall watchdog's avoidPreferred path
	// additionally deletes it below. Tiered ranking stays OFF for WS
	// (guarded in getSessionsUniqueEndpoints). The WS safety net there prevents an empty pool.
	const filterByReputation = true
	endpoints, _, err := p.getUniqueEndpoints(ctx, serviceID, activeSessions, filterByReputation, sharedtypes.RPCType_WEBSOCKET, allowedSuppliers, preferredAddr)
	if err != nil {
		logger.Error().Err(err).Msg("rebind: failed to build endpoint set for the new session")
		return nil, false, metrics.WSRebindFailedSessionError, err
	}
	if len(endpoints) == 0 {
		err := fmt.Errorf("%w: service %s new session has no websocket endpoints", protocol.ErrEndpointUnavailable, serviceID)
		return nil, false, metrics.WSRebindFailedNoEndpoints, err
	}

	// Per-operator concentration cap for the rebind target (config-driven, shipped ON).
	// A tier-2/stall rebind otherwise deterministically funnels every connection to the
	// single smallest-address top-scored endpoint; when scores tie (the common case) this
	// re-homes an outsized share onto one operator. The cap spreads the tied-best band
	// across operators, seeded by the connection's original endpoint so the choice stays
	// reproducible across replays.
	maxOperatorShare := 0.0
	if p.unifiedServicesConfig != nil {
		maxOperatorShare = p.unifiedServicesConfig.GetMaxOperatorShareForService(serviceID)
	}

	// Staleness rebind: the current supplier is the one stalling, so exclude it and force
	// a different supplier. If it was the session's only endpoint, there is nothing to
	// escape to → fail so the bridge closes the client.
	if avoidPreferred {
		delete(endpoints, preferredAddr)
		if len(endpoints) == 0 {
			err := fmt.Errorf("%w: service %s new session has no alternate websocket endpoint to escape a stalling supplier", protocol.ErrEndpointUnavailable, serviceID)
			return nil, false, metrics.WSRebindFailedNoEndpoints, err
		}
		ep := selectReconnectEndpointWithCap(serviceID, endpoints, p.websocketReconnectScoreFunc(ctx, serviceID, endpoints), maxOperatorShare)
		logger.Warn().
			Str("stalling_endpoint", string(preferredAddr)).
			Str("replacement_endpoint", string(ep.Addr())).
			Str("replacement_supplier", ep.Supplier()).
			Msg("🔀 [WS-STALL] excluding stalling supplier — rebinding to a different supplier for the current session")
		return ep, true, "", nil
	}

	// Tier 1: original supplier+URL still present → seamless rebind.
	if ep, ok := endpoints[preferredAddr]; ok {
		return ep, false, "", nil
	}

	// Tier 2: original supplier rotated out of the new session → best-available fallback.
	ep := selectReconnectEndpointWithCap(serviceID, endpoints, p.websocketReconnectScoreFunc(ctx, serviceID, endpoints), maxOperatorShare)
	logger.Warn().
		Str("original_endpoint", string(preferredAddr)).
		Str("fallback_endpoint", string(ep.Addr())).
		Str("fallback_supplier", ep.Supplier()).
		Msg("🔀 [WS-REBIND] original supplier rotated out — rebinding to a different supplier for the current session")
	return ep, true, "", nil
}

// selectBestReconnectEndpoint chooses an endpoint from the new session for a tier-2 rebind
// (original supplier rotated out) or a stall escape. Prefers protocol (non-fallback)
// endpoints, then higher WEBSOCKET reputation score (S2a), then the lexicographically
// smallest address so the choice stays reproducible across pods and replays when scores tie.
//
// scoreOf reports each candidate's WEBSOCKET reputation score (higher = healthier); when
// reputation is unavailable it returns an equal value for all endpoints, collapsing the
// ordering back to (non-fallback, then smallest address) — the pre-S2a behavior.
//
// TODO_IMPROVE(@commoddity,@adshmh): once websocket endpoints have active health checks,
// also factor head freshness here to avoid rebinding onto a reachable-but-lagging node.
func selectBestReconnectEndpoint(endpoints map[protocol.EndpointAddr]endpoint, scoreOf func(endpoint) float64) endpoint {
	var best endpoint
	for _, ep := range endpoints {
		if best == nil || betterReconnectCandidate(ep, best, scoreOf) {
			best = ep
		}
	}
	return best
}

// selectReconnectEndpointWithCap chooses a rebind target, applying the per-operator
// concentration cap when enabled.
//
// With the cap disabled (maxOperatorShare <= 0 or >= 1) or nothing to reshape it is
// identical to selectBestReconnectEndpoint — the deterministic (non-fallback, highest
// score, smallest address) pick. With the cap enabled it takes the set of endpoints tied
// for best (same capability tier + top score — the candidates the deterministic pick
// would otherwise break by address) and spreads them across operators via the shared
// concentration cap. The spread is a plain random pick: a WebSocket connection re-selects
// fresh at every session boundary, so there is no value in a per-connection-stable choice
// — and randomness also lets a reconnect *retry* land on a different endpoint than the one
// that just failed to dial.
func selectReconnectEndpointWithCap(
	serviceID protocol.ServiceID,
	endpoints map[protocol.EndpointAddr]endpoint,
	scoreOf func(endpoint) float64,
	maxOperatorShare float64,
) endpoint {
	if maxOperatorShare <= 0 || maxOperatorShare >= 1 || len(endpoints) <= 1 {
		return selectBestReconnectEndpoint(endpoints, scoreOf)
	}

	band := reconnectTopBand(endpoints, scoreOf)
	if len(band) <= 1 {
		return selectBestReconnectEndpoint(endpoints, scoreOf)
	}

	chosen := selector.SelectWithConcentrationCap(serviceID, band, maxOperatorShare)
	if ep, ok := endpoints[chosen]; ok {
		return ep
	}
	// Defensive: the helper always returns a member of the band, but fall back rather than
	// return nil if that ever changes.
	return selectBestReconnectEndpoint(endpoints, scoreOf)
}

// reconnectTopBand returns the endpoint addresses tied for best under the
// (non-fallback preferred, then highest score) ordering — exactly the candidates
// selectBestReconnectEndpoint would otherwise break by smallest address.
//
// "Tied for best" uses exact score equality, matching betterReconnectCandidate (the
// disabled-path picker), so both paths agree on which endpoints are tied. Reputation
// scores are shared values (a stored score, or the same InitialScore constant for unscored
// endpoints), so exact equality is the right test — no float tolerance is needed, and an
// epsilon band here would disagree with the exact comparison there.
func reconnectTopBand(endpoints map[protocol.EndpointAddr]endpoint, scoreOf func(endpoint) float64) []protocol.EndpointAddr {
	hasNonFallback := false
	for _, ep := range endpoints {
		if !ep.IsFallback() {
			hasNonFallback = true
			break
		}
	}

	maxScore := math.Inf(-1)
	for _, ep := range endpoints {
		if hasNonFallback && ep.IsFallback() {
			continue
		}
		if s := scoreOf(ep); s > maxScore {
			maxScore = s
		}
	}

	var band []protocol.EndpointAddr
	for addr, ep := range endpoints {
		if hasNonFallback && ep.IsFallback() {
			continue
		}
		if scoreOf(ep) >= maxScore {
			band = append(band, addr)
		}
	}
	return band
}

// betterReconnectCandidate reports whether a is a better rebind target than b:
// non-fallback endpoints win over fallback; among the same fallback status the higher
// reputation score wins; ties break on the smaller address (deterministic).
func betterReconnectCandidate(a, b endpoint, scoreOf func(endpoint) float64) bool {
	if a.IsFallback() != b.IsFallback() {
		return !a.IsFallback()
	}
	if sa, sb := scoreOf(a), scoreOf(b); sa != sb {
		return sa > sb
	}
	return a.Addr() < b.Addr()
}

// zeroReconnectScore scores every endpoint equally, collapsing selectBestReconnectEndpoint's
// ordering back to (non-fallback, then smallest address) — the behavior used when reputation
// is unavailable (service nil, empty candidate set, or a batch lookup error).
func zeroReconnectScore(endpoint) float64 { return 0 }

// websocketReconnectScoreFunc returns a scoring function over the reconnect candidate
// endpoints keyed by their WEBSOCKET reputation score — the same per-endpoint scores S0
// populates from stall / dial-failure signals. A tier-2 or stall-escape rebind then prefers
// a proven-good endpoint over a degraded one instead of picking by address order.
//
// Missing (never-scored) endpoints default to the service's InitialScore — benefit of the
// doubt, matching RankEndpointsByScore — so a fresh endpoint is not ranked below a scored
// one. When reputation is unavailable (service nil, or a batch lookup error) every endpoint
// scores 0 equally, so selectBestReconnectEndpoint falls back to its deterministic ordering.
func (p *Protocol) websocketReconnectScoreFunc(
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpoints map[protocol.EndpointAddr]endpoint,
) func(endpoint) float64 {
	if p.reputationService == nil || len(endpoints) == 0 {
		return zeroReconnectScore
	}

	keyBuilder := p.reputationService.KeyBuilderForService(serviceID)
	keyByAddr := make(map[protocol.EndpointAddr]reputation.EndpointKey, len(endpoints))
	keys := make([]reputation.EndpointKey, 0, len(endpoints))
	for addr, ep := range endpoints {
		k := keyBuilder.BuildKey(serviceID, ep.Addr(), sharedtypes.RPCType_WEBSOCKET)
		keyByAddr[addr] = k
		keys = append(keys, k)
	}

	scores, err := p.reputationService.GetScores(ctx, keys)
	if err != nil {
		return zeroReconnectScore
	}
	initialScore := p.reputationService.GetInitialScoreForService(serviceID)
	return func(ep endpoint) float64 {
		if s, ok := scores[keyByAddr[ep.Addr()]]; ok {
			return s.Value
		}
		return initialScore
	}
}

// ApplyWebSocketObservations updates protocol instance state based on endpoint observations.
// Records reputation signals from WebSocket health check observations,
// allowing endpoints to recover from failures via successful health checks.
//
// Implements gateway.Protocol interface.
func (p *Protocol) ApplyWebSocketObservations(observations *protocolobservations.Observations) error {
	// Sanity check the input
	if observations == nil || observations.GetShannon() == nil {
		p.logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: ApplyWebSocketObservations called with nil input or nil Shannon observation list.")
		return nil
	}

	shannonObservations := observations.GetShannon().GetObservations()
	if len(shannonObservations) == 0 {
		p.logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: ApplyWebSocketObservations called with nil set of Shannon request observations.")
		return nil
	}

	// Record reputation signals from observations.
	// This allows health check results (from hydrator) to update endpoint reputation scores.
	if p.reputationService != nil {
		p.recordReputationSignalsFromWebsocketObservations(shannonObservations)
	}

	return nil
}

// recordReputationSignalsFromWebsocketObservations maps websocket protocol observations to reputation signals.
// This is called by ApplyWebSocketObservations to update endpoint reputation scores based on
// health check results from the hydrator or any other observation source.
func (p *Protocol) recordReputationSignalsFromWebsocketObservations(shannonObservations []*protocolobservations.ShannonRequestObservations) {
	for _, observationSet := range shannonObservations {
		serviceID := protocol.ServiceID(observationSet.GetServiceId())

		// Process connection observations
		connObs := observationSet.GetWebsocketConnectionObservation()
		if connObs != nil {
			p.recordSignalFromWebsocketConnectionObservation(serviceID, connObs)
		}
	}
}

// recordSignalFromWebsocketConnectionObservation records a reputation signal for a websocket connection observation.
func (p *Protocol) recordSignalFromWebsocketConnectionObservation(serviceID protocol.ServiceID, obs *protocolobservations.ShannonWebsocketConnectionObservation) {
	endpointAddr := protocol.EndpointAddr(obs.GetEndpointUrl())

	// Build endpoint key using key builder to respect key_granularity setting
	rpcType := sharedtypes.RPCType_WEBSOCKET
	keyBuilder := p.reputationService.KeyBuilderForService(serviceID)
	key := keyBuilder.BuildKey(serviceID, endpointAddr, rpcType)

	// Map observation error type to signal
	// Note: We only have errorType in observations now (sanctions removed)
	errorType := obs.GetErrorType()

	var signal reputation.Signal

	// No error = success
	if errorType == protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNSPECIFIED {
		signal = reputation.NewSuccessSignal(0)
	} else {
		// Map error type directly to reputation signal
		// See ERROR_CLASSIFICATION.md for error category documentation
		signal = errorTypeToSignal(errorType, 0)
	}

	// Record signal (fire-and-forget, non-blocking)
	ctx := context.Background()
	if err := p.reputationService.RecordSignal(ctx, key, signal); err != nil {
		p.logger.Warn().Err(err).
			Str("endpoint", string(endpointAddr)).
			Str("service", string(serviceID)).
			Str("error_type", errorType.String()).
			Msg("Failed to record reputation signal from websocket observation")
	}
}

// ---------- Connection Establishment ----------

// startWebSocketBridge creates and starts a Websocket bridge between client and endpoint.
// It handles all protocol-specific setup including headers, URL generation, and connection establishment.
// This is a private method called by BuildWebsocketRequestContextForEndpoint.
func (wrc *websocketRequestContext) startWebSocketBridge(
	ctx context.Context,
	httpRequest *http.Request,
	httpResponseWriter http.ResponseWriter,
	websocketMessageProcessor websockets.WebsocketMessageProcessor,
	messageObservationsChan chan *observation.RequestResponseObservations,
	connectionObservationChan chan *protocolobservations.Observations,
) error {
	logger := wrc.methodLogger("StartWebSocketBridge")

	// Get the websocket-specific URL from the selected endpoint.
	websocketEndpointURL, err := getWebsocketEndpointURL(logger, wrc.selectedEndpoint)
	if err != nil {
		err = fmt.Errorf("%w: selected endpoint does not support websocket RPC type: %s", errCreatingWebSocketConnection, err.Error())
		logger.Error().Err(err).Msg("❌ Selected endpoint does not support websocket RPC type")

		// Record critical error signal - endpoint doesn't support required RPC type.
		// Latency=0 indicates this is a setup/configuration error, not a response time measurement.
		wrc.recordWebsocketSignal(reputation.NewCriticalErrorSignal("ws_url_not_supported", 0))
		connectionObservationChan <- getWebsocketConnectionErrorObservation(logger, wrc.serviceID, wrc.selectedEndpoint, err)

		return fmt.Errorf("selected endpoint does not support websocket RPC type: %w", err)
	}
	// One-shot mutation: bake the resolved websocket URL into the connection
	// logger so the long-lived bridge goroutine and per-message methods all
	// inherit it. Per-connection only — never per-message.
	wrc.logger = wrc.logger.With("websocket_url", websocketEndpointURL)
	logger = logger.With("websocket_url", websocketEndpointURL)

	// Get the headers for the websocket connection that will be sent to the endpoint.
	endpointConnectionHeaders, err := getWebsocketConnectionHeaders(logger, wrc.selectedEndpoint)
	if err != nil {
		err = fmt.Errorf("%w: failed to get websocket connection headers: %s", errCreatingWebSocketConnection, err.Error())
		logger.Error().Err(err).Msg("❌ Failed to get websocket connection headers")

		// Record major error signal - header construction failed.
		// Latency=0 indicates this is a setup error, not a response time measurement.
		wrc.recordWebsocketSignal(reputation.NewMajorErrorSignal("ws_headers_failed", 0))
		connectionObservationChan <- getWebsocketConnectionErrorObservation(logger, wrc.serviceID, wrc.selectedEndpoint, err)

		return fmt.Errorf("failed to get websocket connection headers: %w", err)
	}

	// Add handshake signature for non-fallback endpoints.
	// This allows RelayMiner to validate the connection upfront (eager validation).
	if !wrc.selectedEndpoint.IsFallback() {
		signature, err := wrc.generateHandshakeSignature()
		if err != nil {
			err = fmt.Errorf("%w: failed to generate handshake signature: %s", errCreatingWebSocketConnection, err.Error())
			logger.Error().Err(err).Msg("❌ Failed to generate handshake signature")

			wrc.recordWebsocketSignal(reputation.NewMajorErrorSignal("ws_signature_failed", 0))
			connectionObservationChan <- getWebsocketConnectionErrorObservation(logger, wrc.serviceID, wrc.selectedEndpoint, err)

			return fmt.Errorf("failed to generate handshake signature: %w", err)
		}
		endpointConnectionHeaders.Set(request.HTTPHeaderSignature, signature)
	}

	// Start the websocket bridge and get a completion channel.
	// The websocketRequestContext handles message processing.
	// When session rebind is enabled, this context is the bridge's EndpointReconnector so
	// an endpoint session-rollover disconnect rebinds the endpoint and replays
	// subscriptions instead of closing the client. Nil (rebind disabled) preserves the
	// pre-rebind behavior where the disconnect closes the client.
	var reconnector websockets.EndpointReconnector
	if wrc.reconnectProvider != nil {
		reconnector = wrc
	}
	bridgeCompletionChan, err := websockets.StartBridge(
		ctx,
		wrc.logger,
		httpRequest,
		httpResponseWriter,
		websocketEndpointURL,
		endpointConnectionHeaders,
		websocketMessageProcessor,
		messageObservationsChan,
		reconnector,
	)
	if err != nil {
		err = fmt.Errorf("%w: failed to start websocket bridge: %s", errCreatingWebSocketConnection, err.Error())
		logger.Error().Err(err).Msg("❌ Failed to start Websocket bridge")

		// Record major error signal - connection establishment failed.
		// Latency=0 indicates this is a connection setup error, not a response time measurement.
		wrc.recordWebsocketSignal(reputation.NewMajorErrorSignal("ws_connection_failed", 0))
		connectionObservationChan <- getWebsocketConnectionErrorObservation(logger, wrc.serviceID, wrc.selectedEndpoint, err)

		return fmt.Errorf("failed to start websocket bridge: %w", err)
	}

	// Start goroutine to handle bridge lifecycle observations
	go func() {
		defer close(connectionObservationChan)

		// Capture the establishment time once and carry it through to the closure
		// observation so the gateway can compute a real connection duration
		// (closedAt - establishedAt). The bridge is live at this point.
		establishedAt := time.Now()

		// Send establishment observation immediately (buffered channel ensures it's captured)
		wrc.logger.Debug().Msg("Websocket bridge started successfully, sending establishment observation")
		connectionObservationChan <- getWebsocketConnectionEstablishedObservation(wrc.logger, wrc.serviceID, wrc.selectedEndpoint, establishedAt)

		// Wait for the bridge to complete (blocks until Websocket connection terminates)
		<-bridgeCompletionChan
		// Send closure observation, stamping the actual close time.
		wrc.logger.Debug().Msg("Websocket connection closed, sending closure observation")
		connectionObservationChan <- getWebsocketConnectionClosedObservation(wrc.logger, wrc.serviceID, wrc.selectedEndpoint, establishedAt, time.Now())
	}()

	return nil
}

// getWebsocketConnectionHeaders returns headers for the websocket connection:
//   - Target-Service-Id: The service ID of the target service
//   - App-Address: The address of the session's application
//   - Rpc-Type: Always "websocket" for websocket connection requests
func getWebsocketConnectionHeaders(logger polylog.Logger, selectedEndpoint endpoint) (http.Header, error) {
	// Requests to fallback endpoints bypass the protocol so RelayMiner headers are not needed.
	// TODO_IMPROVE(@commoddity,@adshmh): Cleanly separate fallback endpoint handling from the protocol package.
	// TODO_ARCHITECTURE: Extract fallback endpoint handling from protocol package
	// Current: Fallback logic is scattered with if wrc.selectedEndpoint.IsFallback() checks
	// Suggestion: Use strategy pattern or separate fallback handler to cleanly separate concerns
	if selectedEndpoint.IsFallback() {
		return http.Header{}, nil
	}

	// If the selected endpoint is a protocol endpoint, add the headers
	// that the RelayMiner requires to forward the request to the Endpoint.
	return getRelayMinerConnectionHeaders(logger, selectedEndpoint)
}

// getRelayMinerConnectionHeaders returns headers for RelayMiner websocket connections.
// These headers carry RelayRequest.Meta equivalent data for connection-time validation.
func getRelayMinerConnectionHeaders(logger polylog.Logger, selectedEndpoint endpoint) (http.Header, error) {
	logger.With("method", "getRelayMinerConnectionHeaders")

	sessionHeader := selectedEndpoint.Session().GetHeader()

	if sessionHeader == nil {
		logger.Error().Msg("❌ SHOULD NEVER HAPPEN: Error getting relay miner connection headers: session header is nil")
		return http.Header{}, fmt.Errorf("session header is nil")
	}

	return http.Header{
		// Original headers
		request.HTTPHeaderTargetServiceID: {sessionHeader.ServiceId},
		request.HTTPHeaderAppAddress:      {sessionHeader.ApplicationAddress},
		proxy.RPCTypeHeader:               {strconv.Itoa(int(sharedtypes.RPCType_WEBSOCKET))},

		// Session metadata headers for RelayMiner validation
		request.HTTPHeaderSessionID:          {sessionHeader.SessionId},
		request.HTTPHeaderSessionStartHeight: {strconv.FormatInt(sessionHeader.SessionStartBlockHeight, 10)},
		request.HTTPHeaderSessionEndHeight:   {strconv.FormatInt(sessionHeader.SessionEndBlockHeight, 10)},
		request.HTTPHeaderSupplierAddress:    {selectedEndpoint.Supplier()},
		// Note: Signature is added separately by addHandshakeSignature when signer is available
	}, nil
}

// getWebsocketEndpointURL returns the websocket URL for the selected endpoint.
// This URL is used to establish the websocket connection to the endpoint.
func getWebsocketEndpointURL(logger polylog.Logger, selectedEndpoint endpoint) (string, error) {
	logger.With("method", "getWebsocketEndpointURL")

	websocketURL, err := selectedEndpoint.WebsocketURL()
	if err != nil {
		logger.Error().Err(err).Msg("❌ Selected endpoint does not support websocket RPC type")
		return "", err
	}

	return websocketURL, nil
}

// generateHandshakeSignature generates a ring signature for the WebSocket handshake.
// This signature is included in the Pocket-Signature header and allows the RelayMiner
// to validate the connection upfront (eager validation) before accepting WebSocket messages.
// The signature is over the same data that would be in a RelayRequest.Meta with empty payload.
func (wrc *websocketRequestContext) generateHandshakeSignature() (string, error) {
	logger := wrc.methodLogger("generateHandshakeSignature")

	// Create a relay request with empty payload for signing.
	// The signature covers the session metadata (same as for regular relay requests).
	// Uses the live session so a reconnect handshake is signed for the new session.
	signingEndpoint := wrc.signingEndpoint()
	handshakeRelayRequest := &servicetypes.RelayRequest{
		Meta: servicetypes.RelayRequestMetadata{
			SessionHeader:           signingEndpoint.Session().GetHeader(),
			SupplierOperatorAddress: signingEndpoint.Supplier(),
		},
		Payload: nil, // Empty payload for handshake
	}

	app := signingEndpoint.Session().GetApplication()
	if app == nil {
		logger.Error().Msg("❌ SHOULD NEVER HAPPEN: session application is nil")
		return "", fmt.Errorf("session application is nil")
	}

	// Sign the handshake relay request using the same signer as regular relay messages.
	signedHandshake, err := wrc.relayRequestSigner.SignRelayRequest(handshakeRelayRequest, *app)
	if err != nil {
		return "", fmt.Errorf("failed to sign handshake: %w", err)
	}

	// Extract the signature bytes from the signed request.
	// The Meta.Signature field contains the ring signature bytes.
	signatureBytes := signedHandshake.Meta.Signature

	// Encode as base64 for transport in HTTP header.
	signatureBase64 := base64.StdEncoding.EncodeToString(signatureBytes)

	logger.Debug().
		Str("session_id", signedHandshake.Meta.SessionHeader.SessionId).
		Int("signature_length", len(signatureBytes)).
		Msg("Generated handshake signature")

	return signatureBase64, nil
}

// ---------- Session Rebind (EndpointReconnector) ----------

// ReconnectEndpoint re-establishes the endpoint connection for the CURRENT session
// after a rollover, implementing websockets.EndpointReconnector. It re-selects an endpoint
// bound to the new session — the original supplier if it is still present, else the
// best-available one (tier-2) — updates the signing endpoint so subsequent frames sign
// against it, and dials a fresh endpoint websocket connection with new-session headers and
// a re-signed handshake.
//
// Runs on the bridge's message-processing goroutine (the only reader/writer of
// reconnectEndpoint), so the endpoint swap needs no synchronization.
func (wrc *websocketRequestContext) ReconnectEndpoint(ctx context.Context, avoidCurrentSupplier bool) (*gorillaws.Conn, error) {
	logger := wrc.methodLogger("ReconnectEndpoint")

	if wrc.reconnectProvider == nil {
		return nil, fmt.Errorf("websocket session rebind not configured")
	}

	// Re-select an endpoint for the current session: the original supplier if it is still
	// in the new session (seamless), else the best-available endpoint (tier-2). When
	// avoidCurrentSupplier is set (staleness watchdog), the original supplier is excluded
	// so the rebind escapes it. Only a session lookup failure or a session with no usable
	// endpoints errors here — the bridge then falls back to closing the client with
	// reconnect guidance.
	ep, differentSupplier, failureReason, err := wrc.reconnectProvider(ctx, avoidCurrentSupplier)
	if err != nil {
		wrc.lastReconnectReason = failureReason
		return nil, fmt.Errorf("re-select endpoint for current session: %w", err)
	}

	// Update the signing endpoint FIRST so the URL/headers/handshake below (and all
	// subsequent message signing) use the new session. Record whether this is a
	// different-supplier (tier-2) rebind so OnReconnectOutcome can tag the metric.
	wrc.reconnectEndpoint = ep
	wrc.lastReconnectDifferentSupplier = differentSupplier

	websocketEndpointURL, err := getWebsocketEndpointURL(logger, ep)
	if err != nil {
		wrc.lastReconnectReason = metrics.WSRebindFailedSelect
		return nil, fmt.Errorf("resolve websocket URL for reconnect: %w", err)
	}

	endpointConnectionHeaders, err := getWebsocketConnectionHeaders(logger, ep)
	if err != nil {
		wrc.lastReconnectReason = metrics.WSRebindFailedSelect
		return nil, fmt.Errorf("build websocket headers for reconnect: %w", err)
	}

	// Non-fallback endpoints carry a handshake signature over the new session.
	if !ep.IsFallback() {
		signature, err := wrc.generateHandshakeSignature() // signs with signingEndpoint() == ep
		if err != nil {
			wrc.lastReconnectReason = metrics.WSRebindFailedSelect
			return nil, fmt.Errorf("generate handshake signature for reconnect: %w", err)
		}
		endpointConnectionHeaders.Set(request.HTTPHeaderSignature, signature)
	}

	conn, err := websockets.ConnectWebsocketEndpoint(logger, websocketEndpointURL, endpointConnectionHeaders)
	if err != nil {
		wrc.lastReconnectReason = metrics.WSRebindFailedDial
		// S0: the reconnect target failed to dial — feed it into reputation so its
		// :websocket score reflects the failure. signingEndpoint() == ep here
		// (wrc.reconnectEndpoint was set above), so the signal attributes to the endpoint
		// that could not be reached.
		wrc.recordWebsocketSignal(reputation.NewMajorErrorSignal("ws_reconnect_dial_failed", 0))
		return nil, fmt.Errorf("dial reconnect endpoint: %w", err)
	}

	logger.Info().
		Str("endpoint_addr", string(ep.Addr())).
		Str("supplier", ep.Supplier()).
		Msg("🔁 re-established websocket endpoint connection for current session")
	return conn, nil
}

// SubscriptionReplayFrames returns the client's active subscribe frames, signed for the
// CURRENT session, to be replayed onto the reconnected endpoint. Implements
// websockets.EndpointReconnector. Called after ReconnectEndpoint, so signingEndpoint()
// already points at the new session.
func (wrc *websocketRequestContext) SubscriptionReplayFrames() ([][]byte, error) {
	if wrc.registry == nil {
		return nil, nil
	}

	rawFrames := wrc.registry.ActiveSubscribeFrames()
	if len(rawFrames) == 0 {
		return nil, nil
	}

	// Fallback endpoints bypass the protocol, so their frames are replayed unsigned —
	// mirroring ProcessProtocolClientWebsocketMessage.
	if wrc.signingEndpoint().IsFallback() {
		return rawFrames, nil
	}

	signedFrames := make([][]byte, 0, len(rawFrames))
	for i, frame := range rawFrames {
		signed, err := wrc.signClientWebsocketMessage(frame)
		if err != nil {
			return nil, fmt.Errorf("sign replay subscribe frame %d: %w", i, err)
		}
		signedFrames = append(signedFrames, signed)
	}
	return signedFrames, nil
}

// OnReconnectOutcome records the terminal result of a rebind episode for canary
// observability (metric + a log visible at LOG_LEVEL=error). Implements
// websockets.EndpointReconnector.
func (wrc *websocketRequestContext) OnReconnectOutcome(success bool, replayedSubscriptions int, stage websockets.ReconnectFailureStage) {
	domain, domainErr := shannonmetrics.ExtractDomainOrHost(wrc.signingEndpoint().PublicURL())
	if domainErr != nil {
		domain = shannonmetrics.ErrDomain
	}
	serviceID := string(wrc.serviceID)

	result := wrc.reconnectResultLabel(success, stage)
	metrics.RecordWebsocketRebind(domain, serviceID, result, replayedSubscriptions)

	// Error level ON PURPOSE for canary visibility. Temporary — downgrade once validated.
	wrc.logger.Error().
		Bool("success", success).
		Bool("different_supplier", wrc.lastReconnectDifferentSupplier).
		Int("replayed_subscriptions", replayedSubscriptions).
		Str("result", result).
		Msg("📈 [WS-REBIND] rebind outcome recorded")
}

// reconnectResultLabel maps a rebind outcome to a WSRebind* metric label, combining the
// bridge-reported failure stage with the selection detail stashed during ReconnectEndpoint
// (same- vs different-supplier on success; the specific reason on a selection failure).
func (wrc *websocketRequestContext) reconnectResultLabel(success bool, stage websockets.ReconnectFailureStage) string {
	switch {
	case success && wrc.lastReconnectDifferentSupplier:
		return metrics.WSRebindSuccessDifferentSupplier
	case success:
		return metrics.WSRebindSuccessSameSupplier
	case stage == websockets.ReconnectStageReplay:
		return metrics.WSRebindFailedReplay
	default: // ReconnectStageSelect — use the specific reason stashed by ReconnectEndpoint
		if wrc.lastReconnectReason != "" {
			return wrc.lastReconnectReason
		}
		return metrics.WSRebindFailedSelect
	}
}

// HasActiveSubscriptions reports whether the client holds at least one established
// subscription that a rebind could replay. Implements websockets.EndpointReconnector; the
// staleness watchdog uses it to decide whether a silent endpoint is worth rebinding.
func (wrc *websocketRequestContext) HasActiveSubscriptions() bool {
	return wrc.registry != nil && wrc.registry.HasActiveSubscriptions()
}

// OnEndpointStallDetected records that the staleness watchdog fired, for canary
// observability. gaveUp=false: forcing a rebind onto a different supplier; gaveUp=true:
// the endpoint stayed silent across repeated rebinds and the bridge is closing the client.
// Implements websockets.EndpointReconnector.
func (wrc *websocketRequestContext) OnEndpointStallDetected(gaveUp bool) {
	domain, domainErr := shannonmetrics.ExtractDomainOrHost(wrc.signingEndpoint().PublicURL())
	if domainErr != nil {
		domain = shannonmetrics.ErrDomain
	}
	serviceID := string(wrc.serviceID)

	metrics.RecordWebsocketEndpointStall(domain, serviceID, gaveUp)

	// S0: feed the stall into reputation so the endpoint's :websocket score reflects it.
	// recordWebsocketSignal attributes to signingEndpoint(). The bridge invokes this handler
	// and the subsequent rebind on its single start() goroutine, in order (stall-detect →
	// OnEndpointStallDetected → handleEndpointDown → ReconnectEndpoint), so signingEndpoint()
	// still resolves to the stalling supplier here — the rebind has not yet reassigned
	// reconnectEndpoint. (Even across earlier rebinds, signingEndpoint() is always the CURRENT
	// serving supplier, which is exactly who should be penalized for stalling.) A stalled data
	// feed is a real endpoint fault → Major (same weight as a connection/handshake failure);
	// consistent stalls accrue toward cooldown.
	wrc.recordWebsocketSignal(reputation.NewMajorErrorSignal("ws_endpoint_stalled", 0))

	// Error level ON PURPOSE for canary visibility. Temporary — downgrade once validated.
	wrc.logger.Error().
		Str("domain", domain).
		Bool("gave_up", gaveUp).
		Msg("📉 [WS-STALL] endpoint stall recorded")
}

// ---------- Client Message Processing ----------

// ProcessProtocolClientWebsocketMessage processes a message from the client.
// Implements gateway.ProtocolRequestContextWebsocket interface.
func (wrc *websocketRequestContext) ProcessProtocolClientWebsocketMessage(msgData []byte) ([]byte, error) {
	logger := wrc.methodLogger("ProcessClientWebsocketMessage")

	logger.Debug().Msgf("received message from client: %s", string(msgData))

	// Extract domain for message metrics
	domain, domainErr := shannonmetrics.ExtractDomainOrHost(wrc.selectedEndpoint.PublicURL())
	if domainErr != nil {
		domain = shannonmetrics.ErrDomain
	}
	serviceID := string(wrc.serviceID)

	// Session rebind: record an eth_subscribe for later replay, and rewrite an
	// eth_unsubscribe's client-facing subscription id to the current supplier's id. This
	// must run BEFORE signing so the signed frame carries the rewritten id. No-op
	// (frame returned unchanged) when rebind is disabled or the frame is not a
	// subscribe/unsubscribe.
	if wrc.registry != nil {
		msgData = wrc.registry.TrackClientMessage(msgData)
	}

	// If the selected endpoint is a fallback endpoint, skip signing the message.
	// Fallback endpoints bypass the protocol so the raw message is sent to the endpoint.
	// TODO_IMPROVE(@commoddity,@adshmh): Cleanly separate fallback endpoint handling from the protocol package.
	if wrc.selectedEndpoint.IsFallback() {
		// Record message metric for client→endpoint direction (fallback = always success)
		metrics.RecordWebsocketMessage(domain, serviceID, metrics.WSDirectionClientToEndpoint, metrics.SignalOK)
		return msgData, nil
	}

	// If the selected endpoint is a protocol endpoint, we need to sign the message.
	signedRelayRequest, err := wrc.signClientWebsocketMessage(msgData)
	if err != nil {
		logger.Error().Err(err).Msg("❌ failed to sign request")
		// Record message metric for client→endpoint direction with error
		// Note: signing errors are PATH-side, not endpoint quality issues, but we track for observability
		metrics.RecordWebsocketMessage(domain, serviceID, metrics.WSDirectionClientToEndpoint, metrics.SignalMinorError)
		return nil, err
	}

	// Record message metric for client→endpoint direction
	metrics.RecordWebsocketMessage(domain, serviceID, metrics.WSDirectionClientToEndpoint, metrics.SignalOK)
	return signedRelayRequest, nil
}

// signClientWebsocketMessage signs a message from the client using the Relay Request Signer.
func (wrc *websocketRequestContext) signClientWebsocketMessage(msgData []byte) ([]byte, error) {
	logger := wrc.methodLogger("signClientWebsocketMessage")

	// Sign against the live session (the rebound endpoint after a rollover, else the original).
	signingEndpoint := wrc.signingEndpoint()
	unsignedRelayRequest := &servicetypes.RelayRequest{
		Meta: servicetypes.RelayRequestMetadata{
			SessionHeader:           signingEndpoint.Session().GetHeader(),
			SupplierOperatorAddress: signingEndpoint.Supplier(),
		},
		Payload: msgData,
	}

	app := signingEndpoint.Session().GetApplication()
	if app == nil {
		logger.Error().Msg("❌ SHOULD NEVER HAPPEN: session application is nil")
		return nil, fmt.Errorf("session application is nil")
	}

	signedRelayRequest, err := wrc.relayRequestSigner.SignRelayRequest(unsignedRelayRequest, *app)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errRelayRequestWebsocketMessageSigningFailed, err.Error())
	}

	relayRequestBz, err := signedRelayRequest.Marshal()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errRelayRequestWebsocketMessageSigningFailed, err.Error())
	}

	return relayRequestBz, nil
}

// ---------- Endpoint Message Processing ----------

// ProcessProtocolEndpointWebsocketMessage processes a message from the endpoint.
func (wrc *websocketRequestContext) ProcessProtocolEndpointWebsocketMessage(
	msgData []byte,
) ([]byte, protocolobservations.Observations, error) {
	logger := wrc.methodLogger("ProcessEndpointWebsocketMessage")
	startTime := time.Now()

	logger.Debug().Msgf("received message from endpoint: %s", string(msgData))

	// Extract domain for message metrics
	domain, domainErr := shannonmetrics.ExtractDomainOrHost(wrc.selectedEndpoint.PublicURL())
	if domainErr != nil {
		domain = shannonmetrics.ErrDomain
	}
	serviceID := string(wrc.serviceID)

	// If the selected endpoint is a fallback endpoint, skip validation.
	// Fallback endpoints bypass the protocol so the raw message is sent to the endpoint.
	// TODO_IMPROVE(@commoddity,@adshmh): Cleanly separate fallback endpoint handling from the protocol package.
	if wrc.selectedEndpoint.IsFallback() {
		// Record success signal for fallback endpoint messages
		wrc.recordWebsocketSignal(reputation.NewSuccessSignal(time.Since(startTime)))
		// Record message metric for endpoint→client direction
		metrics.RecordWebsocketMessage(domain, serviceID, metrics.WSDirectionEndpointToClient, metrics.SignalOK)
		return msgData, getWebsocketMessageSuccessObservation(logger, wrc.serviceID, wrc.selectedEndpoint, msgData), nil
	}

	// If the selected endpoint is a protocol endpoint, we need to validate the message.
	endpointMessageBz, statusCode, err := wrc.validateEndpointWebsocketMessage(msgData)
	if err != nil {
		logger.Error().Err(err).Msg("❌ failed to validate relay response")
		// Record error signal for message validation failure
		wrc.recordWebsocketSignal(reputation.NewMajorErrorSignal("ws_message_validation_failed", time.Since(startTime)))
		// Record message metric for endpoint→client direction with error
		metrics.RecordWebsocketMessage(domain, serviceID, metrics.WSDirectionEndpointToClient, metrics.SignalMajorError)
		return nil, getWebsocketMessageErrorObservation(logger, wrc.serviceID, wrc.selectedEndpoint, msgData, err), err
	}

	// A non-2xx status carried in a POKTHTTPResponse envelope (e.g. "session
	// expired" / 410 when a connection lands on an already-expired session) is an
	// endpoint/session control error, NOT a healthy message. Previously the whole
	// path returned the raw payload with no status, so this case (a) leaked the
	// undecoded protobuf envelope to the client and (b) recorded a SUCCESS signal —
	// rewarding the supplier for returning an error.
	//
	// Do not record success. Do not penalize either: a session-expiry is a
	// session-boundary / PATH-timing issue, not a supplier fault (penalizing here
	// would just be a new reputation leak). Record the metric for observability and
	// forward the DECODED body so the client receives readable JSON instead of a
	// protobuf blob; the endpoint's close frame follows and the client reconnects.
	if statusCode < 200 || statusCode >= 300 {
		// When session rebind is enabled, swallow the relay miner's session-expiry
		// advisory (HTTP 410 "session expired"). The miner sends this envelope frame
		// immediately BEFORE the 4000 close frame (see relay-miner websocket.go
		// handleSessionExpiration → sendSessionExpirationMessage then closeWithReason).
		// The 4000 close drives a transparent reconnect, so forwarding the 410 would show
		// the client a spurious "session expired" error mid-stream. Drop it (nil body);
		// the client sees only the continuous stream. If the reconnect ultimately fails
		// the bridge still closes the client with 1012. Non-410 errors are still forwarded
		// so genuine endpoint errors reach the client.
		if wrc.registry != nil && statusCode == http.StatusGone {
			// Error level for canary visibility (LOG_LEVEL=error). Temporary.
			logger.Error().Msg("🤫 [WS-REBIND] swallowing session-expiry (410) advisory — rebind will reconnect transparently")
			metrics.RecordWebsocketMessage(domain, serviceID, metrics.WSDirectionEndpointToClient, metrics.SignalMinorError)
			return nil, getWebsocketMessageErrorObservation(logger, wrc.serviceID, wrc.selectedEndpoint, msgData, fmt.Errorf("session expired (rebind pending)")), nil
		}

		logger.Warn().
			Int("http_status_code", statusCode).
			Str("body", string(endpointMessageBz)).
			Msg("⚠️ endpoint returned a non-2xx response over websocket — forwarding decoded body, no reputation change")
		metrics.RecordWebsocketMessage(domain, serviceID, metrics.WSDirectionEndpointToClient, metrics.SignalMinorError)
		return endpointMessageBz,
			getWebsocketMessageErrorObservation(logger, wrc.serviceID, wrc.selectedEndpoint, msgData, fmt.Errorf("endpoint returned HTTP %d over websocket", statusCode)),
			nil
	}

	// Session rebind: remap a subscription notification's id back to the client-facing
	// id, or swallow a replay subscribe response the client must not see (forward=false).
	// No-op when rebind is disabled.
	forward := true
	if wrc.registry != nil {
		endpointMessageBz, forward = wrc.registry.TranslateEndpointMessage(endpointMessageBz)
	}

	// Record success signal for validated message
	wrc.recordWebsocketSignal(reputation.NewSuccessSignal(time.Since(startTime)))
	// Record message metric for endpoint→client direction
	metrics.RecordWebsocketMessage(domain, serviceID, metrics.WSDirectionEndpointToClient, metrics.SignalOK)

	// A swallowed replay response is a valid, successful endpoint reply (recorded above)
	// that must not reach the client — return a nil body so the bridge drops it.
	if !forward {
		return nil, getWebsocketMessageSuccessObservation(logger, wrc.serviceID, wrc.selectedEndpoint, msgData), nil
	}
	return endpointMessageBz, getWebsocketMessageSuccessObservation(logger, wrc.serviceID, wrc.selectedEndpoint, msgData), nil
}

// validateEndpointWebsocketMessage validates a message from the endpoint using the
// Shannon FullNode and returns the decoded response body plus the HTTP status the
// relay miner reported (200 for a raw streaming frame).
//
// Normal websocket data frames are delivered as the raw payload (a JSON-RPC response
// or subscription push). Control/error responses — e.g. "session expired" when a
// connection lands on an already-expired session — are delivered as a serialized
// POKTHTTPResponse envelope (status + headers + body) in the SAME payload field.
// Returning that verbatim (as this used to) ships an undecoded protobuf blob to the
// client and hides the real status, so a non-2xx error is recorded as a healthy
// message. Unwrap the envelope so the caller sees the status and the client always
// gets the decoded body.
//
// Discriminator: a raw data frame is JSON and starts with '{' or '['; a serialized
// POKTHTTPResponse starts with a protobuf tag byte (never '{'/'['). Only non-JSON
// payloads are decoded as an envelope, and only when they actually parse as one, so a
// normal frame is never misparsed. Raw frames report status 200.
//
// TODO_IMPROVE(@adshmh): Compare this to 'validateAndProcessResponse' and align the two
// implementations w.r.t design, error handling, etc...
func (wrc *websocketRequestContext) validateEndpointWebsocketMessage(msgData []byte) ([]byte, int, error) {
	logger := wrc.methodLogger("validateEndpointWebsocketMessage")

	// Validate the relay response using the Shannon FullNode (verifies the supplier
	// signature and unwraps the RelayResponse envelope). Uses the live session's supplier
	// so a response from a rebound endpoint validates against the correct supplier.
	relayResponse, err := wrc.fullNode.ValidateRelayResponse(sdk.SupplierAddress(wrc.signingEndpoint().Supplier()), msgData)
	if err != nil {
		logger.Error().Err(err).Msg("❌ failed to validate relay response in websocket message")
		return nil, 0, fmt.Errorf("%w: %s", errRelayResponseInWebsocketMessageValidationFailed, err.Error())
	}

	body, statusCode, unwrapped := extractWebsocketMessageBody(relayResponse.Payload)
	if unwrapped {
		logger.Debug().
			Int("http_status_code", statusCode).
			Msgf("unwrapped POKTHTTPResponse envelope from endpoint websocket message: %s", string(body))
	} else {
		logger.Debug().Msgf("received message from protocol endpoint: %s", string(body))
	}
	return body, statusCode, nil
}

// extractWebsocketMessageBody returns the response body and HTTP status carried by a
// validated relay-response payload, plus whether it was a POKTHTTPResponse envelope.
//
// Normal streaming frames are raw JSON (a JSON-RPC response or a subscription push)
// and are returned as-is with status 200. Control/error responses — e.g. "session
// expired" (HTTP 410) when a connection lands on an already-expired session — are
// delivered as a serialized POKTHTTPResponse envelope (status + headers + body) in
// the SAME payload field; they are decoded so the caller sees the real status and the
// client receives the decoded body rather than an undecoded protobuf blob.
//
// Discriminator: a raw data frame is JSON and starts with '{' or '['; a serialized
// POKTHTTPResponse starts with a protobuf tag byte (never '{'/'['). Only non-JSON
// payloads are considered, and only when they actually parse as an envelope, so a
// normal frame is never misparsed.
func extractWebsocketMessageBody(payload []byte) (body []byte, statusCode int, unwrapped bool) {
	if trimmed := bytes.TrimSpace(payload); len(trimmed) > 0 && trimmed[0] != '{' && trimmed[0] != '[' {
		if httpResp, err := deserializeRelayResponse(payload); err == nil {
			return httpResp.Bytes, httpResp.HTTPStatusCode, true
		}
	}
	return payload, http.StatusOK, false
}

// ---------- Logger Helpers ----------

// buildWebsocketConnectionLogger constructs the per-connection logger ONCE,
// at request-context setup. The supplier, URL, and session app are immutable
// for the connection's lifetime so adding them here avoids re-allocating the
// same context on every message.
//
// Background: this used to be done per-message inside hydratedLogger, which
// also reassigned wrc.logger and so accumulated fields over the connection
// lifetime — pprof showed it as ~36% of all heap allocations on a busy pod.
func buildWebsocketConnectionLogger(
	base polylog.Logger,
	serviceID protocol.ServiceID,
	selectedEndpoint endpoint,
) polylog.Logger {
	logger := base.With(
		"request_type", "websocket",
		"service_id", serviceID,
	)
	if selectedEndpoint == nil {
		return logger
	}
	logger = logger.With(
		"selected_endpoint_supplier", selectedEndpoint.Supplier(),
		"selected_endpoint_url", selectedEndpoint.PublicURL(),
		"endpoint_addr", selectedEndpoint.Addr(),
	)
	if header := selectedEndpoint.Session().GetHeader(); header != nil {
		logger = logger.With("selected_endpoint_app", header.ApplicationAddress)
	}
	return logger
}

// methodLogger returns a per-method logger derived from the connection logger.
// Single allocation per call; does NOT mutate wrc.logger (so the connection
// logger never grows unbounded across messages).
func (wrc *websocketRequestContext) methodLogger(methodName string) polylog.Logger {
	return wrc.logger.With("method", methodName)
}

// ---------- Reputation Signal Recording ----------

// recordWebsocketSignal records a reputation signal for the websocket endpoint.
// This tracks the health of endpoints used for websocket connections.
func (wrc *websocketRequestContext) recordWebsocketSignal(signal reputation.Signal) {
	// Attribute the signal to the live endpoint (the rebound one after a rollover).
	signalEndpoint := wrc.signingEndpoint()
	if wrc.reputationService == nil || signalEndpoint == nil {
		return
	}

	// Build endpoint key using key builder to respect key_granularity setting
	rpcType := sharedtypes.RPCType_WEBSOCKET
	keyBuilder := wrc.reputationService.KeyBuilderForService(wrc.serviceID)
	endpointKey := keyBuilder.BuildKey(wrc.serviceID, signalEndpoint.Addr(), rpcType)

	// Extract domain for metrics
	endpointDomain, domainErr := shannonmetrics.ExtractDomainOrHost(signalEndpoint.PublicURL())
	if domainErr != nil {
		endpointDomain = shannonmetrics.ErrDomain
	}

	wrc.logger.Debug().
		Str("endpoint_addr", string(signalEndpoint.Addr())).
		Str("endpoint_domain", endpointDomain).
		Str("signal_type", string(signal.Type)).
		Msg("Recording websocket reputation signal")

	// Fire-and-forget: don't block request on reputation recording
	if err := wrc.reputationService.RecordSignal(wrc.context, endpointKey, signal); err != nil {
		wrc.logger.Warn().Err(err).Msg("Failed to record websocket reputation signal")
	}
}
