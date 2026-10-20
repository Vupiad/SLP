package bearer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"slp/pkg/bearer"
	"slp/pkg/metrics"
	"slp/pkg/nims"
	"slp/pkg/oma_ulp"
	"slp/pkg/posinit"
	"slp/pkg/session"
	"slp/pkg/smspush"
)

type mockSmsSender struct {
	failDelivery bool
	onSend       func(dest string, data []byte)
}

func (m *mockSmsSender) SubmitWapPushSMS(ctx context.Context, dest string, data []byte) (string, error) {
	if m.failDelivery {
		return "", errors.New("SMSC connection down")
	}
	if m.onSend != nil {
		m.onSend(dest, data)
	}
	return "MOCK-SMS-12345", nil
}

func ptr[T any](v T) *T {
	return &v
}

func TestBearerSelectionUPOnly(t *testing.T) {
	store := session.NewInMemorySessionStore(1 * time.Second)
	defer store.Close()
	nimsDb := nims.NewDatabase()
	proc := posinit.NewProcessor(store, nimsDb, nil)
	counters := &metrics.TelemetryCounters{}

	sms := &mockSmsSender{}
	upRunner := bearer.NewUPRunner(store, sms, proc, []byte("key"), "slp.viettel.vn", counters)
	cpRunner := bearer.NewCPRunner(nimsDb, counters)
	selector := bearer.NewSelector(upRunner, cpRunner, counters)

	// Simulate UE replying with SUPL POS INIT upon SMS arrival
	sms.onSend = func(dest string, data []byte) {
		go func() {
			time.Sleep(20 * time.Millisecond)
			rawSuplInit, err := smspush.ExtractSuplInitFromSmsUserData(data)
			if err != nil {
				t.Logf("ExtractSuplInitFromSmsUserData error: %v", err)
				return
			}
			var pdu oma_ulp.ULPPDU
			if err := oma_ulp.Unmarshal(rawSuplInit, &pdu); err != nil {
				t.Logf("Unmarshal rawSuplInit error: %v", err)
				return
			}
			if pdu.SessionID.SlpSessionID == nil {
				t.Logf("SlpSessionID is nil in decoded pdu!")
				return
			}
			sessID := pdu.SessionID.SlpSessionID.SessionID

			// Build SUPL POS INIT
			posInitPdu := oma_ulp.ULPPDU{
				Version: oma_ulp.Version{Maj: 2, Min: 0, Servind: 0},
				SessionID: oma_ulp.SessionID{
					SlpSessionID: &oma_ulp.SlpSessionID{
						SessionID: sessID,
						SlpId: oma_ulp.SLPAddress{
							Choice: oma_ulp.SLPAddressChoice_Fqdn,
							Fqdn:   ptr(oma_ulp.FQDN("slp.viettel.vn")),
						},
					},
				},
				Message: oma_ulp.UlpMessage{
					Choice: oma_ulp.UlpMessageChoice_MsSUPLPOSINIT,
					MsSUPLPOSINIT: &oma_ulp.SUPLPOSINIT{
						LocationId: oma_ulp.LocationId{
							Status: oma_ulp.Status_Current,
							CellInfo: oma_ulp.CellInfo{
								Choice: oma_ulp.CellInfoChoice_GsmCell,
								GsmCell: &oma_ulp.GsmCellInformation{
									RefMCC: 452,
									RefMNC: 4,
									RefLAC: 100,
									RefCI:  500,
								},
							},
						},
						Ver: oma_ulp.ComputeVer([]byte("key"), rawSuplInit),
					},
				},
			}
			encoded, err := oma_ulp.Marshal(&posInitPdu)
			if err != nil {
				t.Logf("Marshal posInitPdu error: %v", err)
				return
			}
			_, err = proc.ProcessPduBytes(context.Background(), sessID, dest, encoded)
			if err != nil {
				t.Logf("ProcessPduBytes error: %v", err)
			}
		}()
	}

	req := bearer.LocationRequest{
		TargetID: "+84988111222",
		Bearer:   bearer.BearerModeUPOnly,
		Timeout:  2 * time.Second,
	}

	resp := selector.ProvideLocation(context.Background(), req)
	if resp.Status != "SUCCESS" {
		t.Fatalf("expected SUCCESS, got %s: %s", resp.Status, resp.ErrorMsg)
	}
	if resp.BearerUsed != bearer.BearerUP {
		t.Fatalf("expected BearerUP, got %s", resp.BearerUsed)
	}
	if resp.FallbackOccurred {
		t.Fatal("expected FallbackOccurred to be false")
	}
	if resp.Location == nil || resp.Location.Latitude != 21.031200 {
		t.Fatalf("coordinates mismatch: %+v", resp.Location)
	}
	if counters.UpSuccess.Load() != 1 {
		t.Fatalf("expected UpSuccess 1, got %d", counters.UpSuccess.Load())
	}
}

