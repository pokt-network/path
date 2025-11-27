package devtools

import (
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/protocol"
)

type (
	// ProtocolDisqualifiedEndpointsReporter is an interface that provides data about sanctioned endpoints at the protocol level.
	ProtocolDisqualifiedEndpointsReporter interface {
		// GetTotalServiceEndpointsCount returns the total number of service endpoints for a given service ID.
		GetTotalServiceEndpointsCount(protocol.ServiceID, *http.Request) (int, error)
		// HydrateDisqualifiedEndpointsResponse hydrates the disqualified endpoint response with the protocol-specific data.
		HydrateDisqualifiedEndpointsResponse(protocol.ServiceID, *DisqualifiedEndpointResponse)
	}

	// QoSDisqualifiedEndpointsReporter is an interface that provides data about disqualified endpoints at the QoS level.
	QoSDisqualifiedEndpointsReporter interface {
		// HydrateDisqualifiedEndpointsResponse hydrates the disqualified endpoint response with the QoS-specific data.
		HydrateDisqualifiedEndpointsResponse(protocol.ServiceID, *DisqualifiedEndpointResponse)
	}
)

// DisqualifiedEndpointReporter is a reporter that collects data about disqualified
// endpoints from both the protocol and QoS levels.
//
// It is used by the `/disqualified_endpoints` URL path in the router to provide
// useful information about currently disqualified endpoints for development and debugging.
type DisqualifiedEndpointReporter struct {
	Logger                polylog.Logger
	ProtocolLevelReporter ProtocolDisqualifiedEndpointsReporter
	QoSLevelReporters     map[protocol.ServiceID]QoSDisqualifiedEndpointsReporter
}

// ReportEndpointStatus collects data about disqualified endpoints from both the protocol and QoS levels.
// It is used by the `/disqualified_endpoints` URL path in the router to provide
// useful information about currently disqualified endpoints for development and debugging.
func (r *DisqualifiedEndpointReporter) ReportEndpointStatus(serviceID protocol.ServiceID, httpReq *http.Request) (DisqualifiedEndpointResponse, error) {
	r.Logger.Info().Msgf("Reporting disqualified endpoints for service ID: %s", serviceID)

	var serviceEndpointsCount int
	serviceEndpointsCount, err := r.ProtocolLevelReporter.GetTotalServiceEndpointsCount(serviceID, httpReq)
	if err != nil {
		return DisqualifiedEndpointResponse{}, err
	}

	r.Logger.Info().Msgf("DisqualifiedEndpointReporter.Report: Successfully got available endpoints for service ID: %s", serviceID)

	details := DisqualifiedEndpointResponse{
		TotalServiceEndpointsCount: serviceEndpointsCount,
	}

	// Get Protocol-level sanctioned endpoints
	r.ProtocolLevelReporter.HydrateDisqualifiedEndpointsResponse(serviceID, &details)

	// Get QoS-level sanctioned endpoints
	for qosServiceID, qoSLevelReporter := range r.QoSLevelReporters {
		if serviceID != "" && qosServiceID != serviceID {
			continue
		}

		qoSLevelReporter.HydrateDisqualifiedEndpointsResponse(qosServiceID, &details)
	}

	r.Logger.Info().Msgf("DisqualifiedEndpointReporter.Report: Successfully hydrated disqualified endpoint details for service ID: %s", serviceID)

	details.DisqualifiedServiceEndpointsCount = details.GetDisqualifiedEndpointsCount()

	return details, nil
}
