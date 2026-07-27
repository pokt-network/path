package shannon

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	apptypes "github.com/pokt-network/poktroll/x/application/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"

	"github.com/pokt-network/path/protocol"
)

// TODO_DOCUMENT(@adshmh): Convert the following notion doc into a proper README.
//
// Gateway Mode defines the behavior of a specific mode of operation of PATH.
// See the following link for more details on PATH's different modes of operation.
// https://www.notion.so/buildwithgrove/Different-Modes-of-Operation-PATH-LocalNet-Discussions-122a36edfff6805e9090c9a14f72f3b5
//
// SupportedGatewayModes returns the list of gateway modes supported by the Shannon protocol integration.
// Implements the gateway.Protocol interface.
func (p *Protocol) SupportedGatewayModes() []protocol.GatewayMode {
	return supportedGatewayModes()
}

// TODO_TECHDEBT(@commoddity): Most of the functionality in this file should be moved to the Shannon SDK.
// Evaluate the exact implementation of this as defined in issue:
// https://github.com/pokt-network/path/issues/291

// getActiveGatewaySessions returns the active sessions under the supplied gateway mode.
// The active sessions are retrieved as follows:
//   - Centralized mode: gateway address and owned apps addresses (specified in configs) are used to retrieve active sessions.
//   - Delegated mode: gateway address and app address (specified in the HTTP header) are used to retrieve active sessions.
//
// forceCurrentSession skips the session-rollover grace logic and always resolves the
// CURRENT session. Callers on the websocket path must set it: a websocket connection
// binds a session once and then lives on it, so binding to the previous session during
// rollover hands the connection a session that has already ended — it survives only as
// long as the supplier's own grace, then goes silent or is closed ("session expired",
// 4000). An HTTP request bound to the previous session just fails one relay and retries,
// which is why the grace logic is still worth keeping there.
//
// The worst case is the rollover rebind itself (getReconnectEndpoint): it fires at the
// session boundary, which is exactly the window where the grace logic returns the session
// that just ended — so the rebind meant to escape an ending session could land back on it.
func (p *Protocol) getActiveGatewaySessions(
	ctx context.Context,
	serviceID protocol.ServiceID,
	httpReq *http.Request,
	forceCurrentSession bool,
) ([]sessiontypes.Session, error) {
	p.logger.With(
		"service_id", serviceID,
		"gateway_mode", p.gatewayMode,
		"force_current_session", forceCurrentSession,
	).Debug().Msg("fetching active sessions using the current gateway mode and applicable applications.")

	switch p.gatewayMode {

	// Centralized gateway mode uses the gateway's private key to sign the relay requests.
	case protocol.GatewayModeCentralized:
		return p.getCentralizedGatewayModeActiveSessions(ctx, serviceID, forceCurrentSession)

	// Delegated gateway mode uses the gateway's private key to sign the relay requests.
	case protocol.GatewayModeDelegated:
		return p.getDelegatedGatewayModeActiveSession(ctx, serviceID, httpReq, forceCurrentSession)

	// TODO_MVP(@adshmh): Uncomment the following code section once support for Permissionless Gateway mode is added to the shannon package.
	//case protocol.GatewayModePermissionless:
	//	return getPermissionlessGatewayModeApps(p.ownedAppsAddr), nil

	default:
		return nil, fmt.Errorf("%w: %s", errProtocolContextSetupUnsupportedGatewayMode, p.gatewayMode)
	}
}

// getGatewayModePermittedRelaySigner returns the relay request signer matching the supplied gateway mode.
// Returns the pre-initialized signer for supported modes, which uses SignerContext caching
// for optimal ring signature performance.
func (p *Protocol) getGatewayModePermittedRelaySigner(
	gatewayMode protocol.GatewayMode,
) (RelayRequestSigner, error) {
	switch gatewayMode {

	// Centralized gateway mode uses the gateway's private key to sign the relay requests.
	case protocol.GatewayModeCentralized:
		return p.relaySigner, nil

	// Delegated gateway mode uses the gateway's private key to sign the relay requests (i.e. the same as the Centralized gateway mode)
	case protocol.GatewayModeDelegated:
		return p.relaySigner, nil

	default:
		return nil, fmt.Errorf("unsupported gateway mode: %s", gatewayMode)
	}
}

// supportedGatewayModes returns the list of gateway modes currently supported by the Shannon protocol integration.
func supportedGatewayModes() []protocol.GatewayMode {
	return []protocol.GatewayMode{
		protocol.GatewayModeCentralized,
		protocol.GatewayModeDelegated,
		// TODO_MVP(@adshmh): Uncomment this line once support for Permissionless Gateway mode is added to the shannon package.
		// protocol.GatewayModePermissionless,
	}
}

// gatewayHasDelegationsForApp returns true if the supplied application delegates to the supplied gateway address.
func gatewayHasDelegationForApp(gatewayAddr string, app *apptypes.Application) bool {
	return slices.Contains(app.DelegateeGatewayAddresses, gatewayAddr)
}
