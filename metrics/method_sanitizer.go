package metrics

import (
	"strings"
)

// SanitizeMethodLabel bounds the cardinality of the `method` Prometheus label
// on path_observation_pipeline_total. MUST be called on every value flowing
// into ObservationPipeline.WithLabelValues.
//
// Without this guard, raw user-supplied JSON-RPC method names and full Cosmos
// REST URL paths (with dynamic block heights, tx hashes, account addresses)
// flow straight into a Prometheus label, producing 64K+ unique series and a
// multi-GB heap leak in long-lived gateway pods.
//
// The funnel:
//  1. Empty / whitespace → MethodUnknown.
//  2. REST path (leading "/") → drop query string, replace dynamic segments
//     (numeric, hex, bech32, opaque ≥24 chars) with ":var", keep static
//     segments verbatim.
//  3. JSON-RPC method → allowlist hit returns verbatim; allowlist miss
//     buckets by namespace prefix (eth_other / solana_other / cosmos_other /
//     cometbft_other / other).
//  4. Hard length cap of MethodLabelMaxLen as a final safety net.
//
// networkType should be one of NetworkTypeEVM / NetworkTypeCosmos /
// NetworkTypeSolana / NetworkTypePassthrough (see metrics.go). An unknown
// networkType is treated as passthrough — caller-side bug, not a security
// concern, and still produces bounded output.
func SanitizeMethodLabel(networkType, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return MethodUnknown
	}

	if strings.HasPrefix(raw, "/") {
		return capLen(normalizeRESTPath(raw))
	}

	if isAllowlisted(networkType, raw) {
		return capLen(raw)
	}

	return capLen(classifyByPrefix(networkType, raw))
}

// isAllowlisted returns true if `raw` is on the per-network allowlist of
// known-good JSON-RPC method names.
//
// EVM and Cosmos allowlists are checked together for Cosmos chains because
// Cosmos JSON-RPC traffic is a mix of CometBFT methods and a few non-CometBFT
// RPCs (check_tx, broadcast_evidence). Passthrough/unknown networks consult
// every allowlist — better to keep a real method verbatim than over-bucket.
func isAllowlisted(networkType, raw string) bool {
	switch networkType {
	case NetworkTypeEVM:
		_, ok := evmAllowlist[raw]
		return ok
	case NetworkTypeSolana:
		_, ok := solanaAllowlist[raw]
		return ok
	case NetworkTypeCosmos:
		if _, ok := cometBFTAllowlist[raw]; ok {
			return true
		}
		_, ok := cosmosJSONRPCAllowlist[raw]
		return ok
	default:
		// Passthrough / unknown: be permissive across all allowlists.
		if _, ok := evmAllowlist[raw]; ok {
			return true
		}
		if _, ok := solanaAllowlist[raw]; ok {
			return true
		}
		if _, ok := cometBFTAllowlist[raw]; ok {
			return true
		}
		_, ok := cosmosJSONRPCAllowlist[raw]
		return ok
	}
}

// classifyByPrefix returns the bucket label for an unknown method based on its
// shape. Order of checks matters: check most-specific prefix first.
func classifyByPrefix(networkType, raw string) string {
	// EVM-style namespaces (eth_, net_, web3_, debug_, trace_, txpool_, admin_,
	// erigon_) share a single bucket — they're all "EVM-shaped" methods we
	// don't recognize.
	if hasEVMPrefix(raw) {
		return MethodOtherEVM
	}

	// Solana-style: camelCase, no underscore namespace prefix. Only treat as
	// Solana when the network type confirms it; otherwise too easy to
	// misclassify (e.g., a future Cosmos JSON-RPC method named `getFoo`).
	if networkType == NetworkTypeSolana {
		return MethodOtherSolana
	}

	// CometBFT/Cosmos-shaped: snake_case, no namespace dot. Network type
	// confirms it.
	if networkType == NetworkTypeCosmos {
		return MethodOtherCosmos
	}

	return MethodOther
}

// hasEVMPrefix reports whether the method name starts with one of the
// well-known EVM JSON-RPC namespace prefixes. Used by the bucket fallback.
func hasEVMPrefix(raw string) bool {
	for _, p := range evmNamespacePrefixes {
		if strings.HasPrefix(raw, p) {
			return true
		}
	}
	return false
}

