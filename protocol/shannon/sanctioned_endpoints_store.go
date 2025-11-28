package shannon

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/metrics/devtools"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
)

// Constants for sanction expiration and cache cleanup
const (
	// Default TTL for session-limited sanctions
	// TODO_TECHDEBT(@olshansk): Align with protocol parameters for session length (may change in Shannon)
	defaultSessionSanctionExpiration = 1 * time.Hour

	// Interval for purging expired items from the cache
	// DEV_NOTE: Arbitrarily selected; can be changed as needed
	// TODO_TECHDEBT(@olshansk): Re-evaluate appropriate value
	defaultSanctionCacheCleanupInterval = 10 * time.Minute
)

// sanctionedEndpointsStore:
//   - Tracks sanctioned endpoints
//   - Supports both permanent and session-limited sanctions
//   - Session sanctions expire automatically via go-cache
type sanctionedEndpointsStore struct {
	logger polylog.Logger

	// permanentSanctions:
	//   - In-memory map of endpoints with permanent sanctions
	//   - Persists for process lifetime (not on disk)
	//   - Lost on PATH process restart; not shared across instances
	permanentSanctions      map[protocol.EndpointAddr]sanction
	permanentSanctionsMutex sync.RWMutex

	// sessionSanctionsCache:
	//   - Stores session-limited sanctions (auto-expire)
	//   - Key: endpoint address (protocol.EndpointAddr) + session key
	//   - Expire after defaultSessionSanctionExpiration
	//   - Lost on PATH process restart; not shared across instances
	sessionSanctionsCache *cache.Cache
}

// newSanctionedEndpointsStore:
//   - Instantiates a new sanctionedEndpointsStore with logging and caches
func newSanctionedEndpointsStore(logger polylog.Logger) *sanctionedEndpointsStore {
	return &sanctionedEndpointsStore{
		logger:                logger,
		permanentSanctions:    make(map[protocol.EndpointAddr]sanction),
		sessionSanctionsCache: cache.New(defaultSessionSanctionExpiration, defaultSanctionCacheCleanupInterval),
	}
}

// ApplyObservations:
//   - Processes all provided observations and applies sanctions as needed
//   - Main public entry point for handling and sanctioning observations
func (ses *sanctionedEndpointsStore) ApplyObservations(shannonObservations []*protocolobservations.ShannonRequestObservations) {
	logger := ses.logger.With("method", "ApplyObservations")

	if len(shannonObservations) == 0 {
		logger.Warn().Msg("⚠️ Skipping processing: received empty observation list")
		return
	}

	// For each observation set:
	for _, observationSet := range shannonObservations {
		// Process HTTP observations if present
		httpObservations := observationSet.GetHttpObservations()
		if httpObservations != nil {
			ses.processHTTPConnectionObservationForSanctions(logger, httpObservations)
		}

		// Process Websocket connection observations
		websocketConnectionObs := observationSet.GetWebsocketConnectionObservation()
		if websocketConnectionObs != nil {
			ses.processWebSocketConnectionObservationForSanctions(logger, websocketConnectionObs)
		}
	}
}

