package gateway

import (
	"context"
	"sync"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/protocol"
)

// runHTTPChecks performs HTTP-based QoS checks for all services and endpoints.
func (eph *EndpointHydrator) runHTTPChecks() {
	logger := eph.Logger.With(
		"services_count", len(eph.ActiveQoSServices),
		"check_type", "http",
	)
	logger.Info().Msg("Running HTTP Endpoint Hydrator checks")

	// TODO_TECHDEBT: ensure every outgoing request (or the goroutine checking a service ID)
	// has a timeout set.
	var wg sync.WaitGroup
	// A sync.Map is optimized for the use case here,
	// i.e. each map entry is written only once.
	var successfulServiceChecks sync.Map

	for svcID, svcQoS := range eph.ActiveQoSServices {
		wg.Add(1)
		go func(serviceID protocol.ServiceID, serviceQoS QoSService) {
			defer wg.Done()

			logger := eph.Logger.With("serviceID", serviceID)

			err := eph.performHTTPChecks(serviceID, serviceQoS)
			if err != nil {
				logger.Warn().Err(err).Msg("failed to run HTTP QoS checks for service")
				return
			}

			successfulServiceChecks.Store(svcID, true)
			logger.Info().Msg("successfully completed HTTP QoS checks for service")
		}(svcID, svcQoS)
	}
	wg.Wait()

	eph.healthStatusMutex.Lock()
	defer eph.healthStatusMutex.Unlock()

	eph.isHealthy = eph.getHealthStatus(&successfulServiceChecks)
}

// performHTTPChecks performs HTTP-based QoS checks for a specific service.
func (eph *EndpointHydrator) performHTTPChecks(serviceID protocol.ServiceID, serviceQoS QoSService) error {
	logger := eph.Logger.With(
		"method", "performHTTPChecks",
		"service_id", string(serviceID),
	)

	// Passing a nil as the HTTP request, because we assume the hydrator uses "Centralized Operation Mode".
	// This implies there is no need to specify a specific app.
	// TODO_TECHDEBT(@adshmh): support specifying the app(s) used for sending/signing synthetic relay requests by the hydrator.
	// TODO_FUTURE(@adshmh): consider publishing observations if endpoint lookup fails.
	availableEndpoints, _, err := eph.AvailableHTTPEndpoints(context.TODO(), serviceID, nil)
	if err != nil || len(availableEndpoints) == 0 {
		// No session found or no endpoints available for service: skip.
		logger.Warn().Msg("no session found or no endpoints available for service when running HTTP hydrator checks.")
		// do NOT return an error: hydrator and PATH should not report unhealthy status if a single service is unavailable.
		return nil
	}

	logger = logger.With("number_of_endpoints", len(availableEndpoints))

	// Prepare a channel that will keep track of all the parallel async job to perform HTTP QoS checks on every endpoint.
	endpointCheckChan := make(chan protocol.EndpointAddr, len(availableEndpoints))

	var wgEndpoints sync.WaitGroup
	for range eph.MaxEndpointCheckWorkers {
		wgEndpoints.Add(1)

		go func() {
			defer wgEndpoints.Done()

			for endpointAddr := range endpointCheckChan {
				eph.runHTTPQualityChecks(logger, serviceID, serviceQoS, endpointAddr)
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

	// TODO_FUTURE: publish aggregated QoS reports (in addition to reports on endpoints of a specific service)
	return nil
}

// runHTTPQualityChecks performs HTTP-based quality checks for a specific endpoint.
func (eph *EndpointHydrator) runHTTPQualityChecks(
	endpointLogger polylog.Logger,
	serviceID protocol.ServiceID,
	serviceQoS QoSService,
	endpointAddr protocol.EndpointAddr,
) {
	// Retrieve all the required QoS checks for the endpoint.
	requiredQoSChecks := serviceQoS.GetRequiredQualityChecks(endpointAddr)
	if len(requiredQoSChecks) == 0 {
		endpointLogger.Warn().Msg("No required QoS checks for endpoint and service. Skipping checks...")
		return
	}

	// Iterate over every required QoS check for the endpoint and service.
	for _, serviceRequestCtx := range requiredQoSChecks {
		eph.performSingleQoSCheck(
			endpointLogger,
			serviceID,
			serviceQoS,
			endpointAddr,
			serviceRequestCtx,
		)
	}
}

// performSingleQoSCheck performs a single QoS check by sending a synthetic request to the endpoint.
func (eph *EndpointHydrator) performSingleQoSCheck(
	endpointLogger polylog.Logger,
	serviceID protocol.ServiceID,
	serviceQoS QoSService,
	endpointAddr protocol.EndpointAddr,
	serviceRequestCtx RequestQoSContext,
) {
	// Create a new protocol request context with a pre-selected endpoint for each request.
	// IMPORTANT: A new request context MUST be created on each iteration of the loop to
	// avoid race conditions related to concurrent access issues when running concurrent QoS checks.

	// Passing a nil as the HTTP request, because we assume the Centralized Operation Mode being used by the hydrator,
	// which means there is no need for specifying a specific app.
	// TODO_FUTURE(@adshmh): support specifying the app(s) used for sending/signing synthetic relay requests by the hydrator.
	// TODO_FUTURE(@adshmh): consider publishing observations here.
	hydratorRequestCtx, _, err := eph.BuildHTTPRequestContextForEndpoint(context.TODO(), serviceID, endpointAddr, nil)
	if err != nil {
		endpointLogger.Error().Err(err).Msg("Failed to build a protocol request context for the endpoint")
		return
	}

	// Prepare a request context to submit a synthetic relay request to the endpoint on behalf of the gateway for QoS purposes.
	gatewayRequestCtx := requestContext{
		logger:  endpointLogger,
		context: context.TODO(),
		// TODO_MVP(@adshmh): populate the fields of gatewayObservations struct.
		// Mark the request as Synthetic using the following steps:
		// 	1. Define a `gatewayObserver` function as a field in the `requestContext` struct.
		//	2. Define a `hydratorObserver` function in this file: it should at-least set the request type as `Synthetic`
		//	3. Set the `hydratorObserver` function in the `gatewayRequestContext` below.
		gatewayObservations: getSyntheticRequestGatewayObservations(),
		serviceID:           serviceID,
		serviceQoS:          serviceQoS,
		qosCtx:              serviceRequestCtx,
		protocol:            eph.Protocol,
		protocolContexts:    []ProtocolRequestContext{hydratorRequestCtx},
		// metrics reporter for exporting metrics on hydrator service requests.
		metricsReporter: eph.MetricsReporter,
		// data reporter for exporting data on hydrator service requests to the data pipeline.
		dataReporter: eph.DataReporter,
	}

	err = gatewayRequestCtx.HandleRelayRequest()
	if err != nil {
		// TODO_FUTURE: consider skipping the rest of the checks based on the error.
		// e.g. if the endpoint is refusing connections it may be reasonable to skip it
		// in this iteration of QoS checks.
		//
		// TODO_FUTURE: consider retrying failed service requests
		// as the failure may not be related to the quality of the endpoint.
		endpointLogger.Warn().Err(err).Msg("Failed to send a relay. Only protocol-level observations will be applied.")
	}

	// publish all observations gathered through sending the synthetic service requests.
	// e.g. protocol-level, qos-level observations.
	gatewayRequestCtx.BroadcastAllObservations()
}
