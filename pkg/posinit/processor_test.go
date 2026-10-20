package posinit_test

import (
	"context"
	"testing"
	"time"

	"slp/pkg/nims"
	"slp/pkg/oma_ulp"
	"slp/pkg/posinit"
	"slp/pkg/session"
	"slp/pkg/ulpgw"
	"slp/pkg/uper"
)

func ptr[T any](v T) *T {
	return &v
}

func TestProcessorSuccess(t *testing.T) {
	store := session.NewInMemorySessionStore(1 * time.Second)
	defer store.Close()

	nimsDb := nims.NewDatabase()
	proc := posinit.NewProcessor(store, nimsDb, nil)

	ctx := context.Background()

	// 1. Create active session
	sess, err := session.NewSession("+84988111222", 30*time.Second, session.QoP{})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	key := []byte("secret-slp-auth-key-test")
	dummyInitMsg := []byte("supl-init-bytes")
	sess.VER = oma_ulp.ComputeVerRaw(key, dummyInitMsg)
	sess.AuthKey = key
	sess.State = session.StateWakeupSent

	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	// Register waiter
	waiterCh := proc.RegisterWaiter(sess.SessionID)

	// 2. Build incoming SUPL POS INIT PDU with matching VER
	posInitPdu := oma_ulp.ULPPDU{
		Version: oma_ulp.Version{Maj: 2, Min: 0, Servind: 0},
		SessionID: oma_ulp.SessionID{
			SetSessionID: &oma_ulp.SetSessionID{
				SessionId: 8888,
				SetId: oma_ulp.SETId{
					Choice: oma_ulp.SETIdChoice_Msisdn,
					Msisdn: ptr([]byte{0x84, 0x98, 0x81, 0x11, 0x22, 0x20, 0x00, 0x00}),
				},
			},
			SlpSessionID: &oma_ulp.SlpSessionID{
				SessionID: sess.SessionID[:],
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
						Choice: oma_ulp.CellInfoChoice_GsmCell,
						GsmCell: &oma_ulp.GsmCellInformation{
							RefMCC: 452,
							RefMNC: 4,
							RefLAC: 100,
							RefCI:  500,
							Ta:     ptr(int64(1)),
						},
					},
				},
				Ver: oma_ulp.ComputeVer(key, dummyInitMsg),
			},
		},
	}

	encoded, err := oma_ulp.Marshal(&posInitPdu)
	if err != nil {
		t.Fatalf("Marshal SUPLPOSINIT failed: %v", err)
	}

	// 3. Process PDU
	result, err := proc.ProcessPduBytes(ctx, sess.SessionID[:], sess.TargetID, encoded)
	if err != nil {
		t.Fatalf("ProcessPduBytes failed: %v", err)
	}

	if result.Latitude != 21.031200 || result.Longitude != 105.852300 {
		t.Fatalf("resolved coordinates mismatch: %+v", result)
	}

	// Check waiter received result
	select {
	case waitRes := <-waiterCh:
		if waitRes.Latitude != result.Latitude {
			t.Fatalf("waiter received mismatch: %+v", waitRes)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("waiter did not receive result")
	}

	// Check session updated in store to Completed
	savedSess, err := store.GetSession(ctx, sess.SessionID)
	if err != nil || savedSess.State != session.StateCompleted {
		t.Fatalf("expected state Completed, got %v", savedSess.State)
	}
}