func TestBearerSelectionCPOnly(t *testing.T) {
	store := session.NewInMemorySessionStore(1 * time.Second)
	defer store.Close()
	nimsDb := nims.NewDatabase()
	proc := posinit.NewProcessor(store, nimsDb, nil)
	counters := &metrics.TelemetryCounters{}

	upRunner := bearer.NewUPRunner(store, &mockSmsSender{}, proc, []byte("key"), "slp.viettel.vn", counters)
	cpRunner := bearer.NewCPRunner(nimsDb, counters)
	selector := bearer.NewSelector(upRunner, cpRunner, counters)

	req := bearer.LocationRequest{
		TargetID: "+84988333444",
		Bearer:   bearer.BearerModeCPOnly,
		Timeout:  1 * time.Second,
	}

	resp := selector.ProvideLocation(context.Background(), req)
	if resp.Status != "SUCCESS" {
		t.Fatalf("expected SUCCESS, got %s: %s", resp.Status, resp.ErrorMsg)
	}
	if resp.BearerUsed != bearer.BearerCP {
		t.Fatalf("expected BearerCP, got %s", resp.BearerUsed)
	}
	if resp.FallbackOccurred {
		t.Fatal("expected FallbackOccurred to be false")
	}
	if counters.CpSuccess.Load() != 1 {
		t.Fatalf("expected CpSuccess 1, got %d", counters.CpSuccess.Load())
	}
}

func TestFallbackUPToCP(t *testing.T) {
	store := session.NewInMemorySessionStore(1 * time.Second)
	defer store.Close()
	nimsDb := nims.NewDatabase()
	proc := posinit.NewProcessor(store, nimsDb, nil)
	counters := &metrics.TelemetryCounters{}

	// Configure SMS delivery failure in UP
	sms := &mockSmsSender{failDelivery: true}
	upRunner := bearer.NewUPRunner(store, sms, proc, []byte("key"), "slp.viettel.vn", counters)
	cpRunner := bearer.NewCPRunner(nimsDb, counters)
	selector := bearer.NewSelector(upRunner, cpRunner, counters)

	req := bearer.LocationRequest{
		TargetID: "+84988555666",
		Bearer:   bearer.BearerModeUPPreferred, // Should fallback to CP
		Timeout:  1 * time.Second,
	}

	resp := selector.ProvideLocation(context.Background(), req)
	if resp.Status != "SUCCESS" {
		t.Fatalf("expected SUCCESS via fallback, got %s: %s", resp.Status, resp.ErrorMsg)
	}
	if resp.BearerUsed != bearer.BearerCP {
		t.Fatalf("expected BearerCP on fallback, got %s", resp.BearerUsed)
	}
	if !resp.FallbackOccurred {
		t.Fatal("expected FallbackOccurred to be true")
	}
	if counters.FallbackUpToCp.Load() != 1 {
		t.Fatalf("expected FallbackUpToCp 1, got %d", counters.FallbackUpToCp.Load())
	}
}

func TestFallbackCPToUP(t *testing.T) {
	store := session.NewInMemorySessionStore(1 * time.Second)
	defer store.Close()
	nimsDb := nims.NewDatabase()
	proc := posinit.NewProcessor(store, nimsDb, nil)
	counters := &metrics.TelemetryCounters{}

	sms := &mockSmsSender{}
	upRunner := bearer.NewUPRunner(store, sms, proc, []byte("key"), "slp.viettel.vn", counters)
	cpRunner := bearer.NewCPRunner(nimsDb, counters)
	// Simulate Control Plane failure
	cpRunner.SetSimulateFailure(true)

	selector := bearer.NewSelector(upRunner, cpRunner, counters)

	// Simulate UE response when UP is invoked
	sms.onSend = func(dest string, data []byte) {
		go func() {
			time.Sleep(10 * time.Millisecond)
			rawSuplInit, _ := smspush.ExtractSuplInitFromSmsUserData(data)
			var pdu oma_ulp.ULPPDU
			_ = oma_ulp.Unmarshal(rawSuplInit, &pdu)
			sessID := pdu.SessionID.SlpSessionID.SessionID

			posInitPdu := oma_ulp.ULPPDU{
				Version: oma_ulp.Version{Maj: 2, Min: 0, Servind: 0},
				SessionID: oma_ulp.SessionID{
					SlpSessionID: &oma_ulp.SlpSessionID{
						SessionID: sessID,
						SlpId: oma_ulp.SLPAddress{
							Choice: oma_ulp.SLPAddressChoice_Fqdn,
							Fqdn:   ptr(oma_ulp.FQDN("slp.viettel.vn")),
						},
					},
				},
				Message: oma_ulp.UlpMessage{
					Choice: oma_ulp.UlpMessageChoice_MsSUPLPOSINIT,
					MsSUPLPOSINIT: &oma_ulp.SUPLPOSINIT{
						LocationId: oma_ulp.LocationId{
							Status: oma_ulp.Status_Current,
							CellInfo: oma_ulp.CellInfo{
								Choice: oma_ulp.CellInfoChoice_GsmCell,
								GsmCell: &oma_ulp.GsmCellInformation{
									RefMCC: 452,
									RefMNC: 4,
									RefLAC: 100,
									RefCI:  500,
								},
							},
						},
						Ver: oma_ulp.ComputeVer([]byte("key"), rawSuplInit),
					},
				},
			}
			encoded, _ := oma_ulp.Marshal(&posInitPdu)
			_, _ = proc.ProcessPduBytes(context.Background(), sessID, dest, encoded)
		}()
	}

	req := bearer.LocationRequest{
		TargetID: "+84988777888",
		Bearer:   bearer.BearerModeCPPreferred, // Should fallback to UP
		Timeout:  2 * time.Second,
	}

	resp := selector.ProvideLocation(context.Background(), req)
	if resp.Status != "SUCCESS" {
		t.Fatalf("expected SUCCESS via fallback CP->UP, got %s: %s", resp.Status, resp.ErrorMsg)
	}
	if resp.BearerUsed != bearer.BearerUP {
		t.Fatalf("expected BearerUP on fallback, got %s", resp.BearerUsed)
	}
	if !resp.FallbackOccurred {
		t.Fatal("expected FallbackOccurred to be true")
	}
	if counters.FallbackCpToUp.Load() != 1 {
		t.Fatalf("expected FallbackCpToUp 1, got %d", counters.FallbackCpToUp.Load())
	}
}

