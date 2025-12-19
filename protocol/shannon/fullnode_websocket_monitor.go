package shannon

import (
	"context"
	"fmt"
	"time"

	"github.com/cometbft/cometbft/rpc/client/http"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cometbft/cometbft/types"
	"github.com/pokt-network/poktroll/pkg/polylog"
)

const (
	// subscriberID is the identifier for this WebSocket subscriber
	subscriberID = "path-block-monitor"

	// newBlockEventQuery is the CometBFT query for new block events
	newBlockEventQuery = "tm.event='NewBlock'"

	// wsReconnectDelay is how long to wait before attempting to reconnect WebSocket
	wsReconnectDelay = 5 * time.Second

	// wsEventChannelCapacity is the buffer size for the event channel
	wsEventChannelCapacity = 10
)

// blockHeightMonitor manages WebSocket subscriptions for block height updates.
//
// It subscribes to CometBFT NewBlock events via WebSocket for instant block height
// updates. If WebSocket fails or disconnects, it falls back to polling.
type blockHeightMonitor struct {
	logger       polylog.Logger
	ctx          context.Context
	rpcURL       string
	wsClient     *http.HTTP
	heightChan   chan int64
	errorChan    chan error
	stopPolling  chan struct{}
	usingPolling bool
}

// newBlockHeightMonitor creates a new block height monitor with WebSocket support.
func newBlockHeightMonitor(ctx context.Context, logger polylog.Logger, rpcURL string) *blockHeightMonitor {
	return &blockHeightMonitor{
		logger:      logger.With("component", "block_height_monitor"),
		ctx:         ctx,
		rpcURL:      rpcURL,
		heightChan:  make(chan int64, wsEventChannelCapacity),
		errorChan:   make(chan error, 1),
		stopPolling: make(chan struct{}, 1),
	}
}

// start begins monitoring block heights via WebSocket.
// If WebSocket fails, it automatically falls back to polling.
func (m *blockHeightMonitor) start() {
	m.logger.Info().Str("rpc_url", m.rpcURL).Msg("Starting WebSocket block height monitor")

	// Try to establish WebSocket connection
	if err := m.connectWebSocket(); err != nil {
		m.logger.Warn().Err(err).Msg("Failed to connect WebSocket, falling back to polling")
		m.startPolling()
		return
	}

	// Start WebSocket event listener
	go m.listenWebSocketEvents()

	// Start reconnection handler
	go m.handleReconnection()
}

// connectWebSocket establishes a WebSocket connection and subscribes to NewBlock events.
func (m *blockHeightMonitor) connectWebSocket() error {
	// Create CometBFT HTTP client with WebSocket support
	// The wsEndpoint is derived from the RPC URL
	client, err := http.New(m.rpcURL, "/websocket")
	if err != nil {
		return fmt.Errorf("failed to create CometBFT client: %w", err)
	}

	// Start the WebSocket client
	if err := client.Start(); err != nil {
		return fmt.Errorf("failed to start WebSocket client: %w", err)
	}

	m.wsClient = client
	m.usingPolling = false

	m.logger.Info().Msg("WebSocket connection established")
	return nil
}

// listenWebSocketEvents subscribes to NewBlock events and forwards block heights.
func (m *blockHeightMonitor) listenWebSocketEvents() {
	// Subscribe to NewBlock events
	eventChan, err := m.wsClient.Subscribe(
		m.ctx,
		subscriberID,
		newBlockEventQuery,
		wsEventChannelCapacity,
	)
	if err != nil {
		m.logger.Error().Err(err).Msg("Failed to subscribe to NewBlock events")
		m.errorChan <- err
		return
	}

	m.logger.Info().Msg("Subscribed to NewBlock events via WebSocket")

	for {
		select {
		case <-m.ctx.Done():
			m.logger.Info().Msg("Context canceled, stopping WebSocket listener")
			m.cleanup()
			return

		case event, ok := <-eventChan:
			if !ok {
				m.logger.Warn().Msg("WebSocket event channel closed")
				m.errorChan <- fmt.Errorf("websocket event channel closed")
				return
			}

			// Extract block height from the event
			height, err := m.extractBlockHeight(event)
			if err != nil {
				m.logger.Error().Err(err).Msg("Failed to extract block height from event")
				continue
			}

			// Forward the block height
			select {
			case m.heightChan <- height:
				m.logger.Debug().Int64("height", height).Msg("Block height updated from WebSocket")
			case <-m.ctx.Done():
				return
			default:
				// Channel full, skip this update (not critical)
				m.logger.Warn().Int64("height", height).Msg("Height channel full, dropping update")
			}
		}
	}
}

