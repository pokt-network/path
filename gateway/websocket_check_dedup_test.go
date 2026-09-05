package gateway

import (
	"context"
	"testing"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/metrics"
	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	"github.com/pokt-network/path/protocol"
)

// wsDedupStatusCount reads path_health_check_status_total for one endpoint's derived
// domain, using the same label derivation the executor uses so the test cannot drift.
func wsDedupStatusCount(endpointAddr protocol.EndpointAddr, signal string) float64 {
	domain, err := shannonmetrics.ExtractDomainOrHost(string(endpointAddr))
	if err != nil {
		domain = shannonmetrics.ErrDomain
	}
	rpcType := metrics.NormalizeRPCType(sharedtypes.RPCType_WEBSOCKET.String())
	return testutil.ToFloat64(
		metrics.HealthCheckStatus.WithLabelValues(domain, rpcType, string(wsCheckService), wsCheckName, signal),
	)
}

// Suppliers sharing one backend websocket URL must cost ONE handshake, not one each.
//
// HTTP checks were deduplicated by backend URL; websocket checks were not, so a backend
// carrying N supplier registrations took N full TCP+TLS+WS handshakes per cycle where HTTP
// took one. Measured 2026-09-05: 29.85 websocket checks/s fleetwide, 6.62/s of them onto a
// single operator, arriving as ~3/s of full-TLS handshakes per gateway egress IP. Stacked
// registrations are common, so the multiplier tracked how a provider spread its
// registrations rather than how much traffic it served.
//
// Endpoints on a DIFFERENT websocket URL are a different backend and must still be probed
// directly — deduplicating those would stop testing a machine nobody else tests.
func Test_WebsocketCheck_OneHandshakePerBackendURL(t *testing.T) {
	c := require.New(t)

	executor, proto, _, svcConfig := newWebsocketCheckExecutor(t, nil)

	// Different REGISTRABLE domains, not just different hostnames: the metric label is
	// eTLD+1, so two hosts under one registrable domain share a series and the two groups'
	// records would be indistinguishable. That collapse is the same one that hid a
	// per-hostname circuit-breaker lockout behind a healthy-looking per-operator series.
	const sharedURL = "https://ws-shared.example.com"
	const soloURL = "https://ws-solo.example.net"

	shared := []EndpointInfo{
		{Addr: protocol.EndpointAddr("pokt1a-" + sharedURL), WebSocketURL: sharedURL},
		{Addr: protocol.EndpointAddr("pokt1b-" + sharedURL), WebSocketURL: sharedURL},
		{Addr: protocol.EndpointAddr("pokt1c-" + sharedURL), WebSocketURL: sharedURL},
		{Addr: protocol.EndpointAddr("pokt1d-" + sharedURL), WebSocketURL: sharedURL},
	}
	solo := EndpointInfo{Addr: protocol.EndpointAddr("pokt1e-" + soloURL), WebSocketURL: soloURL}

	// An endpoint advertising no websocket URL is neither probed nor recorded.
	noWS := EndpointInfo{Addr: protocol.EndpointAddr("pokt1f-" + sharedURL)}

	members := append(append([]EndpointInfo{}, shared...), solo, noWS)

	sharedOKBefore := wsDedupStatusCount(shared[0].Addr, metrics.SignalOK)
	soloOKBefore := wsDedupStatusCount(solo.Addr, metrics.SignalOK)

	executor.submitDedupedWebsocketChecks(context.Background(), wsCheckService, svcConfig, 0, members)
	executor.wsPool.StopAndWait() // drain: websocket checks run off the cycle

	// The whole point: 2 distinct backend websocket URLs => 2 handshakes, not 5.
	c.EqualValues(2, proto.checksDispatched.Load(),
		"expected one websocket handshake per distinct backend URL")

	// The four suppliers behind the shared URL must ALL be recorded, not just the probed
	// one. Skipping the siblings would leave them clean and selectable while the backend
	// they share is broken — the endpoint gets reached anyway, via another supplier address.
	c.EqualValues(sharedOKBefore+4, wsDedupStatusCount(shared[0].Addr, metrics.SignalOK),
		"every supplier sharing the probed backend URL must be recorded")
	c.EqualValues(soloOKBefore+1, wsDedupStatusCount(solo.Addr, metrics.SignalOK),
		"an endpoint on its own backend URL must still be probed and recorded")
}

// Rotation: which supplier carries the handshake must advance with the cycle counter, so a
// supplier's own websocket path is directly probed rather than one supplier answering for
// the group forever.
func Test_WebsocketCheck_HandshakeRotatesAcrossCycles(t *testing.T) {
	c := require.New(t)

	const sharedURL = "https://ws-rotate.example.com"
	members := []EndpointInfo{
		{Addr: protocol.EndpointAddr("pokt1a-" + sharedURL), WebSocketURL: sharedURL},
		{Addr: protocol.EndpointAddr("pokt1b-" + sharedURL), WebSocketURL: sharedURL},
		{Addr: protocol.EndpointAddr("pokt1c-" + sharedURL), WebSocketURL: sharedURL},
	}

	seen := map[int]struct{}{}
	for cycle := uint64(0); cycle < uint64(len(members)); cycle++ {
		seen[representativeIndex(cycle, len(members))] = struct{}{}
	}
	c.Len(seen, len(members),
		"every supplier behind a shared backend URL must get a directly-probed turn")
}
