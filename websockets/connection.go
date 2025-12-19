package websockets

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pokt-network/poktroll/pkg/polylog"
)

// TODO_IMPROVE(@commoddity): Make all of these configurable
// TODO_CONFIG: Make Websocket timeouts configurable
// Current: Hardcoded timeouts in websockets/connection.go:15-24
// Suggestion: Move to configuration file with sensible defaults
const (
	// Time allowed to write a message to the peer over the websocket connection
	writeWaitDuration = 10 * time.Second

	// Time allowed to read the next pong message from the peer over the websocket connection
	pongWaitDuration = 30 * time.Second

	// Send pings to peer with this period over the websocket connection
	// Must be greater than pongWaitDuration
	pingPeriodDuration = (pongWaitDuration * 9) / 10
)

// messageSource is used to identify the source of a message in a bidirectional websocket connection.
// Possible values are `client` and `endpoint`.
//
// Full data flow: Client <------> PATH <------> Websocket Endpoint
type messageSource string

const (
	messageSourceClient   messageSource = "client"
	messageSourceEndpoint messageSource = "endpoint"
)

// message represents a websocket message that can be:
// - Client request
// - Endpoint response
// - Subscription push event (e.g. eth_subscribe)
type message struct {
	// data is the message payload
	data []byte

	// source may be either `client` or `endpoint`
	source messageSource

	// messageType is an int returned by the gorilla/websocket package
	messageType int
}

// websocketConnection represents a websocket connection between PATH and:
// - A client
// - An endpoint
type websocketConnection struct {
	*websocket.Conn

	ctx       context.Context
	cancelCtx context.CancelFunc

	logger polylog.Logger

	source  messageSource
	msgChan chan<- message
}

// upgradeClientWebsocketConnection upgrades an HTTP connection to a Websocket.
// Used to upgrade a Client's HTTP request to a Websocket connection.
//
// DEV_NOTE: This function uses a permissive CheckOrigin policy (always returns true),
// eliminating origin-based rejections as a potential cause of upgrade failures.
//
// See: https://pkg.go.dev/github.com/gorilla/websocket#hdr-Overview
func upgradeClientWebsocketConnection(
	wsLogger polylog.Logger,
	req *http.Request,
	w http.ResponseWriter,
) (*websocket.Conn, error) {
	upgrader := websocket.Upgrader{
		// Allow all origins.
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	// Upgrade the HTTP connection to a Websocket connection.
	clientConn, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		// Upgrade errors are often client-side protocol violations.
		// But, they can also indicate server resource issues or network problems.
		// The specific error message will help distinguish between client and server-side causes.
		wsLogger.Error().Err(err).Msg("Error upgrading websocket connection request")
		return nil, err
	}

	return clientConn, nil
}

// ConnectWebsocketEndpoint makes a websocket connection to the websocket Endpoint.
func ConnectWebsocketEndpoint(
	wsLogger polylog.Logger,
	websocketURL string,
	headers http.Header,
) (*websocket.Conn, error) {
	wsLogger.Info().Msgf("Connecting to websocket endpoint: %s", websocketURL)

	// Ensure the websocket URL is valid.
	url, err := url.Parse(websocketURL)
	if err != nil {
		wsLogger.Error().Err(err).Msgf("❌ Error parsing endpoint URL: %s", websocketURL)
		return nil, err
	}

	// Connect to the websocket endpoint using the default websocket dialer.
	conn, _, err := websocket.DefaultDialer.Dial(url.String(), headers)
	if err != nil {
		wsLogger.Error().Err(err).Msgf("❌ Error connecting to endpoint: %s", url.String())
		return nil, err
	}

	wsLogger.Debug().Msgf("Connected to websocket endpoint: %s", websocketURL)

	return conn, nil
}

// newConnection creates a new websocket connection wrapper.
func newConnection(
	ctx context.Context,
	cancelCtx context.CancelFunc,
	logger polylog.Logger,
	conn *websocket.Conn,
	source messageSource,
	msgChan chan message,
) *websocketConnection {
	c := &websocketConnection{
		ctx:       ctx,
		cancelCtx: cancelCtx,

		logger: logger.With("connection", source),

		Conn: conn,

		source:  source,
		msgChan: msgChan,
	}

	go c.connLoop()
	go c.pingLoop()

	return c
}

// connLoop reads messages from the websocket connection and sends them to the bridge's msgChan.
// Network-level read errors trigger handleDisconnect() for coordinated bridge shutdown.
func (c *websocketConnection) connLoop() {
	for {
		messageType, msg, err := c.ReadMessage()
		if err != nil {
			c.handleDisconnect(err) // Network read failure → async bridge shutdown
			return
		}

		c.msgChan <- message{
			data:        msg,
			source:      c.source,
			messageType: messageType,
		}
	}
}

// handleDisconnect handles network-level connection failures.
//
// Usage:
// - Network-level errors: Connection drops, read failures, ping/pong timeouts
// - External disconnections: When remote peer closes the connection
// - Health check failures: When ping messages fail to send
//
// Mechanism:
// 1. Cancels the context (cancelCtx) shared with the bridge
// 2. Bridge's context listener detects cancellation and calls bridge.shutdown()
// 3. This provides async, coordinated shutdown signaling between connections and bridge
//
// Note: This is for network transport failures, not application-level message processing errors.
// TODO_FUTURE(#408): Revisit how we handle connection failures.
func (c *websocketConnection) handleDisconnect(err error) {
	c.logger.Warn().Err(err).Msgf("🔌 Handling websocket disconnection")
	c.cancelCtx() // Cancel the context to signal the bridge to handle shutdown
}

// pingLoop sends keep-alive ping messages to the connection and handles pong messages
// This loop is used to keep the connection alive and functions by sending a ping message
// to the connection and waiting for a pong response. If a pong response is not received,
// the connection is considered dead and the stopChan is closed.
// See: https://pkg.go.dev/github.com/gorilla/websocket#hdr-Control_Messages
func (c *websocketConnection) pingLoop() {
	ticker := time.NewTicker(pingPeriodDuration)
	defer ticker.Stop()

	if err := c.SetReadDeadline(time.Now().Add(pongWaitDuration)); err != nil {
		c.logger.Error().Err(err).Msg("failed to set initial read deadline")
	}

	c.SetPongHandler(func(string) error {
		if err := c.SetReadDeadline(time.Now().Add(pongWaitDuration)); err != nil {
			c.logger.Error().Err(err).Msg("failed to set pong handler read deadline")
		}
		return nil
	})

	for {
		select {
		case <-ticker.C:
			if err := c.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWaitDuration)); err != nil {
				c.logger.Error().Err(err).Msg("failed to send ping to connection")
				c.handleDisconnect(fmt.Errorf("failed to send ping: %w", err)) // Health check failure → async bridge shutdown
				return
			}

		case <-c.ctx.Done():
			c.logger.Debug().Msg("pingLoop stopped due to context cancellation")
			return
		}
	}
}
