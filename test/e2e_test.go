package test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"slp/pkg/bearer"
	"slp/pkg/metrics"
	"slp/pkg/nims"
	"slp/pkg/oma_ulp"
	"slp/pkg/posinit"
	"slp/pkg/server"
	"slp/pkg/session"
	"slp/pkg/smspush"
	"slp/pkg/ulpgw"
	"slp/pkg/uper"
)

func ptr[T any](v T) *T {
	return &v
}

func TestEndToEndFigure1NetworkInitiatedService(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 1. Start Mock SMSC (SMPP Server)
	smsc, err := smspush.StartMockSMSC("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start mock smsc: %v", err)
	}
	defer smsc.Stop()

	// 2. Start ULP Gateway (gRPC server)
	gatewayServer := ulpgw.NewUlpGatewayServer()
	if err := gatewayServer.Start("127.0.0.1:0", ""); err != nil {
		t.Fatalf("failed to start gateway server: %v", err)
	}
	defer gatewayServer.Stop()

	// 3. Setup SLP Infrastructure
	counters := &metrics.TelemetryCounters{}
	sessionStore := session.NewInMemorySessionStore(1 * time.Second)
	defer sessionStore.Close()

	nimsDb := nims.NewDatabase()

	// Setup ULP Gateway Client in SLP
	gwClientCfg := ulpgw.ClientConfig{
		GatewayAddr: gatewayServer.GrpcAddr(),
		Backoff:     ulpgw.DefaultBackoffConfig(),
		Keepalive:   ulpgw.DefaultKeepaliveConfig(),
	}
	gwClient := ulpgw.NewUlpGatewayClient(gwClientCfg)
	gwClient.Start(ctx)
	defer gwClient.Close()

	// Wait for gRPC stream between SLP and Gateway to establish
	for i := 0; i < 30; i++ {
		if gwClient.IsConnected() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !gwClient.IsConnected() {
		t.Fatal("SLP ULP Gateway client failed to establish stream with Gateway")
	}

	// Setup SUPL POS INIT Processor in SLP
	processor := posinit.NewProcessor(sessionStore, nimsDb, gwClient)

	// Setup SMPP Client in SLP
	smppCfg := smspush.SMPPConfig{
		Addr:        smsc.Addr(),
		SystemID:    "VIETTEL_SLP",
		Password:    "SECRET",
		SystemType:  "SUPL",
		SourceAddr:  "1900",
		DialTimeout: 2 * time.Second,
		ReadTimeout: 2 * time.Second,
	}
	smppClient := smspush.NewSMPPClient(smppCfg)
	if err := smppClient.Connect(ctx); err != nil {
		t.Fatalf("SLP failed to connect to SMSC: %v", err)
	}
	defer smppClient.Close()

	// Setup Bearer Selector (UP Runner + CP Runner)
	authKey := []byte("secret-viettel-auth-key-2026")
	upRunner := bearer.NewUPRunner(sessionStore, smppClient, processor, authKey, "slp.viettel.vn", counters)
	cpRunner := bearer.NewCPRunner(nimsDb, counters)
	selector := bearer.NewSelector(upRunner, cpRunner, counters)

	// Setup HTTP REST API Server
	apiServer := server.NewServer(selector, counters)
	if err := apiServer.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("failed to start API server: %v", err)
	}
	defer apiServer.Stop(context.Background())

	// 4. Configure Mock UE Behavior (Terminal Agent):
	// When SMS wake-up message arrives at SMSC, UE decodes WAP Push, computes VER, and sends SUPL POS INIT via ULP Gateway
	targetMsisdn := "+84988000999"

	smsc.SetOnSubmitHandler(func(msg smspush.SubmittedMessage) {
		go func() {
			time.Sleep(30 * time.Millisecond) // Simulate terminal processing latency

			// Extract SUPL INIT from WAP Push SMS
			rawSuplInit, err := smspush.ExtractSuplInitFromSmsUserData(msg.Payload)
			if err != nil {
				t.Logf("UE failed to extract SUPL INIT: %v", err)
				return
			}

			var suplInitPdu oma_ulp.ULPPDU
			if err := oma_ulp.Unmarshal(rawSuplInit, &suplInitPdu); err != nil {
				t.Logf("UE failed to unmarshal SUPL INIT: %v", err)
				return
			}

			slpSessID := suplInitPdu.SessionID.SlpSessionID.SessionID

			// Compute terminal VER
			terminalVer := oma_ulp.ComputeVer(authKey, rawSuplInit)

			// Terminal constructs SUPL POS INIT with its current 5G NR cell info
			posInitPdu := oma_ulp.ULPPDU{
				Version: oma_ulp.Version{Maj: 2, Min: 0, Servind: 0},
				SessionID: oma_ulp.SessionID{
					SetSessionID: &oma_ulp.SetSessionID{
						SessionId: 54321,
						SetId: oma_ulp.SETId{
							Choice: oma_ulp.SETIdChoice_Msisdn,
							Msisdn: ptr([]byte{0x84, 0x98, 0x80, 0x00, 0x99, 0x90, 0x00, 0x00}),
						},
					},
					SlpSessionID: &oma_ulp.SlpSessionID{
						SessionID: slpSessID,
						SlpId: oma_ulp.SLPAddress{
							Choice: oma_ulp.SLPAddressChoice_Fqdn,
							Fqdn:   ptr(oma_ulp.FQDN("slp.viettel.vn")),
						},
					},
				},
				Message: oma_ulp.UlpMessage{
					Choice: oma_ulp.UlpMessageChoice_MsSUPLPOSINIT,
					MsSUPLPOSINIT: &oma_ulp.SUPLPOSINIT{
						SETCapabilities: oma_ulp.SETCapabilities{
							PosTechnology: oma_ulp.PosTechnology{AgpsSETassisted: true},
							PrefMethod:    oma_ulp.PrefMethod_AgpsSETassistedPreferred,
							PosProtocol:   oma_ulp.PosProtocol{Rrlp: true},
						},
						LocationId: oma_ulp.LocationId{
							Status: oma_ulp.Status_Current,
							CellInfo: oma_ulp.CellInfo{
								Choice: oma_ulp.CellInfoChoice_Ver2CellInfoExtension,
								Ver2CellInfoExtension: &oma_ulp.Ver2CellInfoExtension{
									Choice: oma_ulp.Ver2CellInfoExtensionChoice_NrCell,
									NrCell: &oma_ulp.NRCellInformation{
										ServingCellInformation: oma_ulp.ServingCellInformationNR{
											oma_ulp.ServCellNR{
												PhysCellId: 100,
												ArfcnNR:    600000,
												CellGlobalId: oma_ulp.CellGlobalIdNR{
													PlmnIdentity: oma_ulp.PLMNIdentity{
														Mcc: &oma_ulp.MCC{4, 5, 2},
														Mnc: oma_ulp.MNC{0, 4},
													},
													CellIdentityNR: oma_ulp.CellIdentityNR(uper.NewBitString([]byte{0x12, 0x34, 0x56, 0x78, 0x90}, 36)),
												},
												TrackingAreaCode: oma_ulp.TrackingAreaCodeNR(uper.NewBitString([]byte{0x03, 0xE9, 0x00}, 24)),
											},
										},
									},
								},
							},
						},
						Ver: terminalVer,
					},
				},
			}

			encodedPosInit, err := oma_ulp.Marshal(&posInitPdu)
			if err != nil {
				t.Logf("UE failed to marshal SUPL POS INIT: %v", err)
				return
			}

			// Terminal pushes SUPL POS INIT into ULP Gateway
			suplEndRespCh, err := gatewayServer.ForwardUeMessage(slpSessID, targetMsisdn, encodedPosInit)
			if err != nil {
				t.Logf("ForwardUeMessage error: %v", err)
				return
			}

			// Wait for SUPL END confirmation from SLP
			select {
			case endBytes := <-suplEndRespCh:
				var endPdu oma_ulp.ULPPDU
				_ = oma_ulp.Unmarshal(endBytes, &endPdu)
				t.Logf("Terminal successfully received SUPL END (%d bytes)", len(endBytes))
			case <-time.After(5 * time.Second):
				t.Log("Terminal timed out waiting for SUPL END")
			}
		}()
	})

	// 5. LCS Client executes HTTP ProvideLocation request
	requestBody := server.ProvideLocationRequest{
		Target:      targetMsisdn,
		ServiceType: "emergency",
		Bearer:      "UP_PREFERRED",
		QoP: &server.ProvideLocQoP{
			Horacc: ptr(int64(50)),
			Veracc: ptr(int64(20)),
		},
		TimeoutMs: 5000,
	}

	reqBytes, _ := json.Marshal(requestBody)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", "http://"+apiServer.Addr()+"/ngmlc/v1/provide-location", bytes.NewReader(reqBytes))
	if err != nil {
		t.Fatalf("failed to create http request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", httpResp.StatusCode)
	}

	var provResp server.ProvideLocationResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&provResp); err != nil {
		t.Fatalf("failed to decode response json: %v", err)
	}

	// 6. Verify result
	if provResp.Status != "SUCCESS" {
		t.Fatalf("expected status SUCCESS, got %s: %s", provResp.Status, provResp.Error)
	}
	if provResp.BearerUsed != "USER_PLANE" {
		t.Fatalf("expected USER_PLANE, got %s", provResp.BearerUsed)
	}
	if provResp.FallbackOccurred {
		t.Fatal("expected FallbackOccurred to be false")
	}
	if provResp.Location == nil {
		t.Fatal("expected non-nil location result")
	}

	// 5G NR Cell Hoa Lac coordinates: (21.013589, 105.527311)
	if provResp.Location.Latitude != 21.013589 || provResp.Location.Longitude != 105.527311 {
		t.Fatalf("resolved 5G coordinates mismatch: %+v", provResp.Location)
	}

	t.Logf("Successfully verified Figure 1 Network-Initiated Service: Lat=%.6f, Lon=%.6f, Bearer=%s, Duration=%dms",
		provResp.Location.Latitude, provResp.Location.Longitude, provResp.BearerUsed, provResp.DurationMs)

	// Verify telemetry counters
	if counters.UpSuccess.Load() != 1 {
		t.Fatalf("expected UpSuccess 1, got %d", counters.UpSuccess.Load())
	}
	if counters.VerMatch.Load() != 1 {
		t.Fatalf("expected VerMatch 1, got %d", counters.VerMatch.Load())
	}
	if counters.SmsSentSuccess.Load() != 1 {
		t.Fatalf("expected SmsSentSuccess 1, got %d", counters.SmsSentSuccess.Load())
	}
}

func TestEndToEndFallbackFromUPToCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Setup SMSC that rejects SMS to trigger fallback
	smsc, err := smspush.StartMockSMSC("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start mock smsc: %v", err)
	}
	defer smsc.Stop()

	gatewayServer := ulpgw.NewUlpGatewayServer()
	if err := gatewayServer.Start("127.0.0.1:0", ""); err != nil {
		t.Fatalf("failed to start gateway server: %v", err)
	}
	defer gatewayServer.Stop()

	counters := &metrics.TelemetryCounters{}
	sessionStore := session.NewInMemorySessionStore(1 * time.Second)
	defer sessionStore.Close()
	nimsDb := nims.NewDatabase()

	gwClient := ulpgw.NewUlpGatewayClient(ulpgw.ClientConfig{
		GatewayAddr: gatewayServer.GrpcAddr(),
		Backoff:     ulpgw.DefaultBackoffConfig(),
		Keepalive:   ulpgw.DefaultKeepaliveConfig(),
	})
	gwClient.Start(ctx)
	defer gwClient.Close()

	processor := posinit.NewProcessor(sessionStore, nimsDb, gwClient)

	// Don't connect smppClient, or connect to dead port to simulate SMS delivery failure
	brokenSmppClient := smspush.NewSMPPClient(smspush.SMPPConfig{
		Addr: "127.0.0.1:54321", // dead port
	})

	authKey := []byte("secret-viettel-auth-key-2026")
	upRunner := bearer.NewUPRunner(sessionStore, brokenSmppClient, processor, authKey, "slp.viettel.vn", counters)
	cpRunner := bearer.NewCPRunner(nimsDb, counters)
	selector := bearer.NewSelector(upRunner, cpRunner, counters)

	apiServer := server.NewServer(selector, counters)
	if err := apiServer.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("failed to start API server: %v", err)
	}
	defer apiServer.Stop(context.Background())

	// Send ProvideLocation request with UP_PREFERRED
	requestBody := server.ProvideLocationRequest{
		Target:    "+84988000888",
		Bearer:    "UP_PREFERRED",
		TimeoutMs: 2000,
	}

	reqBytes, _ := json.Marshal(requestBody)
	httpResp, err := http.Post("http://"+apiServer.Addr()+"/ngmlc/v1/provide-location", "application/json", bytes.NewReader(reqBytes))
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 via fallback, got %d", httpResp.StatusCode)
	}

	var provResp server.ProvideLocationResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&provResp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if provResp.Status != "SUCCESS" {
		t.Fatalf("expected status SUCCESS via fallback, got %s: %s", provResp.Status, provResp.Error)
	}
	if provResp.BearerUsed != "CONTROL_PLANE" {
		t.Fatalf("expected BearerUsed CONTROL_PLANE, got %s", provResp.BearerUsed)
	}
	if !provResp.FallbackOccurred {
		t.Fatal("expected FallbackOccurred to be true")
	}
	if counters.FallbackUpToCp.Load() != 1 {
		t.Fatalf("expected FallbackUpToCp 1, got %d", counters.FallbackUpToCp.Load())
	}
}