func TestProcessorVerMismatch(t *testing.T) {
	store := session.NewInMemorySessionStore(1 * time.Second)
	defer store.Close()

	nimsDb := nims.NewDatabase()
	proc := posinit.NewProcessor(store, nimsDb, nil)

	ctx := context.Background()

	sess, err := session.NewSession("+84988222333", 30*time.Second, session.QoP{})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	sess.VER = []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	sess.State = session.StateWakeupSent
	_ = store.SaveSession(ctx, sess)

	// Build SUPL POS INIT with different VER
	posInitPdu := oma_ulp.ULPPDU{
		Version: oma_ulp.Version{Maj: 2, Min: 0, Servind: 0},
		SessionID: oma_ulp.SessionID{
			SlpSessionID: &oma_ulp.SlpSessionID{
				SessionID: sess.SessionID[:],
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
				// Wrong VER
				Ver: oma_ulp.ComputeVer([]byte("wrong-key"), []byte("wrong-msg")),
			},
		},
	}

	encoded, _ := oma_ulp.Marshal(&posInitPdu)

	_, err = proc.ProcessPduBytes(ctx, sess.SessionID[:], sess.TargetID, encoded)
	if err != posinit.ErrVerMismatch {
		t.Fatalf("expected ErrVerMismatch, got %v", err)
	}

	savedSess, _ := store.GetSession(ctx, sess.SessionID)
	if savedSess.State != session.StateFailed {
		t.Fatalf("expected session state Failed, got %v", savedSess.State)
	}
}

