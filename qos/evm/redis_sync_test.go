package evm

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
	qostypes "github.com/pokt-network/path/qos/types"
	"github.com/pokt-network/path/reputation"
)

// mockRedisSyncRepSvc is a minimal mock that implements the methods used by
// the Redis sync logic for endpoint block heights.
type mockRedisSyncRepSvc struct {
	reputation.ReputationService // embed to satisfy interface; unused methods will panic

	mu             sync.Mutex
	perceivedBlock map[protocol.ServiceID]uint64
	endpointBlocks map[protocol.ServiceID]map[protocol.EndpointAddr]uint64
}

func newMockRedisSyncRepSvc() *mockRedisSyncRepSvc {
	return &mockRedisSyncRepSvc{
		perceivedBlock: make(map[protocol.ServiceID]uint64),
		endpointBlocks: make(map[protocol.ServiceID]map[protocol.EndpointAddr]uint64),
	}
}

func (m *mockRedisSyncRepSvc) SetPerceivedBlockNumber(_ context.Context, serviceID protocol.ServiceID, blockNumber uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if blockNumber > m.perceivedBlock[serviceID] {
		m.perceivedBlock[serviceID] = blockNumber
	}
	return nil
}

func (m *mockRedisSyncRepSvc) GetPerceivedBlockNumber(_ context.Context, serviceID protocol.ServiceID) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.perceivedBlock[serviceID]
}

func (m *mockRedisSyncRepSvc) SetEndpointBlockHeight(_ context.Context, serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr, blockHeight uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.endpointBlocks[serviceID] == nil {
		m.endpointBlocks[serviceID] = make(map[protocol.EndpointAddr]uint64)
	}
	m.endpointBlocks[serviceID][endpointAddr] = blockHeight
	return nil
}

func (m *mockRedisSyncRepSvc) GetEndpointBlockHeights(_ context.Context, serviceID protocol.ServiceID) map[protocol.EndpointAddr]uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.endpointBlocks[serviceID] == nil {
		return make(map[protocol.EndpointAddr]uint64)
	}
	result := make(map[protocol.EndpointAddr]uint64, len(m.endpointBlocks[serviceID]))
	for k, v := range m.endpointBlocks[serviceID] {
		result[k] = v
	}
	return result
}

func (m *mockRedisSyncRepSvc) RemoveEndpointBlockHeights(_ context.Context, serviceID protocol.ServiceID, addrs []protocol.EndpointAddr) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.endpointBlocks[serviceID] != nil {
		for _, addr := range addrs {
			delete(m.endpointBlocks[serviceID], addr)
		}
	}
	return nil
}

func (m *mockRedisSyncRepSvc) SetArchivalStatus(_ context.Context, _ reputation.EndpointKey, _ bool, _ time.Duration) error {
	return nil
}

func (m *mockRedisSyncRepSvc) IsArchivalCapable(_ context.Context, _ reputation.EndpointKey) bool {
	return false
}

func (m *mockRedisSyncRepSvc) GetArchivalEndpoints(_ context.Context, _ protocol.ServiceID) []reputation.EndpointKey {
	return nil
}

func (m *mockRedisSyncRepSvc) SetLogger(_ polylog.Logger) {}

func (m *mockRedisSyncRepSvc) getBlock(serviceID protocol.ServiceID) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.perceivedBlock[serviceID]
}

func (m *mockRedisSyncRepSvc) setBlock(serviceID protocol.ServiceID, block uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.perceivedBlock[serviceID] = block
}

const testEVMServiceID = protocol.ServiceID("evm-test")

func newTestEVMQoS() *QoS {
	logger := polyzero.NewLogger()
	return NewSimpleQoSInstance(logger, testEVMServiceID)
}

func TestEVM_UpdateFromExtractedData_WritesEndpointBlockToRedis(t *testing.T) {
	qos := newTestEVMQoS()
	mock := newMockRedisSyncRepSvc()
	qos.reputationSvc = mock

	// Write two endpoints via UpdateFromExtractedData
	err := qos.UpdateFromExtractedData(protocol.EndpointAddr("ep1"), &qostypes.ExtractedData{BlockHeight: 100})
	require.NoError(t, err)
	err = qos.UpdateFromExtractedData(protocol.EndpointAddr("ep2"), &qostypes.ExtractedData{BlockHeight: 200})
	require.NoError(t, err)

	// Give async goroutines time to write
	time.Sleep(100 * time.Millisecond)

	mock.mu.Lock()
	blocks := mock.endpointBlocks[testEVMServiceID]
	mock.mu.Unlock()

	assert.Equal(t, uint64(100), blocks[protocol.EndpointAddr("ep1")])
	assert.Equal(t, uint64(200), blocks[protocol.EndpointAddr("ep2")])
}

