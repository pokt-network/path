package gateway

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// pnf_path_rules.yaml is published to the URL that active_health_checks.external.url points
// at, and hot-loads into running gateways with no deploy. This mirrors refreshExternalConfig
// exactly: unmarshal []ServiceHealthCheckConfig, HydrateDefaults, Validate.
//
// The failure mode this guards is quiet: a service that fails Validate is SKIPPED WHOLESALE
// in production and merely logged as a warning, so one malformed check silently takes that
// service's other checks with it — and its endpoints stop being probed entirely.
func TestPNFRulesFileIsValid(t *testing.T) {
	b, err := os.ReadFile("../pnf_path_rules.yaml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var configs []ServiceHealthCheckConfig
	if err := yaml.Unmarshal(b, &configs); err != nil {
		t.Fatalf("YAML PARSE FAILED: %v", err)
	}
	t.Logf("parsed %d service configs", len(configs))

	wsCount, wsSvcs, failed := 0, []string{}, 0
	for i := range configs {
		configs[i].HydrateDefaults()
		if err := configs[i].Validate(); err != nil {
			failed++
			t.Errorf("VALIDATE FAILED (service would be SKIPPED, losing all its checks): %s: %v",
				configs[i].ServiceID, err)
			continue
		}
		for _, c := range configs[i].Checks {
			if c.Type == HealthCheckTypeWebSocket {
				wsCount++
				wsSvcs = append(wsSvcs, string(configs[i].ServiceID))
				if c.Timeout <= 0 {
					t.Errorf("%s/%s: websocket check has no timeout", configs[i].ServiceID, c.Name)
				}
			}
		}
	}
	t.Logf("websocket checks that will run: %d across %v", wsCount, wsSvcs)
	if failed > 0 {
		t.Fatalf("%d service configs failed validation", failed)
	}
	if wsCount != 25 {
		t.Errorf("expected 25 websocket checks, got %d", wsCount)
	}
}
