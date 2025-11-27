//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	vegeta "github.com/tsenart/vegeta/lib"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
	"github.com/pokt-network/path/request"
)

type serviceType string

const (
	serviceTypeEVM       serviceType = "evm"
	serviceTypeCosmosSDK serviceType = "cosmos_sdk"
	serviceTypeSolana    serviceType = "solana"
	serviceTypeAnvil     serviceType = "anvil"
)

// -----------------------------------------------------------------------------
// TestServices Struct - Configures the services to test against.
//
// Unmarshalled from the YAML file `config/services_shannon.yaml`
// -----------------------------------------------------------------------------

// DEV_NOTE: All structs and `yaml:` tagged fields must be public to allow for unmarshalling using `gopkg.in/yaml`
type (
	TestServices struct {
		Services []TestService `yaml:"services"` // List of test services to run the tests against
	}

	TestService struct {
		Name          string             `yaml:"name"`                 // Name of the service
		ServiceID     protocol.ServiceID `yaml:"service_id"`           // Service ID to test (identifies the specific blockchain service)
		ServiceType   serviceType        `yaml:"service_type"`         // Type of service to test (evm, cometbft, solana, anvil)
		Alias         string             `yaml:"alias,omitempty"`      // Alias for the service
		Archival      bool               `yaml:"archival,omitempty"`   // Whether this is an archival test (historical data access)
		WebSockets    bool               `yaml:"websockets,omitempty"` // Whether this service should run Websocket tests in addition to HTTP tests
		ServiceParams ServiceParams      `yaml:"service_params"`       // Service-specific parameters for test requests
		SupportedAPIs []string           `yaml:"supported_apis"`       // List of APIs supported by the service
		// Not marshaled from YAML; set in test case.
		serviceType    serviceType
		testMethodsMap map[string]testMethodConfig
		summary        *serviceSummary
	}
	// ServiceParams holds service-specific test data for all methods.
	// TODO_IMPROVE(@commoddity): Look into getting contract address and contract start block
	// from `config/service_qos_config.go` to have only one source of truth for service params
	ServiceParams struct {
		ContractAddress    string `yaml:"contract_address,omitempty"`     // EVM contract address (should match service_qos_config.go)
		ContractStartBlock uint64 `yaml:"contract_start_block,omitempty"` // Minimum block number to use for archival tests
		TransactionHash    string `yaml:"transaction_hash,omitempty"`     // Transaction hash for receipt/transaction queries
		CallData           string `yaml:"call_data,omitempty"`            // Call data for eth_call
		// Not marshaled from YAML; set in test case.
		blockNumber string // Can be "latest" or an archival block number
	}
	testMethodConfig struct {
		target        vegeta.Target // Used to send the request to the service
		serviceConfig ServiceConfig // Used to calculate the test metrics for the method
	}
)

func (ts *TestService) hydrate(serviceConfig ServiceConfig, serviceType serviceType, targets map[string]vegeta.Target, summary *serviceSummary) {
	ts.serviceType = serviceType
	ts.summary = summary

	// Set up the test methods map
	testMethodsMap := make(map[string]testMethodConfig)
	for method, target := range targets {
		testMethodsMap[method] = testMethodConfig{
			target:        target,
			serviceConfig: serviceConfig,
		}
	}
	ts.testMethodsMap = testMethodsMap
}

func (ts *TestService) getVegetaTargets(gatewayURL string) (map[string]vegeta.Target, error) {
	switch ts.ServiceType {
	case serviceTypeEVM:
		return getEVMVegetaTargets(ts, gatewayURL)
	case serviceTypeSolana:
		return getSolanaVegetaTargets(ts, gatewayURL)
	case serviceTypeCosmosSDK:
		return getCosmosSDKVegetaTargets(ts, gatewayURL)
	case serviceTypeAnvil:
		return getAnvilVegetaTargets(ts, gatewayURL)
	}
	return nil, fmt.Errorf("unsupported service type: %s", ts.ServiceType)
}

// supportsWebSockets returns true if the service is configured for Websocket testing
func (ts *TestService) supportsWebSockets() bool {
	return ts.WebSockets
}

// supportsEVMWebSockets returns true if the service supports Websocket EVM JSON-RPC tests
func (ts *TestService) supportsEVMWebSockets() bool {
	// Only EVM-like services can run EVM Websocket tests
	// This includes pure EVM services and Cosmos SDK services with EVM support (like XRPLEVM)
	return ts.supportsWebSockets() && (ts.ServiceType == serviceTypeEVM ||
		(ts.ServiceType == serviceTypeCosmosSDK && ts.ServiceParams.ContractAddress != ""))
}

func getExpectedID(serviceType serviceType) jsonrpc.ID {
	switch serviceType {
	case serviceTypeEVM:
		return evmExpectedID
	case serviceTypeSolana:
		return solanaExpectedID
	case serviceTypeCosmosSDK:
		return cosmosSDKExpectedID
	case serviceTypeAnvil:
		return anvilExpectedID
	default:
		return jsonrpc.IDFromInt(1)
	}
}

// -----------------------------------------------------------------------------
// Utility Functions
// -----------------------------------------------------------------------------

