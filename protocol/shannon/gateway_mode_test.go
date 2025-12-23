package shannon

import (
	"context"
	"net/http"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	apptypes "github.com/pokt-network/poktroll/x/application/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/request"
)

// mockFullNodeForSessionMerging mocks the FullNode interface for testing session merging.
type mockFullNodeForSessionMerging struct {
	FullNode // Embed to satisfy interface (most methods won't be called)

	// Control behavior
	isInRollover            bool
	currentSession          sessiontypes.Session
	extendedSession         sessiontypes.Session
	getSessionError         error
	getExtendedSessionError error
}

func (m *mockFullNodeForSessionMerging) IsInSessionRollover() bool {
	return m.isInRollover
}

func (m *mockFullNodeForSessionMerging) GetSession(
	ctx context.Context,
	serviceID protocol.ServiceID,
	appAddr string,
) (sessiontypes.Session, error) {
	if m.getSessionError != nil {
		return sessiontypes.Session{}, m.getSessionError
	}
	return m.currentSession, nil
}

func (m *mockFullNodeForSessionMerging) GetSessionWithExtendedValidity(
	ctx context.Context,
	serviceID protocol.ServiceID,
	appAddr string,
) (sessiontypes.Session, error) {
	if m.getExtendedSessionError != nil {
		return sessiontypes.Session{}, m.getExtendedSessionError
	}
	return m.extendedSession, nil
}

// newTestProtocolForSessionMerging creates a minimal Protocol instance for testing session merging.
func newTestProtocolForSessionMerging(mockFullNode *mockFullNodeForSessionMerging, gatewayMode protocol.GatewayMode) *Protocol {
	logger := polyzero.NewLogger()

	return &Protocol{
		logger:      logger,
		FullNode:    mockFullNode,
		gatewayMode: gatewayMode,
		gatewayAddr: "pokt1gateway", // Test gateway address
		ownedApps: map[protocol.ServiceID][]string{
			"eth": {"pokt1abc123"}, // Single app for simpler testing
		},
	}
}

func TestCentralizedGatewayMode_SessionMerging_NormalOperation(t *testing.T) {
	// Setup: Normal operation (NOT in rollover)
	mockFullNode := &mockFullNodeForSessionMerging{
		isInRollover: false,
		currentSession: sessiontypes.Session{
			SessionId: "session-100",
			Header: &sessiontypes.SessionHeader{
				SessionStartBlockHeight: 1000,
				SessionEndBlockHeight:   1060,
			},
			Application: &apptypes.Application{
				Address:                   "pokt1abc123",
				DelegateeGatewayAddresses: []string{"pokt1gateway"},
			},
		},
	}

	p := newTestProtocolForSessionMerging(mockFullNode, protocol.GatewayModeCentralized)

	// Execute
	sessions, err := p.getCentralizedGatewayModeActiveSessions(context.Background(), "eth")

	// Verify: Should get exactly 1 session (one per owned app), no extended sessions
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	expectedSessionCount := 1 // 1 owned app
	if len(sessions) != expectedSessionCount {
		t.Errorf("Expected %d sessions (1 per owned app), got %d", expectedSessionCount, len(sessions))
	}

	// Verify no extended sessions were added
	for _, session := range sessions {
		if session.SessionId != "session-100" {
			t.Errorf("Expected only current session (session-100), got session with ID: %s", session.SessionId)
		}
	}
}

