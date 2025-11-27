package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/pokt-network/path/protocol"
)

// websocketCheckInterval is the interval at which Websocket connection checks are performed.
const websocketCheckInterval = 10 * time.Minute

// runWebSocketChecks performs Websocket connection checks for all services and endpoints.
func (eph *EndpointHydrator) runWebSocketChecks() {
	logger := eph.Logger.With(
		"services_count", len(eph.ActiveQoSServices),
		"check_type", "websocket",
	)
	logger.Info().Msg("Running Websocket Endpoint Hydrator checks")

	// TODO_TECHDEBT: ensure every outgoing request (or the goroutine checking a service ID)
	// has a timeout set.
	var wg sync.WaitGroup

	for svcID, svcQoS := range eph.ActiveQoSServices {
		logger := logger.With("service_id", string(svcID))

		// Skip if Websocket checks are not enabled for this service.
		if !svcQoS.CheckWebsocketConnection() {
			logger.Debug().Msg("Service is not configured to run Websocket checks. Skipping.")
			continue
		}

		wg.Add(1)
		go func(serviceID protocol.ServiceID, serviceQoS QoSService) {
			defer wg.Done()

			logger := eph.Logger.With("serviceID", serviceID)

			err := eph.performWebSocketChecks(serviceID, serviceQoS)
			if err != nil {
				logger.Warn().Err(err).Msg("failed to run Websocket checks for service")
				return
			}

			logger.Info().Msg("successfully completed Websocket checks for service")
		}(svcID, svcQoS)
	}
	wg.Wait()
}

// performWebSocketChecks performs Websocket connection checks for a specific service.
func (eph *EndpointHydrator) performWebSocketChecks(serviceID protocol.ServiceID, serviceQoS QoSService) error {
	logger := eph.Logger.With(
		"method", "performWebSocketChecks",
		"service_id", string(serviceID),
	)

	// Passing a nil as the HTTP request, because we assume the hydrator uses "Centralized Operation Mode".
	// TODO_TECHDEBT(@adshmh): support specifying the app(s) used for sending/signing synthetic relay requests by the hydrator.
	// TODO_FUTURE(@adshmh): consider publishing observations if endpoint lookup fails.
	ctx, cancel := context.WithTimeout(context.Background(), hydratorOperationTimeout)
	defer cancel()
	availableEndpoints, _, err := eph.AvailableWebsocketEndpoints(ctx, serviceID, nil)
	if err != nil || len(availableEndpoints) == 0 {
		// No session found or no endpoints available for service: skip.
		logger.Warn().Msg("no session found or no endpoints available for service when running Websocket hydrator checks.")
		// do NOT return an error: hydrator and PATH should not report unhealthy status if a single service is unavailable.
		return nil
	}

	logger = logger.With("number_of_endpoints", len(availableEndpoints))

	// Prepare a channel that will keep track of all the parallel async job to perform Websocket checks on every endpoint.
	endpointCheckChan := make(chan protocol.EndpointAddr, len(availableEndpoints))

	var wgEndpoints sync.WaitGroup
	for range eph.MaxEndpointCheckWorkers {
		wgEndpoints.Add(1)

		go func() {
			defer wgEndpoints.Done()

			for endpointAddr := range endpointCheckChan {
				endpointLogger := logger.With("endpoint_addr", string(endpointAddr))
				endpointLogger.Info().Msg("Running Websocket connection check for endpoint")

				err := eph.performWebSocketConnectionCheck(serviceID, endpointAddr)
				if err != nil {
					endpointLogger.Warn().Err(err).Msg("Websocket connection check failed")
					// Continue with other endpoints even if one Websocket check fails
				}
			}
		}()
	}

	// Kick off the workers above for every unique endpoint.
	for _, endpointAddr := range availableEndpoints {
		endpointCheckChan <- endpointAddr
	}

	close(endpointCheckChan)

	// Wait for all workers to finish processing the endpoints.
	wgEndpoints.Wait()

	// TODO_FUTURE: publish aggregated Websocket check reports
	return nil
}

// performWebSocketConnectionCheck performs a Websocket connection establishment check
// for the given endpoint. It performs a simplified version of the websocket bridge connection process
// to determine if an endpoint can support websocket connections.
func (eph *EndpointHydrator) performWebSocketConnectionCheck(
	serviceID protocol.ServiceID,
	endpointAddr protocol.EndpointAddr,
) error {
	// Create a context with a short timeout for the Websocket connection check
	// This ensures we don't wait too long for unresponsive endpoints
	checkTimeout := 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()

	// TODO_TECHDEBT(@commoddity,@adshmh): this is an internal detail of protocol (similar to e.g. fetching sessions).
	// It should be encapsulated and handled automatically inside protocol (e.g. via a goroutine started at the time of
	// protocol instance initialization), as there is no input required from any other components.
	// This is different from endpoint quality checks, where QoS needs to provide the payload to send to the endpoint.
	// Protocol can perform regular WS checks against in-session endpoints and adjust the list of available endpoints
	// for WS requests accordingly.
	obs := eph.CheckWebsocketConnection(ctx, serviceID, endpointAddr)
	if obs != nil {
		err := eph.ApplyWebSocketObservations(obs)
		if err != nil {
			eph.Logger.Error().Err(err).Msg("❌ failed to apply Websocket observations")
		}
	}

	return nil
}
