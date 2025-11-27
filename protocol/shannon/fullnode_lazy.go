package shannon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/pokt-network/poktroll/pkg/polylog"
	apptypes "github.com/pokt-network/poktroll/x/application/types"
	servicetypes "github.com/pokt-network/poktroll/x/service/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	sdk "github.com/pokt-network/shannon-sdk"
	sdktypes "github.com/pokt-network/shannon-sdk/types"

	"github.com/pokt-network/path/network/grpc"
	"github.com/pokt-network/path/protocol"
)

// The Shannon FullNode interface is implemented by the LazyFullNode struct below.
//
// A LazyFullNode queries the onchain data for every data item it needs to do an action (e.g. serve a relay request, etc).
// This is done to enable supporting short block times (a few seconds), by avoiding caching
// which can result in failures due to stale data in the cache.
var _ FullNode = &LazyFullNode{}

// TODO_MVP(@adshmh): Rename `LazyFullNode`: this struct does not perform any caching and should be named accordingly.
//
// LazyFullNode: default implementation of a full node for the Shannon.
//
// Key differences from a caching full node:
// - Intentionally avoids caching:
//   - Enables support for short block times (e.g. LocalNet)
//   - Use CachingFullNode struct if caching is desired for performance
type LazyFullNode struct {
	logger polylog.Logger

	appClient     *sdk.ApplicationClient
	sessionClient *sdk.SessionClient
	blockClient   *sdk.BlockClient
	accountClient *sdk.AccountClient
	sharedClient  *sdk.SharedClient

	// Session rollover monitoring state
	rolloverState *sessionRolloverState
}

// NewLazyFullNode builds and returns a LazyFullNode using the provided configuration.
func NewLazyFullNode(logger polylog.Logger, config FullNodeConfig) (*LazyFullNode, error) {
	logger = logger.With("component", "fullnode_lazy")

	blockClient, err := newBlockClient(config.RpcURL)
	if err != nil {
		return nil, fmt.Errorf("NewSdk: error creating new Shannon block client at URL %s: %w", config.RpcURL, err)
	}

	sessionClient, err := newSessionClient(config.GRPCConfig)
	if err != nil {
		return nil, fmt.Errorf("NewSdk: error creating new Shannon session client using URL %s: %w", config.GRPCConfig.HostPort, err)
	}

	appClient, err := newAppClient(config.GRPCConfig)
	if err != nil {
		return nil, fmt.Errorf("NewSdk: error creating new GRPC connection at url %s: %w", config.GRPCConfig.HostPort, err)
	}

	accountClient, err := newAccClient(config.GRPCConfig)
	if err != nil {
		return nil, fmt.Errorf("NewSdk: error creating new account client using url %s: %w", config.GRPCConfig.HostPort, err)
	}

	sharedClient, err := newSharedClient(config.GRPCConfig)
	if err != nil {
		return nil, fmt.Errorf("NewSdk: error creating new shared client using url %s: %w", config.GRPCConfig.HostPort, err)
	}

	return &LazyFullNode{
		logger:        logger,
		sessionClient: sessionClient,
		appClient:     appClient,
		blockClient:   blockClient,
		accountClient: accountClient,
		sharedClient:  sharedClient,
		rolloverState: newSessionRolloverState(logger, blockClient, config.SessionRolloverBlocks),
	}, nil
}

// GetApp:
// - Returns the onchain application matching the supplied application address.
// - Required to fulfill the FullNode interface.
func (lfn *LazyFullNode) GetApp(ctx context.Context, appAddr string) (*apptypes.Application, error) {
	app, err := lfn.appClient.GetApplication(ctx, appAddr)
	return &app, err
}

// GetSession:
// - Uses the Shannon SDK to fetch a session for the (serviceID, appAddr) combination.
// - Required to fulfill the FullNode interface.
func (lfn *LazyFullNode) GetSession(
	ctx context.Context,
	serviceID protocol.ServiceID,
	appAddr string,
) (sessiontypes.Session, error) {
	session, err := lfn.sessionClient.GetSession(
		ctx,
		appAddr,
		string(serviceID),
		0,
	)

	if err != nil {
		return sessiontypes.Session{},
			fmt.Errorf("GetSession: error getting the session for service %s app %s: %w",
				serviceID, appAddr, err,
			)
	}

	if session == nil {
		return sessiontypes.Session{},
			fmt.Errorf("GetSession: got nil session for service %s app %s: %w",
				serviceID, appAddr, err,
			)
	}

	// Update session rollover boundaries for rollover monitoring
	lfn.rolloverState.updateSessionRolloverBoundaries(*session)

	return *session, nil
}