func TestCentralizedGatewayMode_SessionMerging_DuringRollover(t *testing.T) {
	// Setup: During rollover period
	// NEW BEHAVIOR: Returns ONLY ONE session per app (chosen by GetSessionWithExtendedValidity logic)
	// This prevents mixing endpoints from different sessions
	mockFullNode := &mockFullNodeForSessionMerging{
		isInRollover: true,
		currentSession: sessiontypes.Session{
			SessionId: "session-101",
			Header: &sessiontypes.SessionHeader{
				SessionStartBlockHeight: 1060,
				SessionEndBlockHeight:   1120,
			},
			Application: &apptypes.Application{
				Address:                   "pokt1abc123",
				DelegateeGatewayAddresses: []string{"pokt1gateway"},
			},
		},
		extendedSession: sessiontypes.Session{
			SessionId: "session-100", // Different from current - this will be chosen during grace period
			Header: &sessiontypes.SessionHeader{
				SessionStartBlockHeight: 1000,
				SessionEndBlockHeight:   1060,
			},
			Application: &apptypes.Application{
				Address:                   "pokt1abc123",
				DelegateeGatewayAddresses: []string{"pokt1gateway"},
			},
		},
	}

	p := newTestProtocolForSessionMerging(mockFullNode, protocol.GatewayModeCentralized)

	// Execute
	sessions, err := p.getCentralizedGatewayModeActiveSessions(context.Background(), "eth")

	// Verify: Should get ONLY 1 session (1 per owned app)
	// GetSessionWithExtendedValidity chooses which session to use
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	expectedSessionCount := 1 // 1 owned app = 1 session (no merging)
	if len(sessions) != expectedSessionCount {
		t.Errorf("Expected %d sessions during rollover (1 per app), got %d", expectedSessionCount, len(sessions))
	}

	// Verify we got the extended session (session-100)
	// GetSessionWithExtendedValidity should return the extended session during grace period
	if sessions[0].SessionId != "session-100" {
		t.Errorf("Expected extended session (session-100) during rollover, got: %s", sessions[0].SessionId)
	}
}

func TestCentralizedGatewayMode_SessionMerging_RolloverWithSameSessionID(t *testing.T) {
	// Setup: During rollover but extended session has same ID as current
	// This can happen at the very start of a new session
	mockFullNode := &mockFullNodeForSessionMerging{
		isInRollover: true,
		currentSession: sessiontypes.Session{
			SessionId: "session-100",
			Header: &sessiontypes.SessionHeader{
				SessionStartBlockHeight: 1000,
				SessionEndBlockHeight:   1060,
			},
			Application: &apptypes.Application{
				Address:                   "pokt1abc123",
				DelegateeGatewayAddresses: []string{"pokt1gateway"},
			},
		},
		extendedSession: sessiontypes.Session{
			SessionId: "session-100", // SAME as current - should be deduplicated
			Header: &sessiontypes.SessionHeader{
				SessionStartBlockHeight: 1000,
				SessionEndBlockHeight:   1060,
			},
			Application: &apptypes.Application{
				Address:                   "pokt1abc123",
				DelegateeGatewayAddresses: []string{"pokt1gateway"},
			},
		},
	}

	p := newTestProtocolForSessionMerging(mockFullNode, protocol.GatewayModeCentralized)

	// Execute
	sessions, err := p.getCentralizedGatewayModeActiveSessions(context.Background(), "eth")

	// Verify: Should only get 1 session (1 per owned app), extended not added due to same ID
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	expectedSessionCount := 1 // Only current session, extended deduplicated
	if len(sessions) != expectedSessionCount {
		t.Errorf("Expected %d sessions (extended deduplicated), got %d", expectedSessionCount, len(sessions))
	}

	// Verify all sessions have the same ID
	for _, session := range sessions {
		if session.SessionId != "session-100" {
			t.Errorf("Expected only session-100, got: %s", session.SessionId)
		}
	}
}

func TestCentralizedGatewayMode_SessionMerging_ExtendedSessionError(t *testing.T) {
	// Setup: During rollover but getting extended session fails
	mockFullNode := &mockFullNodeForSessionMerging{
		isInRollover: true,
		currentSession: sessiontypes.Session{
			SessionId: "session-101",
			Header: &sessiontypes.SessionHeader{
				SessionStartBlockHeight: 1060,
				SessionEndBlockHeight:   1120,
			},
			Application: &apptypes.Application{
				Address:                   "pokt1abc123",
				DelegateeGatewayAddresses: []string{"pokt1gateway"},
			},
		},
		getExtendedSessionError: errProtocolContextSetupFetchSession, // Simulates error
	}

	p := newTestProtocolForSessionMerging(mockFullNode, protocol.GatewayModeCentralized)

	// Execute
	sessions, err := p.getCentralizedGatewayModeActiveSessions(context.Background(), "eth")

	// Verify: Should succeed with only current session (graceful degradation)
	if err != nil {
		t.Fatalf("Expected no error (graceful degradation), got: %v", err)
	}

	expectedSessionCount := 1 // Only current session, extended failed
	if len(sessions) != expectedSessionCount {
		t.Errorf("Expected %d sessions (extended fetch failed), got %d", expectedSessionCount, len(sessions))
	}

	// Verify we only have current sessions
	for _, session := range sessions {
		if session.SessionId != "session-101" {
			t.Errorf("Expected only current session (session-101), got: %s", session.SessionId)
		}
	}
}

