package session_test

import (
	"context"
	"testing"
	"time"

	"slp/pkg/session"
)

func TestSessionIDGeneration(t *testing.T) {
	ids := make(map[[4]byte]bool)
	for i := 0; i < 1000; i++ {
		id, err := session.GenerateRandomSessionID()
		if err != nil {
			t.Fatalf("failed to generate session id: %v", err)
		}
		if len(id) != 4 {
			t.Fatalf("expected 4 bytes, got %d", len(id))
		}
		if ids[id] {
			t.Fatalf("duplicate session ID detected: %x", id)
		}
		ids[id] = true
	}
}

func TestSessionStateMachine(t *testing.T) {
	sess, err := session.NewSession("+84988111222", 30*time.Second, session.QoP{})
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	if sess.State != session.StateInit {
		t.Fatalf("initial state should be StateInit, got %v", sess.State)
	}

	// Legal transition: Init -> WakeupSent
	if err := sess.SetState(session.StateWakeupSent); err != nil {
		t.Fatalf("legal transition Init->WakeupSent failed: %v", err)
	}

	// Legal transition: WakeupSent -> PosInitReceived
	if err := sess.SetState(session.StatePosInitReceived); err != nil {
		t.Fatalf("legal transition WakeupSent->PosInitReceived failed: %v", err)
	}

	// Legal transition: PosInitReceived -> Completed
	if err := sess.SetState(session.StateCompleted); err != nil {
		t.Fatalf("legal transition PosInitReceived->Completed failed: %v", err)
	}

	// Illegal transition: Completed -> WakeupSent (terminal state)
	if err := sess.SetState(session.StateWakeupSent); err == nil {
		t.Fatal("expected error transitioning out of terminal state Completed")
	}
}

func TestInMemorySessionStore(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemorySessionStore(50 * time.Millisecond)
	defer store.Close()

	sess, err := session.NewSession("+84988333444", 2*time.Second, session.QoP{})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	sess.VER = []byte{1, 2, 3, 4, 5, 6, 7, 8}

	// Save
	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	// Get
	retrieved, err := store.GetSession(ctx, sess.SessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if retrieved.TargetID != sess.TargetID {
		t.Fatalf("target mismatch: got %s, want %s", retrieved.TargetID, sess.TargetID)
	}

	// Update
	if err := sess.SetState(session.StateWakeupSent); err != nil {
		t.Fatalf("SetState failed: %v", err)
	}
	if err := store.UpdateSession(ctx, sess); err != nil {
		t.Fatalf("UpdateSession failed: %v", err)
	}
	updated, err := store.GetSession(ctx, sess.SessionID)
	if err != nil || updated.State != session.StateWakeupSent {
		t.Fatalf("state update failed: %v", err)
	}

	// Delete
	if err := store.DeleteSession(ctx, sess.SessionID); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}
	_, err = store.GetSession(ctx, sess.SessionID)
	if err != session.ErrSessionNotFound {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestSessionExpirationAndCleanup(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemorySessionStore(20 * time.Millisecond)
	defer store.Close()

	expiredSignal := make(chan string, 1)
	store.SetOnExpireHandler(func(sess *session.LocationSession) {
		expiredSignal <- sess.TargetID
	})

	// Short TTL of 50ms
	sess, err := session.NewSession("+84988555666", 50*time.Millisecond, session.QoP{})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	// Wait for expiration
	select {
	case target := <-expiredSignal:
		if target != "+84988555666" {
			t.Fatalf("expired target mismatch: %s", target)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("session cleanup callback did not trigger in time")
	}

	// Check GetSession returns ErrSessionNotFound or ErrSessionExpired
	_, err = store.GetSession(ctx, sess.SessionID)
	if err != session.ErrSessionNotFound && err != session.ErrSessionExpired {
		t.Fatalf("expected session not found or expired, got %v", err)
	}
}

func TestAerospikeSessionStore(t *testing.T) {
	ctx := context.Background()
	driver := session.NewMockAerospikeDriver()
	store := session.NewAerospikeSessionStore(driver, "slp", "sessions")
	defer store.Close()

	sess, err := session.NewSession("+84988777888", 5*time.Second, session.QoP{})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	sess.VER = []byte{0xAA, 0xBB, 0xCC, 0xDD, 0x11, 0x22, 0x33, 0x44}
	sess.Result = &session.LocationResult{
		Latitude:    21.028511,
		Longitude:   105.804817,
		Altitude:    15.0,
		Uncertainty: 20.0,
		Confidence:  90,
	}

	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession to Aerospike failed: %v", err)
	}

	retrieved, err := store.GetSession(ctx, sess.SessionID)
	if err != nil {
		t.Fatalf("GetSession from Aerospike failed: %v", err)
	}

	if retrieved.TargetID != sess.TargetID {
		t.Fatalf("TargetID mismatch: got %s, want %s", retrieved.TargetID, sess.TargetID)
	}
	if retrieved.Result == nil || retrieved.Result.Latitude != 21.028511 {
		t.Fatalf("Result coordinates mismatch: %+v", retrieved.Result)
	}

	// Delete
	if err := store.DeleteSession(ctx, sess.SessionID); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}
	_, err = store.GetSession(ctx, sess.SessionID)
	if err != session.ErrSessionNotFound {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestSessionAdditionalCoverage(t *testing.T) {
	// 1. SessionState String
	states := []session.SessionState{
		session.StateInit,
		session.StateWakeupSent,
		session.StatePosInitReceived,
		session.StateCompleted,
		session.StateFailed,
		session.StateExpired,
		session.SessionState(99),
	}
	for _, st := range states {
		if st.String() == "" {
			t.Fatalf("expected non-empty string for state %d", st)
		}
	}

	// 2. HexID
	sess, _ := session.NewSession("+84988111222", 10*time.Second, session.QoP{})
	if sess.HexID() == "" {
		t.Fatal("expected non-empty HexID")
	}

	// 3. Aerospike expired save and scan
	driver := session.NewMockAerospikeDriver()
	store := session.NewAerospikeSessionStore(driver, "slp", "sessions")
	defer store.Close()

	ctx := context.Background()

	// Expired session save error
	expiredSess := *sess
	expiredSess.ExpiresAt = time.Now().Add(-1 * time.Second)
	if err := store.SaveSession(ctx, &expiredSess); err == nil {
		t.Fatal("expected error saving already expired session to Aerospike")
	}

	// Aerospike update session
	validSess, _ := session.NewSession("+84988000111", 5*time.Second, session.QoP{})
	_ = store.SaveSession(ctx, validSess)
	_ = validSess.SetState(session.StateWakeupSent)
	if err := store.UpdateSession(ctx, validSess); err != nil {
		t.Fatalf("Aerospike UpdateSession failed: %v", err)
	}

	// Aerospike cleanup call
	_, _ = store.CleanupExpired(ctx)

	// MockAerospikeDriver ScanAll
	scanned, err := driver.ScanAll(ctx, "slp", "sessions")
	if err != nil || len(scanned) == 0 {
		t.Fatalf("ScanAll failed: %v, count: %d", err, len(scanned))
	}
}
