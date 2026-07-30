package gateway

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// The property under test: endpoint resolution reaches the chain, and a single service
// whose lookup never returns must not stop any OTHER service's checks from being
// submitted. Before the fix the lookup ran inline in the submission loop, so one hung
// lookup meant every service after it in config order was never submitted at all —
// measured in production as the first few services trickling while the remaining
// sixty-one sat at exactly zero for 21 minutes.

// lookupProbeProtocol records which services actually had a check job execute.
//
// IsSessionActive is the first thing a submitted job calls for an endpoint carrying a
// session ID, so recording it observes SUBMISSION without needing a live endpoint.
// Returning false makes the job return immediately instead of firing a relay.
type lookupProbeProtocol struct {
	*mockProtocolForRetry

	mu      sync.Mutex
	visited map[protocol.ServiceID]int
}

func (m *lookupProbeProtocol) IsSessionActive(_ context.Context, serviceID protocol.ServiceID, _ string) bool {
	m.mu.Lock()
	m.visited[serviceID]++
	m.mu.Unlock()
	return false
}

func (m *lookupProbeProtocol) visitedServices() map[protocol.ServiceID]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := make(map[protocol.ServiceID]int, len(m.visited))
	for id, n := range m.visited {
		snapshot[id] = n
	}
	return snapshot
}

// newLookupProbeExecutor builds an executor with one json_rpc check per service.
func newLookupProbeExecutor(t *testing.T, serviceIDs []protocol.ServiceID) (*HealthCheckExecutor, *lookupProbeProtocol) {
	t.Helper()

	cfg := &ActiveHealthChecksConfig{Enabled: true}
	for _, serviceID := range serviceIDs {
		cfg.Local = append(cfg.Local, ServiceHealthCheckConfig{
			ServiceID: serviceID,
			Checks: []HealthCheckConfig{{
				Name:   "probe",
				Type:   HealthCheckTypeJSONRPC,
				Method: http.MethodPost,
				Path:   "/",
				Body:   `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber"}`,
			}},
		})
	}

	proto := &lookupProbeProtocol{
		mockProtocolForRetry: &mockProtocolForRetry{},
		visited:              make(map[protocol.ServiceID]int),
	}

	executor := NewHealthCheckExecutor(HealthCheckExecutorConfig{
		Config:     cfg,
		Logger:     polyzero.NewLogger(),
		Protocol:   proto,
		MaxWorkers: 8,
	})
	// Keep the test fast; production uses defaultEndpointLookupBudget.
	executor.endpointLookupBudget = 300 * time.Millisecond
	t.Cleanup(executor.Stop)

	return executor, proto
}

func endpointInfoFor(serviceID protocol.ServiceID) []EndpointInfo {
	url := "https://" + string(serviceID) + ".example"
	return []EndpointInfo{{
		Addr:      protocol.EndpointAddr("pokt1supplier-" + url),
		HTTPURL:   url,
		SessionID: "session-" + string(serviceID),
	}}
}

// The whole point of the fix: one hung lookup must not starve the other services.
// The hung service is FIRST in config order, which is exactly the arrangement that
// silenced the entire fleet before the fix.
func Test_RunAllChecksViaProtocol_HungLookupDoesNotStarveOtherServices(t *testing.T) {
	c := require.New(t)

	const hung protocol.ServiceID = "hung-service"
	serviceIDs := []protocol.ServiceID{hung, "eth", "poly", "bsc", "solana"}

	executor, proto := newLookupProbeExecutor(t, serviceIDs)

	// Released only at test teardown: the lookup for `hung` never returns on its own,
	// which is the production failure mode (chain query on a context with no deadline).
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	getEndpointInfos := func(serviceID protocol.ServiceID) ([]EndpointInfo, error) {
		if serviceID == hung {
			<-release
			return nil, errors.New("unreachable in this test")
		}
		return endpointInfoFor(serviceID), nil
	}

	start := time.Now()
	c.NoError(executor.RunAllChecksViaProtocol(context.Background(), getEndpointInfos))
	elapsed := time.Since(start)

	visited := proto.visitedServices()
	for _, serviceID := range serviceIDs[1:] {
		c.Positivef(visited[serviceID], "service %q was never submitted - a single hung lookup starved it", serviceID)
	}
	c.Zero(visited[hung], "the hung service must not be submitted")

	// The cycle must not be held open by the hung lookup beyond the resolution budget.
	c.Lessf(elapsed, 5*time.Second, "cycle took %s - the hung lookup blocked it", elapsed)
}

