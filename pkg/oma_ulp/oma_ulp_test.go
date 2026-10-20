package oma_ulp_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"slp/pkg/oma_ulp"
)

func ptr[T any](v T) *T {
	return &v
}

func TestVerComputationAndVerification(t *testing.T) {
	key := []byte("secret-slp-auth-key-1234567890")
	message := []byte("supl-init-payload-sample-bytes-for-testing")

	// 1. Compute HMAC directly for reference
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	fullHmac := mac.Sum(nil)
	expectedRaw := fullHmac[:8]

	// 2. Compute via helper
	raw := oma_ulp.ComputeVerRaw(key, message)
	if !bytes.Equal(raw, expectedRaw) {
		t.Fatalf("ComputeVerRaw mismatch: got %x, want %x", raw, expectedRaw)
	}
	if len(raw) != 8 {
		t.Fatalf("expected 8 bytes, got %d", len(raw))
	}

	// 3. Compute as *Ver
	ver := oma_ulp.ComputeVer(key, message)
	if ver == nil {
		t.Fatal("expected non-nil *Ver")
	}
	if ver.BitLength != 64 {
		t.Fatalf("expected BitLength 64, got %d", ver.BitLength)
	}
	if !bytes.Equal(ver.Bytes[:8], expectedRaw) {
		t.Fatalf("Ver bytes mismatch: got %x, want %x", ver.Bytes[:8], expectedRaw)
	}

	// 4. Verification succeeds with correct key and message
	if !oma_ulp.VerifyVer(ver, key, message) {
		t.Fatal("VerifyVer expected true, got false")
	}
	if !oma_ulp.VerifyVerAgainstRaw(ver, expectedRaw) {
		t.Fatal("VerifyVerAgainstRaw expected true, got false")
	}

	// 5. Verification fails with wrong key
	wrongKey := []byte("wrong-key-00000000000000000000")
	if oma_ulp.VerifyVer(ver, wrongKey, message) {
		t.Fatal("VerifyVer expected false for wrong key, got true")
	}

	// 6. Verification fails with tampered message
	tamperedMessage := []byte("supl-init-payload-sample-bytes-tampered!!!")
	if oma_ulp.VerifyVer(ver, key, tamperedMessage) {
		t.Fatal("VerifyVer expected false for tampered message, got true")
	}

	// 7. Nil checks
	if oma_ulp.VerifyVer(nil, key, message) {
		t.Fatal("VerifyVer expected false for nil ver")
	}
	if oma_ulp.VerifyVerAgainstRaw(nil, expectedRaw) {
		t.Fatal("VerifyVerAgainstRaw expected false for nil ver")
	}
	if oma_ulp.VerifyVerAgainstRaw(ver, []byte{1, 2, 3}) {
		t.Fatal("VerifyVerAgainstRaw expected false for short expected slice")
	}
}

func TestComputeVerForPdu(t *testing.T) {
	key := []byte("secret-slp-auth-key-5g")
	pdu := oma_ulp.ULPPDU{
		Length: 0,
		Version: oma_ulp.Version{
			Maj:     2,
			Min:     0,
			Servind: 0,
		},
		SessionID: oma_ulp.SessionID{
			SlpSessionID: &oma_ulp.SlpSessionID{
				SessionID: []byte{0x12, 0x34, 0x56, 0x78},
				SlpId: oma_ulp.SLPAddress{
					Choice: oma_ulp.SLPAddressChoice_Fqdn,
					Fqdn:   ptr(oma_ulp.FQDN("slp.viettel.vn")),
				},
			},
		},
		Message: oma_ulp.UlpMessage{
			Choice: oma_ulp.UlpMessageChoice_MsSUPLINIT,
			MsSUPLINIT: &oma_ulp.SUPLINIT{
				PosMethod: oma_ulp.PosMethod_AgpsSETassisted,
				SLPMode:   oma_ulp.SLPMode_Proxy,
			},
		},
	}

	ver, encoded, err := oma_ulp.ComputeVerForPdu(key, &pdu)
	if err != nil {
		t.Fatalf("ComputeVerForPdu error: %v", err)
	}
	if ver == nil || len(encoded) == 0 {
		t.Fatalf("unexpected nil ver or empty encoded bytes")
	}

	// Decode back
	var decoded oma_ulp.ULPPDU
	if err := oma_ulp.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if decoded.SessionID.SlpSessionID == nil || !bytes.Equal(decoded.SessionID.SlpSessionID.SessionID, []byte{0x12, 0x34, 0x56, 0x78}) {
		t.Fatal("slpSessionID mismatch in decoded PDU")
	}

	// Nil pdu test
	_, _, err = oma_ulp.ComputeVerForPdu(key, nil)
	if err == nil {
		t.Fatal("expected error for nil PDU")
	}
}