func TestEVM_StartBackgroundSync_SyncsEndpointBlocks(t *testing.T) {
	qos := newTestEVMQoS()
	mock := newMockRedisSyncRepSvc()

	localHeight := uint64(50)
	highLocalHeight := uint64(300)

	// Pre-populate local endpoint store with endpoints that have block numbers
	qos.endpointStore.endpointsMu.Lock()
	qos.endpointStore.endpoints[protocol.EndpointAddr("ep1")] = endpoint{
		checkBlockNumber: endpointCheckBlockNumber{parsedBlockNumberResponse: &localHeight},
	}
	qos.endpointStore.endpoints[protocol.EndpointAddr("ep2")] = endpoint{
		checkBlockNumber: endpointCheckBlockNumber{parsedBlockNumberResponse: &highLocalHeight},
	}
	// ep_nil has nil block number — should still be updated since Redis > 0
	qos.endpointStore.endpoints[protocol.EndpointAddr("ep_nil")] = endpoint{}
	qos.endpointStore.endpointsMu.Unlock()

	// Pre-populate "Redis" with higher block height for ep1
	mock.mu.Lock()
	mock.endpointBlocks[testEVMServiceID] = map[protocol.EndpointAddr]uint64{
		protocol.EndpointAddr("ep1"):   500,
		protocol.EndpointAddr("ep2"):   200, // lower than local — should NOT overwrite
		protocol.EndpointAddr("ep3"):   400, // not in local store — should be CREATED from Redis
		protocol.EndpointAddr("ep_nil"): 600, // nil local block — Redis > 0, should update
	}
	mock.mu.Unlock()

	qos.reputationSvc = mock

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// StartBackgroundSync performs an immediate sync on startup
	qos.StartBackgroundSync(ctx, 50*time.Millisecond)

	// Verify
	qos.endpointStore.endpointsMu.RLock()
	ep1 := qos.endpointStore.endpoints[protocol.EndpointAddr("ep1")]
	ep2 := qos.endpointStore.endpoints[protocol.EndpointAddr("ep2")]
	epNil := qos.endpointStore.endpoints[protocol.EndpointAddr("ep_nil")]
	ep3, ep3Exists := qos.endpointStore.endpoints[protocol.EndpointAddr("ep3")]
	qos.endpointStore.endpointsMu.RUnlock()

	require.NotNil(t, ep1.checkBlockNumber.parsedBlockNumberResponse)
	assert.Equal(t, uint64(500), *ep1.checkBlockNumber.parsedBlockNumberResponse, "ep1 should be updated from Redis")
	require.NotNil(t, ep2.checkBlockNumber.parsedBlockNumberResponse)
	assert.Equal(t, uint64(300), *ep2.checkBlockNumber.parsedBlockNumberResponse, "ep2 should NOT be downgraded")
	require.NotNil(t, epNil.checkBlockNumber.parsedBlockNumberResponse)
	assert.Equal(t, uint64(600), *epNil.checkBlockNumber.parsedBlockNumberResponse, "ep_nil should be updated from Redis")
	assert.True(t, ep3Exists, "ep3 should be created in local store from Redis")
	require.NotNil(t, ep3.checkBlockNumber.parsedBlockNumberResponse)
	assert.Equal(t, uint64(400), *ep3.checkBlockNumber.parsedBlockNumberResponse, "ep3 block height should match Redis")
}

func TestEVM_StartBackgroundSync_PeriodicEndpointBlockSync(t *testing.T) {
	qos := newTestEVMQoS()
	mock := newMockRedisSyncRepSvc()

	localHeight := uint64(100)

	// Pre-populate local endpoint store
	qos.endpointStore.endpointsMu.Lock()
	qos.endpointStore.endpoints[protocol.EndpointAddr("ep1")] = endpoint{
		checkBlockNumber: endpointCheckBlockNumber{parsedBlockNumberResponse: &localHeight},
	}
	qos.endpointStore.endpointsMu.Unlock()

	qos.reputationSvc = mock

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start with empty Redis endpoint blocks
	qos.StartBackgroundSync(ctx, 50*time.Millisecond)

	// Now add data to Redis and wait for a tick
	mock.mu.Lock()
	mock.endpointBlocks[testEVMServiceID] = map[protocol.EndpointAddr]uint64{
		protocol.EndpointAddr("ep1"): 999,
	}
	mock.mu.Unlock()

	time.Sleep(150 * time.Millisecond)

	// Verify ep1 was updated by periodic sync
	qos.endpointStore.endpointsMu.RLock()
	ep1 := qos.endpointStore.endpoints[protocol.EndpointAddr("ep1")]
	qos.endpointStore.endpointsMu.RUnlock()

	require.NotNil(t, ep1.checkBlockNumber.parsedBlockNumberResponse)
	assert.Equal(t, uint64(999), *ep1.checkBlockNumber.parsedBlockNumberResponse, "ep1 should be updated by periodic sync")
}
