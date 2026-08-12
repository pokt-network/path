package metrics

import (
	"strings"
	"unicode/utf8"
)

const (
	// DomainUnknown — the caller had no domain to report (empty string).
	DomainUnknown = "unknown"

	// DomainSupplierAddr — the caller passed a bech32 supplier address where a
	// domain was expected. Collapsed to a single sentinel rather than kept
	// verbatim: the whole point of `domain` is to COLLAPSE many suppliers onto
	// one operator, so admitting an address inverts the label's purpose and
	// makes it expand ~1:1 with supplier count.
	DomainSupplierAddr = "supplier_addr"

	// DomainLabelMaxLen — hard cap on any `domain` label value. A registrable
	// domain is far shorter; this is a safety net against a pathological host.
	DomainLabelMaxLen = 64
)

// SanitizeDomainLabel bounds the cardinality of the `domain` Prometheus label.
// MUST be called on every value flowing into a `domain` label.
//
// The problem it solves: shannonmetrics.ExtractDomainOrHost is shared between
// the metrics path and the ROUTING path (operator concentration cap, drains,
// blocked_domains, reputation keys). It returns a bare bech32 supplier address
// verbatim and with a nil error — a dotless host takes the
// isPrivateOrInternalDomain branch, which returns it as-is — so callers that
// pass an EndpointAddr rather than a URL silently emit `pokt1…` as a domain,
// and their `err != nil` fallback never fires. Fixing that inside the shared
// extractor would change endpoint selection; fixing it here cannot.
//
// Measured 2026-08-12: 4,172 of 4,608 distinct `domain` values in production
// were raw supplier addresses, driving path_probation_events_total to 2.35M
// series (27% of the whole TSDB).
//
// Deliberately NOT collapsed:
//   - Bare IPs. Operationally useful when a supplier registers one, and few
//     enough to be a non-issue (1 observed). The per-metric cardinality guard
//     is what protects against an IP-registration flood.
//   - Dotless internal hostnames (`relayminer1`). Bounded and meaningful.
func SanitizeDomainLabel(raw string) string {
	d := strings.ToLower(strings.TrimSpace(raw))
	if d == "" {
		return DomainUnknown
	}

	// Bech32 check is gated on "no dot" so it can never fire on a real domain:
	// isBech32Like requires the post-`1` remainder to be entirely alphanumeric,
	// and any registrable domain contains a dot in that remainder. Without the
	// gate this would be a heuristic on hostnames; with it, it is exact.
	if !strings.Contains(d, ".") && isBech32Like(d) {
		return DomainSupplierAddr
	}

	if len(d) > DomainLabelMaxLen {
		d = d[:DomainLabelMaxLen]
	}
	// prometheus/client_golang panics inside WithLabelValues on non-UTF-8 label
	// values, and the truncation above can split a multibyte rune.
	if !utf8.ValidString(d) {
		d = strings.ToValidUTF8(d, "�")
	}
	return d
}