func TestProcessorWcdmaAndLteAndNr(t *testing.T) {
	store := session.NewInMemorySessionStore(1 * time.Second)
	defer store.Close()
	nimsDb := nims.NewDatabase()
	proc := posinit.NewProcessor(store, nimsDb, nil)
	ctx := context.Background()

	// 1. WCDMA Cell
	sessWcdma, _ := session.NewSession("+84988111333", 30*time.Second, session.QoP{})
	sessWcdma.VER = []byte{1, 2, 3, 4, 5, 6, 7, 8}
	_ = store.SaveSession(ctx, sessWcdma)

	wcdmaPdu := oma_ulp.ULPPDU{
		Version: oma_ulp.Version{Maj: 2, Min: 0, Servind: 0},
		SessionID: oma_ulp.SessionID{
			SlpSessionID: &oma_ulp.SlpSessionID{
				SessionID: sessWcdma.SessionID[:],
				SlpId:     oma_ulp.SLPAddress{Choice: oma_ulp.SLPAddressChoice_Fqdn, Fqdn: ptr(oma_ulp.FQDN("slp.viettel.vn"))},
			},
		},
		Message: oma_ulp.UlpMessage{
			Choice: oma_ulp.UlpMessageChoice_MsSUPLPOSINIT,
			MsSUPLPOSINIT: &oma_ulp.SUPLPOSINIT{
				LocationId: oma_ulp.LocationId{
					Status: oma_ulp.Status_Current,
					CellInfo: oma_ulp.CellInfo{
						Choice: oma_ulp.CellInfoChoice_WcdmaCell,
						WcdmaCell: &oma_ulp.WcdmaCellInformation{
							RefMCC: 452,
							RefMNC: 4,
							RefUC:  600,
						},
					},
				},
				Ver: oma_ulp.ComputeVer([]byte("any"), []byte("any")),
			},
		},
	}
	wcdmaPdu.Message.MsSUPLPOSINIT.Ver.Bytes = sessWcdma.VER

	encodedWcdma, _ := oma_ulp.Marshal(&wcdmaPdu)
	resWcdma, err := proc.ProcessPduBytes(ctx, sessWcdma.SessionID[:], sessWcdma.TargetID, encodedWcdma)
	if err != nil {
		t.Fatalf("WCDMA process error: %v", err)
	}
	if resWcdma.Latitude != 10.776530 {
		t.Fatalf("WCDMA latitude mismatch: %f", resWcdma.Latitude)
	}

	// 2. LTE Cell
	sessLte, _ := session.NewSession("+84988111444", 30*time.Second, session.QoP{})
	sessLte.VER = []byte{2, 3, 4, 5, 6, 7, 8, 9}
	_ = store.SaveSession(ctx, sessLte)

	ltePdu := oma_ulp.ULPPDU{
		Version: oma_ulp.Version{Maj: 2, Min: 0, Servind: 0},
		SessionID: oma_ulp.SessionID{
			SlpSessionID: &oma_ulp.SlpSessionID{
				SessionID: sessLte.SessionID[:],
				SlpId:     oma_ulp.SLPAddress{Choice: oma_ulp.SLPAddressChoice_Fqdn, Fqdn: ptr(oma_ulp.FQDN("slp.viettel.vn"))},
			},
		},
		Message: oma_ulp.UlpMessage{
			Choice: oma_ulp.UlpMessageChoice_MsSUPLPOSINIT,
			MsSUPLPOSINIT: &oma_ulp.SUPLPOSINIT{
				LocationId: oma_ulp.LocationId{
					Status: oma_ulp.Status_Current,
					CellInfo: oma_ulp.CellInfo{
						Choice: oma_ulp.CellInfoChoice_Ver2CellInfoExtension,
						Ver2CellInfoExtension: &oma_ulp.Ver2CellInfoExtension{
							Choice: oma_ulp.Ver2CellInfoExtensionChoice_LteCell,
							LteCell: &oma_ulp.LteCellInformation{
								CellGlobalIdEUTRA: oma_ulp.CellGlobalIdEUTRA{
									PlmnIdentity: oma_ulp.PLMNIdentity{
										Mcc: &oma_ulp.MCC{4, 5, 2},
										Mnc: oma_ulp.MNC{0, 4},
									},
									CellIdentity: oma_ulp.CellIdentity(oma_ulp.Ver(uper.NewBitString([]byte{0x00, 0x12, 0xD6, 0x87}, 28))),
								},
								TrackingAreaCode: oma_ulp.TrackingAreaCode(oma_ulp.Ver(uper.NewBitString([]byte{0x03, 0xEA}, 16))),
								Ta:               ptr(int64(2)),
							},
						},
					},
				},
				Ver: oma_ulp.ComputeVer([]byte("any"), []byte("any")),
			},
		},
	}
	ltePdu.Message.MsSUPLPOSINIT.Ver.Bytes = sessLte.VER

	encodedLte, _ := oma_ulp.Marshal(&ltePdu)
	resLte, err := proc.ProcessPduBytes(ctx, sessLte.SessionID[:], sessLte.TargetID, encodedLte)
	if err != nil {
		t.Fatalf("LTE process error: %v", err)
	}
	if resLte.Latitude != 21.028511 {
		t.Fatalf("LTE latitude mismatch: %f", resLte.Latitude)
	}

	// 3. 5G NR Cell
	sessNr, _ := session.NewSession("+84988111555", 30*time.Second, session.QoP{})
	sessNr.VER = []byte{3, 4, 5, 6, 7, 8, 9, 10}
	_ = store.SaveSession(ctx, sessNr)

	nrPdu := oma_ulp.ULPPDU{
		Version: oma_ulp.Version{Maj: 2, Min: 0, Servind: 0},
		SessionID: oma_ulp.SessionID{
			SlpSessionID: &oma_ulp.SlpSessionID{
				SessionID: sessNr.SessionID[:],
				SlpId:     oma_ulp.SLPAddress{Choice: oma_ulp.SLPAddressChoice_Fqdn, Fqdn: ptr(oma_ulp.FQDN("slp.viettel.vn"))},
			},
		},
		Message: oma_ulp.UlpMessage{
			Choice: oma_ulp.UlpMessageChoice_MsSUPLPOSINIT,
			MsSUPLPOSINIT: &oma_ulp.SUPLPOSINIT{
				LocationId: oma_ulp.LocationId{
					Status: oma_ulp.Status_Current,
					CellInfo: oma_ulp.CellInfo{
						Choice: oma_ulp.CellInfoChoice_Ver2CellInfoExtension,
						Ver2CellInfoExtension: &oma_ulp.Ver2CellInfoExtension{
							Choice: oma_ulp.Ver2CellInfoExtensionChoice_NrCell,
							NrCell: &oma_ulp.NRCellInformation{
								ServingCellInformation: oma_ulp.ServingCellInformationNR{
									oma_ulp.ServCellNR{
										PhysCellId: 10,
										ArfcnNR:    1000,
										CellGlobalId: oma_ulp.CellGlobalIdNR{
											PlmnIdentity: oma_ulp.PLMNIdentity{
												Mcc: &oma_ulp.MCC{4, 5, 2},
												Mnc: oma_ulp.MNC{0, 4},
											},
											CellIdentityNR: oma_ulp.CellIdentityNR(oma_ulp.Ver(uper.NewBitString([]byte{0x12, 0x34, 0x56, 0x78, 0x90}, 36))),
										},
										TrackingAreaCode: oma_ulp.TrackingAreaCodeNR(oma_ulp.Ver(uper.NewBitString([]byte{0x03, 0xE9, 0x00}, 24))),
									},
								},
							},
						},
					},
				},
				Ver: oma_ulp.ComputeVer([]byte("any"), []byte("any")),
			},
		},
	}
	nrPdu.Message.MsSUPLPOSINIT.Ver.Bytes = sessNr.VER

	encodedNr, _ := oma_ulp.Marshal(&nrPdu)
	resNr, err := proc.ProcessPduBytes(ctx, sessNr.SessionID[:], sessNr.TargetID, encodedNr)
	if err != nil {
		t.Fatalf("NR process error: %v", err)
	}
	if resNr.Latitude != 21.013589 {
		t.Fatalf("NR latitude mismatch: %f", resNr.Latitude)
	}
}

