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

	"github.com/buildwithgrove/path/protocol"
	"github.com/buildwithgrove/path/request"
)

// Delegated Gateway Mode - Shannon Protocol Integration
//
// - Represents a gateway operation mode with the following behavior:
// - Each relay request is signed by the gateway key and sent on behalf of an app selected by the user.
// - Users must select a specific app for each relay request (currently via HTTP request headers).
//
// getDelegatedGatewayModeActiveSession returns active sessions for the selected app under Delegated gateway mode, for the supplied HTTP request.
func (p *Protocol) getDelegatedGatewayModeActiveSession(
	ctx context.Context,
	serviceID protocol.ServiceID,
	httpReq *http.Request,
) ([]hydratedSession, error) {
	logger := p.logger.With("method", "getDelegatedGatewayModeActiveSession")

	extractedAppAddr, err := getAppAddrFromHTTPReq(httpReq)
	if err != nil {
		// Wrap the context setup error: used for observations.
		err = fmt.Errorf("%w: %+v: %w. ", errProtocolContextSetupGetAppFromHTTPReq, httpReq, err)
		logger.Error().Err(err).Msg("error getting the app address from the HTTP request. Relay request will fail.")
		return nil, err
	}

	session, err := p.getSession(ctx, logger, extractedAppAddr, serviceID)
	if err != nil {
		return nil, err
	}

	// Skip the session's app if it is not staked for the requested service.
	selectedApp := session.session.Application
	if !appIsStakedForService(serviceID, selectedApp) {
		err = fmt.Errorf("%w: Trying to use app %s that is not staked for the service %s", errProtocolContextSetupAppNotStaked, selectedApp.Address, serviceID)
		logger.Error().Err(err).Msgf("SHOULD NEVER HAPPEN: %s", err.Error())
		return nil, err
	}

	logger.Debug().Msgf("successfully verified the gateway (%s) has delegation for the selected app (%s) for service (%s).", p.gatewayAddr, selectedApp.Address, serviceID)

	return []hydratedSession{session}, nil
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
