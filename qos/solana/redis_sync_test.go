package solana

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	qostypes "github.com/pokt-network/path/qos/types"
	"github.com/pokt-network/path/reputation"
)

// mockReputationSvc is a minimal mock that only implements the methods used by
// the Redis sync logic (SetPerceivedBlockNumber, GetPerceivedBlockNumber,
// SetEndpointBlockHeight, and GetEndpointBlockHeights).
// All other ReputationService methods panic so any unexpected call is caught.
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

const testSolanaServiceID = protocol.ServiceID("solana-test")

func newTestSolanaQoS() *QoS {
	logger := polyzero.NewLogger()
	return &QoS{
		logger: logger,
		EndpointStore: &EndpointStore{
			logger:    logger,
			endpoints: make(map[protocol.EndpointAddr]endpoint),
		},
		ServiceState: &ServiceState{
			logger:    logger,
			serviceID: testSolanaServiceID,
		},
		requestValidator: &requestValidator{
			logger:    logger,
			serviceID: testSolanaServiceID,
		},
	}
}

func TestSolana_SetReputationService(t *testing.T) {
	qos := newTestSolanaQoS()
	require.Nil(t, qos.reputationSvc)

	mock := newMockReputationSvc()
	qos.SetReputationService(mock)
	require.NotNil(t, qos.reputationSvc)
}

func TestSolana_StartBackgroundSync_UpdatesFromRedis(t *testing.T) {
	qos := newTestSolanaQoS()
	mock := newMockReputationSvc()

	// Pre-populate Redis with a higher block
	mock.setBlock(testSolanaServiceID, 500)

	qos.SetReputationService(mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// StartBackgroundSync performs an immediate sync on startup
	qos.StartBackgroundSync(ctx, 50*time.Millisecond)

	// The immediate sync should have picked up the Redis value
	assert.Equal(t, uint64(500), qos.GetPerceivedBlockNumber())

	// Now raise Redis and wait for a tick
	mock.setBlock(testSolanaServiceID, 700)
	time.Sleep(150 * time.Millisecond)

	assert.Equal(t, uint64(700), qos.GetPerceivedBlockNumber())
}

func TestSolana_StartBackgroundSync_NilReputationSvc(t *testing.T) {
	qos := newTestSolanaQoS()
	// Should not panic; just returns early
	qos.StartBackgroundSync(context.Background(), time.Second)
}

func TestSolana_UpdateFromExtractedData_WritesToRedis(t *testing.T) {
	qos := newTestSolanaQoS()
	mock := newMockReputationSvc()
	qos.SetReputationService(mock)

	err := qos.UpdateFromExtractedData(
		protocol.EndpointAddr("ep1"),
		&qostypes.ExtractedData{BlockHeight: 100},
	)
	require.NoError(t, err)
	assert.Equal(t, uint64(100), qos.GetPerceivedBlockNumber())

	// Give async goroutine time to write to Redis
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, uint64(100), mock.getBlock(testSolanaServiceID))

	// Second call with higher block should also write
	err = qos.UpdateFromExtractedData(
		protocol.EndpointAddr("ep2"),
		&qostypes.ExtractedData{BlockHeight: 200},
	)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, uint64(200), mock.getBlock(testSolanaServiceID))

	// Lower block should NOT write
	err = qos.UpdateFromExtractedData(
		protocol.EndpointAddr("ep3"),
		&qostypes.ExtractedData{BlockHeight: 150},
	)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, uint64(200), mock.getBlock(testSolanaServiceID))
}

