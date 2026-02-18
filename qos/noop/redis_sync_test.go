package noop

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
// the Redis sync logic (SetPerceivedBlockNumber and GetPerceivedBlockNumber).
type mockReputationSvc struct {
	reputation.ReputationService // embed to satisfy interface; unused methods will panic

	mu             sync.Mutex
	perceivedBlock map[protocol.ServiceID]uint64
}

func newMockReputationSvc() *mockReputationSvc {
	return &mockReputationSvc{
		perceivedBlock: make(map[protocol.ServiceID]uint64),
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

func newTestNoOpQoS() *NoOpQoS {
	logger := polyzero.NewLogger()
	return NewNoOpQoSService(logger, "noop-test")
}

func TestNoOp_SetReputationService(t *testing.T) {
	qos := newTestNoOpQoS()
	require.Nil(t, qos.reputationSvc)

	mock := newMockReputationSvc()
	qos.SetReputationService(mock)
	require.NotNil(t, qos.reputationSvc)
}

func TestNoOp_StartBackgroundSync_UpdatesFromRedis(t *testing.T) {
	qos := newTestNoOpQoS()
	mock := newMockReputationSvc()

	// Pre-populate Redis with a higher block
	mock.setBlock(qos.serviceID, 500)

	qos.SetReputationService(mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// StartBackgroundSync performs an immediate sync on startup
	qos.StartBackgroundSync(ctx, 50*time.Millisecond)

	// The immediate sync should have picked up the Redis value
	assert.Equal(t, uint64(500), qos.GetPerceivedBlockNumber())

	// Now raise Redis and wait for a tick
	mock.setBlock(qos.serviceID, 700)
	time.Sleep(150 * time.Millisecond)

	assert.Equal(t, uint64(700), qos.GetPerceivedBlockNumber())
}

func TestNoOp_StartBackgroundSync_NilReputationSvc(t *testing.T) {
	qos := newTestNoOpQoS()
	// Should not panic; just returns early
	qos.StartBackgroundSync(context.Background(), time.Second)
}

func TestNoOp_UpdateFromExtractedData_WritesToRedis(t *testing.T) {
	qos := newTestNoOpQoS()
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
	assert.Equal(t, uint64(100), mock.getBlock(qos.serviceID))

	// Second call with higher block should also write
	err = qos.UpdateFromExtractedData(
		protocol.EndpointAddr("ep2"),
		&qostypes.ExtractedData{BlockHeight: 200},
	)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, uint64(200), mock.getBlock(qos.serviceID))

	// Lower block should NOT write
	err = qos.UpdateFromExtractedData(
		protocol.EndpointAddr("ep3"),
		&qostypes.ExtractedData{BlockHeight: 150},
	)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, uint64(200), mock.getBlock(qos.serviceID))
}

func TestNoOp_ConsumeExternalBlockHeight_WritesToRedis(t *testing.T) {
	qos := newTestNoOpQoS()
	mock := newMockReputationSvc()
	qos.SetReputationService(mock)

	// Set a non-zero perceived block so the external floor can be applied
	qos.serviceStateMu.Lock()
	qos.perceivedBlockHeight = 50
	qos.serviceStateMu.Unlock()

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
	assert.Equal(t, uint64(300), mock.getBlock(qos.serviceID))
}