func TestDelegatedGatewayMode_SessionMerging_NormalOperation(t *testing.T) {
	// Setup: Normal operation (NOT in rollover)
	mockFullNode := &mockFullNodeForSessionMerging{
		isInRollover: false,
		currentSession: sessiontypes.Session{
			SessionId: "session-100",
			Header: &sessiontypes.SessionHeader{
				SessionStartBlockHeight: 1000,
				SessionEndBlockHeight:   1060,
			},
			Application: &apptypes.Application{
				Address:                   "pokt1userapp",
				DelegateeGatewayAddresses: []string{"pokt1gateway"},
				ServiceConfigs: []*sharedtypes.ApplicationServiceConfig{
					{ServiceId: "eth"},
				},
			},
		},
	}

	p := newTestProtocolForSessionMerging(mockFullNode, protocol.GatewayModeDelegated)

	// Create HTTP request with app address header
	req := &http.Request{
		Header: http.Header{
			request.HTTPHeaderAppAddress: []string{"pokt1userapp"},
		},
	}

	// Execute
	sessions, err := p.getDelegatedGatewayModeActiveSession(context.Background(), "eth", req)

	// Verify: Should get exactly 1 session
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session, got %d", len(sessions))
	}

	if sessions[0].SessionId != "session-100" {
		t.Errorf("Expected session-100, got: %s", sessions[0].SessionId)
	}
}

func TestDelegatedGatewayMode_SessionMerging_DuringRollover(t *testing.T) {
	// Setup: During rollover period
	mockFullNode := &mockFullNodeForSessionMerging{
		isInRollover: true,
		currentSession: sessiontypes.Session{
			SessionId: "session-101",
			Header: &sessiontypes.SessionHeader{
				SessionStartBlockHeight: 1060,
				SessionEndBlockHeight:   1120,
			},
			Application: &apptypes.Application{
				Address:                   "pokt1userapp",
				DelegateeGatewayAddresses: []string{"pokt1gateway"},
				ServiceConfigs: []*sharedtypes.ApplicationServiceConfig{
					{ServiceId: "eth"},
				},
			},
		},
		extendedSession: sessiontypes.Session{
			SessionId: "session-100", // Different from current
			Header: &sessiontypes.SessionHeader{
				SessionStartBlockHeight: 1000,
				SessionEndBlockHeight:   1060,
			},
			Application: &apptypes.Application{
				Address:                   "pokt1userapp",
				DelegateeGatewayAddresses: []string{"pokt1gateway"},
				ServiceConfigs: []*sharedtypes.ApplicationServiceConfig{
					{ServiceId: "eth"},
				},
			},
		},
	}

	p := newTestProtocolForSessionMerging(mockFullNode, protocol.GatewayModeDelegated)

	// Create HTTP request with app address header
	req := &http.Request{
		Header: http.Header{
			request.HTTPHeaderAppAddress: []string{"pokt1userapp"},
		},
	}

	// Execute
	sessions, err := p.getDelegatedGatewayModeActiveSession(context.Background(), "eth", req)

	// Verify: Should get 2 sessions (current + extended)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if len(sessions) != 2 {
		t.Fatalf("Expected 2 sessions during rollover, got %d", len(sessions))
	}

	// Verify we have both sessions
	sessionIDs := make(map[string]bool)
	for _, session := range sessions {
		sessionIDs[session.SessionId] = true
	}

	if !sessionIDs["session-101"] {
		t.Errorf("Expected current session (session-101) to be present")
	}
	if !sessionIDs["session-100"] {
		t.Errorf("Expected extended session (session-100) to be present")
	}
}

