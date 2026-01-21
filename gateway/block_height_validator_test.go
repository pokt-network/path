package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
)

func TestBlockHeightValidator_ValidateBlockHeight(t *testing.T) {
	logger := polyzero.NewLogger()
	cache := NewExternalReferenceCache(logger)
	validator := NewBlockHeightValidator(logger, cache)

	ctx := context.Background()
	serviceID := protocol.ServiceID("test-service")

	// Helper to create a JSON-RPC response body
	rpcResp := func(height interface{}) []byte {
		var val string
		switch v := height.(type) {
		case int:
			val = fmt.Sprintf("0x%x", v)
		case string:
			val = v
		}
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","result":"%s","id":1}`, val))
	}

	tests := []struct {
		name          string
		responseBody  []byte
		validation    *BlockHeightValidation
		expectError   bool
		expectedError string
	}{
		{
			name:         "Static: Success (>=)",
			responseBody: rpcResp(1000),
			validation: &BlockHeightValidation{
				Operator: BlockHeightOperatorGreaterThanOrEqual,
				Reference: BlockHeightReference{
					Type:  BlockHeightReferenceTypeStatic,
					Value: 1000,
				},
			},
			expectError: false,
		},
		{
			name:         "Static: Fail (>=) with tolerance",
			responseBody: rpcResp(900),
			validation: &BlockHeightValidation{
				Operator: BlockHeightOperatorGreaterThanOrEqual,
				Reference: BlockHeightReference{
					Type:      BlockHeightReferenceTypeStatic,
					Value:     1000,
					Tolerance: 50, // Effective threshold: 950
				},
			},
			expectError:   true,
			expectedError: "block height validation failed",
		},
		{
			name:         "Static: Success (>=) within tolerance",
			responseBody: rpcResp(960),
			validation: &BlockHeightValidation{
				Operator: BlockHeightOperatorGreaterThanOrEqual,
				Reference: BlockHeightReference{
					Type:      BlockHeightReferenceTypeStatic,
					Value:     1000,
					Tolerance: 50, // Effective threshold: 950
				},
			},
			expectError: false,
		},
		{
			name:         "Static: Equal Operator (Strict)",
			responseBody: rpcResp(1000),
			validation: &BlockHeightValidation{
				Operator: BlockHeightOperatorEqual,
				Reference: BlockHeightReference{
					Type:  BlockHeightReferenceTypeStatic,
					Value: 1000,
				},
			},
			expectError: false,
		},
		{
			name:         "Static: Equal Operator (Range Success)",
			responseBody: rpcResp(1005),
			validation: &BlockHeightValidation{
				Operator: BlockHeightOperatorEqual,
				Reference: BlockHeightReference{
					Type:      BlockHeightReferenceTypeStatic,
					Value:     1000,
					Tolerance: 5,
				},
			},
			expectError: false,
		},
		{
			name:         "Static: Equal Operator (Range Fail)",
			responseBody: rpcResp(1006),
			validation: &BlockHeightValidation{
				Operator: BlockHeightOperatorEqual,
				Reference: BlockHeightReference{
					Type:      BlockHeightReferenceTypeStatic,
					Value:     1000,
					Tolerance: 5,
				},
			},
			expectError:   true,
			expectedError: "block height validation failed",
		},
		{
			name:         "Cosmos Format: Success",
			responseBody: []byte(`{"result":{"sync_info":{"latest_block_height":"12345"}}}`),
			validation: &BlockHeightValidation{
				Operator: BlockHeightOperatorGreaterThanOrEqual,
				Reference: BlockHeightReference{
					Type:  BlockHeightReferenceTypeStatic,
					Value: 12000,
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.ValidateBlockHeight(ctx, serviceID, tt.responseBody, tt.validation)
			if tt.expectError {
				assert.Error(t, err)
				if tt.expectedError != "" {
					assert.Contains(t, err.Error(), tt.expectedError)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestBlockHeightValidator_ExternalReference(t *testing.T) {
	logger := polyzero.NewLogger()

	// Mock External Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","result":"0x400","id":1}`) // 1024
	}))
	defer server.Close()

	cache := NewExternalReferenceCache(logger)
	validator := NewBlockHeightValidator(logger, cache)

	ctx := context.Background()
	serviceID := protocol.ServiceID("test-external")

	validation := &BlockHeightValidation{
		Operator: BlockHeightOperatorGreaterThanOrEqual,
		Reference: BlockHeightReference{
			Type:          BlockHeightReferenceTypeExternal,
			Endpoint:      server.URL,
			Method:        "eth_blockNumber",
			Tolerance:     10,
			CacheDuration: 5 * time.Second,
			Timeout:       1 * time.Second,
		},
	}

	// 1. Endpoint at 1020, Reference at 1024, Tolerance 10 -> Should Pass (1020 >= 1014)
	err := validator.ValidateBlockHeight(ctx, serviceID, []byte(`{"result":"0x3fc"}`), validation)
	assert.NoError(t, err)

	// 2. Endpoint at 1010, Reference at 1024, Tolerance 10 -> Should Fail (1010 < 1014)
	err = validator.ValidateBlockHeight(ctx, serviceID, []byte(`{"result":"0x3f2"}`), validation)
	assert.Error(t, err)
}

func TestBlockHeightValidator_CompareHeightsWithTolerance(t *testing.T) {
	v := &BlockHeightValidator{}

	tests := []struct {
		endpoint  int64
		reference int64
		tolerance int64
		operator  BlockHeightOperator
		expected  bool
	}{
		// Greater Than or Equal
		{100, 105, 10, BlockHeightOperatorGreaterThanOrEqual, true}, // 100 >= 95
		{90, 105, 10, BlockHeightOperatorGreaterThanOrEqual, false}, // 90 < 95
		// Equal (Range check)
		{100, 100, 5, BlockHeightOperatorEqual, true},
		{105, 100, 5, BlockHeightOperatorEqual, true},
		{95, 100, 5, BlockHeightOperatorEqual, true},
		{106, 100, 5, BlockHeightOperatorEqual, false},
		{94, 100, 5, BlockHeightOperatorEqual, false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d %s %d (tol %d)", tt.endpoint, tt.operator, tt.reference, tt.tolerance), func(t *testing.T) {
			res := v.compareHeightsWithTolerance(tt.endpoint, tt.reference, tt.tolerance, tt.operator)
			assert.Equal(t, tt.expected, res)
		})
	}
}
