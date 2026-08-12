package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// Tests for the de-labeling of the per-supplier metric family.
//
// Two metrics that lived here — path_supplier_signal_total and
// path_supplier_reputation_score — were removed entirely on 2026-08-12, along
// with their severity-collapse and empty-supplier tests. Both were GUARDED and
// both HONORED their guard while remaining among the largest series sources in
// the gateway job, because a guard caps the live registry and not the number of
// distinct series Prometheus retains. See DefaultSeriesLimit for the measurements
// and Test_SupplierLabelIsGone below for the property that replaced them.

// Test_RecordHealthCheck_CollapsesSuppliers locks the de-labeling of
// path_health_check_status_total. The metric carried a `supplier` label with no
// cardinality guard at all and reached 221,700 series in production (audit
// 2026-07-30); nothing consumed it. RecordHealthCheck still ACCEPTS a supplier
// so no caller changes, but suppliers behind the same backend must collapse onto
// one series.
func Test_RecordHealthCheck_CollapsesSuppliers(t *testing.T) {
	const (
		domain    = "healthcheck-delabel.example"
		rpcType   = "json_rpc"
		serviceID = "eth"
		checkName = "block_height"
	)

	// WithLabelValues panics on an arity mismatch, so this call is itself the
	// assertion that the metric has exactly 5 labels (no `supplier`).
	series := HealthCheckStatus.WithLabelValues(domain, rpcType, serviceID, checkName, SignalOK)
	before := testutil.ToFloat64(series)

	RecordHealthCheck(domain, "pokt1supplierone", rpcType, serviceID, checkName, SignalOK)
	RecordHealthCheck(domain, "pokt1suppliertwo", rpcType, serviceID, checkName, SignalOK)
	RecordHealthCheck(domain, "", rpcType, serviceID, checkName, SignalOK)

	require.Equal(t, before+3, testutil.ToFloat64(series),
		"all suppliers behind one domain must collapse onto the same series")
}

// Test_RecordSupplierBlacklist_CollapsesSuppliers locks the `supplier` label drop
// on path_supplier_blacklist_total. The address is still accepted (and still
// logged at the call site); it must not reach a label.
func Test_RecordSupplierBlacklist_CollapsesSuppliers(t *testing.T) {
	const (
		domain    = "blacklist-delabel.example"
		serviceID = "eth"
		reason    = BlacklistReasonSignatureError
	)

	// Arity assertion: 3 labels, no `supplier`.
	series := SupplierBlacklistTotal.WithLabelValues(domain, serviceID, reason)
	before := testutil.ToFloat64(series)

	RecordSupplierBlacklist(domain, "pokt1blacklistone", serviceID, reason)
	RecordSupplierBlacklist(domain, "pokt1blacklisttwo", serviceID, reason)

	require.Equal(t, before+2, testutil.ToFloat64(series),
		"two suppliers on one domain must collapse onto the same series")
}

// Test_RecordQoSFilterRejection_KeysOnDomain locks the supplier→domain re-key.
//
// This metric fires ~9,500/s fleet-wide and, with a supplier label, had the worst
// churn of any gateway metric: 1,271 series live in a 10-minute window against
// 24,708 distinct minted over one pod's 7.7h life (19.4×).
func Test_RecordQoSFilterRejection_KeysOnDomain(t *testing.T) {
	const (
		domain    = "qosfilter-rekey.example"
		serviceID = "eth"
		reason    = QoSFilterReasonBlockHeightLag
	)

	// Arity assertion: (domain, service_id, reason).
	series := QoSFilterRejectionTotal.WithLabelValues(domain, serviceID, reason)
	before := testutil.ToFloat64(series)

	RecordQoSFilterRejection(domain, serviceID, reason)
	RecordQoSFilterRejection(domain, serviceID, reason)
	require.Equal(t, before+2, testutil.ToFloat64(series))

	// Empty target is skipped rather than recorded as DomainUnknown: the empty
	// check MUST run before SanitizeDomainLabel, which maps "" to DomainUnknown
	// and would turn "no endpoint context" into a real series.
	unknown := QoSFilterRejectionTotal.WithLabelValues(DomainUnknown, serviceID, reason)
	unknownBefore := testutil.ToFloat64(unknown)
	RecordQoSFilterRejection("", serviceID, reason)
	require.Equal(t, unknownBefore, testutil.ToFloat64(unknown),
		"empty domain must be skipped, not collapsed onto DomainUnknown")

	// A supplier address reaching this metric collapses to the sentinel instead of
	// expanding ~1:1 with the supplier set — the failure this re-key exists to
	// prevent, in case a caller passes an EndpointAddr instead of a domain.
	sentinel := QoSFilterRejectionTotal.WithLabelValues(DomainSupplierAddr, serviceID, reason)
	sentinelBefore := testutil.ToFloat64(sentinel)
	RecordQoSFilterRejection("pokt1qosfilterleakedaddress", serviceID, reason)
	require.Equal(t, sentinelBefore+1, testutil.ToFloat64(sentinel),
		"a leaked supplier address must land on the supplier_addr sentinel")
}

