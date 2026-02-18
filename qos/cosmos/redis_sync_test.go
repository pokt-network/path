package cosmos

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
	qostypes "github.com/pokt-network/path/qos/types"
	"github.com/pokt-network/path/reputation"
)

// mockReputationSvc is a minimal mock that only implements the methods used by
// the Redis sync logic (SetPerceivedBlockNumber, GetPerceivedBlockNumber,
// SetEndpointBlockHeight, and GetEndpointBlockHeights).
type mockReputationSvc struct {
	reputation.ReputationService // embed to satisfy interface; unused methods will panic

	mu             sync.Mutex
	perceivedBlock map[protocol.ServiceID]uint64
	endpointBlocks map[protocol.ServiceID]map[protocol.EndpointAddr]uint64
}

func newMockReputationSvc() *mockReputationSvc {
	return &mockReputationSvc{
		perceivedBlock: make(map[protocol.ServiceID]uint64),
		endpointBlocks: make(map[protocol.ServiceID]map[protocol.EndpointAddr]uint64),
	}
}

func (m *mockReputationSvc) SetPerceivedBlockNumber(_ context.Context, serviceID protocol.ServiceID, blockNumber uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if blockNumber > m.perceivedBlock[serviceID] {
		m.perceivedBlock[serviceID] = blockNumber
	}
	return nil
}

func (m *mockReputationSvc) GetPerceivedBlockNumber(_ context.Context, serviceID protocol.ServiceID) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.perceivedBlock[serviceID]
}

func (m *mockReputationSvc) getBlock(serviceID protocol.ServiceID) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.perceivedBlock[serviceID]
}

func (m *mockReputationSvc) setBlock(serviceID protocol.ServiceID, block uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.perceivedBlock[serviceID] = block
}

func (m *mockReputationSvc) SetEndpointBlockHeight(_ context.Context, serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr, blockHeight uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.endpointBlocks[serviceID] == nil {
		m.endpointBlocks[serviceID] = make(map[protocol.EndpointAddr]uint64)
	}
	m.endpointBlocks[serviceID][endpointAddr] = blockHeight
	return nil
}

func (m *mockReputationSvc) GetEndpointBlockHeights(_ context.Context, serviceID protocol.ServiceID) map[protocol.EndpointAddr]uint64 {
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

func newTestCosmosQoS() *QoS {
	logger := polyzero.NewLogger()
	return NewSimpleQoSInstance(logger, "cosmos-test")
}

func TestCosmos_SetReputationService(t *testing.T) {
	qos := newTestCosmosQoS()
	require.Nil(t, qos.reputationSvc)

	mock := newMockReputationSvc()
	qos.SetReputationService(mock)
	require.NotNil(t, qos.reputationSvc)
}

func TestCosmos_StartBackgroundSync_UpdatesFromRedis(t *testing.T) {
	qos := newTestCosmosQoS()
	mock := newMockReputationSvc()
	serviceID := qos.serviceQoSConfig.GetServiceID()

	// Pre-populate Redis with a higher block
	mock.setBlock(serviceID, 500)

	qos.SetReputationService(mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// StartBackgroundSync performs an immediate sync on startup
	qos.StartBackgroundSync(ctx, 50*time.Millisecond)

	// The immediate sync should have picked up the Redis value
	assert.Equal(t, uint64(500), qos.GetPerceivedBlockNumber())

	// Now raise Redis and wait for a tick
	mock.setBlock(serviceID, 700)
	time.Sleep(150 * time.Millisecond)

	assert.Equal(t, uint64(700), qos.GetPerceivedBlockNumber())
}

func TestCosmos_StartBackgroundSync_NilReputationSvc(t *testing.T) {
	qos := newTestCosmosQoS()
	// Should not panic; just returns early
	qos.StartBackgroundSync(context.Background(), time.Second)
}

func TestCosmos_UpdateFromExtractedData_WritesToRedis(t *testing.T) {
	qos := newTestCosmosQoS()
	mock := newMockReputationSvc()
	qos.SetReputationService(mock)
	serviceID := qos.serviceQoSConfig.GetServiceID()

	err := qos.UpdateFromExtractedData(
		protocol.EndpointAddr("ep1"),
		&qostypes.ExtractedData{BlockHeight: 100},
	)
	require.NoError(t, err)
	assert.Equal(t, uint64(100), qos.GetPerceivedBlockNumber())

	// Give async goroutine time to write to Redis
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, uint64(100), mock.getBlock(serviceID))

	// Second call with higher block should also write
	err = qos.UpdateFromExtractedData(
		protocol.EndpointAddr("ep2"),
		&qostypes.ExtractedData{BlockHeight: 200},
	)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, uint64(200), mock.getBlock(serviceID))

	// Lower block should NOT write
	err = qos.UpdateFromExtractedData(
		protocol.EndpointAddr("ep3"),
		&qostypes.ExtractedData{BlockHeight: 150},
	)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, uint64(200), mock.getBlock(serviceID))
}

