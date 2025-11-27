// TODO_MVP(@adshmh): Add a mermaid diagram of the different structural
// (i.e. packages, types) components to help clarify the role of each.
package gateway

import (
    "context"
    "errors"
    "sync"
    "time"

    "github.com/pokt-network/poktroll/pkg/polylog"

    "github.com/pokt-network/path/health"
    "github.com/pokt-network/path/protocol"
)

// EndpointHydrator provides the functionality required for health check.
var _ health.Check = &EndpointHydrator{}

// componentNameHydrator is the name used when reporting the status of the endpoint hydrator
const componentNameHydrator = "endpoint-hydrator"

// Please see the following link for details on the use of `Hydrator` word in the name.
// https://stackoverflow.com/questions/6991135/what-does-it-mean-to-hydrate-an-object
//
// EndpointHydrator augments the available dataset on quality of endpoints.
// For example, it can be used to process raw data into QoS data.
// This ensures that each service on each instance has the information
// needed to make real-time decisions to handle user requests.
//
// An example QoS transformation workflow can be:
// 1. Consulting each service's QoS instance on the checks required to validate an endpoint.
// 2. Performing the required checks on the endpoint, in the form of a (synthetic) service request.
// 3. Reporting the results back to the service's QoS instance.
type EndpointHydrator struct {
    Logger polylog.Logger

    // Protocol instance to be used by the hydrator when listing endpoints and sending relays.
    Protocol

    // ActiveQoSServices provides the hydrator with the QoS instances
    // it needs to invoke for generating synthetic service requests.
    // IMPORTANT: ActiveQoSServices should not be modified after the hydrator is started.
    ActiveQoSServices map[protocol.ServiceID]QoSService

    // MetricsReporter is used to export metrics based on observations made in handling service requests.
    MetricsReporter RequestResponseReporter

    // DataReporter is used to export, to the data pipeline, observations made in handling service requests.
    // It is declared separately from the `MetricsReporter` to be consistent with the gateway package's role
    // of explicitly defining PATH gateway's components and their interactions.
    DataReporter RequestResponseReporter

    // RunInterval is the interval at which the Endpoint Hydrator will run HTTP checks in milliseconds.
    RunInterval time.Duration
    // MaxEndpointCheckWorkers is the maximum number of workers that will be used to concurrently check endpoints.
    MaxEndpointCheckWorkers int

    // TODO_FUTURE: a more sophisticated health status indicator
    // may eventually be needed, e.g. one that checks whether any
    // of the attempted service requests returned a response.
    //
    // isHealthy indicates whether the hydrator's
    // most recent iteration has been successful
    // i.e. it has successfully run checks against
    // every configured service.
    isHealthy         bool
    healthStatusMutex sync.RWMutex
}

// Start should be called to signal this instance of the hydrator
// to start generating and sending endpoint check requests.
// It starts two separate goroutines: one for HTTP checks and one for Websocket checks.
// The ctx parameter allows for graceful shutdown - when ctx is canceled, both goroutines will exit.
func (eph *EndpointHydrator) Start(ctx context.Context) error {
    if eph.Protocol == nil {
        return errors.New("an instance of Protocol must be provided")
    }

    if len(eph.ActiveQoSServices) == 0 {
        return errors.New("at least one QoS instance must be provided to the endpoint hydrator to start sending check requests")
    }

    // Start HTTP checks on the configured interval
    go func() {
        ticker := time.NewTicker(eph.RunInterval)
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                eph.Logger.Info().Msg("Hydrator HTTP check goroutine shutting down")
                return
            case <-ticker.C:
                eph.runHTTPChecks()
            }
        }
    }()

    // Start Websocket checks on a separate interval
    go func() {
        ticker := time.NewTicker(websocketCheckInterval)
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                eph.Logger.Info().Msg("Hydrator WebSocket check goroutine shutting down")
                return
            case <-ticker.C:
                eph.runWebSocketChecks()
            }
        }
    }()

    return nil
}

// Name is used when checking the status/health of the hydrator.
func (eph *EndpointHydrator) Name() string {
    return componentNameHydrator
}

// IsAlive returns true if the hydrator has completed 1 iteration.
// It is used to check the status/health of the hydrator
func (eph *EndpointHydrator) IsAlive() bool {
    eph.healthStatusMutex.RLock()
    defer eph.healthStatusMutex.RUnlock()

    return eph.isHealthy
}

// getHealthStatus returns the health status of the hydrator
// based on the results of the most recently completed iteration
// of running checks against service endpoints.
func (eph *EndpointHydrator) getHealthStatus(successfulServiceChecks *sync.Map) bool {
    // TODO_FUTURE: allow reporting unhealthy status if
    // certain services could not be processed.
    for svcID := range eph.ActiveQoSServices {
        value, found := successfulServiceChecks.Load(svcID)
        if !found {
            return false
        }

        successful, ok := value.(bool)
        if !ok || !successful {
            return false
        }
    }

    return true
}