func TestDelegatedGatewayMode_SessionMerging_RolloverWithSameSessionID(t *testing.T) {
	// Setup: During rollover but extended has same ID as current
	mockFullNode := &mockFullNodeForSessionMerging{
		isInRollover: true,
		currentSession: sessiontypes.Session{
			SessionId: "session-100",
			Header: &sessiontypes.SessionHeader{
				SessionStartBlockHeight: 1000,
				SessionEndBlockHeight:   1060,
			},
			Application: &apptypes.Application{
				Address:                   "pokt1userapp",
				DelegateeGatewayAddresses: []string{"pokt1gateway"},
				ServiceConfigs: []*sharedtypes.ApplicationServiceConfig{
					{ServiceId: "eth"},
				},
			},
		},
		extendedSession: sessiontypes.Session{
			SessionId: "session-100", // SAME - should be deduplicated
			Header: &sessiontypes.SessionHeader{
				SessionStartBlockHeight: 1000,
				SessionEndBlockHeight:   1060,
			},
			Application: &apptypes.Application{
				Address:                   "pokt1userapp",
				DelegateeGatewayAddresses: []string{"pokt1gateway"},
				ServiceConfigs: []*sharedtypes.ApplicationServiceConfig{
					{ServiceId: "eth"},
				},
			},
		},
	}

	p := newTestProtocolForSessionMerging(mockFullNode, protocol.GatewayModeDelegated)

	// Create HTTP request with app address header
	req := &http.Request{
		Header: http.Header{
			request.HTTPHeaderAppAddress: []string{"pokt1userapp"},
		},
	}

	// Execute
	sessions, err := p.getDelegatedGatewayModeActiveSession(context.Background(), "eth", req)

	// Verify: Should only get 1 session (extended deduplicated)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if len(sessions) != 1 {
		t.Errorf("Expected 1 session (extended deduplicated), got %d", len(sessions))
	}

	if sessions[0].SessionId != "session-100" {
		t.Errorf("Expected session-100, got: %s", sessions[0].SessionId)
	}
}

func TestDelegatedGatewayMode_SessionMerging_ExtendedSessionError(t *testing.T) {
	// Setup: During rollover but getting extended session fails
	mockFullNode := &mockFullNodeForSessionMerging{
		isInRollover: true,
		currentSession: sessiontypes.Session{
			SessionId: "session-101",
			Header: &sessiontypes.SessionHeader{
				SessionStartBlockHeight: 1060,
				SessionEndBlockHeight:   1120,
			},
			Application: &apptypes.Application{
				Address:                   "pokt1userapp",
				DelegateeGatewayAddresses: []string{"pokt1gateway"},
				ServiceConfigs: []*sharedtypes.ApplicationServiceConfig{
					{ServiceId: "eth"},
				},
			},
		},
		getExtendedSessionError: errProtocolContextSetupFetchSession, // Simulates error
	}

	p := newTestProtocolForSessionMerging(mockFullNode, protocol.GatewayModeDelegated)

	// Create HTTP request with app address header
	req := &http.Request{
		Header: http.Header{
			request.HTTPHeaderAppAddress: []string{"pokt1userapp"},
		},
	}

	// Execute
	sessions, err := p.getDelegatedGatewayModeActiveSession(context.Background(), "eth", req)

	// Verify: Should succeed with only current session (graceful degradation)
	if err != nil {
		t.Fatalf("Expected no error (graceful degradation), got: %v", err)
	}

	if len(sessions) != 1 {
		t.Errorf("Expected 1 session (extended fetch failed), got %d", len(sessions))
	}

	if sessions[0].SessionId != "session-101" {
		t.Errorf("Expected current session (session-101), got: %s", sessions[0].SessionId)
	}
}
