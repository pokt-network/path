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

	// Max, when > 0, caps how many connections are moved. Candidates are ordered by
	// descending per-domain concentration, so a cap is spent on the most concentrated
	// operators first. Use it to redistribute gradually rather than rebinding everything
	// at once.
	Max int

	// DryRun reports what would move without moving anything.
	DryRun bool
}

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

	DryRun bool `json:"dry_run"`
}
