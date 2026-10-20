package bearer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"slp/pkg/metrics"
	"slp/pkg/nims"
	"slp/pkg/session"
)

// CPRunner simulates 5GC Control Plane positioning (3GPP 23.273 GMLC -> AMF -> LMF)
type CPRunner struct {
	nimsDb       nims.NimsClient
	counters     *metrics.TelemetryCounters
	mu           sync.RWMutex
	simulateFail bool
}

// NewCPRunner creates a new Control Plane positioning runner
func NewCPRunner(db nims.NimsClient, counters *metrics.TelemetryCounters) *CPRunner {
	if counters == nil {
		counters = metrics.DefaultCounters
	}
	return &CPRunner{
		nimsDb:   db,
		counters: counters,
	}
}

// SetSimulateFailure toggles simulated Control Plane failure for testing fallback to UP
func (r *CPRunner) SetSimulateFailure(fail bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.simulateFail = fail
}

// ExecuteCP performs Control Plane Cell-ID location query via simulated 5GC C-Plane
func (r *CPRunner) ExecuteCP(ctx context.Context, req LocationRequest) (*session.LocationResult, error) {
	r.counters.CpRequests.Add(1)

	r.mu.RLock()
	fail := r.simulateFail
	r.mu.RUnlock()

	if fail {
		r.counters.CpFailed.Add(1)
		return nil, errors.New("5GC Control Plane signaling congestion (AMF/LMF unavailable)")
	}

	// In 5GC C-Plane, GMLC queries UDM/AMF for current serving cell.
	// For simulation, we resolve the subscriber's current 5G NR serving cell.
	servingCell := nims.CellIdentifier{
		RadioType: nims.Radio5GNR,
		MCC:       452,
		MNC:       4,
		TAC_LAC:   1001,
		CellID:    987654321,
	}

	pos, err := r.nimsDb.LookupCell(ctx, servingCell)
	if err != nil {
		r.counters.CpFailed.Add(1)
		return nil, fmt.Errorf("control plane cell resolution failed: %w", err)
	}

	res := &session.LocationResult{
		LatitudeSign: 0,
		Latitude:     pos.Latitude,
		Longitude:    pos.Longitude,
		Altitude:     pos.Altitude,
		Uncertainty:  pos.Uncertainty,
		Confidence:   pos.Confidence,
		Timestamp:    time.Now().UTC(),
	}

	r.counters.CpSuccess.Add(1)
	return res, nil
}
