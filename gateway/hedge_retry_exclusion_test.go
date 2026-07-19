package gateway

import (
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
)

// Two subdomains of one operator (registrable domain "opalpha.net") plus a distinct operator.
const (
	epAlphaPkp  = protocol.EndpointAddr("pokt1a-https://pkp.opalpha.net:443")
	epAlphaNr   = protocol.EndpointAddr("pokt1b-https://nr.opalpha.net:443")
	epBetaRm    = protocol.EndpointAddr("pokt1c-https://rm.opbeta.net:443")
	epAlphaWSS  = protocol.EndpointAddr("pokt1d-wss://ws.opalpha.net:443")
	epIPLiteral = protocol.EndpointAddr("pokt1e-https://10.0.0.1:443")
)

// TestExtractRegistrableDomain covers the eTLD+1 helper both exclusion sites rely on:
// subdomains of one operator collapse to the same registrable domain, and an
// undecidable host (IP literal) falls back to the full hostname.
func TestExtractRegistrableDomain(t *testing.T) {
	c := require.New(t)

	// Subdomains of the same operator → identical registrable domain.
	c.Equal("opalpha.net", extractRegistrableDomain(epAlphaPkp))
	c.Equal("opalpha.net", extractRegistrableDomain(epAlphaNr))
	c.Equal(extractRegistrableDomain(epAlphaPkp), extractRegistrableDomain(epAlphaNr),
		"sibling subdomains must share a registrable domain")

	// Distinct operator → distinct registrable domain.
	c.Equal("opbeta.net", extractRegistrableDomain(epBetaRm))
	c.NotEqual(extractRegistrableDomain(epAlphaPkp), extractRegistrableDomain(epBetaRm))

	// WebSocket scheme parsed the same way.
	c.Equal("opalpha.net", extractRegistrableDomain(epAlphaWSS))

	// IP literal has no registrable domain → fall back to full host (non-empty, stable).
	c.Equal("10.0.0.1", extractRegistrableDomain(epIPLiteral))
}

// TestFilterEndpoints_ExcludesOperatorSubdomains verifies the retry-side exclusion: once an
// endpoint of an operator is tried (markDomainTried writes eTLD+1), filterEndpoints excludes
// EVERY subdomain of that operator, while keeping a genuinely different operator. This is the
// self-retry the fix closes.
func TestFilterEndpoints_ExcludesOperatorSubdomains(t *testing.T) {
	c := require.New(t)

	available := protocol.EndpointAddrList{epAlphaPkp, epAlphaNr, epBetaRm}

	triedDomains := map[string]bool{}
	markDomainTried(triedDomains, epAlphaPkp) // records "opalpha.net"
	tried := map[protocol.EndpointAddr]bool{epAlphaPkp: true}

	filtered := filterEndpoints(available, tried, triedDomains)

	c.Equal(protocol.EndpointAddrList{epBetaRm}, filtered,
		"the tried endpoint AND its operator-sibling must both be excluded; only the other operator remains")
}

// TestFilterEndpoints_CircuitBreakerStaysPerHostname verifies the correctness point the naive
// eTLD+1 switch would have broken: circuit-breaker entries are seeded at FULL HOSTNAME, and
// must exclude only that exact instance — a sibling subdomain of the same operator stays
// eligible (per-instance breaking must not fault the whole operator).
func TestFilterEndpoints_CircuitBreakerStaysPerHostname(t *testing.T) {
	c := require.New(t)

	available := protocol.EndpointAddrList{epAlphaPkp, epAlphaNr, epBetaRm}

	// Circuit breaker seeds the broken instance's full hostname (not eTLD+1).
	triedDomains := map[string]bool{"pkp.opalpha.net": true}
	tried := map[protocol.EndpointAddr]bool{}

	filtered := filterEndpoints(available, tried, triedDomains)

	c.NotContains(filtered, epAlphaPkp, "the CB-broken instance must be excluded")
	c.Contains(filtered, epAlphaNr, "a sibling instance of the same operator must NOT be excluded by a per-hostname CB entry")
	c.Contains(filtered, epBetaRm)
	c.Len(filtered, 2)
}

// TestSelectHedgeEndpoint_ExcludesOperatorSibling verifies the hedge-side exclusion: when the
// only alternative to the primary is a different subdomain of the SAME operator, the hedge has
// no valid target (returns empty) rather than self-hedging. Pre-fix, full-hostname comparison
// would have let the sibling through.
func TestSelectHedgeEndpoint_ExcludesOperatorSibling(t *testing.T) {
	c := require.New(t)

	hr := &hedgeRacer{logger: polyzero.NewLogger()}

	// primary = pkp.opalpha.net; only other candidate is its operator-sibling nr.opalpha.net.
	got := hr.selectHedgeEndpoint(protocol.EndpointAddrList{epAlphaPkp, epAlphaNr}, epAlphaPkp)

	c.Empty(got, "hedge must not target a different subdomain of the same operator as the primary")
}

// TestSelectHedgeEndpoint_RecordsSelfOperatorAvoided verifies the self-avoided counter fires
// once per skipped sibling: with two operator-siblings alongside the primary, both are skipped
// and the counter for that service advances by 2 — the live signal for the eTLD+1 fix.
func TestSelectHedgeEndpoint_RecordsSelfOperatorAvoided(t *testing.T) {
	c := require.New(t)

	const svc = "test-hedge-avoided"
	hr := &hedgeRacer{logger: polyzero.NewLogger(), serviceID: svc}

	before := testutil.ToFloat64(metrics.HedgeSelfOperatorAvoidedTotal.WithLabelValues(svc))

	// primary pkp.opalpha.net + two siblings (nr, ws) of the same operator + one distinct operator.
	got := hr.selectHedgeEndpoint(
		protocol.EndpointAddrList{epAlphaPkp, epAlphaNr, epAlphaWSS, epBetaRm},
		epAlphaPkp,
	)

	c.Equal(epBetaRm, got, "hedge must land on the distinct operator")

	after := testutil.ToFloat64(metrics.HedgeSelfOperatorAvoidedTotal.WithLabelValues(svc))
	c.Equal(2.0, after-before, "two operator-siblings skipped => counter +2")
}
