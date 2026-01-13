package shannon

import (
	"context"
	"fmt"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"

	"github.com/pokt-network/path/protocol"
)

var (
	// TODO_UPNEXT(@olshansk): Experiment the difference between active and extended sessions.
	// - Make this configurable at the gateway yaml level
	// - Add metrics to track how active vs extended sessions are used
	// - Evaluate the impact of active vs extended sessions on performance
	// - Enable making two parallel requests: one with active session and one with extended session
	// DEV_NOTE: As of PR #339, we are hard-coding this to prevent any business logic changes to enable
	// faster iteration on main and prevent the outstanding PR from getting stale.
	extendedSessionEnabled = false
)

// getSession returns the session for the app address provided.
// It may retrieve the current active or previous extended session depending on the configurations.
func (p *Protocol) getSession(
	ctx context.Context,
	logger polylog.Logger,
	appAddr string,
	serviceID protocol.ServiceID,
) (sessiontypes.Session, error) {
	logger.Debug().Msgf("About to get a session for app %s for service %s", appAddr, serviceID)

	var err error
	var session sessiontypes.Session

	// Retrieve the session for the owned app, without grace period logic.
	if extendedSessionEnabled {
		session, err = p.GetSessionWithExtendedValidity(ctx, serviceID, appAddr)
	} else {
		session, err = p.GetSession(ctx, serviceID, appAddr)
	}
	if err != nil {
		err = fmt.Errorf("%w: Error getting the current session from the full node for app: %s, error: %w", errProtocolContextSetupFetchSession, appAddr, err)
		logger.Error().Err(err).Msgf("SHOULD NEVER HAPPEN: %s", err.Error())
		return session, err
	}

	// Select the first session in the list.
	selectedApp := session.Application
	logger.Debug().Msgf("fetched the app with the selected address %s.", selectedApp.Address)

	if appAddr != selectedApp.Address {
		err = fmt.Errorf("%w: The app retrieved from the full node %s does not match the app address %s", errProtocolContextSetupAppDoesNotDelegate, selectedApp.Address, appAddr)
		logger.Error().Err(err).Msgf("SHOULD NEVER HAPPEN: %s", err.Error())
		return session, err
	}

	// Verify both apps delegate to the gateway
	if !gatewayHasDelegationForApp(p.gatewayAddr, selectedApp) {
		err = fmt.Errorf("%w: The app retrieved from the full node %s does not delegate to the gateway %s", errProtocolContextSetupAppDoesNotDelegate, selectedApp.Address, p.gatewayAddr)
		logger.Error().Err(err).Msgf("SHOULD NEVER HAPPEN: %s", err.Error())
		return session, err
	}

	return session, nil
}
