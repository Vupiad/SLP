package session

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Aerospike Bin names
const (
	BinSessionID  = "sess_id"
	BinState      = "state"
	BinTargetID   = "target_id"
	BinVER        = "ver"
	BinAuthKey    = "auth_key"
	BinSuplInit   = "supl_init"
	BinSetSessID  = "set_sess_id"
	BinCreatedAt  = "created_at"
	BinUpdatedAt  = "updated_at"
	BinExpiresAt  = "expires_at"
	BinLat        = "lat"
	BinLon        = "lon"
	BinAlt        = "alt"
	BinUncertain  = "unc"
	BinConfidence = "conf"
)

// AerospikeRecord models an Aerospike record with bins and expiration
type AerospikeRecord struct {
	Bins       map[string]any
	Generation uint32
	Expiration uint32 // seconds
}

// AerospikeClient defines the methods required from the Aerospike Go driver
type AerospikeClient interface {
	PutBins(ctx context.Context, namespace, set, key string, bins map[string]any, ttl time.Duration) error
	Get(ctx context.Context, namespace, set, key string) (*AerospikeRecord, error)
	Delete(ctx context.Context, namespace, set, key string) (bool, error)
	ScanAll(ctx context.Context, namespace, set string) (map[string]*AerospikeRecord, error)
	Close()
}

// MockAerospikeDriver implements AerospikeClient in memory for local execution and unit tests
type MockAerospikeDriver struct {
	mu      sync.RWMutex
	records map[string]*AerospikeRecord
}

// NewMockAerospikeDriver creates a mock Aerospike database client
func NewMockAerospikeDriver() *MockAerospikeDriver {
	return &MockAerospikeDriver{
		records: make(map[string]*AerospikeRecord),
	}
}

func recordKey(ns, set, key string) string {
	return fmt.Sprintf("%s:%s:%s", ns, set, key)
}

func (m *MockAerospikeDriver) PutBins(ctx context.Context, namespace, set, key string, bins map[string]any, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	k := recordKey(namespace, set, key)
	rec, ok := m.records[k]
	if !ok {
		rec = &AerospikeRecord{
			Bins:       make(map[string]any),
			Generation: 0,
		}
		m.records[k] = rec
	}

	rec.Generation++
	rec.Expiration = uint32(time.Now().Add(ttl).Unix())
	for binName, val := range bins {
		rec.Bins[binName] = val
	}
	return nil
}

func (m *MockAerospikeDriver) Get(ctx context.Context, namespace, set, key string) (*AerospikeRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	k := recordKey(namespace, set, key)
	rec, ok := m.records[k]
	if !ok {
		return nil, ErrSessionNotFound
	}
	// Check expiration
	if rec.Expiration > 0 && uint32(time.Now().Unix()) > rec.Expiration {
		return nil, ErrSessionExpired
	}

	// Copy bins
	copied := &AerospikeRecord{
		Bins:       make(map[string]any),
		Generation: rec.Generation,
		Expiration: rec.Expiration,
	}
	for k, v := range rec.Bins {
		copied.Bins[k] = v
	}
	return copied, nil
}

func (m *MockAerospikeDriver) Delete(ctx context.Context, namespace, set, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	k := recordKey(namespace, set, key)
	if _, ok := m.records[k]; ok {
		delete(m.records, k)
		return true, nil
	}
	return false, nil
}

func (m *MockAerospikeDriver) ScanAll(ctx context.Context, namespace, set string) (map[string]*AerospikeRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res := make(map[string]*AerospikeRecord)
	prefix := fmt.Sprintf("%s:%s:", namespace, set)
	now := uint32(time.Now().Unix())

	for k, rec := range m.records {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			if rec.Expiration > 0 && now > rec.Expiration {
				continue
			}
			keyPart := k[len(prefix):]
			res[keyPart] = rec
		}
	}
	return res, nil
}

func (m *MockAerospikeDriver) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = make(map[string]*AerospikeRecord)
}

// AerospikeSessionStore implements SessionStore backed by an Aerospike cluster
type AerospikeSessionStore struct {
	client    AerospikeClient
	namespace string
	set       string
}

// NewAerospikeSessionStore initializes a new Aerospike-backed session store
func NewAerospikeSessionStore(client AerospikeClient, namespace, set string) *AerospikeSessionStore {
	if namespace == "" {
		namespace = "slp"
	}
	if set == "" {
		set = "sessions"
	}
	return &AerospikeSessionStore{
		client:    client,
		namespace: namespace,
		set:       set,
	}
}

