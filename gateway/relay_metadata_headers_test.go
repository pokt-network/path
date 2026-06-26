package gateway

import (
	"net/http/httptest"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"

	"github.com/pokt-network/path/protocol"
)

func TestAddRelayMetadataHeaders_SingleRelayFastPath(t *testing.T) {
	rc := &requestContext{
		logger: polyzero.NewLogger(polyzero.WithLevel(polyzero.ParseLevel("warn"))),
		relayMetadata: []protocol.RelayMetadata{
			{AppAddress: "app1", SupplierAddress: "sup1", SessionID: "sess1"},
		},
	}

	w := httptest.NewRecorder()
	rc.addRelayMetadataHeaders(w)

	if got := w.Header().Get("X-App-Address"); got != "app1" {
		t.Fatalf("X-App-Address = %q, want %q", got, "app1")
	}
	if got := w.Header().Get("X-Supplier-Address"); got != "sup1" {
		t.Fatalf("X-Supplier-Address = %q, want %q", got, "sup1")
	}
	if got := w.Header().Get("X-Session-ID"); got != "sess1" {
		t.Fatalf("X-Session-ID = %q, want %q", got, "sess1")
	}
}

func TestAddRelayMetadataHeaders_MultiRelayDedup(t *testing.T) {
	rc := &requestContext{
		logger: polyzero.NewLogger(polyzero.WithLevel(polyzero.ParseLevel("warn"))),
		relayMetadata: []protocol.RelayMetadata{
			{AppAddress: "app1", SupplierAddress: "sup1", SessionID: "sess1"},
			{AppAddress: "app1", SupplierAddress: "sup2", SessionID: "sess1"},
		},
	}

	w := httptest.NewRecorder()
	rc.addRelayMetadataHeaders(w)

	// app + session are duplicated -> single value; suppliers differ -> joined.
	if got := w.Header().Get("X-App-Address"); got != "app1" {
		t.Fatalf("X-App-Address = %q, want %q", got, "app1")
	}
	if got := w.Header().Get("X-Supplier-Address"); got != "sup1,sup2" {
		t.Fatalf("X-Supplier-Address = %q, want %q", got, "sup1,sup2")
	}
	if got := w.Header().Get("X-Session-ID"); got != "sess1" {
		t.Fatalf("X-Session-ID = %q, want %q", got, "sess1")
	}
}

func TestAddRelayMetadataHeaders_NoMetadata(t *testing.T) {
	rc := &requestContext{
		logger: polyzero.NewLogger(polyzero.WithLevel(polyzero.ParseLevel("warn"))),
	}
	w := httptest.NewRecorder()
	rc.addRelayMetadataHeaders(w)

	if got := w.Header().Get("X-Supplier-Address"); got != "" {
		t.Fatalf("expected no X-Supplier-Address, got %q", got)
	}
}
