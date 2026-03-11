package types

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockExtractor implements DataExtractor for testing.
type mockExtractor struct {
	blockHeight    int64
	blockHeightErr error
	chainID        string
	chainIDErr     error
	syncing        bool
	syncingErr     error
	archival       bool
	archivalErr    error
	valid          bool
	validErr       error
}

func (m *mockExtractor) ExtractBlockHeight(_, _ []byte) (int64, error) {
	return m.blockHeight, m.blockHeightErr
}
func (m *mockExtractor) ExtractChainID(_, _ []byte) (string, error) {
	return m.chainID, m.chainIDErr
}
func (m *mockExtractor) IsSyncing(_, _ []byte) (bool, error) { return m.syncing, m.syncingErr }
func (m *mockExtractor) IsArchival(_, _ []byte) (bool, error) { return m.archival, m.archivalErr }
func (m *mockExtractor) IsValidResponse(_, _ []byte) (bool, error) { return m.valid, m.validErr }

func TestExtractAll_InvalidBlockHeight(t *testing.T) {
	t.Run("sets InvalidBlockHeight when extractor returns sentinel error", func(t *testing.T) {
		extractor := &mockExtractor{
			blockHeightErr: fmt.Errorf("%w: result is not a string", ErrInvalidBlockHeightResult),
			valid:          true,
		}

		ed := NewExtractedData("test-endpoint", 200, []byte(`{"result":[]}`), 0)
		ed.ExtractAll(extractor, []byte(`{"method":"eth_blockNumber"}`))

		assert.True(t, ed.InvalidBlockHeight, "InvalidBlockHeight should be true")
		assert.Equal(t, int64(0), ed.BlockHeight, "BlockHeight should be 0")
		assert.Contains(t, ed.ExtractionErrors, "block_height")
	})

	t.Run("does NOT set InvalidBlockHeight for normal errors", func(t *testing.T) {
		extractor := &mockExtractor{
			blockHeightErr: errors.New("not an eth_blockNumber request"),
			valid:          true,
		}

		ed := NewExtractedData("test-endpoint", 200, []byte(`{"result":"0x1234"}`), 0)
		ed.ExtractAll(extractor, []byte(`{"method":"eth_getBalance"}`))

		assert.False(t, ed.InvalidBlockHeight, "InvalidBlockHeight should be false for non-sentinel errors")
		assert.Contains(t, ed.ExtractionErrors, "block_height")
	})

	t.Run("does NOT set InvalidBlockHeight on success", func(t *testing.T) {
		extractor := &mockExtractor{
			blockHeight: 12345,
			valid:       true,
		}

		ed := NewExtractedData("test-endpoint", 200, []byte(`{"result":"0x3039"}`), 0)
		ed.ExtractAll(extractor, []byte(`{"method":"eth_blockNumber"}`))

		assert.False(t, ed.InvalidBlockHeight, "InvalidBlockHeight should be false on success")
		assert.Equal(t, int64(12345), ed.BlockHeight)
	})
}

func TestExtractWithConfig_InvalidBlockHeight(t *testing.T) {
	t.Run("sets InvalidBlockHeight when config enables block height extraction", func(t *testing.T) {
		extractor := &mockExtractor{
			blockHeightErr: fmt.Errorf("%w: result is empty", ErrInvalidBlockHeightResult),
		}

		ed := NewExtractedData("test-endpoint", 200, []byte(`{"result":[]}`), 0)
		config := ExtractionConfig{ExtractBlockHeight: true}
		ed.ExtractWithConfig(extractor, []byte(`{"method":"eth_blockNumber"}`), config)

		assert.True(t, ed.InvalidBlockHeight)
	})

	t.Run("does NOT set InvalidBlockHeight when block height extraction disabled", func(t *testing.T) {
		extractor := &mockExtractor{
			blockHeightErr: fmt.Errorf("%w: result is empty", ErrInvalidBlockHeightResult),
		}

		ed := NewExtractedData("test-endpoint", 200, []byte(`{"result":[]}`), 0)
		config := ExtractionConfig{ExtractBlockHeight: false}
		ed.ExtractWithConfig(extractor, []byte(`{"method":"eth_blockNumber"}`), config)

		assert.False(t, ed.InvalidBlockHeight, "should not set flag when extraction is disabled")
	})
}

func TestErrInvalidBlockHeightResult_Wrapping(t *testing.T) {
	t.Run("errors.Is works with wrapped error", func(t *testing.T) {
		wrapped := fmt.Errorf("%w: result is not a string", ErrInvalidBlockHeightResult)
		require.True(t, errors.Is(wrapped, ErrInvalidBlockHeightResult))
	})

	t.Run("errors.Is returns false for unrelated error", func(t *testing.T) {
		unrelated := errors.New("not an eth_blockNumber request")
		require.False(t, errors.Is(unrelated, ErrInvalidBlockHeightResult))
	})
}