// ValidateRelayResponse:
//   - Validates the raw response bytes received from an endpoint.
//   - Uses the SDK and the lazy full node's account client for validation.
//   - Will make a call to the remote full node to fetch the account public key.
func (lfn *LazyFullNode) ValidateRelayResponse(supplierAddr sdk.SupplierAddress, responseBz []byte) (*servicetypes.RelayResponse, error) {
	return sdk.ValidateRelayResponse(
		context.Background(),
		supplierAddr,
		responseBz,
		lfn.accountClient,
	)
}

// IsHealthy:
// - Always returns true for a LazyFullNode.
// - Required to fulfill the FullNode interface.
func (lfn *LazyFullNode) IsHealthy() bool {
	return true
}

// GetAccountClient:
// - Returns the account client created by the lazy fullnode.
// - Used to create relay request signers.
func (lfn *LazyFullNode) GetAccountClient() *sdk.AccountClient {
	return lfn.accountClient
}

// GetSharedParams:
// - Returns the shared module parameters from the blockchain.
// - Used to get grace period and session configuration.
func (lfn *LazyFullNode) GetSharedParams(ctx context.Context) (*sharedtypes.Params, error) {
	params, err := lfn.sharedClient.GetParams(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetSharedParams: error getting shared module parameters: %w", err)
	}
	return &params, nil
}

// GetCurrentBlockHeight:
// - Returns the current block height from the blockchain.
// - Used for session validation and grace period calculations.
func (lfn *LazyFullNode) GetCurrentBlockHeight(ctx context.Context) (int64, error) {
	height, err := lfn.blockClient.LatestBlockHeight(ctx)
	if err != nil {
		return 0, fmt.Errorf("GetCurrentBlockHeight: error getting latest block height: %w", err)
	}
	return height, nil
}

// GetSessionWithExtendedValidity:
// - Returns the appropriate session considering grace period logic.
// - If within grace period of a session rollover, it may return the previous session.
func (lfn *LazyFullNode) GetSessionWithExtendedValidity(
	ctx context.Context,
	serviceID protocol.ServiceID,
	appAddr string,
) (sessiontypes.Session, error) {
	logger := lfn.logger.With(
		"service_id", serviceID,
		"app_addr", appAddr,
		"method", "GetSessionWithExtendedValidity",
	)

	// Get the current session
	currentSession, err := lfn.GetSession(ctx, serviceID, appAddr)
	if err != nil {
		logger.Error().Err(err).Msg("failed to get current session")
		return sessiontypes.Session{}, fmt.Errorf("error getting current session: %w", err)
	}

	// Get shared parameters to determine grace period
	sharedParams, err := lfn.GetSharedParams(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("failed to get shared params, falling back to current session")
		return currentSession, nil
	}

	// Get current block height
	currentHeight, err := lfn.GetCurrentBlockHeight(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("failed to get current block height, falling back to current session")
		return currentSession, nil
	}

	// Calculate when the previous session's grace period would end
	prevSessionEndHeight := currentSession.Header.SessionStartBlockHeight - 1
	prevSessionEndHeightWithExtendedValidity := prevSessionEndHeight + int64(sharedParams.GracePeriodEndOffsetBlocks)

	logger = logger.With(
		"prev_session_end_height", prevSessionEndHeight,
		"prev_session_end_height_with_extended_validity", prevSessionEndHeightWithExtendedValidity,
		"current_height", currentHeight,
		"current_session_start_height", currentSession.Header.SessionStartBlockHeight,
		"current_session_end_height", currentSession.Header.SessionEndBlockHeight,
	)

	// If we're not within the grace period of the previous session, return the current session
	if currentHeight > prevSessionEndHeightWithExtendedValidity {
		logger.Debug().Msg("IS NOT WITHIN GRACE PERIOD: Returning current session")
		return currentSession, nil
	}

	// Get the previous session
	prevSession, err := lfn.sessionClient.GetSession(ctx, appAddr, string(serviceID), prevSessionEndHeight)
	if err != nil || prevSession == nil {
		logger.Warn().Err(err).Msg("Failed to get previous session, falling back to current session")
		return currentSession, nil
	}

	// Update session rollover boundaries for rollover monitoring
	lfn.rolloverState.updateSessionRolloverBoundaries(currentSession)

	// Return the previous session
	return *prevSession, nil
}

// IsInSessionRollover returns true if we're currently in a session rollover period.
func (lfn *LazyFullNode) IsInSessionRollover() bool {
	return lfn.rolloverState.getSessionRolloverState()
}

// serviceRequestPayload:
// - Contents of the request received by the underlying service's API server.