// A lookup that is merely SLOW (returns inside the budget) must still be checked, and
// must not delay the services behind it: resolution runs concurrently, so total time is
// one slow lookup, not the sum of them.
func Test_RunAllChecksViaProtocol_SlowLookupsResolveConcurrently(t *testing.T) {
	c := require.New(t)

	serviceIDs := []protocol.ServiceID{"eth", "poly", "bsc", "solana", "base"}
	executor, proto := newLookupProbeExecutor(t, serviceIDs)
	executor.endpointLookupBudget = 2 * time.Second

	const perLookupDelay = 150 * time.Millisecond
	getEndpointInfos := func(serviceID protocol.ServiceID) ([]EndpointInfo, error) {
		time.Sleep(perLookupDelay)
		return endpointInfoFor(serviceID), nil
	}

	start := time.Now()
	c.NoError(executor.RunAllChecksViaProtocol(context.Background(), getEndpointInfos))
	elapsed := time.Since(start)

	visited := proto.visitedServices()
	for _, serviceID := range serviceIDs {
		c.Positivef(visited[serviceID], "service %q was never submitted", serviceID)
	}

	// Serial resolution would take len(serviceIDs)*perLookupDelay.
	c.Lessf(elapsed, time.Duration(len(serviceIDs))*perLookupDelay,
		"resolution took %s - lookups are still serialized", elapsed)
}

// resolveEndpointInfos in isolation: hung services are absent, everyone else resolves,
// and the phase returns within its budget.
func Test_ResolveEndpointInfos_HungServiceIsSkippedWithinBudget(t *testing.T) {
	c := require.New(t)

	const hung protocol.ServiceID = "hung-service"
	serviceIDs := []protocol.ServiceID{hung, "eth", "poly", "bsc"}

	executor, _ := newLookupProbeExecutor(t, serviceIDs)
	executor.endpointLookupBudget = 200 * time.Millisecond

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	start := time.Now()
	resolved := executor.resolveEndpointInfos(
		context.Background(),
		serviceIDs,
		func(serviceID protocol.ServiceID) ([]EndpointInfo, error) {
			if serviceID == hung {
				<-release
				return nil, nil
			}
			return endpointInfoFor(serviceID), nil
		},
	)
	elapsed := time.Since(start)

	c.Len(resolved, len(serviceIDs)-1)
	c.NotContains(resolved, hung)
	for _, serviceID := range serviceIDs[1:] {
		c.Contains(resolved, serviceID)
	}
	c.Lessf(elapsed, time.Second, "resolution took %s - it waited on the hung lookup", elapsed)
}

// A lookup error skips only that service. It must be absent from the map (not present
// with a nil slice) so the submission loop can tell "failed" from "no endpoints".
func Test_ResolveEndpointInfos_ErrorSkipsOnlyThatService(t *testing.T) {
	c := require.New(t)

	const broken protocol.ServiceID = "broken-service"
	serviceIDs := []protocol.ServiceID{"eth", broken, "poly"}

	executor, _ := newLookupProbeExecutor(t, serviceIDs)

	resolved := executor.resolveEndpointInfos(
		context.Background(),
		serviceIDs,
		func(serviceID protocol.ServiceID) ([]EndpointInfo, error) {
			if serviceID == broken {
				return nil, errors.New("failed to get sessions for service")
			}
			return endpointInfoFor(serviceID), nil
		},
	)

	c.NotContains(resolved, broken)
	c.Contains(resolved, protocol.ServiceID("eth"))
	c.Contains(resolved, protocol.ServiceID("poly"))
}

// A canceled cycle context must abort resolution promptly rather than run the budget out.
func Test_ResolveEndpointInfos_HonoursContextCancellation(t *testing.T) {
	c := require.New(t)

	serviceIDs := []protocol.ServiceID{"eth", "poly"}
	executor, _ := newLookupProbeExecutor(t, serviceIDs)
	executor.endpointLookupBudget = 10 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	start := time.Now()
	executor.resolveEndpointInfos(ctx, serviceIDs, func(protocol.ServiceID) ([]EndpointInfo, error) {
		<-release
		return nil, nil
	})

	c.Less(time.Since(start), time.Second)
}