func TestSolana_UpdateFromExtractedData_WritesEndpointBlockToRedis(t *testing.T) {
	qos := newTestSolanaQoS()
	mock := newMockReputationSvc()
	qos.SetReputationService(mock)

	// Write two endpoints via UpdateFromExtractedData
	err := qos.UpdateFromExtractedData(protocol.EndpointAddr("ep1"), &qostypes.ExtractedData{BlockHeight: 100})
	require.NoError(t, err)
	err = qos.UpdateFromExtractedData(protocol.EndpointAddr("ep2"), &qostypes.ExtractedData{BlockHeight: 200})
	require.NoError(t, err)

	// Give async goroutines time to write
	time.Sleep(100 * time.Millisecond)

	mock.mu.Lock()
	blocks := mock.endpointBlocks[testSolanaServiceID]
	mock.mu.Unlock()

	assert.Equal(t, uint64(100), blocks[protocol.EndpointAddr("ep1")])
	assert.Equal(t, uint64(200), blocks[protocol.EndpointAddr("ep2")])
}

func TestSolana_StartBackgroundSync_SyncsEndpointBlocks(t *testing.T) {
	qos := newTestSolanaQoS()
	mock := newMockReputationSvc()

	// Pre-populate local endpoint store with endpoints that have epoch info
	qos.endpointsMu.Lock()
	qos.endpoints[protocol.EndpointAddr("ep1")] = endpoint{
		SolanaGetEpochInfoResponse: &qosobservations.SolanaGetEpochInfoResponse{BlockHeight: 50},
	}
	qos.endpoints[protocol.EndpointAddr("ep2")] = endpoint{
		SolanaGetEpochInfoResponse: &qosobservations.SolanaGetEpochInfoResponse{BlockHeight: 300},
	}
	// ep_no_epoch has no SolanaGetEpochInfoResponse — should be skipped
	qos.endpoints[protocol.EndpointAddr("ep_no_epoch")] = endpoint{}
	qos.endpointsMu.Unlock()

	// Pre-populate "Redis" with higher block height for ep1
	mock.mu.Lock()
	mock.endpointBlocks[testSolanaServiceID] = map[protocol.EndpointAddr]uint64{
		protocol.EndpointAddr("ep1"):       500,
		protocol.EndpointAddr("ep2"):       200, // lower than local — should NOT overwrite
		protocol.EndpointAddr("ep3"):       400, // not in local store — should be ignored
		protocol.EndpointAddr("ep_no_epoch"): 600, // no epoch info — should be skipped
	}
	mock.mu.Unlock()

	qos.SetReputationService(mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// StartBackgroundSync performs an immediate sync on startup
	qos.StartBackgroundSync(ctx, 50*time.Millisecond)

	// Verify: ep1 should be updated to 500, ep2 should stay at 300
	qos.endpointsMu.RLock()
	ep1 := qos.endpoints[protocol.EndpointAddr("ep1")]
	ep2 := qos.endpoints[protocol.EndpointAddr("ep2")]
	epNoEpoch := qos.endpoints[protocol.EndpointAddr("ep_no_epoch")]
	_, ep3Exists := qos.endpoints[protocol.EndpointAddr("ep3")]
	qos.endpointsMu.RUnlock()

	assert.Equal(t, uint64(500), ep1.BlockHeight, "ep1 should be updated from Redis")
	assert.Equal(t, uint64(300), ep2.BlockHeight, "ep2 should NOT be downgraded")
	assert.Nil(t, epNoEpoch.SolanaGetEpochInfoResponse, "ep_no_epoch should not have epoch info created")
	assert.False(t, ep3Exists, "ep3 should not be created in local store")
}

func TestSolana_ConsumeExternalBlockHeight_WritesToRedis(t *testing.T) {
	qos := newTestSolanaQoS()
	mock := newMockReputationSvc()
	qos.SetReputationService(mock)

	// Set a non-zero perceived block so the external floor can be applied
	qos.serviceStateLock.Lock()
	qos.perceivedBlockHeight = 50
	qos.serviceStateLock.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	heights := make(chan int64, 5)
	// Use 0 grace period to skip waiting
	qos.ConsumeExternalBlockHeight(ctx, heights, 1*time.Millisecond)

	// Wait for grace period to elapse
	time.Sleep(10 * time.Millisecond)

	heights <- 300
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, uint64(300), qos.GetPerceivedBlockNumber())
	assert.Equal(t, uint64(300), mock.getBlock(testSolanaServiceID))
}