// evmNamespacePrefixes is the set of namespace prefixes used by the EVM JSON-RPC
// ecosystem. Methods matching any of these but missing from evmAllowlist are
// bucketed to MethodOtherEVM.
var evmNamespacePrefixes = []string{
	"eth_",
	"net_",
	"web3_",
	"debug_",
	"trace_",
	"txpool_",
	"admin_",
	"erigon_",
}

// normalizeRESTPath collapses a REST URL path into a route template suitable
// for use as a Prometheus label. It:
//   - drops the query string (everything from `?` onward),
//   - splits on `/`,
//   - replaces dynamic segments (numeric, hex, bech32, ≥24-char opaque) with
//     `:var`,
//   - leaves static segments (`accounts`, `latest`, `v1beta1`, `blocks`, …)
//     verbatim,
//   - rejoins with `/`.
//
// Length cap is applied by the caller (capLen).
func normalizeRESTPath(p string) string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	// Path always starts with "/" by precondition (checked in
	// SanitizeMethodLabel); split keeps a leading empty segment we'll skip.
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		if seg == "" {
			continue
		}
		if isDynamicSegment(seg) {
			parts[i] = ":var"
		}
	}
	return strings.Join(parts, "/")
}

// isDynamicSegment reports whether a path segment looks like a request-specific
// identifier (block height, tx hash, account address, opaque token) rather than
// a stable route component.
//
// The detection is deliberately conservative: we'd rather leave a dynamic
// segment in (mild cardinality cost) than collapse a route component (lost
// signal). Heuristics covered:
//   - all digits (`12345`) → block height, tx index
//   - leading `0x` then hex → tx hash, address
//   - bech32-style (`cosmos1…`, `pokt1…`, `osmo1…`, etc.) → account address
//   - ≥24 chars and only alphanumerics → opaque token / ID
//
// `latest`, `accounts`, `v1beta1`, `blocks`, `tx`, etc. are NOT dynamic.
func isDynamicSegment(seg string) bool {
	if seg == "" {
		return false
	}

	// All-digit (block height, index, etc.).
	if isAllDigits(seg) {
		return true
	}

	// Hex with 0x prefix (tx hash, address, payload).
	if len(seg) > 2 && seg[0] == '0' && (seg[1] == 'x' || seg[1] == 'X') && isHexBody(seg[2:]) {
		return true
	}

	// Bech32-style (e.g., cosmos1abc..., pokt1xyz...). Conservative match: a
	// short lowercase HRP (2–8 chars) followed by `1` and ≥10 alphanumerics.
	if isBech32Like(seg) {
		return true
	}

	// Long opaque token: ≥24 chars and entirely alphanumeric. Catches base58
	// transaction ids, request hashes, etc., without colliding with normal
	// route components which are typically short and word-like.
	if len(seg) >= 24 && isAlphanumeric(seg) {
		return true
	}

	return false
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isHexBody(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

func isAlphanumeric(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		default:
			return false
		}
	}
	return true
}

// isBech32Like detects strings shaped like `<hrp>1<data>` where hrp is 2–8
// lowercase letters and the data part is ≥10 alphanumerics. Covers cosmos1…,
// pokt1…, osmo1…, etc., without false-positiving on normal route components.
func isBech32Like(s string) bool {
	if len(s) < 14 {
		return false
	}
	one := strings.IndexByte(s, '1')
	if one < 2 || one > 8 {
		return false
	}
	// HRP must be all lowercase letters.
	for i := 0; i < one; i++ {
		c := s[i]
		if c < 'a' || c > 'z' {
			return false
		}
	}
	// Data part must be ≥10 alphanumerics.
	data := s[one+1:]
	if len(data) < 10 {
		return false
	}
	return isAlphanumeric(data)
}

// capLen enforces MethodLabelMaxLen as the final safety net on every value
// returned to the caller.
func capLen(s string) string {
	if len(s) <= MethodLabelMaxLen {
		return s
	}
	return s[:MethodLabelMaxLen]
}