// TODO_TECHDEBT(@commoddity,@adshmh): sanctioned endpoints stores are categorized on RPC type, but contain HTTP methods.
// The approach is in the right direction in general, but requires a few refactors to encapsulate observation processing
// logic specific to each RPC type.
//
// processHTTPConnectionObservationForSanctions processes HTTP endpoint observations for sanctions
func (ses *sanctionedEndpointsStore) processHTTPConnectionObservationForSanctions(
	logger polylog.Logger,
	httpObservations *protocolobservations.ShannonHTTPEndpointObservations,
) {
	// For each endpoint observation in the set:
	for _, endpointObservation := range httpObservations.GetEndpointObservations() {
		// Build endpoint from observation
		endpoint := buildEndpointFromObservation(endpointObservation)

		// Hydrate logger with endpoint context
		logger := hydrateLoggerWithEndpoint(logger, endpoint).With("method", "processHTTPConnectionObservationForSanctions")
		logger.Debug().Msg("processing endpoint observation.")

		// Skip if no sanction is recommended
		recommendedSanction := endpointObservation.GetRecommendedSanction()
		if recommendedSanction == protocolobservations.ShannonSanctionType_SHANNON_SANCTION_UNSPECIFIED {
			continue
		}

		// Build sanction from observation
		sanctionData := buildSanctionFromObservation(endpointObservation)

		// Apply appropriate type of sanction:
		switch recommendedSanction {
		case protocolobservations.ShannonSanctionType_SHANNON_SANCTION_PERMANENT:
			// Permanent sanction:
			//   - Persists for process lifetime (not on disk)
			//   - Lost on PATH restart; not shared
			logger.Info().Msg("Adding permanent sanction for endpoint")
			ses.addPermanentSanction(endpoint.Addr(), sanctionData)

		case protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION:
			// Session-based sanction:
			//   - Expires after set duration
			//   - More ephemeral than permanent
			//   - Lost on PATH restart; not shared
			logger.Info().Msg("Adding session sanction for endpoint")
			ses.addSessionSanction(endpoint, sanctionData)

		default:
			logger.Warn().Msg("sanction type not supported by the store. skipping.")
		}
	}
}

// processWebSocketConnectionObservationForSanctions processes a Websocket connection observation for sanctions
func (ses *sanctionedEndpointsStore) processWebSocketConnectionObservationForSanctions(
	logger polylog.Logger,
	websocketConnectionObs *protocolobservations.ShannonWebsocketConnectionObservation,
) {
	// Build endpoint from Websocket connection observation
	endpoint := buildEndpointFromWebSocketConnectionObservation(websocketConnectionObs)

	// Hydrate logger with endpoint context
	logger = hydrateLoggerWithEndpoint(logger, endpoint).With("method", "processWebSocketConnectionObservationForSanctions")
	logger.Debug().
		Str("recommended_sanction", websocketConnectionObs.GetRecommendedSanction().String()).
		Msg("processing Websocket connection observation for sanctions")

	// Skip if no sanction is recommended
	recommendedSanction := websocketConnectionObs.GetRecommendedSanction()
	if recommendedSanction == protocolobservations.ShannonSanctionType_SHANNON_SANCTION_UNSPECIFIED {
		return
	}

	// Build sanction from Websocket connection observation
	sanctionData := buildSanctionFromWebSocketConnectionObservation(websocketConnectionObs)

	// Apply the sanction
	ses.addSessionSanction(endpoint, sanctionData)
}

// FilterSanctionedEndpoints:
//   - Removes sanctioned endpoints from the provided list
//   - Returns only endpoints without active sanctions
//   - Used during endpoint selection to avoid sanctioned endpoints
func (ses *sanctionedEndpointsStore) FilterSanctionedEndpoints(
	allEndpoints map[protocol.EndpointAddr]endpoint,
) map[protocol.EndpointAddr]endpoint {
	filteredEndpoints := make(map[protocol.EndpointAddr]endpoint)

	for endpointAddr, endpoint := range allEndpoints {
		sanctioned, reason := ses.isSanctioned(endpoint)
		if sanctioned {
			// Log and skip sanctioned endpoints
			hydratedLogger := hydrateLoggerWithEndpoint(ses.logger, endpoint)
			hydratedLogger.With("sanction_reason", reason).Debug().Msg("Filtering out sanctioned endpoint")
			continue
		}
		filteredEndpoints[endpointAddr] = endpoint
	}

	return filteredEndpoints
}

// addPermanentSanction:
//   - Adds a permanent sanction for an endpoint
//   - Never expires; requires manual removal
//   - Used for serious errors (e.g., validation failures, suspected malicious behavior)
func (ses *sanctionedEndpointsStore) addPermanentSanction(
	endpointAddr protocol.EndpointAddr,
	sanctionData sanction,
) {
	ses.permanentSanctionsMutex.Lock()
	defer ses.permanentSanctionsMutex.Unlock()
	ses.permanentSanctions[endpointAddr] = sanctionData
}

