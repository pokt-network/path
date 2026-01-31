package protocol

import (
	"fmt"
	"strings"
)

// EndpointAddr is used as the unique identifier for a service endpoint.
// operator address and the endpoint's URL, separated by a "-" character.
//
// For example:
//   - "pokt1ggdpwj5stslx2e567qcm50wyntlym5c4n0dst8-https://im.oldgreg.org"
type EndpointAddr string

type EndpointAddrList []EndpointAddr

// Endpoint represents an entity which serves relay requests.
type Endpoint interface {
	// Addr is used to uniquely identify an endpoint.
	// Defining this as an interface allows Shannon to
	// define its own service endpoint address scheme.
	// See the comment on EndpointAddr type for more details.
	Addr() EndpointAddr

	// PublicURL is the publically exposed/accessible URL to which relay requests can be sent.
	PublicURL() string

	// WebsocketURL is the URL of the endpoint for websocket RPC type requests.
	// Returns an error if the endpoint does not support websocket RPC type requests.
	WebsocketURL() (string, error)
}

// EndpointSelector defines the functionality that the user of a protocol needs to provide.
// E.g. selecting an endpoint, from the list of available ones, to which the relay will be sent.
type EndpointSelector interface {
	Select(EndpointAddrList) (EndpointAddr, error)
	SelectMultiple(EndpointAddrList, uint) (EndpointAddrList, error)
	// SelectMultipleWithArchival selects multiple endpoints with optional archival filtering.
	// When requiresArchival is true, only endpoints that have passed archival capability checks
	// are considered. When false, all valid endpoints are considered (same as SelectMultiple).
	// This enables archival requests to be routed only to archival-capable endpoints.
	SelectMultipleWithArchival(EndpointAddrList, uint, bool) (EndpointAddrList, error)
}

func (e EndpointAddrList) String() string {
	// Converts each EndpointAddr to string and joins them with a comma
	addrs := make([]string, len(e))
	for i, addr := range e {
		addrs[i] = string(addr)
	}
	return strings.Join(addrs, ", ")
}

func (e EndpointAddr) String() string {
	return string(e)
}

// GetURL returns the effective TLD+1 domain of the endpoint address.
// For example:
// - Given the endpoint address "pokt1eetcwfv2agdl2nvpf4cprhe89rdq3cxdf037wq-https://relayminer.shannon-mainnet.eu.nodefleet.net"
// - Would return "https://relayminer.shannon-mainnet.eu.nodefleet.net"
func (e EndpointAddr) GetURL() (string, error) {
	// Split by dash to separate the address part from the URL part
	endpointAddrParts := strings.SplitN(e.String(), "-", 2)
	if len(endpointAddrParts) < 2 {
		return "", fmt.Errorf("endpoint address %s does not contain a dash separator", e.String())
	}

	// Take everything after the first dash as the URL
	return endpointAddrParts[1], nil
}

// GetAddress returns the address of the endpoint.
// For example:
// - Given the endpoint address "pokt1eetcwfv2agdl2nvpf4cprhe89rdq3cxdf037wq-https://relayminer.shannon-mainnet.eu.nodefleet.net"
// - Would return "pokt1eetcwfv2agdl2nvpf4cprhe89rdq3cxdf037wq"
func (e EndpointAddr) GetAddress() (string, error) {
	// Split by dash to separate the address part from the URL part
	endpointAddrParts := strings.Split(e.String(), "-")
	if len(endpointAddrParts) < 2 {
		return "", fmt.Errorf("endpoint address %s does not contain a dash separator", e.String())
	}

	// Take everything before the first dash as the address
	return endpointAddrParts[0], nil
}
