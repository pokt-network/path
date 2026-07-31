package protocol

// Websocket "tumble" — operator-forced redistribution of live websocket connections.
//
// A websocket connection binds ONE endpoint for its entire lifetime and only moves at a
// Shannon session rollover or when the staleness watchdog fires. A long-lived
// high-volume subscriber therefore stays on whichever operator it first landed on, for
// hours, and no change to endpoint selection can move it — selection only governs where
// NEW connections go. Tumbling forces existing connections to rebind onto different
// suppliers while keeping clients connected and replaying their subscriptions.
//
// These types live in the protocol package so the router can depend on the capability
// without importing a concrete protocol implementation.

// WebsocketTumbleRequest describes which of a service's live connections to move.
type WebsocketTumbleRequest struct {
	// ServiceID is required; only this service's connections are considered.
	ServiceID string

	// Domain, when non-empty, restricts the tumble to connections currently bound to
	// that registrable domain (eTLD+1) — the usual case, since the point is to move
	// connections OFF a concentrated operator.
	Domain string

	// Max, when > 0, caps how many connections are moved. Candidates are ordered so a cap
	// is spent where it does the most good — by default on the operators carrying the most
	// TRAFFIC, not the most sockets. Use it to redistribute gradually rather than rebinding
	// everything at once.
	Max int

	// OrderBy selects how candidates are ranked when Max caps the tumble. Ignored when Max
	// is 0, since then everything matched moves anyway.
	//
	// Defaults to throughput. Connection count is a poor proxy for load: one firehose
	// subscriber can outweigh a dozen idle sockets, so ordering by socket count spends the
	// budget moving connections that were not the problem while leaving the heavy ones in
	// place.
	OrderBy TumbleOrder

	// DryRun reports what would move without moving anything.
	DryRun bool
}

// TumbleOrder selects how a capped tumble ranks its candidates.
type TumbleOrder string

const (
	// TumbleOrderThroughput ranks by descending per-domain share of delivered frames — the
	// default, and what "redistribute load" actually means.
	TumbleOrderThroughput TumbleOrder = "throughput"

	// TumbleOrderConnections ranks by descending per-domain connection count. The original
	// behaviour, kept for the case where an operator wants to even out socket counts
	// irrespective of how busy those sockets are.
	TumbleOrderConnections TumbleOrder = "connections"
)

// WebsocketTumbleResult reports what a tumble did.
type WebsocketTumbleResult struct {
	ServiceID string `json:"service_id"`

	// Total is every live connection this pod holds for the service.
	Total int `json:"total_connections"`

	// Matched passed the domain filter and was eligible to move.
	Matched int `json:"matched_connections"`

	// Tumbled was actually asked to rebind. Lower than Matched when Max capped it, or
	// when a connection already had a tumble queued.
	Tumbled int `json:"tumbled_connections"`

	// Skipped could not accept a tumble right now: rebind disabled for that connection,
	// or one was already queued for it.
	Skipped int `json:"skipped_connections"`

	// ByDomain counts tumbled connections per registrable domain.
	ByDomain map[string]int `json:"tumbled_by_domain"`

	// DomainCounts is the pre-tumble distribution of ALL live connections per domain —
	// the "before" picture the operator acted on. Populated even on a dry run, which
	// makes a dry run a cheap way to just inspect the distribution.
	DomainCounts map[string]int `json:"connections_by_domain"`

	// DomainThroughput is the pre-tumble distribution of DELIVERED FRAMES PER SECOND per
	// domain. This is the distribution that matters for load: connection counts routinely
	// disagree with it by an order of magnitude, because one firehose subscriber can carry
	// more traffic than a dozen idle sockets on another operator.
	//
	// Populated even on a dry run, so a dry run answers "who is actually carrying this
	// service" — which connection counts alone cannot.
	DomainThroughput map[string]float64 `json:"throughput_by_domain_msgs_per_sec"`

	// TumbledThroughput is the frames-per-second moved off each domain by this tumble, so
	// an operator can see how much load actually shifted rather than just how many sockets.
	TumbledThroughput map[string]float64 `json:"tumbled_throughput_by_domain_msgs_per_sec"`

	// OrderBy records how candidates were ranked, so a response is self-describing.
	OrderBy string `json:"order_by"`

	DryRun bool `json:"dry_run"`
}
