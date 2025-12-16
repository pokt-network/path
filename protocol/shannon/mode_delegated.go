package shannon

// - TODO(@Olshansk): Revisit the security specification & requirements for how the paying app is selected.
// - TODO_DOCUMENT(@Olshansk): Convert the Notion doc into a proper README.
// - For more details, see:
//   https://www.notion.so/buildwithgrove/Different-Modes-of-Operation-PATH-LocalNet-Discussions-122a36edfff6805e9090c9a14f72f3b5?pvs=4#122a36edfff680eea2fbd46c7696d845

import (
	"context"
	"fmt"
	"net/http"

	apptypes "github.com/pokt-network/poktroll/x/application/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/request"
)

// Delegated Gateway Mode - Shannon Protocol Integration
//
// - Represents a gateway operation mode with the following behavior:
// - Each relay request is signed by the gateway key and sent on behalf of an app selected by the user.
// - Users must select a specific app for each relay request (currently via HTTP request headers).
//
// getDelegatedGatewayModeActiveSession returns active sessions for the selected app under Delegated gateway mode, for the supplied HTTP request.
//
// During session rollover periods:
//   - Fetches BOTH current session AND extended (previous) session for the app
//   - Merges endpoints from both sessions to ensure continuity during rollover
//   - Extended session is only added if it differs from current session
//
// During normal operation:
//   - Fetches only the current session for the app
func (p *Protocol) getDelegatedGatewayModeActiveSession(
	ctx context.Context,
	serviceID protocol.ServiceID,
	httpReq *http.Request,
) ([]sessiontypes.Session, error) {
	logger := p.logger.With("method", "getDelegatedGatewayModeActiveSession")

	extractedAppAddr, err := getAppAddrFromHTTPReq(httpReq)
	if err != nil {
		// Wrap the context setup error: used for observations.
		err = fmt.Errorf("%w: %+v: %w. ", errProtocolContextSetupGetAppFromHTTPReq, httpReq, err)
		logger.Error().Err(err).Msg("error getting the app address from the HTTP request. Relay request will fail.")
		return nil, err
	}

	// Always fetch current session
	currentSession, err := p.getSession(ctx, logger, extractedAppAddr, serviceID)
	if err != nil {
		return nil, err
	}

	// Skip the session's app if it is not staked for the requested service.
	selectedApp := currentSession.Application
	if !appIsStakedForService(serviceID, selectedApp) {
		err = fmt.Errorf("%w: Trying to use app %s that is not staked for the service %s", errProtocolContextSetupAppNotStaked, selectedApp.Address, serviceID)
		logger.Error().Err(err).Msgf("SHOULD NEVER HAPPEN: %s", err.Error())
		return nil, err
	}

	logger.Debug().Msgf("successfully verified the gateway (%s) has delegation for the selected app (%s) for service (%s).", p.gatewayAddr, selectedApp.Address, serviceID)

	sessions := []sessiontypes.Session{currentSession}

	// During rollover: ALSO fetch extended (previous) session for continuity
	if p.IsInSessionRollover() {
		extendedSession, err := p.GetSessionWithExtendedValidity(ctx, serviceID, extractedAppAddr)
		if err != nil {
			logger.Warn().Err(err).
				Str("app_address", extractedAppAddr).
				Msg("Failed to get extended session during rollover - continuing with current session only")
			return sessions, nil
		}

		// Only add extended session if it's different from current session
		// (i.e., it's actually the previous session)
		if extendedSession.SessionId != currentSession.SessionId {
			sessions = append(sessions, extendedSession)
			logger.Info().
				Str("app_address", extractedAppAddr).
				Str("current_session_id", currentSession.SessionId).
				Str("extended_session_id", extendedSession.SessionId).
				Msg("✨ Added extended session endpoints during rollover period")
		}
	}

	return sessions, nil
}

// appIsStakedForService returns true if the supplied application is staked for the supplied service ID.
func appIsStakedForService(serviceID protocol.ServiceID, app *apptypes.Application) bool {
	for _, svcCfg := range app.ServiceConfigs {
		if protocol.ServiceID(svcCfg.ServiceId) == serviceID {
			return true
		}
	}
	return false
}

// getAppAddrFromHTTPReq extracts the application address specified by the supplied HTTP request's headers.
func getAppAddrFromHTTPReq(httpReq *http.Request) (string, error) {
	if httpReq == nil || len(httpReq.Header) == 0 {
		return "", fmt.Errorf("getAppAddrFromHTTPReq: no HTTP headers supplied")
	}

	extractedAppAddr := httpReq.Header.Get(request.HTTPHeaderAppAddress)
	if extractedAppAddr == "" {
		return "", fmt.Errorf("getAppAddrFromHTTPReq: a target app must be supplied as HTTP header %s", request.HTTPHeaderAppAddress)
	}

	return extractedAppAddr, nil
}