func shannonJsonRpcHttpRequest(payload protocol.Payload, url string) (*http.Request, error) {
	jsonRpcServiceReq, err := http.NewRequest(
		payload.Method,
		url,
		io.NopCloser(bytes.NewReader([]byte(payload.Data))),
	)
	if err != nil {
		return nil, fmt.Errorf("shannonJsonRpcHttpRequest: failed to create a new HTTP request for url %s: %w", url, err)
	}

	jsonRpcServiceReq.Header.Set("Content-Type", "application/json")
	return jsonRpcServiceReq, nil
}

func embedHttpRequest(reqToEmbed *http.Request) (*servicetypes.RelayRequest, error) {
	_, reqToEmbedBz, err := sdktypes.SerializeHTTPRequest(reqToEmbed)
	if err != nil {
		return nil, fmt.Errorf("embedHttpRequest: failed to Serialize HTTP Request for URL %s: %w", reqToEmbed.URL, err)
	}

	return &servicetypes.RelayRequest{
		Payload: reqToEmbedBz,
	}, nil
}

// TODO_IMPROVE: consider enhancing the service or RelayRequest/RelayResponse types in poktroll repo (see links below) to perform
// Serialization/Deserialization of the payload.
//
// Benefits:
// - Makes code easier to read and less error-prone by consolidating serialization/deserialization in a single source (e.g. relay.go).
//
// Current behavior:
// - Relay miner serializes the HTTP response received from the proxied service (see link below).
// - Deserialization is handled here (see sdktypes.DeserializeHTTPResponse below).
//
// Links:
//   - Relay miner serializing the service response:
//     https://github.com/pokt-network/poktroll/blob/e5024ea5d28cc94d09e531f84701a85cefb9d56f/pkg/relayer/proxy/synchronous.go#L361-L363
//   - Relay response validation (potential package for serialization/deserialization):
//     https://github.com/pokt-network/poktroll/blob/e5024ea5d28cc94d09e531f84701a85cefb9d56f/x/service/types/relay.go#L68

// deserializeRelayResponse:
// - Uses the Shannon sdk to deserialize the relay response payload received from an endpoint into a protocol.Response.
// - Required because the relay miner (the endpoint serving the relay) returns the HTTP response in serialized format in its payload.
func deserializeRelayResponse(bz []byte) (protocol.Response, error) {
	poktHttpResponse, err := sdktypes.DeserializeHTTPResponse(bz)
	if err != nil {
		return protocol.Response{}, err
	}

	return protocol.Response{
		Bytes:          poktHttpResponse.BodyBz,
		HTTPStatusCode: int(poktHttpResponse.StatusCode),
	}, nil
}

func newSessionClient(config grpc.GRPCConfig) (*sdk.SessionClient, error) {
	conn, err := grpc.ConnectGRPC(config)
	if err != nil {
		return nil, fmt.Errorf("could not create new Shannon session client: error establishing grpc connection to %s: %w", config.HostPort, err)
	}

	return &sdk.SessionClient{PoktNodeSessionFetcher: sdk.NewPoktNodeSessionFetcher(conn)}, nil
}

func newBlockClient(fullNodeURL string) (*sdk.BlockClient, error) {
	_, err := url.Parse(fullNodeURL)
	if err != nil {
		return nil, fmt.Errorf("newBlockClient: error parsing url %s: %w", fullNodeURL, err)
	}

	nodeStatusFetcher, err := sdk.NewPoktNodeStatusFetcher(fullNodeURL)
	if err != nil {
		return nil, fmt.Errorf("newBlockClient: error connecting to a full node %s: %w", fullNodeURL, err)
	}

	return &sdk.BlockClient{PoktNodeStatusFetcher: nodeStatusFetcher}, nil
}

func newAppClient(config grpc.GRPCConfig) (*sdk.ApplicationClient, error) {
	appConn, err := grpc.ConnectGRPC(config)
	if err != nil {
		return nil, fmt.Errorf("NewSdk: error creating new GRPC connection at url %s: %w", config.HostPort, err)
	}

	return &sdk.ApplicationClient{QueryClient: apptypes.NewQueryClient(appConn)}, nil
}

func newAccClient(config grpc.GRPCConfig) (*sdk.AccountClient, error) {
	conn, err := grpc.ConnectGRPC(config)
	if err != nil {
		return nil, fmt.Errorf("newAccClient: error creating new GRPC connection for account client at url %s: %w", config.HostPort, err)
	}

	return &sdk.AccountClient{PoktNodeAccountFetcher: sdk.NewPoktNodeAccountFetcher(conn)}, nil
}

func newSharedClient(config grpc.GRPCConfig) (*sdk.SharedClient, error) {
	conn, err := grpc.ConnectGRPC(config)
	if err != nil {
		return nil, fmt.Errorf("newSharedClient: error creating new GRPC connection for shared client at url %s: %w", config.HostPort, err)
	}

	return &sdk.SharedClient{QueryClient: sharedtypes.NewQueryClient(conn)}, nil
}
