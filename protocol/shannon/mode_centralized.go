package shannon

// Centralized Gateway Mode - Shannon Protocol Integration
//
// - PATH (Shannon instance) holds private keys for gateway operator's apps
// - All apps are owned by the gateway (PATH) operator
// - All apps delegate (onchain) to the gateway address
// - Each relay request is sent for one of these apps (owned by the gateway operator)
// - Each relay is signed by the gateway's private key (via Shannon ring signatures)
// More details: https://www.notion.so/buildwithgrove/Different-Modes-of-Operation-PATH-LocalNet-Discussions-122a36edfff6805e9090c9a14f72f3b5?pvs=4#122a36edfff680d4a0fff3a40dea543e

import (
	"context"
	"fmt"

	sessiontypes "github.com/pokt-network/poktroll/x/session/types"

	"github.com/pokt-network/path/protocol"
)

// getCentralizedGatewayModeActiveSessions returns the set of active sessions under the Centralized gateway mode.
//
// During session rollover periods:
//   - Fetches BOTH current session AND extended (previous) session for each app
//   - Merges endpoints from both sessions to ensure continuity during rollover
//   - Extended session is only added if it differs from current session
//
// During normal operation:
//   - Fetches only the current session for each app
func (p *Protocol) getCentralizedGatewayModeActiveSessions(
	ctx context.Context,
	serviceID protocol.ServiceID,
) ([]sessiontypes.Session, error) {
	logger := p.logger.With(
		"method", "getCentralizedGatewayModeActiveSessions",
		"service_id", string(serviceID),
	)
	logger.Debug().Msgf("fetching active sessions for the service %s.", serviceID)

	// TODO_CRITICAL(@commoddity): if an owned app is changed (i.e. re-staked) for
	// a different service, PATH must be restarned for changes to take effect.
	ownedAppsForService, ok := p.ownedApps[serviceID]
	if !ok || len(ownedAppsForService) == 0 {
		err := fmt.Errorf("%s: %s", errProtocolContextSetupCentralizedNoAppsForService, serviceID)
		logger.Error().Err(err).Msg("🚨 MISCONFIGURATION: ❌ ZERO owned apps found for service.")
		return nil, err
	}

	// Check if we're in session rollover period
	inRollover := p.IsInSessionRollover()

	// Loop over the address of apps owned by the gateway in Centralized gateway mode.
	var ownedAppSessions []sessiontypes.Session
	for _, ownedAppAddr := range ownedAppsForService {
		// Always fetch current session
		currentSession, err := p.getSession(ctx, logger, ownedAppAddr, serviceID)
		if err != nil {
			return nil, err
		}
		ownedAppSessions = append(ownedAppSessions, currentSession)

		// During rollover: ALSO fetch extended (previous) session for continuity
		if inRollover {
			extendedSession, err := p.GetSessionWithExtendedValidity(ctx, serviceID, ownedAppAddr)
			if err != nil {
				logger.Warn().Err(err).
					Str("app_address", ownedAppAddr).
					Msg("Failed to get extended session during rollover - continuing with current session only")
				continue
			}

			// Only add extended session if it's different from current session
			// (i.e., it's actually the previous session)
			if extendedSession.SessionId != currentSession.SessionId {
				ownedAppSessions = append(ownedAppSessions, extendedSession)
				logger.Info().
					Str("app_address", ownedAppAddr).
					Str("current_session_id", currentSession.SessionId).
					Str("extended_session_id", extendedSession.SessionId).
					Msg("✨ Added extended session endpoints during rollover period")
			}
		}
	}

	// If no sessions were found, return an error.
	if len(ownedAppSessions) == 0 {
		err := fmt.Errorf("%w: service %s", errProtocolContextSetupCentralizedNoSessions, serviceID)
		logger.Error().Msg(err.Error())
		return nil, err
	}

	if inRollover {
		logger.Info().Msgf("🔄 Session rollover active: fetched %d sessions (%d current + %d extended) for %d owned apps for service %s.",
			len(ownedAppSessions), len(ownedAppsForService), len(ownedAppSessions)-len(ownedAppsForService), len(ownedAppsForService), serviceID)
	} else {
		logger.Info().Msgf("Successfully fetched %d sessions for %d owned apps for service %s.",
			len(ownedAppSessions), len(ownedAppsForService), serviceID)
	}

	return ownedAppSessions, nil
}
