package gateway

import (
	"testing"
	"time"
)

func hedgeCapPtr(i int) *int { return &i }

// Hedging a batch costs up to one extra relay per ITEM, and large batches exceed a flat
// hedge_delay as a matter of course — so the cap must hold for realistic production batch
// sizes (scroll averages ~70 items/batch, opbnb ~37).
func TestShouldSuppressHedgeForBatch(t *testing.T) {
	tests := []struct {
		name      string
		maxBatch  *int
		batchSize int
		want      bool
	}{
		{"single request under default cap", hedgeCapPtr(10), 1, false},
		{"small batch at the cap boundary", hedgeCapPtr(10), 10, false},
		{"one item past the cap", hedgeCapPtr(10), 11, true},
		{"production-sized opbnb batch", hedgeCapPtr(10), 37, true},
		{"production-sized scroll batch", hedgeCapPtr(10), 70, true},
		{"cap of 0 disables suppression", hedgeCapPtr(0), 500, false},
		{"negative cap disables suppression", hedgeCapPtr(-1), 500, false},
		{"unset cap disables suppression", nil, 500, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldSuppressHedgeForBatch(&ServiceRetryConfig{HedgeMaxBatchSize: tt.maxBatch}, tt.batchSize)
			if got != tt.want {
				t.Errorf("shouldSuppressHedgeForBatch(max=%v, size=%d) = %v, want %v",
					tt.maxBatch, tt.batchSize, got, tt.want)
			}
		})
	}
}

// A nil retry config must not panic and must leave hedging alone.
func TestShouldSuppressHedgeForBatch_NilConfig(t *testing.T) {
	if shouldSuppressHedgeForBatch(nil, 500) {
		t.Error("nil retry config must not suppress hedging")
	}
}

// The cap must be hydrated into defaults, and inherited by a service that does not set it
// while still yielding to one that does.
func TestHedgeMaxBatchSize_DefaultAndMerge(t *testing.T) {
	c := &UnifiedServicesConfig{
		Services: []ServiceConfig{
			{ID: "inherits"},
			{ID: "overrides", RetryConfig: &ServiceRetryConfig{HedgeMaxBatchSize: hedgeCapPtr(50)}},
		},
	}
	c.HydrateDefaults()

	if c.Defaults.RetryConfig.HedgeMaxBatchSize == nil {
		t.Fatal("default hedge_max_batch_size was not hydrated")
	}
	if got := *c.Defaults.RetryConfig.HedgeMaxBatchSize; got != defaultHedgeMaxBatchSize {
		t.Errorf("default hedge_max_batch_size = %d, want %d", got, defaultHedgeMaxBatchSize)
	}

	inherited := c.GetMergedServiceConfig("inherits")
	if inherited.RetryConfig.HedgeMaxBatchSize == nil {
		t.Fatal("service did not inherit hedge_max_batch_size")
	}
	if got := *inherited.RetryConfig.HedgeMaxBatchSize; got != defaultHedgeMaxBatchSize {
		t.Errorf("inherited hedge_max_batch_size = %d, want %d", got, defaultHedgeMaxBatchSize)
	}

	overridden := c.GetMergedServiceConfig("overrides")
	if got := *overridden.RetryConfig.HedgeMaxBatchSize; got != 50 {
		t.Errorf("per-service override = %d, want 50", got)
	}
}

// NewProtocol takes GatewayConfig by value and keeps a pointer into that copy, so the config
// instance that serves requests is the one SetDefaultsFromParent runs on — HydrateDefaults
// runs on a different instance entirely. The cap must therefore survive a config that has
// ONLY been through SetDefaultsFromParent. Without this, hedge_max_batch_size stays nil in
// production and the suppression silently never fires.
func TestHedgeMaxBatchSize_SetByParentDefaultsAlone(t *testing.T) {
	c := &UnifiedServicesConfig{
		Services: []ServiceConfig{{ID: "scroll"}},
	}
	hedgeDelay := 200 * time.Millisecond

	// Deliberately NOT calling HydrateDefaults: reproduces the production wiring.
	c.SetDefaultsFromParent(ParentConfigDefaults{HedgeDelay: &hedgeDelay})

	if c.Defaults.RetryConfig.HedgeMaxBatchSize == nil {
		t.Fatal("hedge_max_batch_size is nil after SetDefaultsFromParent — suppression would never fire in production")
	}
	if got := *c.Defaults.RetryConfig.HedgeMaxBatchSize; got != defaultHedgeMaxBatchSize {
		t.Errorf("hedge_max_batch_size = %d, want %d", got, defaultHedgeMaxBatchSize)
	}

	merged := c.GetMergedServiceConfig("scroll")
	if !shouldSuppressHedgeForBatch(merged.RetryConfig, 70) {
		t.Error("a 70-item batch must suppress hedging under production wiring")
	}
	if shouldSuppressHedgeForBatch(merged.RetryConfig, 1) {
		t.Error("a single request must still hedge")
	}
}

// An explicit gateway_config.retry_config.hedge_max_batch_size must win over the default,
// including 0, which disables the cap.
func TestHedgeMaxBatchSize_ParentOverride(t *testing.T) {
	for _, tt := range []struct {
		name      string
		parentCap int
		batchSize int
		want      bool
	}{
		{"explicit cap of 50 suppresses a 70-item batch", 50, 70, true},
		{"explicit cap of 50 allows a 20-item batch", 50, 20, false},
		{"explicit 0 disables the cap entirely", 0, 500, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &UnifiedServicesConfig{Services: []ServiceConfig{{ID: "scroll"}}}
			c.SetDefaultsFromParent(ParentConfigDefaults{HedgeMaxBatchSize: hedgeCapPtr(tt.parentCap)})

			merged := c.GetMergedServiceConfig("scroll")
			if got := shouldSuppressHedgeForBatch(merged.RetryConfig, tt.batchSize); got != tt.want {
				t.Errorf("suppress(cap=%d, size=%d) = %v, want %v", tt.parentCap, tt.batchSize, got, tt.want)
			}
		})
	}
}
