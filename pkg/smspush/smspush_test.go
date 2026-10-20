package smspush_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"slp/pkg/smspush"
)

func TestWapPushPackaging(t *testing.T) {
	sampleSuplInit := []byte{0x00, 0x1A, 0x02, 0x00, 0x00, 0x10, 0x20, 0x30, 0x40, 0x50}
	tid := byte(0x42)

	smsUserData := smspush.BuildWapPushSmsPackage(tid, sampleSuplInit)
	if len(smsUserData) <= len(sampleSuplInit) {
		t.Fatalf("unexpected sms package length: %d", len(smsUserData))
	}

	extracted, err := smspush.ExtractSuplInitFromSmsUserData(smsUserData)
	if err != nil {
		t.Fatalf("ExtractSuplInitFromSmsUserData failed: %v", err)
	}

	if !bytes.Equal(extracted, sampleSuplInit) {
		t.Fatalf("extracted SUPL INIT mismatch: got %x, want %x", extracted, sampleSuplInit)
	}
}

func TestWspPduEncodingDecoding(t *testing.T) {
	payload := []byte("raw-supl-init-data")
	tid := byte(0x05)

	encoded := smspush.EncodeWspPushPdu(tid, payload)
	msg, err := smspush.ParseWspPushPdu(encoded)
	if err != nil {
		t.Fatalf("ParseWspPushPdu failed: %v", err)
	}

	if msg.TID != tid {
		t.Fatalf("TID mismatch: got %d, want %d", msg.TID, tid)
	}
	if msg.PduType != smspush.PduTypePush {
		t.Fatalf("PduType mismatch: got %d, want %d", msg.PduType, smspush.PduTypePush)
	}
	if !bytes.Equal(msg.Payload, payload) {
		t.Fatalf("payload mismatch: got %s, want %s", string(msg.Payload), string(payload))
	}
}

func TestSmppClientWithMockSMSC(t *testing.T) {
	smsc, err := smspush.StartMockSMSC("")
	if err != nil {
		t.Fatalf("failed to start mock smsc: %v", err)
	}
	defer smsc.Stop()

	cfg := smspush.SMPPConfig{
		Addr:        smsc.Addr(),
		SystemID:    "SLP_CLIENT",
		Password:    "SECRET",
		SystemType:  "SUPL",
		SourceAddr:  "1900",
		DialTimeout: 2 * time.Second,
		ReadTimeout: 2 * time.Second,
	}

	client := smspush.NewSMPPClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("client.Connect failed: %v", err)
	}
	defer client.Close()

	// Test EnquireLink
	if err := client.EnquireLink(ctx); err != nil {
		t.Fatalf("client.EnquireLink failed: %v", err)
	}

	// Test SubmitWapPushSMS
	destMsisdn := "+84988123456"
	wapPayload := smspush.BuildWapPushSmsPackage(0x01, []byte{0xDE, 0xAD, 0xBE, 0xEF})
	msgID, err := client.SubmitWapPushSMS(ctx, destMsisdn, wapPayload)
	if err != nil {
		t.Fatalf("SubmitWapPushSMS failed: %v", err)
	}
	if msgID == "" {
		t.Fatal("expected non-empty msgID")
	}

	// Verify server received message
	time.Sleep(50 * time.Millisecond)
	received := smsc.GetSubmittedMessages()
	if len(received) != 1 {
		t.Fatalf("expected 1 message received by SMSC, got %d", len(received))
	}
	if received[0].DestAddr != destMsisdn {
		t.Fatalf("dest addr mismatch: got %s, want %s", received[0].DestAddr, destMsisdn)
	}
	if received[0].EsmClass != 0x40 {
		t.Fatalf("esm_class mismatch: got 0x%02x, want 0x40 (UDHI)", received[0].EsmClass)
	}
	if !bytes.Equal(received[0].Payload, wapPayload) {
		t.Fatalf("payload mismatch in SMSC")
	}
}