func TestCosmos_UpdateFromExtractedData_WritesEndpointBlockToRedis(t *testing.T) {
	qos := newTestCosmosQoS()
	mock := newMockReputationSvc()
	qos.SetReputationService(mock)
	serviceID := qos.serviceQoSConfig.GetServiceID()

	// Write two endpoints via UpdateFromExtractedData
	err := qos.UpdateFromExtractedData(protocol.EndpointAddr("ep1"), &qostypes.ExtractedData{BlockHeight: 100})
	require.NoError(t, err)
	err = qos.UpdateFromExtractedData(protocol.EndpointAddr("ep2"), &qostypes.ExtractedData{BlockHeight: 200})
	require.NoError(t, err)

	// Give async goroutines time to write
	time.Sleep(100 * time.Millisecond)

	mock.mu.Lock()
	blocks := mock.endpointBlocks[serviceID]
	mock.mu.Unlock()

	assert.Equal(t, uint64(100), blocks[protocol.EndpointAddr("ep1")])
	assert.Equal(t, uint64(200), blocks[protocol.EndpointAddr("ep2")])
}

func TestCosmos_StartBackgroundSync_SyncsEndpointBlocks(t *testing.T) {
	qos := newTestCosmosQoS()
	mock := newMockReputationSvc()
	serviceID := qos.serviceQoSConfig.GetServiceID()

	localHeight := uint64(50)
	highLocalHeight := uint64(300)

	// Pre-populate local endpoint store with endpoints that have block heights
	qos.endpointStore.endpointsMu.Lock()
	qos.endpointStore.endpoints[protocol.EndpointAddr("ep1")] = endpoint{
		checkCometBFTStatus: endpointCheckCometBFTStatus{latestBlockHeight: &localHeight},
	}
	qos.endpointStore.endpoints[protocol.EndpointAddr("ep2")] = endpoint{
		checkCometBFTStatus: endpointCheckCometBFTStatus{latestBlockHeight: &highLocalHeight},
	}
	qos.endpointStore.endpointsMu.Unlock()

	// Pre-populate "Redis" with higher block height for ep1
	mock.mu.Lock()
	mock.endpointBlocks[serviceID] = map[protocol.EndpointAddr]uint64{
		protocol.EndpointAddr("ep1"): 500,
		protocol.EndpointAddr("ep2"): 200, // lower than local — should NOT overwrite
		protocol.EndpointAddr("ep3"): 400, // not in local store — should be ignored
	}
	mock.mu.Unlock()

	qos.SetReputationService(mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// StartBackgroundSync performs an immediate sync on startup
	qos.StartBackgroundSync(ctx, 50*time.Millisecond)

	// Verify: ep1 should be updated to 500, ep2 should stay at 300
	qos.endpointStore.endpointsMu.RLock()
	ep1 := qos.endpointStore.endpoints[protocol.EndpointAddr("ep1")]
	ep2 := qos.endpointStore.endpoints[protocol.EndpointAddr("ep2")]
	_, ep3Exists := qos.endpointStore.endpoints[protocol.EndpointAddr("ep3")]
	qos.endpointStore.endpointsMu.RUnlock()

	require.NotNil(t, ep1.checkCometBFTStatus.latestBlockHeight)
	assert.Equal(t, uint64(500), *ep1.checkCometBFTStatus.latestBlockHeight, "ep1 should be updated from Redis")
	require.NotNil(t, ep2.checkCometBFTStatus.latestBlockHeight)
	assert.Equal(t, uint64(300), *ep2.checkCometBFTStatus.latestBlockHeight, "ep2 should NOT be downgraded")
	assert.False(t, ep3Exists, "ep3 should not be created in local store")
}

func TestCosmos_ConsumeExternalBlockHeight_WritesToRedis(t *testing.T) {
	qos := newTestCosmosQoS()
	mock := newMockReputationSvc()
	qos.SetReputationService(mock)
	serviceID := qos.serviceQoSConfig.GetServiceID()

	// Set a non-zero perceived block so the external floor can be applied
	qos.serviceStateLock.Lock()
	qos.perceivedBlockNumber = 50
	qos.serviceStateLock.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	heights := make(chan int64, 5)
	// Use short grace period to skip waiting
	qos.ConsumeExternalBlockHeight(ctx, heights, 1*time.Millisecond)

	// Wait for grace period to elapse
	time.Sleep(10 * time.Millisecond)

	heights <- 300
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, uint64(300), qos.GetPerceivedBlockNumber())
	assert.Equal(t, uint64(300), mock.getBlock(serviceID))
}