func TestGoldenSuplEndVectors(t *testing.T) {
	// Golden vector 1: StatusCode_ProtocolError -> 000c0200001483
	expectedHex := "000c0200001483"
	expectedBytes, err := hex.DecodeString(expectedHex)
	if err != nil {
		t.Fatalf("hex decode failed: %v", err)
	}

	pdu := oma_ulp.ULPPDU{
		Length: 12,
		Version: oma_ulp.Version{
			Maj:     2,
			Min:     0,
			Servind: 0,
		},
		SessionID: oma_ulp.SessionID{},
		Message: oma_ulp.UlpMessage{
			Choice: oma_ulp.UlpMessageChoice_MsSUPLEND,
			MsSUPLEND: &oma_ulp.SUPLEND{
				StatusCode: ptr(oma_ulp.StatusCode_ProtocolError),
			},
		},
	}

	encoded, err := oma_ulp.Marshal(&pdu)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if !bytes.Equal(encoded, expectedBytes) {
		t.Fatalf("golden vector mismatch: got %x, want %x", encoded, expectedBytes)
	}

	var decoded oma_ulp.ULPPDU
	if err := oma_ulp.Unmarshal(expectedBytes, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if *decoded.Message.MsSUPLEND.StatusCode != oma_ulp.StatusCode_ProtocolError {
		t.Fatalf("decoded status code mismatch: got %v", *decoded.Message.MsSUPLEND.StatusCode)
	}
}

func TestSuplPosInitRoundtrip(t *testing.T) {
	key := []byte("my-auth-key")
	dummyMsg := []byte("supl-init-raw-pdu-payload")
	computedVer := oma_ulp.ComputeVer(key, dummyMsg)

	pdu := oma_ulp.ULPPDU{
		Version: oma_ulp.Version{Maj: 2, Min: 0, Servind: 0},
		SessionID: oma_ulp.SessionID{
			SetSessionID: &oma_ulp.SetSessionID{
				SessionId: 1001,
				SetId: oma_ulp.SETId{
					Choice: oma_ulp.SETIdChoice_Msisdn,
					Msisdn: ptr([]byte{0x84, 0x98, 0x76, 0x54, 0x32, 0x10, 0x00, 0x00}),
				},
			},
			SlpSessionID: &oma_ulp.SlpSessionID{
				SessionID: []byte{0xAA, 0xBB, 0xCC, 0xDD},
				SlpId: oma_ulp.SLPAddress{
					Choice: oma_ulp.SLPAddressChoice_Fqdn,
					Fqdn:   ptr(oma_ulp.FQDN("slp.viettel.com.vn")),
				},
			},
		},
		Message: oma_ulp.UlpMessage{
			Choice: oma_ulp.UlpMessageChoice_MsSUPLPOSINIT,
			MsSUPLPOSINIT: &oma_ulp.SUPLPOSINIT{
				SETCapabilities: oma_ulp.SETCapabilities{
					PosTechnology: oma_ulp.PosTechnology{
						AgpsSETassisted: true,
						AgpsSETBased:    true,
						AutonomousGPS:   true,
					},
					PrefMethod: oma_ulp.PrefMethod_AgpsSETassistedPreferred,
					PosProtocol: oma_ulp.PosProtocol{
						Rrlp: true,
					},
				},
				LocationId: oma_ulp.LocationId{
					Status: oma_ulp.Status_Current,
					CellInfo: oma_ulp.CellInfo{
						Choice: oma_ulp.CellInfoChoice_GsmCell,
						GsmCell: &oma_ulp.GsmCellInformation{
							RefMCC: 452, // Vietnam MCC
							RefMNC: 4,   // Viettel MNC
							RefLAC: 100,
							RefCI:  500,
						},
					},
				},
				Ver: computedVer,
			},
		},
	}

	encoded, err := oma_ulp.Marshal(&pdu)
	if err != nil {
		t.Fatalf("Marshal SUPLPOSINIT failed: %v", err)
	}

	var decoded oma_ulp.ULPPDU
	if err := oma_ulp.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal SUPLPOSINIT failed: %v", err)
	}

	if decoded.Message.Choice != oma_ulp.UlpMessageChoice_MsSUPLPOSINIT {
		t.Fatalf("expected choice MsSUPLPOSINIT, got %v", decoded.Message.Choice)
	}
	posInit := decoded.Message.MsSUPLPOSINIT
	if posInit.LocationId.CellInfo.GsmCell.RefMCC != 452 {
		t.Fatalf("RefMCC mismatch: got %d", posInit.LocationId.CellInfo.GsmCell.RefMCC)
	}
	if posInit.Ver == nil {
		t.Fatal("expected Ver to be present in decoded SUPLPOSINIT")
	}
	if !bytes.Equal(posInit.Ver.Bytes[:8], computedVer.Bytes[:8]) {
		t.Fatalf("Ver bytes mismatch: got %x, want %x", posInit.Ver.Bytes[:8], computedVer.Bytes[:8])
	}
}
