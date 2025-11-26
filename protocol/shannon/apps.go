package shannon

import (
	"context"
	"fmt"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/protocol/crypto"
)

// getOwnedApps:
//   - Returns list of apps owned by the gateway.
//   - It is built from supplied private keys
//   - Supplied private keys are used for either:
//   - Signing organic relays for Centralized gateway mode (i.e. real usage)
//   - Signing synthetic relays for both Centralized and Delegated gateway modes (i.e. QoS eval)
//   - Populates `appAddr` and `stakedServiceID` for each app
//
// Returns:
//   - ownedAppsMap maps service IDs to a list of app addresses owned by the gateway operator.
//
// Note that one service ID can have multiple apps owned by the gateway operator.
//
// Example:
//
//	{
//	  "xrplevm": ["pokt1...", "pokt2..."],
//	  "eth": ["pokt3...", "pokt4..."],
//	}
func getOwnedApps(
	logger polylog.Logger,
	ownedAppsPrivateKeysHex []string,
	fullNode FullNode,
) (map[protocol.ServiceID][]string, error) {
	logger = logger.With("method", "getOwnedApps")
	logger.Debug().Msg("Building the list of owned apps.")

	ownedAppsMap := make(map[protocol.ServiceID][]string)
	for _, ownedAppPrivateKeyHex := range ownedAppsPrivateKeysHex {
		// Retrieve the app's secp256k1 private key from the hex string.
		ownedAppPrivateKey, err := crypto.GetSecp256k1PrivateKeyFromKeyHex(ownedAppPrivateKeyHex)
		if err != nil {
			logger.Error().Err(err).Msgf("error getting app private key from hex for app with private key %s", ownedAppPrivateKeyHex)
			return nil, err
		}

		// Retrieve the app's address from the private key.
		appAddr, err := crypto.GetAddressFromPrivateKey(ownedAppPrivateKey)
		if err != nil {
			logger.Error().Err(err).Msgf("error getting app address from private key for app with private key %s", ownedAppPrivateKeyHex)
			return nil, err
		}

		// Retrieve the app's onchain data using the full node to ensure the request
		// is a remote request and not attempting to use cached data.
		app, err := fullNode.GetApp(context.Background(), appAddr)
		if err != nil {
			logger.Error().Err(err).Msgf("error getting onchain data for app with address %s", appAddr)
			return nil, err
		}

		// Retrieve the app's service configs.
		appServiceConfigs := app.GetServiceConfigs()
		if len(appServiceConfigs) != 1 {
			logger.Error().Msgf("should never happen: app with address %s is not staked for exactly one service but %d instead", appAddr, len(appServiceConfigs))
			return nil, fmt.Errorf("app with address %s is not staked for exactly one service", appAddr)
		}

		appServiceConfig := appServiceConfigs[0]
		serviceID := protocol.ServiceID(appServiceConfig.GetServiceId())
		if serviceID == "" {
			logger.Error().Msgf("should never happen: app with address %s is staked for service with an empty ID", appAddr)
			return nil, fmt.Errorf("app with address %s is staked for service with an empty ID", appAddr)
		}

		// Add the app to the list of owned apps.
		ownedAppsMap[serviceID] = append(ownedAppsMap[serviceID], appAddr)
	}

	logger.Debug().Msgf("Successfully built the list of %d owned apps.", len(ownedAppsMap))
	return ownedAppsMap, nil
}
