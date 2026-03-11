package protocol

// EndpointDetails contains detailed information about a single endpoint.
type EndpointDetails struct {
	// Address is the unique identifier for this endpoint (supplier-url format)
	Address string `json:"address"`

	// SupplierAddress is the on-chain address of the supplier
	SupplierAddress string `json:"supplier_address"`

	// URL is the public URL of the endpoint
	URL string `json:"url"`

	// IsFallback indicates whether this is a fallback (non-protocol) endpoint
	IsFallback bool `json:"is_fallback"`

	// Reputation contains the endpoint's reputation metrics
	Reputation *EndpointReputation `json:"reputation,omitempty"`

	// Archival contains archival capability information (EVM services only)
	Archival *EndpointArchival `json:"archival,omitempty"`

	// RPCTypes lists the supported RPC types for this endpoint
	RPCTypes []string `json:"rpc_types,omitempty"`

	// Tier indicates the current reputation tier (1=highest, 2=medium, 3=lowest)
	Tier int `json:"tier,omitempty"`

	// InCooldown indicates if the endpoint is currently in cooldown
	InCooldown bool `json:"in_cooldown,omitempty"`

	// CooldownRemaining is the remaining cooldown time if in cooldown
	CooldownRemaining string `json:"cooldown_remaining,omitempty"`
}

// EndpointReputation contains reputation metrics for an endpoint.
type EndpointReputation struct {
	// Score is the current reputation score (0-100)
	Score float64 `json:"score"`

	// SuccessCount is the total number of successful requests
	SuccessCount int64 `json:"success_count"`

	// ErrorCount is the total number of failed requests
	ErrorCount int64 `json:"error_count"`

	// LastUpdated is when the score was last modified
	LastUpdated string `json:"last_updated,omitempty"`

	// Latency contains latency metrics for the endpoint
	Latency *EndpointLatency `json:"latency,omitempty"`

	// CriticalStrikes is the current strike count
	CriticalStrikes int `json:"critical_strikes,omitempty"`
}

// EndpointLatency contains latency metrics for an endpoint.
type EndpointLatency struct {
	// AvgLatencyMs is the average response latency in milliseconds
	AvgLatencyMs float64 `json:"avg_latency_ms"`

	// MinLatencyMs is the minimum observed latency in milliseconds
	MinLatencyMs float64 `json:"min_latency_ms"`

	// MaxLatencyMs is the maximum observed latency in milliseconds
	MaxLatencyMs float64 `json:"max_latency_ms"`

	// LastLatencyMs is the most recent latency sample in milliseconds
	LastLatencyMs float64 `json:"last_latency_ms"`

	// SampleCount is the number of latency samples collected
	SampleCount int64 `json:"sample_count"`
}

// EndpointArchival contains archival capability information.
type EndpointArchival struct {
	// IsArchival indicates if the endpoint supports archival requests
	IsArchival bool `json:"is_archival"`

	// ExpiresAt indicates when the archival status needs re-validation
	ExpiresAt string `json:"expires_at,omitempty"`
}
