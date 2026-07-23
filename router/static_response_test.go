package router

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"

	"github.com/pokt-network/path/config"
	"github.com/pokt-network/path/health"
	"github.com/pokt-network/path/request"
)

// stubStaticResolver is a test StaticResponseResolver driven by a function.
type stubStaticResolver struct {
	fn func(serviceID, path, method string) ([]byte, int, map[string]string, bool)
}

func (s stubStaticResolver) ResolveStaticResponse(serviceID, path, method string) ([]byte, int, map[string]string, bool) {
	return s.fn(serviceID, path, method)
}

func newStaticTestRouter(t *testing.T, resolver StaticResponseResolver) (*MockgatewayHandler, *httptest.Server) {
	ctrl := gomock.NewController(t)
	mockGateway := NewMockgatewayHandler(ctrl)
	mockDisq := NewMockdisqualifiedEndpointsReporter(ctrl)

	r := NewRouter(
		polyzero.NewLogger(),
		mockGateway,
		mockDisq,
		&health.Checker{},
		config.RouterConfig{},
		nil,
		nil,
		resolver,
	)
	ts := httptest.NewServer(r.mux)
	t.Cleanup(ts.Close)
	return mockGateway, ts
}

// Test_staticResponse_ServedOnMatch verifies a matching static route is served directly with
// its configured status/body/headers and the relay handler is never invoked.
func Test_staticResponse_ServedOnMatch(t *testing.T) {
	c := require.New(t)

	resolver := stubStaticResolver{
		fn: func(serviceID, path, method string) ([]byte, int, map[string]string, bool) {
			c.Equal("solana", serviceID)
			c.Equal("/example", path, "middleware must resolve on the cleaned path")
			return []byte("example-body"), http.StatusOK, map[string]string{
				"Content-Type": "text/plain; charset=utf-8",
				"X-Test":       "1",
			}, true
		},
	}
	// No gateway EXPECT: a static hit must never reach the relay handler.
	_, ts := newStaticTestRouter(t, resolver)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/example", nil)
	c.NoError(err)
	req.Header.Set(request.HTTPHeaderTargetServiceID, "solana")

	resp, err := (&http.Client{}).Do(req)
	c.NoError(err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	c.NoError(err)
	c.Equal(http.StatusOK, resp.StatusCode)
	c.Equal("example-body", string(body))
	c.Equal("1", resp.Header.Get("X-Test"))
	c.Equal("text/plain; charset=utf-8", resp.Header.Get("Content-Type"))
}

// Test_staticResponse_RelaysOnNoMatch verifies a non-matching path falls through to the
// relay handler unchanged.
func Test_staticResponse_RelaysOnNoMatch(t *testing.T) {
	c := require.New(t)

	resolver := stubStaticResolver{
		fn: func(serviceID, path, method string) ([]byte, int, map[string]string, bool) {
			return nil, 0, nil, false
		},
	}
	mockGateway, ts := newStaticTestRouter(t, resolver)
	mockGateway.EXPECT().HandleServiceRequest(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ *http.Request, w http.ResponseWriter) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("relayed"))
		},
	)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/not-static", nil)
	c.NoError(err)
	req.Header.Set(request.HTTPHeaderTargetServiceID, "solana")

	resp, err := (&http.Client{}).Do(req)
	c.NoError(err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	c.NoError(err)
	c.Equal("relayed", string(body))
}

// Test_staticResponse_WebSocketUpgradeBypasses verifies a WebSocket upgrade is never
// intercepted by a static route — it must reach the relay handler even when the path would
// otherwise match.
func Test_staticResponse_WebSocketUpgradeBypasses(t *testing.T) {
	c := require.New(t)

	resolver := stubStaticResolver{
		fn: func(serviceID, path, method string) ([]byte, int, map[string]string, bool) {
			t.Errorf("resolver must not be consulted for an upgrade request")
			return []byte("static"), http.StatusOK, nil, true
		},
	}
	mockGateway, ts := newStaticTestRouter(t, resolver)
	mockGateway.EXPECT().HandleServiceRequest(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ *http.Request, w http.ResponseWriter) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("relayed"))
		},
	)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/example", nil)
	c.NoError(err)
	req.Header.Set(request.HTTPHeaderTargetServiceID, "solana")
	req.Header.Set("Upgrade", "websocket")

	resp, err := (&http.Client{}).Do(req)
	c.NoError(err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	c.NoError(err)
	c.Equal("relayed", string(body))
}
