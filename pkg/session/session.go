package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// SessionState represents the state of a SUPL location session
type SessionState int

const (
	StateInit SessionState = iota
	StateWakeupSent
	StatePosInitReceived
	StateCompleted
	StateFailed
	StateExpired
)

func (s SessionState) String() string {
	switch s {
	case StateInit:
		return "INIT"
	case StateWakeupSent:
		return "WAKEUP_SENT"
	case StatePosInitReceived:
		return "POS_INIT_RECEIVED"
	case StateCompleted:
		return "COMPLETED"
	case StateFailed:
		return "FAILED"
	case StateExpired:
		return "EXPIRED"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", s)
	}
}

// QoP represents Quality of Position parameters
type QoP struct {
	Horacc      *int64 // Horizontal accuracy in meters
	Veracc      *int64 // Vertical accuracy in meters
	MaxLocAge   *int64 // Maximum location age in seconds
	Delay       *int64 // Max response delay in seconds
}

// LocationResult holds the resolved geographical coordinates
type LocationResult struct {
	LatitudeSign int     // 0: North, 1: South
	Latitude     float64 // Decimal degrees
	Longitude    float64 // Decimal degrees
	Altitude     float64 // Altitude in meters
	Uncertainty  float64 // Uncertainty in meters
	Confidence   int64   // Confidence percentage (0-100)
	Timestamp    time.Time
}

// LocationSession represents an active SUPL positioning session
type LocationSession struct {
	SessionID   [4]byte        // 4-byte random SLP Session ID
	State       SessionState   // Current lifecycle state
	TargetID    string         // MSISDN, IMSI, or GPSI of the target terminal
	QoP         QoP            // Requested Quality of Position
	VER         []byte         // 8-byte HMAC-SHA256-64 verification tag
	AuthKey     []byte         // Derived or shared authentication key
	SuplInitPdu []byte         // Raw UPER-encoded SUPL INIT message
	SetSessionID *int64        // Client SET Session ID received in SUPL POS INIT
	Result      *LocationResult // Final calculated position
	FailReason  string         // Error description if failed
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ExpiresAt   time.Time
}

// GenerateRandomSessionID generates a cryptographically random 4-byte Session ID
func GenerateRandomSessionID() ([4]byte, error) {
	var id [4]byte
	_, err := rand.Read(id[:])
	if err != nil {
		return id, fmt.Errorf("failed to generate random session id: %w", err)
	}
	return id, nil
}

// HexID returns the hex representation of the 4-byte session ID
func (s *LocationSession) HexID() string {
	return hex.EncodeToString(s.SessionID[:])
}

// IsExpired checks whether the session has expired
func (s *LocationSession) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

// ValidateTransition verifies whether a state transition is legally allowed
func (s *LocationSession) ValidateTransition(next SessionState) error {
	switch s.State {
	case StateInit:
		if next == StateWakeupSent || next == StateFailed || next == StateExpired {
			return nil
		}
	case StateWakeupSent:
		if next == StatePosInitReceived || next == StateFailed || next == StateExpired {
			return nil
		}
	case StatePosInitReceived:
		if next == StateCompleted || next == StateFailed || next == StateExpired {
			return nil
		}
	case StateCompleted, StateFailed, StateExpired:
		return fmt.Errorf("session already in terminal state %s, cannot transition to %s", s.State, next)
	}
	return fmt.Errorf("invalid state transition from %s to %s", s.State, next)
}

// SetState updates the session state and tracks update time
func (s *LocationSession) SetState(next SessionState) error {
	if err := s.ValidateTransition(next); err != nil {
		return err
	}
	s.State = next
	s.UpdatedAt = time.Now()
	return nil
}

// NewSession creates an initialized session with a random 4-byte ID and specified TTL
func NewSession(targetID string, ttl time.Duration, qop QoP) (*LocationSession, error) {
	id, err := GenerateRandomSessionID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return &LocationSession{
		SessionID: id,
		State:     StateInit,
		TargetID:  targetID,
		QoP:       qop,
		CreatedAt: now,
		UpdatedAt: now,
		ExpiresAt: now.Add(ttl),
	}, nil
}

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrSessionExpired  = errors.New("session expired")
)
