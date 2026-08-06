package gateway

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// The property under test: a service's own check_interval decides how often it is
// checked. The loop ticks at ONE global period (the fleet minimum), so before this
// gate existed every enabled service ran on every tick and check_interval could only
// ever lower the global floor. Measured 2026-08-03 against pnf_path_rules.yaml: 33 of
// 69 services ran faster than they asked for, up to 6x, and the worst offenders were
// services with no user traffic at all.

func scheduleConfig(intervals map[protocol.ServiceID]time.Duration) []ServiceHealthCheckConfig {
	configs := make([]ServiceHealthCheckConfig, 0, len(intervals))
	for serviceID, interval := range intervals {
		configs = append(configs, ServiceHealthCheckConfig{
			ServiceID:     serviceID,
			CheckInterval: interval,
			Checks: []HealthCheckConfig{{
				Name:   "probe",
				Type:   HealthCheckTypeJSONRPC,
				Method: http.MethodPost,
				Path:   "/",
			}},
		})
	}
	return configs
}

func newScheduleExecutor(t *testing.T, configs []ServiceHealthCheckConfig) *HealthCheckExecutor {
	t.Helper()

	executor := NewHealthCheckExecutor(HealthCheckExecutorConfig{
		Config:     &ActiveHealthChecksConfig{Enabled: true, Local: configs},
		Logger:     polyzero.NewLogger(),
		Protocol:   &mockProtocolForRetry{},
		MaxWorkers: 4,
	})
	t.Cleanup(executor.Stop)
	return executor
}

// A 30s service must run once per three 10s ticks, not on every one.
func Test_dueServices_HonorsPerServiceInterval(t *testing.T) {
	c := require.New(t)

	const (
		fast protocol.ServiceID = "fast"
		slow protocol.ServiceID = "slow"
	)
	configs := scheduleConfig(map[protocol.ServiceID]time.Duration{
		fast: 10 * time.Second,
		slow: 30 * time.Second,
	})
	executor := newScheduleExecutor(t, configs)

	const tick = 10 * time.Second
	base := time.Now()

	runs := map[protocol.ServiceID]int{}
	// 9 ticks == 90 seconds of wall clock.
	for i := range 9 {
		for serviceID := range executor.dueServices(configs, base.Add(time.Duration(i)*tick), tick) {
			runs[serviceID]++
		}
	}

	c.Equal(9, runs[fast], "a service at the tick period must run on every tick")
	c.Equal(3, runs[slow], "a 30s service must run 3 times in 90s, not 9")
}

// The regression the half-tick slack exists for. The loop ticks on a fixed period, so
// a 30s service reaching t=30s microseconds late would miss its tick under a bare
// `elapsed >= interval` and fire at t=40s instead - silently rounding every interval up
// to the next multiple of the tick, which is the bug this whole change removes.
func Test_dueServices_JitterDoesNotPushServiceToNextTick(t *testing.T) {
	c := require.New(t)

	const slow protocol.ServiceID = "slow"
	configs := scheduleConfig(map[protocol.ServiceID]time.Duration{slow: 30 * time.Second})
	executor := newScheduleExecutor(t, configs)

	const tick = 10 * time.Second
	base := time.Now()

	// Each tick lands slightly LATE, the way a real ticker plus cycle work does. The
	// t=30s tick is therefore at 30s + jitter relative to the t=0 run, and a naive
	// comparison against the previous run time would still be under 30s of elapsed
	// only if jitter were negative - so the failing direction here is the tick landing
	// EARLY, which a ticker draining a backlog does do.
	jitter := []time.Duration{0, 0, 0, -2 * time.Millisecond}

	fired := make([]int, 0, 4)
	for i := range 4 {
		now := base.Add(time.Duration(i)*tick + jitter[i])
		if _, due := executor.dueServices(configs, now, tick)[slow]; due {
			fired = append(fired, i)
		}
	}

	c.Equal([]int{0, 3}, fired, "the 30s service must fire on the t=30s tick, not slip to t=40s")
}

// Nothing has run at startup, so everything is due on the first cycle regardless of
// interval - a fresh pod must not wait 60s for its slowest service's first signal.
func Test_dueServices_FirstCycleRunsEverything(t *testing.T) {
	c := require.New(t)

	configs := scheduleConfig(map[protocol.ServiceID]time.Duration{
		"a": 10 * time.Second,
		"b": 60 * time.Second,
		"c": 30 * time.Second,
	})
	executor := newScheduleExecutor(t, configs)

	due := executor.dueServices(configs, time.Now(), 10*time.Second)
	c.Len(due, 3, "every service must be checked on the first cycle")
}

