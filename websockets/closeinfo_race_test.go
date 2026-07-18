package websockets

import (
	"context"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/pokt-network/poktroll/pkg/polylog"
)

// Test_CloseInfo_DataRace proves that lastCloseCode/lastCloseText are accessed
// without synchronization: handleDisconnect (called concurrently from connLoop
// and pingLoop of the SAME connection) writes them, while GetCloseInfo (called
// from the bridge shutdown path) reads them. Run with -race.
func Test_CloseInfo_DataRace(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := &websocketConnection{
		logger:       polylog.Ctx(context.Background()),
		onDisconnect: func(error) { cancel() },
		source:       messageSourceEndpoint,
	}

	// A close error so handleDisconnect takes the write path (closeCode != 0).
	closeErr := &websocket.CloseError{Code: websocket.CloseNormalClosure, Text: "bye"}

	var wg sync.WaitGroup
	// Writer 1: simulates connLoop hitting a read error.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			c.handleDisconnect(closeErr)
		}
	}()
	// Writer 2: simulates pingLoop hitting a ping-write failure.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			c.handleDisconnect(closeErr)
		}
	}()
	// Reader: simulates bridge.shutdown → determineCloseCodeAndMessage → GetCloseInfo.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_, _ = c.GetCloseInfo()
		}
	}()
	wg.Wait()
}
