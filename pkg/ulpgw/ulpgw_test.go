package ulpgw_test

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"slp/pkg/ulpgw"
)

func TestUlpFrameProtobufCodec(t *testing.T) {
	frame := ulpgw.UlpFrame{
		SessionID:   []byte{0x01, 0x02, 0x03, 0x04},
		UeID:        "+84988112233",
		Payload:     []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x55, 0xAA},
		IsHeartbeat: false,
		Timestamp:   1234567890,
	}

	encoded, err := frame.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded ulpgw.UlpFrame
	if err := decoded.Unmarshal(encoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if !bytes.Equal(decoded.SessionID, frame.SessionID) {
		t.Fatalf("SessionID mismatch: got %x, want %x", decoded.SessionID, frame.SessionID)
	}
	if decoded.UeID != frame.UeID {
		t.Fatalf("UeID mismatch: got %s, want %s", decoded.UeID, frame.UeID)
	}
	if !bytes.Equal(decoded.Payload, frame.Payload) {
		t.Fatalf("Payload mismatch: got %x, want %x", decoded.Payload, frame.Payload)
	}
	if decoded.IsHeartbeat != frame.IsHeartbeat {
		t.Fatalf("IsHeartbeat mismatch: got %v, want %v", decoded.IsHeartbeat, frame.IsHeartbeat)
	}
	if decoded.Timestamp != frame.Timestamp {
		t.Fatalf("Timestamp mismatch: got %d, want %d", decoded.Timestamp, frame.Timestamp)
	}
}

func TestHeartbeatFrameCodec(t *testing.T) {
	hb := ulpgw.NewHeartbeatFrame()
	encoded, err := hb.Marshal()
	if err != nil {
		t.Fatalf("Marshal heartbeat failed: %v", err)
	}

	var decoded ulpgw.UlpFrame
	if err := decoded.Unmarshal(encoded); err != nil {
		t.Fatalf("Unmarshal heartbeat failed: %v", err)
	}

	if !decoded.IsHeartbeat {
		t.Fatal("expected IsHeartbeat true")
	}
}

func TestUlpGatewayClientServerStreaming(t *testing.T) {
	// 1. Start Server
	server := ulpgw.NewUlpGatewayServer()
	if err := server.Start("127.0.0.1:0", ""); err != nil {
		t.Fatalf("failed to start gateway server: %v", err)
	}
	defer server.Stop()

	// 2. Start Client
	cfg := ulpgw.ClientConfig{
		GatewayAddr: server.GrpcAddr(),
		Backoff: ulpgw.BackoffConfig{
			InitialInterval: 50 * time.Millisecond,
			Multiplier:      1.5,
			MaxInterval:     500 * time.Millisecond,
		},
		Keepalive: ulpgw.KeepaliveConfig{
			Interval: 100 * time.Millisecond,
			Timeout:  500 * time.Millisecond,
		},
	}
	client := ulpgw.NewUlpGatewayClient(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	receivedCh := make(chan *ulpgw.UlpFrame, 1)
	client.SetMessageHandler(func(f *ulpgw.UlpFrame) {
		receivedCh <- f
	})

	client.Start(ctx)
	defer client.Close()

	// Wait for connection
	for i := 0; i < 20; i++ {
		if client.IsConnected() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !client.IsConnected() {
		t.Fatal("client failed to connect to gateway server")
	}

	// 3. Test sending message from Gateway (UE side) -> SLP Client
	testSessID := []byte{0x12, 0x34, 0x56, 0x78}
	testPayload := []byte("supl-pos-init-bytes")
	respCh, err := server.ForwardUeMessage(testSessID, "+84988000111", testPayload)
	if err != nil {
		t.Fatalf("ForwardUeMessage failed: %v", err)
	}

	select {
	case frame := <-receivedCh:
		if !bytes.Equal(frame.SessionID, testSessID) {
			t.Fatalf("SessionID mismatch: got %x, want %x", frame.SessionID, testSessID)
		}
		if !bytes.Equal(frame.Payload, testPayload) {
			t.Fatalf("Payload mismatch: got %s, want %s", string(frame.Payload), string(testPayload))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for frame in SLP client")
	}

	// 4. Test sending response from SLP Client -> Gateway
	suplEndPayload := []byte("supl-end-response-bytes")
	if err := client.SendPdu(testSessID, "+84988000111", suplEndPayload); err != nil {
		t.Fatalf("SendPdu failed: %v", err)
	}

	select {
	case resp := <-respCh:
		if !bytes.Equal(resp, suplEndPayload) {
			t.Fatalf("response payload mismatch: got %s, want %s", string(resp), string(suplEndPayload))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response in gateway respCh")
	}
}

func TestUlpGwAdditionalCoverage(t *testing.T) {
	// 1. String(), ProtoReflect(), Reset(), Name()
	frame := ulpgw.NewDataFrame([]byte{1, 2, 3, 4}, "+84988", []byte{0xAA, 0xBB})
	if frame.String() == "" {
		t.Fatal("expected non-empty String()")
	}
	_ = frame.ProtoReflect()

	codec := ulpgw.UlpCodec{}
	if codec.Name() != ulpgw.UlpCodecName {
		t.Fatalf("codec name mismatch: %s", codec.Name())
	}

	// Codec errors
	_, err := codec.Marshal("invalid-string")
	if err == nil {
		t.Fatal("expected error marshalling string")
	}
	var strTarget string
	err = codec.Unmarshal([]byte{1, 2}, &strTarget)
	if err == nil {
		t.Fatal("expected error unmarshalling into string")
	}

	// 2. Unmarshal error cases
	var invalidFrame ulpgw.UlpFrame
	if err := invalidFrame.Unmarshal([]byte{0xFF, 0xFF}); err == nil {
		t.Fatal("expected error for corrupted protobuf bytes")
	}

	// 3. Start server with UE Edge listener
	server := ulpgw.NewUlpGatewayServer()
	if err := server.Start("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer server.Stop()

	if server.UeAddr() == "" {
		t.Fatal("expected non-empty UeAddr()")
	}
}

func TestUeEdgeTcpConnection(t *testing.T) {
	server := ulpgw.NewUlpGatewayServer()
	if err := server.Start("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer server.Stop()

	// Connect to UE Edge address
	conn, err := net.Dial("tcp", server.UeAddr())
	if err != nil {
		t.Fatalf("failed to connect to UE edge: %v", err)
	}
	defer conn.Close()

	// Send short message with 2-byte length prefix
	payload := []byte("hello-ue")
	pduLen := uint16(len(payload) + 2)
	var lenBuf [2]byte
	lenBuf[0] = byte(pduLen >> 8)
	lenBuf[1] = byte(pduLen & 0xFF)
	_, _ = conn.Write(lenBuf[:])
	_, _ = conn.Write(payload)

	time.Sleep(50 * time.Millisecond)
}
