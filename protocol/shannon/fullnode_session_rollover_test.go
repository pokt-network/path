package shannon

import (
	"context"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	sdk "github.com/pokt-network/shannon-sdk"
)

// newMockSessionRolloverState creates a minimal sessionRolloverState for testing
func newMockSessionRolloverState() *sessionRolloverState {
	logger := polyzero.NewLogger()

	// Create a mock block client - we can use nil since the tests don't actually call block height methods
	// For tests that need block height functionality, we'll set up the rollover state manually
	var mockBlockClient *sdk.BlockClient = nil

	// Use a test RPC URL (won't be used in tests that don't actually connect)
	const testRpcURL = "http://localhost:26657"

	// Use the default rollover blocks value for testing
	const testSessionRolloverBlocks = 24

	return newSessionRolloverState(context.Background(), logger, mockBlockClient, testRpcURL, testSessionRolloverBlocks)
}

func Test_getSessionRolloverState(t *testing.T) {
	tests := []struct {
		name           string
		initialState   bool
		expectedResult bool
	}{
		{
			name:           "returns true when in rollover",
			initialState:   true,
			expectedResult: true,
		},
		{
			name:           "returns false when not in rollover",
			initialState:   false,
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srs := newMockSessionRolloverState()
			srs.isInSessionRollover = tt.initialState

			result := srs.getSessionRolloverState()

			if result != tt.expectedResult {
				t.Errorf("getSessionRolloverState() = %v, want %v", result, tt.expectedResult)
			}
		})
	}
}

func Test_updateSessionRolloverBoundaries(t *testing.T) {
	tests := []struct {
		name                   string
		session                sessiontypes.Session
		initialRolloverStart   int64
		initialRolloverEnd     int64
		initialBlockHeight     int64
		expectedRolloverStart  int64
		expectedRolloverEnd    int64
		expectedInRollover     bool
		shouldReturn           bool // whether function should return early
		shouldUpdateBoundaries bool // whether rollover boundaries should be updated
	}{
		{
			name: "nil session header returns early",
			session: sessiontypes.Session{
				Header: nil,
			},
			initialRolloverStart:   100,
			initialRolloverEnd:     170,
			initialBlockHeight:     105,
			expectedRolloverStart:  100, // unchanged
			expectedRolloverEnd:    170, // unchanged
			expectedInRollover:     false,
			shouldReturn:           true,
			shouldUpdateBoundaries: false,
		},
		{
			name: "first session - boundaries not set yet",
			session: sessiontypes.Session{
				Header: &sessiontypes.SessionHeader{
					SessionStartBlockHeight: 100,
					SessionEndBlockHeight:   160,
				},
			},
			initialRolloverStart:   0, // not set yet
			initialRolloverEnd:     0, // not set yet
			initialBlockHeight:     105,
			expectedRolloverStart:  159,   // 160 - 1
			expectedRolloverEnd:    184,   // 160 + 24
			expectedInRollover:     false, // 105 is not in [159, 184]
			shouldReturn:           false,
			shouldUpdateBoundaries: true,
		},
		{
			name: "rollover period ended - update boundaries",
			session: sessiontypes.Session{
				Header: &sessiontypes.SessionHeader{
					SessionStartBlockHeight: 200,
					SessionEndBlockHeight:   260,
				},
			},
			initialRolloverStart:   159, // previous session
			initialRolloverEnd:     184, // previous session
			initialBlockHeight:     190, // past previous rollover end
			expectedRolloverStart:  259, // 260 - 1
			expectedRolloverEnd:    284, // 260 + 24
			expectedInRollover:     false,
			shouldReturn:           false,
			shouldUpdateBoundaries: true,
		},
		{
			name: "rollover period active - don't update boundaries",
			session: sessiontypes.Session{
				Header: &sessiontypes.SessionHeader{
					SessionStartBlockHeight: 200,
					SessionEndBlockHeight:   260,
				},
			},
			initialRolloverStart:   159, // current session
			initialRolloverEnd:     184, // current session
			initialBlockHeight:     165, // within current rollover window
			expectedRolloverStart:  159, // unchanged
			expectedRolloverEnd:    184, // unchanged
			expectedInRollover:     true,
			shouldReturn:           false,
			shouldUpdateBoundaries: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srs := newMockSessionRolloverState()
			srs.sessionRolloverStart = tt.initialRolloverStart
			srs.sessionRolloverEnd = tt.initialRolloverEnd
			srs.currentBlockHeight = tt.initialBlockHeight

			srs.updateSessionRolloverBoundaries(tt.session)

			if tt.shouldReturn && (srs.sessionRolloverStart != tt.expectedRolloverStart || srs.sessionRolloverEnd != tt.expectedRolloverEnd) {
				t.Errorf("updateSessionRolloverBoundaries() should have returned early, but state changed")
				return
			}

			if srs.sessionRolloverStart != tt.expectedRolloverStart {
				t.Errorf("sessionRolloverStart = %v, want %v", srs.sessionRolloverStart, tt.expectedRolloverStart)
			}

			if srs.sessionRolloverEnd != tt.expectedRolloverEnd {
				t.Errorf("sessionRolloverEnd = %v, want %v", srs.sessionRolloverEnd, tt.expectedRolloverEnd)
			}

			if !tt.shouldReturn && srs.isInSessionRollover != tt.expectedInRollover {
				t.Errorf("isInSessionRollover = %v, want %v", srs.isInSessionRollover, tt.expectedInRollover)
			}
		})
	}
}

func Test_calculateRolloverStatus(t *testing.T) {
	tests := []struct {
		name               string
		currentBlockHeight int64
		rolloverStart      int64
		rolloverEnd        int64
		expected           bool
	}{
		{
			name:               "no block height returns false",
			currentBlockHeight: 0,
			rolloverStart:      159,
			rolloverEnd:        184,
			expected:           false,
		},
		{
			name:               "no rollover start returns false",
			currentBlockHeight: 165,
			rolloverStart:      0,
			rolloverEnd:        184,
			expected:           false,
		},
		{
			name:               "no rollover end returns false",
			currentBlockHeight: 165,
			rolloverStart:      159,
			rolloverEnd:        0,
			expected:           false,
		},
		{
			name:               "block height within rollover window",
			currentBlockHeight: 165,
			rolloverStart:      159, // session end height - 1
			rolloverEnd:        184, // session end height + grace period
			expected:           true,
		},
		{
			name:               "block height at rollover start",
			currentBlockHeight: 159,
			rolloverStart:      159,
			rolloverEnd:        184,
			expected:           true,
		},
		{
			name:               "block height at rollover end",
			currentBlockHeight: 184,
			rolloverStart:      159,
			rolloverEnd:        184,
			expected:           true,
		},
		{
			name:               "block height before rollover window",
			currentBlockHeight: 158,
			rolloverStart:      159,
			rolloverEnd:        184,
			expected:           false,
		},
		{
			name:               "block height after rollover window",
			currentBlockHeight: 185,
			rolloverStart:      159,
			rolloverEnd:        184,
			expected:           false,
		},
		{
			name:               "large block heights",
			currentBlockHeight: 1000005,
			rolloverStart:      999999,
			rolloverEnd:        1000010,
			expected:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srs := newMockSessionRolloverState()
			srs.currentBlockHeight = tt.currentBlockHeight
			srs.sessionRolloverStart = tt.rolloverStart
			srs.sessionRolloverEnd = tt.rolloverEnd

			result := srs.calculateRolloverStatus()

			if result != tt.expected {
				t.Errorf("calculateRolloverStatus() = %v, want %v", result, tt.expected)
			}
		})
	}
}