// Disabled services are never due, and must not accumulate scheduling state.
func Test_dueServices_SkipsDisabled(t *testing.T) {
	c := require.New(t)

	disabled := false
	configs := scheduleConfig(map[protocol.ServiceID]time.Duration{"on": 10 * time.Second})
	configs = append(configs, ServiceHealthCheckConfig{
		ServiceID:     "off",
		CheckInterval: 10 * time.Second,
		Enabled:       &disabled,
	})
	executor := newScheduleExecutor(t, configs)

	due := executor.dueServices(configs, time.Now(), 10*time.Second)
	c.Contains(due, protocol.ServiceID("on"))
	c.NotContains(due, protocol.ServiceID("off"))
}

// The rotation counter must advance once per run of ITS service. A global counter
// advanced by 3 per run of a 30s service, so a backend-URL group of size 3 would
// re-probe the same representative forever and never directly validate its siblings.
func Test_dueServices_RotationCounterIsPerService(t *testing.T) {
	c := require.New(t)

	const slow protocol.ServiceID = "slow"
	configs := scheduleConfig(map[protocol.ServiceID]time.Duration{
		"fast": 10 * time.Second,
		slow:   30 * time.Second,
	})
	executor := newScheduleExecutor(t, configs)

	const tick = 10 * time.Second
	base := time.Now()

	cycles := make([]uint64, 0, 3)
	for i := range 9 {
		if cycle, due := executor.dueServices(configs, base.Add(time.Duration(i)*tick), tick)[slow]; due {
			cycles = append(cycles, cycle)
		}
	}

	c.Equal([]uint64{1, 2, 3}, cycles, "the slow service's counter must step by 1 per run, not by elapsed ticks")

	// Consecutive counters walk every member of a group; a counter stepping by 3 would
	// return index 0 forever for a group of 3.
	seen := map[int]struct{}{}
	for _, cycle := range cycles {
		seen[representativeIndex(cycle, 3)] = struct{}{}
	}
	c.Len(seen, 3, "three consecutive runs must probe three different representatives")
}

// The tick period must be the fleet minimum: it is the resolution floor below which no
// service's interval can be honored.
func Test_MinCheckInterval(t *testing.T) {
	c := require.New(t)

	configs := scheduleConfig(map[protocol.ServiceID]time.Duration{
		"a": 30 * time.Second,
		"b": 10 * time.Second,
		"c": 60 * time.Second,
	})
	c.Equal(10*time.Second, newScheduleExecutor(t, configs).MinCheckInterval())

	// An unset interval falls back to the default rather than collapsing the tick to 0.
	unset := []ServiceHealthCheckConfig{{ServiceID: "a"}, {ServiceID: "b", CheckInterval: 30 * time.Second}}
	c.Equal(DefaultHealthCheckInterval, newScheduleExecutor(t, unset).MinCheckInterval())

	var nilExecutor *HealthCheckExecutor
	c.Equal(DefaultHealthCheckInterval, nilExecutor.MinCheckInterval())
}

// A disabled service must not drag the whole fleet's tick period down with it.
func Test_MinCheckInterval_IgnoresDisabled(t *testing.T) {
	c := require.New(t)

	disabled := false
	configs := []ServiceHealthCheckConfig{
		{ServiceID: "on", CheckInterval: 30 * time.Second},
		{ServiceID: "off", CheckInterval: time.Second, Enabled: &disabled},
	}
	c.Equal(30*time.Second, newScheduleExecutor(t, configs).MinCheckInterval())
}

// End-to-end on the real cycle entry point: a second cycle immediately after the first
// must submit nothing AND resolve no endpoints. Endpoint resolution is the expensive
// phase, so a gate that only skipped the relays would leave most of the cost in place.
func Test_RunAllChecksViaProtocol_SkipsServicesNotDue(t *testing.T) {
	c := require.New(t)

	serviceIDs := []protocol.ServiceID{"eth", "poly", "bsc"}
	executor, proto := newLookupProbeExecutor(t, serviceIDs)

	var mu sync.Mutex
	lookups := 0
	getEndpointInfos := func(serviceID protocol.ServiceID) ([]EndpointInfo, error) {
		mu.Lock()
		lookups++
		mu.Unlock()
		return endpointInfoFor(serviceID), nil
	}

	c.NoError(executor.RunAllChecksViaProtocol(context.Background(), getEndpointInfos))

	mu.Lock()
	afterFirst := lookups
	mu.Unlock()
	c.Equal(len(serviceIDs), afterFirst, "the first cycle must check every service")

	visitedAfterFirst := proto.visitedServices()
	for _, serviceID := range serviceIDs {
		c.Positive(visitedAfterFirst[serviceID])
	}

	// Immediately again: no service's interval (10s by default) has elapsed.
	c.NoError(executor.RunAllChecksViaProtocol(context.Background(), getEndpointInfos))

	mu.Lock()
	afterSecond := lookups
	mu.Unlock()
	c.Equal(afterFirst, afterSecond, "a not-due cycle must not resolve endpoints - that is the expensive phase")
	c.Equal(visitedAfterFirst, proto.visitedServices(), "a not-due cycle must not submit checks")
}