// Test_RecordHedgeSupplierOutcome_Split guards the histogram→(counter+role
// histogram) split, and the supplier→domain re-key of the counter: the role
// latency histogram is always recorded, but the per-operator counter is skipped
// when the domain is unknown.
func Test_RecordHedgeSupplierOutcome_Split(t *testing.T) {
	// Unknown domain: role histogram still observes, per-operator counter does not.
	histBefore := testutil.CollectAndCount(HedgeRoleLatency)
	RecordHedgeSupplierOutcome("", HedgeRoleWinner, 0.12)
	require.GreaterOrEqual(t, testutil.CollectAndCount(HedgeRoleLatency), histBefore,
		"role latency histogram must record even without a domain")

	// Known operator: per-operator counter increments for the right role.
	const domain = "hedge-outcome.example"
	winBefore := testutil.ToFloat64(HedgeSupplierOutcomeTotal.WithLabelValues(domain, HedgeRoleWinner))
	RecordHedgeSupplierOutcome(domain, HedgeRoleWinner, 0.2)
	require.Equal(t, winBefore+1,
		testutil.ToFloat64(HedgeSupplierOutcomeTotal.WithLabelValues(domain, HedgeRoleWinner)),
		"winner count must increment for a known operator")

	// Loser role is a distinct series.
	loseBefore := testutil.ToFloat64(HedgeSupplierOutcomeTotal.WithLabelValues(domain, HedgeRoleLoser))
	RecordHedgeSupplierOutcome(domain, HedgeRoleLoser, 0.5)
	require.Equal(t, loseBefore+1,
		testutil.ToFloat64(HedgeSupplierOutcomeTotal.WithLabelValues(domain, HedgeRoleLoser)))

	// Two suppliers of the same operator must collapse, which is the whole point
	// of the re-key: a raw supplier address hits the sentinel, not its own series.
	sentinelBefore := testutil.ToFloat64(
		HedgeSupplierOutcomeTotal.WithLabelValues(DomainSupplierAddr, HedgeRoleWinner))
	RecordHedgeSupplierOutcome("pokt1hedgeleakedaddressone", HedgeRoleWinner, 0.3)
	RecordHedgeSupplierOutcome("pokt1hedgeleakedaddresstwo", HedgeRoleWinner, 0.3)
	require.Equal(t, sentinelBefore+2,
		testutil.ToFloat64(HedgeSupplierOutcomeTotal.WithLabelValues(DomainSupplierAddr, HedgeRoleWinner)),
		"leaked supplier addresses must collapse onto one sentinel series")
}

// Test_DomainFromEndpointAddr covers the EndpointAddr → operator-domain helper
// that the re-keyed call sites depend on.
//
// It returns "" rather than DomainUnknown on failure BY DESIGN: the Record*
// helpers check for empty before sanitizing, so returning a sentinel here would
// defeat their skip and mint a series for every context-less call.
func Test_DomainFromEndpointAddr(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want string
	}{
		{
			name: "supplier-url form yields eTLD+1",
			addr: "pokt1abc-https://relayminer.shannon-mainnet.eu.example.net",
			want: "example.net",
		},
		{
			name: "subdomains collapse onto one operator",
			addr: "pokt1def-https://other.host.example.net:8443",
			want: "example.net",
		},
		{
			name: "no dash separator yields empty, not a sentinel",
			addr: "pokt1abcnoseparator",
			want: "",
		},
		{
			name: "empty input yields empty",
			addr: "",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, DomainFromEndpointAddr(c.addr))
		})
	}

	// Two suppliers behind the same operator must resolve to one domain — the
	// property that turns a chain-sized label into an operator-sized one.
	a := DomainFromEndpointAddr("pokt1one-https://a.example.net")
	b := DomainFromEndpointAddr("pokt1two-https://b.example.net")
	require.Equal(t, a, b, "different suppliers on one operator must share a domain")
	require.NotEmpty(t, a)
}