// addSessionSanction:
//   - Adds a session-based sanction for an endpoint
//   - Sanction expires after defaultSessionSanctionExpiration
//   - Used for temporary issues (e.g., timeouts, connection problems)
func (ses *sanctionedEndpointsStore) addSessionSanction(
	endpoint endpoint,
	sanction sanction,
) {
	sessionSanctionKey := buildSessionSanctionKey(endpoint)

	ses.sessionSanctionsCache.Set(sessionSanctionKey.string(), sanction, defaultSessionSanctionExpiration)
}

// isSanctioned checks if an endpoint has any active sanction (permanent or session-based)
func (ses *sanctionedEndpointsStore) isSanctioned(endpoint endpoint) (bool, string) {
	// Check permanent sanctions first - these apply regardless of session
	ses.permanentSanctionsMutex.RLock()
	defer ses.permanentSanctionsMutex.RUnlock()
	sanctionRecord, hasPermanentSanction := ses.permanentSanctions[endpoint.Addr()]

	if hasPermanentSanction {
		return true, fmt.Sprintf("permanent sanction: %s", sanctionRecord.reason)
	}

	// Check session sanctions - these are specific to endpoint+session
	sessionSanctionKey := buildSessionSanctionKey(endpoint)

	sessionSanctionObj, hasSessionSanction := ses.sessionSanctionsCache.Get(sessionSanctionKey.string())
	if hasSessionSanction {
		sanctionRecord, ok := sessionSanctionObj.(sanction)
		if !ok {
			ses.logger.Error().Msg("SHOULD NEVER HAPPEN: cached sanction is not a sanction type")
			return false, ""
		}
		return true, fmt.Sprintf("session sanction: %s", sanctionRecord.reason)
	}

	return false, ""
}

// --------- Session Sanction Key ---------

type sessionSanctionKey struct {
	endpointAddr protocol.EndpointAddr
	sessionID    string
}

// string returns the string representation of the sessionSanctionKey.
// For example, the string representation is:
//   - "pokt1ggdpwj5stslx2e567qcm50wyntlym5c4n0dst8-https://im.oldgreg.org-1234567890".
func (s sessionSanctionKey) string() string {
	return fmt.Sprintf("%s-%s", s.endpointAddr, s.sessionID)
}

// buildSessionSanctionKey creates a key for a session-based sanction.
// The session ID is appended to ensure that session sanctions do not extend beyond the session duration.
//
// Example for the key "pokt1ggdpwj5stslx2e567qcm50wyntlym5c4n0dst8-https://im.oldgreg.org-1234567890":
//   - Endpoint address (supplier address + endpoint URL): "pokt1ggdpwj5stslx2e567qcm50wyntlym5c4n0dst8-https://im.oldgreg.org"
//   - Session ID: "1234567890"
//
// The key is used to store and retrieve session-based sanctions from the cache.
func buildSessionSanctionKey(endpoint endpoint) sessionSanctionKey {
	endpointAddr := endpoint.Addr()
	sessionID := endpoint.Session().Header.SessionId
	return sessionSanctionKey{
		endpointAddr: endpointAddr,
		sessionID:    sessionID,
	}
}

