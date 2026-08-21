package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/pokt-network/path/config"
	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/health"
)

type fakeRequestSampleAdmin struct {
	report  gateway.RequestSampleReport
	found   bool
	lastSvc string
	lastWin bool
	lastTop int
}

func (f *fakeRequestSampleAdmin) Report(serviceID string, previous bool, top int) (gateway.RequestSampleReport, bool) {
	f.lastSvc, f.lastWin, f.lastTop = serviceID, previous, top
	return f.report, f.found
}
func (f *fakeRequestSampleAdmin) Summary() []gateway.RequestSampleSummary {
	return []gateway.RequestSampleSummary{{ServiceID: "solana", Sampled: 3}}
}

func newRouterWithSampleAdmin(t *testing.T, admin RequestSampleAdmin) *httptest.Server {
	t.Helper()
	ctrl := gomock.NewController(t)
	r := NewRouter(polyzero.NewLogger(), NewMockgatewayHandler(ctrl), NewMockdisqualifiedEndpointsReporter(ctrl),
		&health.Checker{}, config.RouterConfig{}, nil, nil, nil, nil, nil, admin)
	ts := httptest.NewServer(r.mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestRequestSampleEndpoint(t *testing.T) {
	t.Run("disabled sampler reports 503", func(t *testing.T) {
		ts := newRouterWithSampleAdmin(t, nil)
		resp, err := http.Get(ts.URL + "/admin/request-sample/solana")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	})

	t.Run("summary without service id", func(t *testing.T) {
		ts := newRouterWithSampleAdmin(t, &fakeRequestSampleAdmin{})
		resp, err := http.Get(ts.URL + "/admin/request-sample")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body struct {
			Services []gateway.RequestSampleSummary `json:"services"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Len(t, body.Services, 1)
		require.Equal(t, "solana", body.Services[0].ServiceID)
	})

	t.Run("report passes window and top through", func(t *testing.T) {
		admin := &fakeRequestSampleAdmin{found: true, report: gateway.RequestSampleReport{ServiceID: "solana", Sampled: 7, Uniqueness: 0.5}}
		ts := newRouterWithSampleAdmin(t, admin)
		resp, err := http.Get(ts.URL + "/admin/request-sample/solana?window=previous&top=5")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "solana", admin.lastSvc)
		require.True(t, admin.lastWin)
		require.Equal(t, 5, admin.lastTop)
		var got gateway.RequestSampleReport
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Equal(t, uint64(7), got.Sampled)
	})

	t.Run("unknown service is 404", func(t *testing.T) {
		ts := newRouterWithSampleAdmin(t, &fakeRequestSampleAdmin{found: false})
		resp, err := http.Get(ts.URL + "/admin/request-sample/nope")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("bad top is 400", func(t *testing.T) {
		ts := newRouterWithSampleAdmin(t, &fakeRequestSampleAdmin{found: true})
		resp, err := http.Get(ts.URL + "/admin/request-sample/solana?top=zero")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}