// getRPCTypeFromString converts a string from the config file to a sharedtypes.RPCType.
// Used to convert a Service's SupportedAPIs to a sharedtypes.RPCType in order
// to determine which test methods to run.
//
// For example, when running Cosmos SDK service tests, the service
// may support Cosmos SDK, CometBFT and REST APIs.
func getRPCTypeFromString(rpcType string) sharedtypes.RPCType {
	rpcTypeUpper := strings.ToUpper(rpcType)
	rpcTypeValue, ok := sharedtypes.RPCType_value[rpcTypeUpper]
	if !ok {
		return sharedtypes.RPCType_UNKNOWN_RPC
	}
	return sharedtypes.RPCType(rpcTypeValue)
}

// getRequestHeaders returns the HTTP headers for a given service ID, including Portal credentials if in load test mode.
func getRequestHeaders(serviceID protocol.ServiceID) http.Header {
	headers := http.Header{
		"Content-Type":                    []string{"application/json"},
		request.HTTPHeaderTargetServiceID: []string{string(serviceID)},
	}

	if cfg.getTestMode() == testModeLoad {
		// Portal App ID is required for load tests
		headers.Set(gateway.HttpHeaderPortalAppID, cfg.E2ELoadTestConfig.LoadTestConfig.PortalApplicationID)

		// Portal API Key is optional for load tests
		if cfg.E2ELoadTestConfig.LoadTestConfig.PortalAPIKey != "" {
			headers.Set(gateway.HttpHeaderAuthorization, cfg.E2ELoadTestConfig.LoadTestConfig.PortalAPIKey)
		}
	}

	return headers
}

// setServiceIDInGatewayURLSubdomain inserts the service ID as a subdomain in the gateway URL.
// Will be used if testing against production; ie. a URL that does NOT contain `localhost`.
//   - https://rpc.grove.city/v1 → https://eth.rpc.grove.city/v1
//   - https://api.example.com/path?query=param → https://eth.api.example.com/path?query=param
//
// TODO_TECHDEBT(@commoddity): Remove this once PATH in production supports service in headers
//   - Issue: https://github.com/buildwithgrove/infrastructure/issues/91
func setServiceIDInGatewayURLSubdomain(gatewayURL string, serviceID protocol.ServiceID, alias string) string {
	// If the alias is set, use it instead of the service ID to fill in the subdomain.
	// This is necessary because some services have subdomain aliases that differ
	// from the Shannon onchain service ID so the alias must be used to hit production.
	if alias != "" {
		serviceID = protocol.ServiceID(alias)
	}

	parsedURL, err := url.Parse(gatewayURL)
	if err != nil {
		// If parsing fails, fall back to simple string insertion
		return gatewayURL
	}
	// TODO_TECHDEBT(@commoddity): Find a way to make the entire `_` vs `-` thing consistent.
	serviceIdWithDashes := strings.ReplaceAll(string(serviceID), "_", "-")

	parsedURL.Host = fmt.Sprintf("%s.%s", serviceIdWithDashes, parsedURL.Host)
	return parsedURL.String()
}

// validate validates all test services
func (ts *TestServices) validate() error {
	if len(ts.Services) == 0 {
		return fmt.Errorf("no test services configured")
	}

	for i, service := range ts.Services {
		if err := ts.validateTestService(service, i); err != nil {
			return err
		}
	}

	return nil
}

// validateTestService validates an individual test service and its config
func (ts *TestServices) validateTestService(tc TestService, index int) error {
	// Validate common fields
	if tc.Name == "" {
		return fmt.Errorf("test service #%d: Name is required", index)
	}
	if tc.ServiceID == "" {
		return fmt.Errorf("test service #%d: ServiceID is required", index)
	}
	if tc.ServiceType == "" {
		return fmt.Errorf("test service #%d: ServiceType is required", index)
	}

	// Validate service params based on service type
	switch tc.ServiceType {
	case serviceTypeEVM:
		// EVM services require all four parameters
		if tc.ServiceParams.ContractAddress == "" {
			return fmt.Errorf("test service #%d: ContractAddress is required for EVM services", index)
		}
		if tc.ServiceParams.ContractStartBlock == 0 {
			return fmt.Errorf("test service #%d: ContractStartBlock is required for EVM services", index)
		}
		if tc.ServiceParams.TransactionHash == "" {
			return fmt.Errorf("test service #%d: TransactionHash is required for EVM services", index)
		}
		if tc.ServiceParams.CallData == "" {
			return fmt.Errorf("test service #%d: CallData is required for EVM services", index)
		}
	case serviceTypeSolana:
		// Solana services require only contract_address and transaction_hash
		if tc.ServiceParams.ContractAddress == "" {
			return fmt.Errorf("test service #%d: ContractAddress is required for Solana services", index)
		}
		if tc.ServiceParams.TransactionHash == "" {
			return fmt.Errorf("test service #%d: TransactionHash is required for Solana services", index)
		}
	case serviceTypeCosmosSDK:
		// No specific validation for Cosmos SDK yet
	case serviceTypeAnvil:
		// Anvil services require no specific parameters since all test methods use empty params
		// This is intentionally minimal - just verify the service can respond to basic JSON-RPC calls
		return nil
	default:
		return fmt.Errorf("test service #%d: Unsupported service type: %s", index, tc.ServiceType)
	}

	return nil
}
