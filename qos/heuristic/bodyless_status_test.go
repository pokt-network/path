package heuristic

import (
	"testing"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/assert"
)

// An empty body is correct on a status defined to carry none. Without this exemption
// 204/205/304 were reported as empty_response — harmless while that signal was MINOR,
// but a CRITICAL reputation penalty for correct behaviour once empty_response is
// weighted as the protocol violation it is on a body-bearing status.
func TestAnalyze_EmptyBodyOnBodylessStatusIsNotAFault(t *testing.T) {
	for _, code := range []int{204, 205, 304} {
		t.Run(http(code), func(t *testing.T) {
			result := Analyze([]byte{}, code, sharedtypes.RPCType_JSON_RPC, "getSlot")

			assert.False(t, result.ShouldRetry, "HTTP %d carries no body; empty is correct", code)
			assert.Equal(t, "no_body_expected", result.Reason)
		})
	}
}

// The exemption must not swallow the real case: on a body-bearing 2xx an empty payload
// is still a fault, and it is the one this whole change exists to score correctly.
func TestAnalyze_EmptyBodyOnBodyBearingStatusIsStillAFault(t *testing.T) {
	for _, code := range []int{200, 201, 202} {
		t.Run(http(code), func(t *testing.T) {
			result := Analyze([]byte{}, code, sharedtypes.RPCType_JSON_RPC, "getTokenAccountsByOwner")

			assert.True(t, result.ShouldRetry, "HTTP %d promises a body; empty is a violation", code)
			assert.Equal(t, "empty_response", result.Reason)
		})
	}
}

// A populated body on a bodyless status is not something the exemption should hide —
// the guard is keyed on the body actually being empty, not on the status alone.
func TestAnalyze_BodylessStatusWithBodyIsNotExempted(t *testing.T) {
	result := Analyze([]byte(`{"jsonrpc":"2.0","result":1,"id":1}`), 204, sharedtypes.RPCType_JSON_RPC, "getSlot")
	assert.NotEqual(t, "no_body_expected", result.Reason, "exemption must require an empty body")
}

func http(code int) string {
	switch code {
	case 200:
		return "200_OK"
	case 201:
		return "201_Created"
	case 202:
		return "202_Accepted"
	case 204:
		return "204_NoContent"
	case 205:
		return "205_ResetContent"
	case 304:
		return "304_NotModified"
	}
	return "other"
}
