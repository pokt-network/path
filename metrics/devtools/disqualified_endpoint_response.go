package devtools

import (
	"time"

	"github.com/pokt-network/path/protocol"
)

// DisqualifiedEndpointResponse contains useful information about currently disqualified endpoints.
// It reports data from both the:
//   - Protocol-level disqualified endpoints
//   - QoS-level disqualified endpoints
//
// It also reports the total number of service endpoints, the number of qualified service endpoints, and the number of disqualified service endpoints.
type DisqualifiedEndpointResponse struct {
	ProtocolLevelDisqualifiedEndpoints map[string]ProtocolLevelDataResponse `json:"protocol_level_disqualified_endpoints"`
	QoSLevelDisqualifiedEndpoints      QoSLevelDataResponse                 `json:"qos_level_disqualified_endpoints"`
	TotalServiceEndpointsCount         int                                  `json:"total_service_endpoints_count"`
	DisqualifiedServiceEndpointsCount  int                                  `json:"disqualified_service_endpoints_count"`
}

// GetDisqualifiedEndpointsCount sums:
//   - Protocol-level permanently sanctioned endpoints
//   - Protocol-level session sanctioned endpoints
//   - QoS-level disqualified endpoints
func (r *DisqualifiedEndpointResponse) GetDisqualifiedEndpointsCount() int {
	protocolLevelDisqualifiedEndpointsCount := 0
	for _, protocolLevelDisqualifiedEndpoints := range r.ProtocolLevelDisqualifiedEndpoints {
		protocolLevelDisqualifiedEndpointsCount += len(protocolLevelDisqualifiedEndpoints.PermanentlySanctionedEndpoints) +
			len(protocolLevelDisqualifiedEndpoints.SessionSanctionedEndpoints)
	}
	return protocolLevelDisqualifiedEndpointsCount +
		len(r.QoSLevelDisqualifiedEndpoints.DisqualifiedEndpoints)
}

type (
	// ProtocolLevelDataResponse contains data about sanctioned endpoints at the protocol level.
	// It reports the number of permanently sanctioned endpoints, the number of session sanctioned endpoints, and the total number of sanctioned endpoints.
	ProtocolLevelDataResponse struct {
		// A mapping from endpoint address to an endpoint sanction at the protocol level.
		PermanentlySanctionedEndpoints map[protocol.EndpointAddr]SanctionedEndpoint `json:"permanently_sanctioned_endpoints"`

		// A mapping from session sanction key to an endpoint sanction at the protocol level.
		//
		// DEV_NOTE: The key is a string since it is a composite of the endpoint address and session ID.
		// Example for the key "pokt1ggdpwj5stslx2e567qcm50wyntlym5c4n0dst8-https://im.oldgreg.org-1234567890":
		//   - Endpoint address (supplier address + endpoint URL): "pokt1ggdpwj5stslx2e567qcm50wyntlym5c4n0dst8-https://im.oldgreg.org"
		//   - Session ID: "1234567890"
		SessionSanctionedEndpoints map[string]SanctionedEndpoint `json:"session_sanctioned_endpoints"`

		// Counters related to sanctioning details
		PermanentSanctionedEndpointsCount int `json:"permanent_sanctioned_endpoints_count"`
		SessionSanctionedEndpointsCount   int `json:"session_sanctioned_endpoints_count"`
		TotalSanctionedEndpointsCount     int `json:"total_sanctioned_endpoints_count"`
	}

	// QoSLevelDataResponse contains data about disqualified endpoints at the QoS level.
	// It reports the number of disqualified endpoints, the number of empty response endpoints, the number of chain ID check errors, the number of archival check errors, and the number of block number check errors.
	QoSLevelDataResponse struct {
		DisqualifiedEndpoints       map[protocol.EndpointAddr]QoSDisqualifiedEndpoint `json:"disqualified_endpoints"`
		EmptyResponseCount          int                                               `json:"empty_response_count"`
		ChainIDCheckErrorsCount     int                                               `json:"chain_id_check_errors_count"`
		ArchivalCheckErrorsCount    int                                               `json:"archival_check_errors_count"`
		BlockNumberCheckErrorsCount int                                               `json:"block_number_check_errors_count"`
	}

	// SanctionedEndpoint represents an endpoint sanctioned at the protocol level.
	SanctionedEndpoint struct {
		EndpointAddr protocol.EndpointAddr `json:"endpoint_addr"`
		// SessionID is only set for session-based sanctions.
		SessionID     string             `json:"session_id,omitempty"`
		ServiceID     protocol.ServiceID `json:"service_id"`
		Reason        string             `json:"reason"`
		SanctionType  string             `json:"sanction_type"`
		ErrorType     string             `json:"error_type"`
		SessionHeight int64              `json:"session_height"`
		CreatedAt     time.Time          `json:"created_at"`
	}

	// QoSDisqualifiedEndpoint represents an endpoint disqualified at the QoS level.
	QoSDisqualifiedEndpoint struct {
		EndpointAddr protocol.EndpointAddr `json:"endpoint_addr"`
		Reason       string                `json:"reason"`
		ServiceID    protocol.ServiceID    `json:"service_id"`
	}
)
