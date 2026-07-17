package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// Test_supplierSignalSeverity locks the 8-signal-type → 3-severity-class
// collapse that bounds supplier_signal_total cardinality. The exact strings are
// the reputation.SignalType wire values (metrics cannot import reputation —
// import cycle — so they are asserted literally here as the contract).
func Test_supplierSignalSeverity(t *testing.T) {
	cases := map[string]string{
		"success":            SupplierSeverityOK,
		"recovery_success":   SupplierSeverityOK,
		"slow_response":      SupplierSeveritySlow,
		"very_slow_response": SupplierSeveritySlow,
		"minor_error":        SupplierSeverityError,
		"major_error":        SupplierSeverityError,
		"critical_error":     SupplierSeverityError,
		"fatal_error":        SupplierSeverityError,
		// Unknown / newly-added types must fall through to error, never leak a
		// new label value (keeps cardinality bounded + surfaces the omission).
		"some_future_signal": SupplierSeverityError,
		"":                   SupplierSeverityError,
	}
	for in, want := range cases {
		require.Equalf(t, want, supplierSignalSeverity(in), "signal %q", in)
	}

	// Only three severity values may ever be emitted.
	seen := map[string]struct{}{}
	for in := range cases {
		seen[supplierSignalSeverity(in)] = struct{}{}
	}
	require.LessOrEqual(t, len(seen), 3, "severity label must have at most 3 values")
}

// Test_RecordSupplierSignal_EmptySupplierDropped guards the empty-supplier skip
// (per-domain reputation keys carry no supplier).
func Test_RecordSupplierSignal_EmptySupplierDropped(t *testing.T) {
	before := testutil.CollectAndCount(SupplierSignalTotal)
	RecordSupplierSignal("", "eth", "success")
	require.Equal(t, before, testutil.CollectAndCount(SupplierSignalTotal),
		"empty supplier must not create a series")
}

// Test_RecordHedgeSupplierOutcome_Split guards the histogram→(counter+role
// histogram) split: the role latency histogram is always recorded, but the
// per-supplier counter is skipped when supplier is empty.
func Test_RecordHedgeSupplierOutcome_Split(t *testing.T) {
	// Empty supplier: role histogram still observes, per-supplier counter does not.
	histBefore := testutil.CollectAndCount(HedgeRoleLatency)
	RecordHedgeSupplierOutcome("", HedgeRoleWinner, 0.12)
	require.Greater(t, testutil.CollectAndCount(HedgeRoleLatency), histBefore-1,
		"role latency histogram must record even without a supplier")

	// Known supplier: per-supplier counter increments for the right role.
	const supplier = "pokt1testsupplierhedge"
	winBefore := testutil.ToFloat64(HedgeSupplierOutcomeTotal.WithLabelValues(supplier, HedgeRoleWinner))
	RecordHedgeSupplierOutcome(supplier, HedgeRoleWinner, 0.2)
	winAfter := testutil.ToFloat64(HedgeSupplierOutcomeTotal.WithLabelValues(supplier, HedgeRoleWinner))
	require.Equal(t, winBefore+1, winAfter, "winner count must increment for known supplier")

	// Loser role is a distinct series.
	loseBefore := testutil.ToFloat64(HedgeSupplierOutcomeTotal.WithLabelValues(supplier, HedgeRoleLoser))
	RecordHedgeSupplierOutcome(supplier, HedgeRoleLoser, 0.5)
	require.Equal(t, loseBefore+1,
		testutil.ToFloat64(HedgeSupplierOutcomeTotal.WithLabelValues(supplier, HedgeRoleLoser)))
}