func (a *AerospikeSessionStore) sessionKey(id [4]byte) string {
	return hex.EncodeToString(id[:])
}

// SaveSession writes a session into Aerospike
func (a *AerospikeSessionStore) SaveSession(ctx context.Context, sess *LocationSession) error {
	ttl := time.Until(sess.ExpiresAt)
	if ttl <= 0 {
		return errors.New("cannot save already expired session")
	}

	bins := map[string]any{
		BinSessionID: sess.SessionID[:],
		BinState:     int(sess.State),
		BinTargetID:  sess.TargetID,
		BinVER:       sess.VER,
		BinAuthKey:   sess.AuthKey,
		BinSuplInit:  sess.SuplInitPdu,
		BinCreatedAt: sess.CreatedAt.UnixNano(),
		BinUpdatedAt: sess.UpdatedAt.UnixNano(),
		BinExpiresAt: sess.ExpiresAt.UnixNano(),
	}

	if sess.SetSessionID != nil {
		bins[BinSetSessID] = *sess.SetSessionID
	}
	if sess.Result != nil {
		bins[BinLat] = sess.Result.Latitude
		bins[BinLon] = sess.Result.Longitude
		bins[BinAlt] = sess.Result.Altitude
		bins[BinUncertain] = sess.Result.Uncertainty
		bins[BinConfidence] = sess.Result.Confidence
	}

	return a.client.PutBins(ctx, a.namespace, a.set, a.sessionKey(sess.SessionID), bins, ttl)
}

// GetSession reads a session from Aerospike
func (a *AerospikeSessionStore) GetSession(ctx context.Context, sessionID [4]byte) (*LocationSession, error) {
	rec, err := a.client.Get(ctx, a.namespace, a.set, a.sessionKey(sessionID))
	if err != nil {
		return nil, err
	}

	sess := &LocationSession{
		SessionID: sessionID,
		State:     SessionState(rec.Bins[BinState].(int)),
		TargetID:  rec.Bins[BinTargetID].(string),
		CreatedAt: time.Unix(0, rec.Bins[BinCreatedAt].(int64)),
		UpdatedAt: time.Unix(0, rec.Bins[BinUpdatedAt].(int64)),
		ExpiresAt: time.Unix(0, rec.Bins[BinExpiresAt].(int64)),
	}

	if v, ok := rec.Bins[BinVER].([]byte); ok {
		sess.VER = v
	}
	if k, ok := rec.Bins[BinAuthKey].([]byte); ok {
		sess.AuthKey = k
	}
	if pdu, ok := rec.Bins[BinSuplInit].([]byte); ok {
		sess.SuplInitPdu = pdu
	}
	if setID, ok := rec.Bins[BinSetSessID].(int64); ok {
		sess.SetSessionID = &setID
	}
	if lat, ok := rec.Bins[BinLat].(float64); ok {
		sess.Result = &LocationResult{
			Latitude:    lat,
			Longitude:   rec.Bins[BinLon].(float64),
			Altitude:    rec.Bins[BinAlt].(float64),
			Uncertainty: rec.Bins[BinUncertain].(float64),
			Confidence:  rec.Bins[BinConfidence].(int64),
			Timestamp:   sess.UpdatedAt,
		}
	}

	if sess.IsExpired() {
		return nil, ErrSessionExpired
	}

	return sess, nil
}

// UpdateSession updates the session in Aerospike
func (a *AerospikeSessionStore) UpdateSession(ctx context.Context, sess *LocationSession) error {
	return a.SaveSession(ctx, sess)
}

// DeleteSession deletes a session from Aerospike
func (a *AerospikeSessionStore) DeleteSession(ctx context.Context, sessionID [4]byte) error {
	_, err := a.client.Delete(ctx, a.namespace, a.set, a.sessionKey(sessionID))
	return err
}

// CleanupExpired scans and clears expired records
func (a *AerospikeSessionStore) CleanupExpired(ctx context.Context) (int, error) {
	// Aerospike cleans up records automatically according to their TTL.
	return 0, nil
}

// Close closes the Aerospike client
func (a *AerospikeSessionStore) Close() error {
	a.client.Close()
	return nil
}