func TestProcessorEdgeCases(t *testing.T) {
	store := session.NewInMemorySessionStore(1 * time.Second)
	defer store.Close()
	nimsDb := nims.NewDatabase()
	proc := posinit.NewProcessor(store, nimsDb, nil)
	ctx := context.Background()

	// 1. Not SUPL POS INIT (e.g. SUPL END)
	suplEndPdu := oma_ulp.ULPPDU{
		Version: oma_ulp.Version{Maj: 2, Min: 0, Servind: 0},
		Message: oma_ulp.UlpMessage{
			Choice:    oma_ulp.UlpMessageChoice_MsSUPLEND,
			MsSUPLEND: &oma_ulp.SUPLEND{},
		},
	}
	encodedEnd, _ := oma_ulp.Marshal(&suplEndPdu)
	_, err := proc.ProcessPduBytes(ctx, []byte{1, 2, 3, 4}, "+84988", encodedEnd)
	if err != posinit.ErrNotSuplPosInit {
		t.Fatalf("expected ErrNotSuplPosInit, got %v", err)
	}

	// 2. Unknown Session ID
	posInitPdu := oma_ulp.ULPPDU{
		Version: oma_ulp.Version{Maj: 2, Min: 0, Servind: 0},
		SessionID: oma_ulp.SessionID{
			SlpSessionID: &oma_ulp.SlpSessionID{
				SessionID: []byte{0xDE, 0xAD, 0xBE, 0xEF},
				SlpId:     oma_ulp.SLPAddress{Choice: oma_ulp.SLPAddressChoice_Fqdn, Fqdn: ptr(oma_ulp.FQDN("slp.viettel.vn"))},
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
			},
		},
	}
	encodedPosInit, _ := oma_ulp.Marshal(&posInitPdu)
	_, err = proc.ProcessPduBytes(ctx, []byte{0xDE, 0xAD, 0xBE, 0xEF}, "+84988", encodedPosInit)
	if err == nil {
		t.Fatal("expected error for unknown session ID")
	}

	// 3. Unregister waiter
	var testId [4]byte
	copy(testId[:], []byte{1, 2, 3, 4})
	proc.RegisterWaiter(testId)
	proc.UnregisterWaiter(testId)
}