func TestWapPushErrorCases(t *testing.T) {
	// 1. Short user data
	_, err := smspush.ExtractSuplInitFromSmsUserData([]byte{0x01, 0x02})
	if err == nil {
		t.Fatal("expected error for short user data")
	}

	// 2. Invalid UDH length
	_, err = smspush.ExtractSuplInitFromSmsUserData([]byte{0x20, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06})
	if err == nil {
		t.Fatal("expected error for invalid UDH length")
	}

	// 3. Short WSP data
	_, err = smspush.ParseWspPushPdu([]byte{0x01, 0x02})
	if err == nil {
		t.Fatal("expected error for short wsp data")
	}

	// 4. Unsupported PDU type (not 0x06)
	_, err = smspush.ParseWspPushPdu([]byte{0x01, 0x07, 0x00})
	if err == nil {
		t.Fatal("expected error for unsupported pdu type")
	}

	// 5. Invalid header length
	_, err = smspush.ParseWspPushPdu([]byte{0x01, 0x06, 0x20, 0x00})
	if err == nil {
		t.Fatal("expected error for invalid header length")
	}

	// 6. ASCII text content type in WSP
	textWsp := []byte{
		0x01, // TID
		0x06, // Type Push
		32,   // Header len
	}
	textWsp = append(textWsp, []byte("application/vnd.omaloc-supl-init")...)
	textWsp = append(textWsp, []byte("payload-body")...)
	msg, err := smspush.ParseWspPushPdu(textWsp)
	if err != nil || msg.ContentType != "application/vnd.omaloc-supl-init" {
		t.Fatalf("failed to parse ascii text content type: %v, msg: %+v", err, msg)
	}

	// 7. Text content type with null byte
	textWspNull := []byte{0x02, 0x06, 33}
	textWspNull = append(textWspNull, []byte("application/vnd.omaloc-supl-init\x00")...)
	textWspNull = append(textWspNull, []byte("payload-body-2")...)
	msgNull, err := smspush.ParseWspPushPdu(textWspNull)
	if err != nil || msgNull.ContentType != "application/vnd.omaloc-supl-init" {
		t.Fatalf("failed to parse ascii text content type with null: %v, msg: %+v", err, msgNull)
	}
}

func TestSmppClientErrorCases(t *testing.T) {
	// 1. Unbound client submit
	client := smspush.NewSMPPClient(smspush.SMPPConfig{
		Addr: "127.0.0.1:9999",
	})
	ctx := context.Background()

	_, err := client.SubmitWapPushSMS(ctx, "+84988", []byte{1, 2, 3})
	if err == nil {
		t.Fatal("expected error submitting via unbound client")
	}

	if err := client.EnquireLink(ctx); err == nil {
		t.Fatal("expected error enquireLink on unbound client")
	}

	// 2. Large payload > 254 bytes
	hugePayload := make([]byte, 300)
	// Even if bound, it rejects > 254 bytes
	// Connect to mock smsc first
	smsc, _ := smspush.StartMockSMSC("")
	defer smsc.Stop()

	boundClient := smspush.NewSMPPClient(smspush.SMPPConfig{
		Addr: smsc.Addr(),
	})
	_ = boundClient.Connect(ctx)
	defer boundClient.Close()

	_, err = boundClient.SubmitWapPushSMS(ctx, "+84988", hugePayload)
	if err == nil {
		t.Fatal("expected error for payload > 254 bytes")
	}
}

func TestSmscOnSubmitHandler(t *testing.T) {
	smsc, _ := smspush.StartMockSMSC("")
	defer smsc.Stop()

	callbackCh := make(chan string, 1)
	smsc.SetOnSubmitHandler(func(msg smspush.SubmittedMessage) {
		callbackCh <- msg.DestAddr
	})

	client := smspush.NewSMPPClient(smspush.SMPPConfig{
		Addr: smsc.Addr(),
	})
	ctx := context.Background()
	_ = client.Connect(ctx)
	defer client.Close()

	_, _ = client.SubmitWapPushSMS(ctx, "+84988111999", []byte{1, 2, 3})

	select {
	case dest := <-callbackCh:
		if dest != "+84988111999" {
			t.Fatalf("dest mismatch: %s", dest)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("onSubmit callback not called")
	}
}
