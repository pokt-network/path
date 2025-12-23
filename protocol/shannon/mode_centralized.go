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
// CRITICAL: Returns ONLY ONE session per app to ensure all endpoints reference the same session.
//
// During session rollover periods:
//   - Uses GetSessionWithExtendedValidity to determine the appropriate session
//   - May return either current or previous session depending on grace period logic
//
// During normal operation:
//   - Fetches the current session for each app
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
	// a different service, PATH must be restarted for changes to take effect.
	ownedAppsForService, ok := p.ownedApps[serviceID]
	if !ok || len(ownedAppsForService) == 0 {
		err := fmt.Errorf("%s: %s", errProtocolContextSetupCentralizedNoAppsForService, serviceID)
		// Log only once per service to avoid spamming logs on every request
		errorKey := "no_apps_" + string(serviceID)
		if _, alreadyLogged := p.loggedMisconfigErrors.LoadOrStore(errorKey, true); !alreadyLogged {
			logger.Error().Err(err).Msg("MISCONFIGURATION: No owned apps found for service - check config")
		}
		return nil, err
	}

	// Check if we're in session rollover period
	inRollover := p.IsInSessionRollover()

	// Loop over the address of apps owned by the gateway in Centralized gateway mode.
	// CRITICAL: Each app gets ONLY ONE session to ensure all endpoints reference the same session.
	var ownedAppSessions []sessiontypes.Session
	for _, ownedAppAddr := range ownedAppsForService {
		var selectedSession sessiontypes.Session
		var err error

		// During rollover: Use extended validity logic to get the appropriate session
		// This may return either current or previous session depending on grace period
		if inRollover {
			selectedSession, err = p.GetSessionWithExtendedValidity(ctx, serviceID, ownedAppAddr)
			if err != nil {
				logger.Warn().Err(err).
					Str("app_address", ownedAppAddr).
					Msg("Failed to get session with extended validity during rollover - trying current session")

				// Fallback to current session if extended validity fails
				selectedSession, err = p.getSession(ctx, logger, ownedAppAddr, serviceID)
				if err != nil {
					return nil, err
				}
			}
		} else {
			// Normal operation: fetch the current session
			selectedSession, err = p.getSession(ctx, logger, ownedAppAddr, serviceID)
			if err != nil {
				return nil, err
			}
		}

		// Add ONLY ONE session per app
		ownedAppSessions = append(ownedAppSessions, selectedSession)

		logger.Debug().
			Str("app_address", ownedAppAddr).
			Str("session_id", selectedSession.SessionId).
			Int64("session_start_height", selectedSession.Header.SessionStartBlockHeight).
			Int64("session_end_height", selectedSession.Header.SessionEndBlockHeight).
			Bool("in_rollover", inRollover).
			Msg("Selected session for app")
	}

	// If no sessions were found, return an error.
	if len(ownedAppSessions) == 0 {
		err := fmt.Errorf("%w: service %s", errProtocolContextSetupCentralizedNoSessions, serviceID)
		logger.Error().Msg(err.Error())
		return nil, err
	}

	logger.Debug().
		Bool("in_rollover", inRollover).
		Int("session_count", len(ownedAppSessions)).
		Int("app_count", len(ownedAppsForService)).
		Str("service_id", string(serviceID)).
		Msgf("Successfully fetched %d sessions (1 per app) for %d owned apps for service %s", len(ownedAppSessions), len(ownedAppsForService), serviceID)

	return ownedAppSessions, nil
}
