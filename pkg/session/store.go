package session

import (
	"context"
	"sync"
	"time"
)

// SessionStore defines the storage interface for SUPL location sessions
type SessionStore interface {
	SaveSession(ctx context.Context, session *LocationSession) error
	GetSession(ctx context.Context, sessionID [4]byte) (*LocationSession, error)
	UpdateSession(ctx context.Context, session *LocationSession) error
	DeleteSession(ctx context.Context, sessionID [4]byte) error
	CleanupExpired(ctx context.Context) (int, error)
	Close() error
}

// InMemorySessionStore implements SessionStore in memory with background cleanup
type InMemorySessionStore struct {
	mu           sync.RWMutex
	sessions     map[[4]byte]*LocationSession
	stopCleaner  chan struct{}
	cleanerDone  chan struct{}
	onExpireFunc func(s *LocationSession)
}

// NewInMemorySessionStore creates a new in-memory session store with active TTL cleanup
func NewInMemorySessionStore(cleanupInterval time.Duration) *InMemorySessionStore {
	if cleanupInterval <= 0 {
		cleanupInterval = 1 * time.Second
	}

	store := &InMemorySessionStore{
		sessions:    make(map[[4]byte]*LocationSession),
		stopCleaner: make(chan struct{}),
		cleanerDone: make(chan struct{}),
	}

	go store.runCleaner(cleanupInterval)
	return store
}

// SetOnExpireHandler registers a hook when a session expires during cleanup
func (s *InMemorySessionStore) SetOnExpireHandler(fn func(sess *LocationSession)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onExpireFunc = fn
}

func (s *InMemorySessionStore) runCleaner(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer func() {
		ticker.Stop()
		close(s.cleanerDone)
	}()

	for {
		select {
		case <-ticker.C:
			_, _ = s.CleanupExpired(context.Background())
		case <-s.stopCleaner:
			return
		}
	}
}

// SaveSession persists a new session
func (s *InMemorySessionStore) SaveSession(ctx context.Context, sess *LocationSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clone to avoid data race
	copied := *sess
	s.sessions[sess.SessionID] = &copied
	return nil
}

// GetSession retrieves an active session by its 4-byte ID
func (s *InMemorySessionStore) GetSession(ctx context.Context, sessionID [4]byte) (*LocationSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}

	if sess.IsExpired() {
		_ = sess.SetState(StateExpired)
		return nil, ErrSessionExpired
	}

	copied := *sess
	return &copied, nil
}

// UpdateSession updates an existing session
func (s *InMemorySessionStore) UpdateSession(ctx context.Context, sess *LocationSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[sess.SessionID]; !ok {
		return ErrSessionNotFound
	}

	copied := *sess
	copied.UpdatedAt = time.Now()
	s.sessions[sess.SessionID] = &copied
	return nil
}

// DeleteSession removes a session
func (s *InMemorySessionStore) DeleteSession(ctx context.Context, sessionID [4]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.sessions, sessionID)
	return nil
}

// CleanupExpired cleans up expired sessions and triggers callbacks
func (s *InMemorySessionStore) CleanupExpired(ctx context.Context) (int, error) {
	s.mu.Lock()
	var expiredList []*LocationSession
	now := time.Now()

	for id, sess := range s.sessions {
		if now.After(sess.ExpiresAt) {
			_ = sess.SetState(StateExpired)
			expiredList = append(expiredList, sess)
			delete(s.sessions, id)
		}
	}
	handler := s.onExpireFunc
	s.mu.Unlock()

	if handler != nil {
		for _, exp := range expiredList {
			handler(exp)
		}
	}

	return len(expiredList), nil
}

// Close stops the background cleaner
func (s *InMemorySessionStore) Close() error {
	select {
	case <-s.stopCleaner:
		// already closed
		return nil
	default:
		close(s.stopCleaner)
		<-s.cleanerDone
		return nil
	}
}