// newSessionSanctionKeyFromKey creates a sessionSanctionKey from a string.
// It returns the sessionSanctionKey and an error if the key is invalid.
//
// Example:
// - Full Key: pokt1ggdpwj5stslx2e567qcm50wyntlym5c4n0dst8-https://im.oldgreg.org-1234567890
//   - Endpoint address: "pokt1ggdpwj5stslx2e567qcm50wyntlym5c4n0dst8-https://im.oldgreg.org"
//   - Session ID: "1234567890"
func newSessionSanctionKeyFromKey(key string) (sessionSanctionKey, error) {
	// Find the last hyphen to split endpointAddr from sessionID
	// This handles cases where the URL in endpointAddr contains hyphens
	lastHyphenIndex := strings.LastIndex(key, "-")
	if lastHyphenIndex == -1 {
		// If no hyphen found, return the entire key as endpointAddr and empty sessionID
		return sessionSanctionKey{}, fmt.Errorf("no hyphen found in key: %s", key)
	}

	endpointAddr := key[:lastHyphenIndex]
	sessionID := key[lastHyphenIndex+1:]

	return sessionSanctionKey{
		endpointAddr: protocol.EndpointAddr(endpointAddr),
		sessionID:    sessionID,
	}, nil
}

// --------- Sanction Details ---------

// getSanctionDetails returns the sanctioned endpoints for a given service ID.
// It provides information about:
//   - the currently sanctioned endpoints, including the reason
//   - counts for valid and sanctioned endpoints
//
// It is called by the router to allow quick information about currently sanctioned endpoints.
func (ses *sanctionedEndpointsStore) getSanctionDetails(serviceID protocol.ServiceID) devtools.ProtocolLevelDataResponse {
	permanentSanctionDetails := make(map[protocol.EndpointAddr]devtools.SanctionedEndpoint)
	sessionSanctionDetails := make(map[string]devtools.SanctionedEndpoint)

	// First get permanent sanctions
	for endpointAddr, sanction := range ses.permanentSanctions {
		sanctionServiceID := protocol.ServiceID(sanction.sessionServiceID)

		// Only return sanctions for the provided service ID
		// Filter out all sanctions for other service IDs.
		if sanctionServiceID != serviceID {
			continue
		}

		// Permanent sanctions are not associated with a session ID.
		permanentSanctionDetails[endpointAddr] = sanction.permanentSanctionToDetails(
			endpointAddr,
			protocolobservations.ShannonSanctionType_SHANNON_SANCTION_PERMANENT,
		)
	}

	// Then get session sanctions
	for key, cachedSanction := range ses.sessionSanctionsCache.Items() {
		sanction, ok := cachedSanction.Object.(sanction)
		if !ok {
			ses.logger.Error().
				Str("cache_key", key).
				Str("object_type", fmt.Sprintf("%T", cachedSanction.Object)).
				Msg("INVARIANT VIOLATION: cached sanction object has unexpected type, skipping entry")
			continue
		}

		sanctionKey, err := newSessionSanctionKeyFromKey(key)
		if err != nil {
			ses.logger.Error().
				Err(err).
				Str("cache_key", key).
				Msg("INVARIANT VIOLATION: failed to parse session sanction key, skipping entry")
			continue
		}

		sanctionEndpointAddr := sanctionKey.endpointAddr
		sanctionServiceID := protocol.ServiceID(sanction.sessionServiceID)

		// Only return sanctions for the provided service ID
		// Filter out all sanctions for other service IDs.
		if sanctionServiceID != serviceID {
			continue
		}

		sessionSanctionDetails[sanctionKey.string()] = sanction.sessionSanctionToDetails(
			sanctionEndpointAddr,
			sanctionKey.sessionID,
			protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		)
	}

	permanentSanctionedEndpointsCount := len(permanentSanctionDetails)
	sessionSanctionedEndpointsCount := len(sessionSanctionDetails)
	totalSanctionedEndpointsCount := permanentSanctionedEndpointsCount + sessionSanctionedEndpointsCount

	return devtools.ProtocolLevelDataResponse{
		PermanentlySanctionedEndpoints:    permanentSanctionDetails,
		SessionSanctionedEndpoints:        sessionSanctionDetails,
		PermanentSanctionedEndpointsCount: permanentSanctionedEndpointsCount,
		SessionSanctionedEndpointsCount:   sessionSanctionedEndpointsCount,
		TotalSanctionedEndpointsCount:     totalSanctionedEndpointsCount,
	}
}
