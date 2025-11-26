package qos

import (
	"strings"

	"github.com/pokt-network/poktroll/pkg/polylog"

	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	"github.com/pokt-network/path/protocol"
)

// SolanaObservationInterpreter extracts and interprets data from Solana request observations.
// It provides methods to access metrics-relevant information for Prometheus reporting.
type SolanaObservationInterpreter struct {
	// Logger for reporting issues during interpretation
	Logger polylog.Logger

	// Observations contains the raw Solana request data
	Observations *SolanaRequestObservations
}

// GetChainID returns the blockchain identifier from observations.
func (i *SolanaObservationInterpreter) GetChainID() string {
	if i.Observations == nil {
		i.Logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: Cannot get chain ID: nil observations")
		return ""
	}
	return i.Observations.ChainId
}

// GetServiceID returns the service identifier from observations.
func (i *SolanaObservationInterpreter) GetServiceID() string {
	if i.Observations == nil {
		i.Logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: Cannot get service ID: nil observations")
		return ""
	}
	return i.Observations.ServiceId
}

// GetRequestMethod returns the JSON-RPC method name from the request.
func (i *SolanaObservationInterpreter) GetRequestMethod() string {
	if i.Observations == nil {
		i.Logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: Cannot get request method: nil observations")
		return ""
	}

	// JsonrpcRequest can be nil in case of internal errors or parsing errors - this is expected
	if i.Observations.JsonrpcRequest == nil {
		return ""
	}

	return i.Observations.JsonrpcRequest.Method
}

// IsRequestSuccessful determines if the request completed without errors.
func (i *SolanaObservationInterpreter) IsRequestSuccessful() bool {
	if i.Observations == nil {
		i.Logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: Cannot determine request success: nil observations")
		return false
	}

	// RequestError being nil is normal for successful requests
	return i.Observations.RequestError == nil
}

// GetRequestErrorType returns the error type if request failed or empty string if successful.
func (i *SolanaObservationInterpreter) GetRequestErrorType() string {
	if i.Observations == nil {
		i.Logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: Cannot get error type: nil observations")
		return ""
	}

	// RequestError being nil is normal for successful requests
	if i.Observations.RequestError == nil {
		return ""
	}

	return i.Observations.RequestError.ErrorKind.String()
}

// GetRequestHTTPStatus returns the HTTP status code from the last endpoint observation.
// Returns 0 if observations are nil or no endpoint observations exist.
func (i *SolanaObservationInterpreter) GetRequestHTTPStatus() int32 {
	if i.Observations == nil {
		i.Logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: Cannot get HTTP status: nil observations")
		return 0 // Return 0 to indicate observation issues to metrics
	}

	// If there's a request error, return its HTTP status code
	if i.Observations.RequestError != nil {
		return i.Observations.RequestError.HttpStatusCode
	}

	// Loop through endpoint observations and return the HTTP status code from the last one
	if len(i.Observations.EndpointObservations) > 0 {
		lastObservation := i.Observations.EndpointObservations[len(i.Observations.EndpointObservations)-1]
		return lastObservation.HttpStatusCode
	}

	// No endpoint observations available
	i.Logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: Cannot get HTTP status: no endpoint observations available")
	return 0
}

// GetEndpointDomain returns the domain of the endpoint that served the request.
//
// If multiple endpoint observations are present, it returns the domain of the first endpoint observation.
// If no endpoint observations are present, it returns an empty string.
//
// TODO_TECHDEBT: Consolidate this with the business logic of other "GetEndpointDomain" implementations.
func (i *SolanaObservationInterpreter) GetEndpointDomain() string {
	// Ensure observations are not nil
	if i.Observations == nil {
		i.Logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: Cosmos observations are nil")
		return ""
	}

	// Ensure endpoint observations are not empty
	endpointObservations := i.Observations.GetEndpointObservations()
	if len(endpointObservations) == 0 {
		i.Logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: Cosmos endpoint observations are empty")
		return ""
	}

	// Build a set of unique endpoint addresses
	uniqueEndpointAddrs := make(map[string]struct{})
	endpointAddrs := make([]string, 0, len(endpointObservations))
	for _, eo := range endpointObservations {
		endpointAddr := eo.GetEndpointAddr()
		if _, seen := uniqueEndpointAddrs[endpointAddr]; !seen {
			uniqueEndpointAddrs[endpointAddr] = struct{}{}
			endpointAddrs = append(endpointAddrs, endpointAddr)
		}
	}

	// If multiple endpoint addresses are observed, log a warning and use the first one for domain extraction
	// TODO_DISCUSS: Decide how we want to handle this case in the future.
	numUniqueEndpointAddrs := len(uniqueEndpointAddrs)
	if numUniqueEndpointAddrs > 1 {
		i.Logger.With(
			"num_unique_endpoint_addrs", numUniqueEndpointAddrs,
			"unique_endpoint_addrs", strings.Join(endpointAddrs, ", "),
		).Warn().Msg("Multiple endpoint addresses observed for a single request. Using the first one for metrics domain.")
	}

	// Use the first observed endpoint address for domain extraction
	// Use the first observed endpoint address for domain extraction
	endpointAddr := endpointAddrs[0]
	endpointURL, err := protocol.EndpointAddr(endpointAddr).GetURL()
	if err != nil {
		i.Logger.Error().Err(err).Msgf("SHOULD NEVER HAPPEN: Cannot get endpoint URL from endpoint address: %s", endpointAddr)
	}

	domain, err := shannonmetrics.ExtractDomainOrHost(endpointURL)
	if err != nil {
		i.Logger.Error().Err(err).Msgf("SHOULD NEVER HAPPEN: Cannot get endpoint domain from endpoint address: %s", endpointAddr)
	}

	return domain
}