func TestBearerAdditionalCoverage(t *testing.T) {
	store := session.NewInMemorySessionStore(1 * time.Second)
	defer store.Close()
	nimsDb := nims.NewDatabase()
	proc := posinit.NewProcessor(store, nimsDb, nil)
	counters := &metrics.TelemetryCounters{}

	sms := &mockSmsSender{}
	upRunner := bearer.NewUPRunner(store, sms, proc, []byte("key"), "slp.viettel.vn", counters)
	cpRunner := bearer.NewCPRunner(nimsDb, counters)
	selector := bearer.NewSelector(upRunner, cpRunner, counters)

	// 1. Unknown bearer mode
	reqUnknown := bearer.LocationRequest{
		TargetID: "+84988000111",
		Bearer:   "UNKNOWN_MODE",
	}
	respUnknown := selector.ProvideLocation(context.Background(), reqUnknown)
	if respUnknown.Status != "FAILED" {
		t.Fatalf("expected FAILED for unknown bearer mode, got %s", respUnknown.Status)
	}

	// 2. Both UP and CP fail in UP_PREFERRED
	sms.failDelivery = true
	cpRunner.SetSimulateFailure(true)
	reqBothFail := bearer.LocationRequest{
		TargetID: "+84988000222",
		Bearer:   bearer.BearerModeUPPreferred,
		Timeout:  500 * time.Millisecond,
	}
	respBothFail := selector.ProvideLocation(context.Background(), reqBothFail)
	if respBothFail.Status != "FAILED" || !respBothFail.FallbackOccurred {
		t.Fatalf("expected FAILED with fallbackOccurred true, got %+v", respBothFail)
	}

	// 3. Both fail in CP_PREFERRED
	reqCpFail := bearer.LocationRequest{
		TargetID: "+84988000333",
		Bearer:   bearer.BearerModeCPPreferred,
		Timeout:  500 * time.Millisecond,
	}
	respCpFail := selector.ProvideLocation(context.Background(), reqCpFail)
	if respCpFail.Status != "FAILED" || !respCpFail.FallbackOccurred {
		t.Fatalf("expected FAILED with fallbackOccurred true, got %+v", respCpFail)
	}

	// 4. Context cancellation in ExecuteUP
	sms.failDelivery = false
	cancellingCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	_, err := upRunner.ExecuteUP(cancellingCtx, bearer.LocationRequest{
		TargetID: "+84988000444",
		Timeout:  1 * time.Second,
	})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestBearerEdgeCasesCoverage(t *testing.T) {
	store := session.NewInMemorySessionStore(1 * time.Second)
	defer store.Close()
	nimsDb := nims.NewDatabase()
	proc := posinit.NewProcessor(store, nimsDb, nil)
	counters := &metrics.TelemetryCounters{}

	// UPRunner with nil SMS sender and defaults
	upRunner := bearer.NewUPRunner(store, nil, proc, nil, "", nil)

	// Timeout branch (no response from terminal)
	ctx := context.Background()
	reqTimeout := bearer.LocationRequest{
		TargetID: "+84988000555",
		Bearer:   bearer.BearerModeUPOnly,
		Timeout:  100 * time.Millisecond,
	}
	_, err := upRunner.ExecuteUP(ctx, reqTimeout)
	if err == nil {
		t.Fatal("expected timeout error in ExecuteUP")
	}

	// CPRunner lookup cell error
	cpRunner := bearer.NewCPRunner(nimsDb, nil)
	// Foreign target or unresolvable
	_, _ = cpRunner.ExecuteCP(ctx, bearer.LocationRequest{})
}