func TestProcessorAdditionalCoverage(t *testing.T) {
	store := session.NewInMemorySessionStore(1 * time.Second)
	defer store.Close()
	nimsDb := nims.NewDatabase()
	proc := posinit.NewProcessor(store, nimsDb, nil)
	ctx := context.Background()

	// 1. Hook test & Hex
	var hookCalled bool
	proc.SetOnResultHandler(func(sessID [4]byte, res *session.LocationResult) {
		hookCalled = true
	})

	sess, _ := session.NewSession("+84988999888", 30*time.Second, session.QoP{})
	sess.VER = []byte{9, 9, 9, 9, 8, 8, 8, 8}
	_ = store.SaveSession(ctx, sess)

	_ = proc.Hex(sess.SessionID)

	// South latitude cell (e.g. Sydney, MCC=505, MNC=01, LAC=10, CI=20)
	nimsDb.RegisterCell(nims.CellIdentifier{
		RadioType: nims.Radio2GGSM,
		MCC:       505,
		MNC:       1,
		TAC_LAC:   10,
		CellID:    20,
	}, nims.GeoPosition{
		Latitude:    -33.8688,
		Longitude:   151.2093,
		Altitude:    10.0,
		Uncertainty: 50.0,
		Confidence:  90,
	})

	posInitPdu := oma_ulp.ULPPDU{
		Version: oma_ulp.Version{Maj: 2, Min: 0, Servind: 0},
		SessionID: oma_ulp.SessionID{
			SlpSessionID: &oma_ulp.SlpSessionID{
				SessionID: sess.SessionID[:],
				SlpId:     oma_ulp.SLPAddress{Choice: oma_ulp.SLPAddressChoice_Fqdn, Fqdn: ptr(oma_ulp.FQDN("slp.viettel.vn"))},
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
							RefMCC: 505,
							RefMNC: 1,
							RefLAC: 10,
							RefCI:  20,
						},
					},
				},
				Ver: oma_ulp.ComputeVer([]byte("any"), []byte("any")),
			},
		},
	}
	posInitPdu.Message.MsSUPLPOSINIT.Ver.Bytes = sess.VER

	encoded, _ := oma_ulp.Marshal(&posInitPdu)

	// Test HandleGatewayFrame directly
	proc.HandleGatewayFrame(nil) // nil guard
	proc.HandleGatewayFrame(&ulpgw.UlpFrame{Payload: nil}) // empty payload guard
	proc.HandleGatewayFrame(&ulpgw.UlpFrame{
		SessionID: sess.SessionID[:],
		UeID:      sess.TargetID,
		Payload:   encoded,
	})

	time.Sleep(50 * time.Millisecond)
	if !hookCalled {
		t.Fatal("expected onResultFunc hook to be called")
	}

	// 2. Cell lookup failure test
	sessFail, _ := session.NewSession("+84988000123", 30*time.Second, session.QoP{})
	sessFail.VER = []byte{7, 7, 7, 7, 6, 6, 6, 6}
	_ = store.SaveSession(ctx, sessFail)

	// Foreign cell with no NIMS record
	posInitFail := oma_ulp.ULPPDU{
		Version: oma_ulp.Version{Maj: 2, Min: 0, Servind: 0},
		SessionID: oma_ulp.SessionID{
			SlpSessionID: &oma_ulp.SlpSessionID{
				SessionID: sessFail.SessionID[:],
				SlpId:     oma_ulp.SLPAddress{Choice: oma_ulp.SLPAddressChoice_Fqdn, Fqdn: ptr(oma_ulp.FQDN("slp.viettel.vn"))},
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
							RefMCC: 999, // unknown MCC
							RefMNC: 99,
							RefLAC: 999,
							RefCI:  999,
						},
					},
				},
				Ver: oma_ulp.ComputeVer([]byte("any"), []byte("any")),
			},
		},
	}
	posInitFail.Message.MsSUPLPOSINIT.Ver.Bytes = sessFail.VER

	encodedFail, _ := oma_ulp.Marshal(&posInitFail)
	_, err := proc.ProcessPduBytes(ctx, sessFail.SessionID[:], sessFail.TargetID, encodedFail)
	if err == nil {
		t.Fatal("expected cell lookup failure error")
	}
}