// extractBlockHeight extracts the block height from a NewBlock event.
func (m *blockHeightMonitor) extractBlockHeight(event ctypes.ResultEvent) (int64, error) {
	eventDataNewBlock, ok := event.Data.(types.EventDataNewBlock)
	if !ok {
		return 0, fmt.Errorf("unexpected event data type: %T", event.Data)
	}

	if eventDataNewBlock.Block == nil {
		return 0, fmt.Errorf("block is nil in event")
	}

	return eventDataNewBlock.Block.Height, nil
}

// handleReconnection monitors for WebSocket errors and handles reconnection.
func (m *blockHeightMonitor) handleReconnection() {
	for {
		select {
		case <-m.ctx.Done():
			return

		case err := <-m.errorChan:
			m.logger.Warn().Err(err).Msg("WebSocket error detected, attempting to recover")

			// Clean up existing connection
			m.cleanup()

			// Fall back to polling
			m.startPolling()

			// Attempt to reconnect WebSocket periodically
			go m.attemptReconnect()
		}
	}
}

// attemptReconnect tries to re-establish WebSocket connection.
func (m *blockHeightMonitor) attemptReconnect() {
	ticker := time.NewTicker(wsReconnectDelay)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return

		case <-ticker.C:
			m.logger.Debug().Msg("Attempting to reconnect WebSocket")

			if err := m.connectWebSocket(); err != nil {
				m.logger.Warn().Err(err).Msg("WebSocket reconnection failed, will retry")
				continue
			}

			// Reconnection successful - stop polling and restart WebSocket listener
			m.logger.Info().Msg("WebSocket reconnected successfully")
			m.stopPolling <- struct{}{}
			go m.listenWebSocketEvents()
			return
		}
	}
}

// startPolling begins polling for block height updates as a fallback.
func (m *blockHeightMonitor) startPolling() {
	if m.usingPolling {
		return // Already polling
	}

	m.usingPolling = true
	m.logger.Info().Msg("Switched to polling mode for block height updates")

	go m.pollingLoop()
}

// pollingLoop periodically polls for block height updates.
func (m *blockHeightMonitor) pollingLoop() {
	ticker := time.NewTicker(blockCheckInterval)
	defer ticker.Stop()

	// Create a simple HTTP client for polling
	httpClient, err := http.New(m.rpcURL, "")
	if err != nil {
		m.logger.Error().Err(err).Msg("Failed to create polling HTTP client")
		return
	}

	if err := httpClient.Start(); err != nil {
		m.logger.Error().Err(err).Msg("Failed to start polling HTTP client")
		return
	}
	defer func() {
		if err := httpClient.Stop(); err != nil {
			m.logger.Error().Err(err).Msg("Failed to stop polling HTTP client")
		}
	}()

	for {
		select {
		case <-m.ctx.Done():
			return

		case <-m.stopPolling:
			m.logger.Info().Msg("Stopping polling mode")
			m.usingPolling = false
			return

		case <-ticker.C:
			status, err := httpClient.Status(m.ctx)
			if err != nil {
				m.logger.Error().Err(err).Msg("Failed to get node status during polling")
				continue
			}

			if status.SyncInfo.LatestBlockHeight > 0 {
				select {
				case m.heightChan <- status.SyncInfo.LatestBlockHeight:
					m.logger.Debug().
						Int64("height", status.SyncInfo.LatestBlockHeight).
						Msg("Block height updated from polling")
				case <-m.ctx.Done():
					return
				default:
					// Channel full, skip
				}
			}
		}
	}
}

// cleanup closes the WebSocket client and unsubscribes.
func (m *blockHeightMonitor) cleanup() {
	if m.wsClient == nil {
		return
	}

	// Unsubscribe from all events
	if err := m.wsClient.UnsubscribeAll(context.Background(), subscriberID); err != nil {
		m.logger.Warn().Err(err).Msg("Failed to unsubscribe from events")
	}

	// Stop the WebSocket client
	if err := m.wsClient.Stop(); err != nil {
		m.logger.Warn().Err(err).Msg("Failed to stop WebSocket client")
	}

	m.wsClient = nil
}
