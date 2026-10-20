package bearer

import (
	"context"
	"fmt"
	"time"

	"slp/pkg/metrics"
	"slp/pkg/session"
)

// Selector coordinates bearer selection between User Plane (UP) and Control Plane (CP)
// with bidirectional fallback as specified in the Viettel 5GC SUPL architecture.
type Selector struct {
	upRunner *UPRunner
	cpRunner *CPRunner
	counters *metrics.TelemetryCounters
}

// NewSelector initializes a new Bearer Selector
func NewSelector(up *UPRunner, cp *CPRunner, counters *metrics.TelemetryCounters) *Selector {
	if counters == nil {
		counters = metrics.DefaultCounters
	}
	return &Selector{
		upRunner: up,
		cpRunner: cp,
		counters: counters,
	}
}

// ProvideLocation resolves the geographic location for a subscriber using the selected bearer policy
func (s *Selector) ProvideLocation(ctx context.Context, req LocationRequest) LocationResponse {
	start := time.Now()
	s.counters.TotalRequests.Add(1)

	mode := req.Bearer
	if mode == "" || mode == BearerModeAuto {
		mode = BearerModeUPPreferred
	}

	var resp LocationResponse

	switch mode {
	case BearerModeUPOnly:
		res, err := s.upRunner.ExecuteUP(ctx, req)
		if err != nil {
			resp = s.buildFailResp(BearerUP, false, err)
		} else {
			resp = s.buildSuccessResp(BearerUP, false, res)
		}

	case BearerModeCPOnly:
		res, err := s.cpRunner.ExecuteCP(ctx, req)
		if err != nil {
			resp = s.buildFailResp(BearerCP, false, err)
		} else {
			resp = s.buildSuccessResp(BearerCP, false, res)
		}

	case BearerModeUPPreferred:
		// 1. Try User Plane first
		res, err := s.upRunner.ExecuteUP(ctx, req)
		if err == nil {
			resp = s.buildSuccessResp(BearerUP, false, res)
			break
		}

		// 2. Fallback to Control Plane
		s.counters.FallbackUpToCp.Add(1)
		cpRes, cpErr := s.cpRunner.ExecuteCP(ctx, req)
		if cpErr != nil {
			combinedErr := fmt.Errorf("UP failed: %v; CP fallback failed: %w", err, cpErr)
			resp = s.buildFailResp(BearerCP, true, combinedErr)
		} else {
			resp = s.buildSuccessResp(BearerCP, true, cpRes)
		}

	case BearerModeCPPreferred:
		// 1. Try Control Plane first
		res, err := s.cpRunner.ExecuteCP(ctx, req)
		if err == nil {
			resp = s.buildSuccessResp(BearerCP, false, res)
			break
		}

		// 2. Fallback to User Plane
		s.counters.FallbackCpToUp.Add(1)
		upRes, upErr := s.upRunner.ExecuteUP(ctx, req)
		if upErr != nil {
			combinedErr := fmt.Errorf("CP failed: %v; UP fallback failed: %w", err, upErr)
			resp = s.buildFailResp(BearerUP, true, combinedErr)
		} else {
			resp = s.buildSuccessResp(BearerUP, true, upRes)
		}

	default:
		resp = s.buildFailResp(BearerUP, false, fmt.Errorf("unknown bearer mode: %s", mode))
	}

	duration := time.Since(start)
	resp.Duration = duration
	s.counters.RecordDuration(duration)
	return resp
}

func (s *Selector) buildSuccessResp(used BearerUsed, fallback bool, loc *session.LocationResult) LocationResponse {
	return LocationResponse{
		Status:           "SUCCESS",
		BearerUsed:       used,
		FallbackOccurred: fallback,
		Location:         loc,
	}
}

func (s *Selector) buildFailResp(used BearerUsed, fallback bool, err error) LocationResponse {
	return LocationResponse{
		Status:           "FAILED",
		BearerUsed:       used,
		FallbackOccurred: fallback,
		ErrorMsg:         err.Error(),
	}
}
