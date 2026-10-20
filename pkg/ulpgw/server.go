package ulpgw

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
)

const (
	StreamUlpMethodName = "/ulpgw.UlpGatewayService/StreamUlp"
)

// UlpGatewayServer implements the gRPC streaming service and UE edge listener
type UlpGatewayServer struct {
	grpcServer   *grpc.Server
	grpcListener net.Listener
	ueListener   net.Listener
	grpcAddr     string
	ueAddr       string

	mu           sync.RWMutex
	activeStreams map[grpc.ServerStream]chan *UlpFrame
	pendingUeResp map[string]chan []byte // Key: sessionID hex or ueID
	closed       bool
}

// NewUlpGatewayServer creates a new ULP Gateway instance
func NewUlpGatewayServer() *UlpGatewayServer {
	return &UlpGatewayServer{
		activeStreams: make(map[grpc.ServerStream]chan *UlpFrame),
		pendingUeResp: make(map[string]chan []byte),
	}
}

// Start launches the gRPC server and optional UE TCP/TLS listener
func (s *UlpGatewayServer) Start(grpcListenAddr, ueListenAddr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Start gRPC listener
	if grpcListenAddr == "" {
		grpcListenAddr = "127.0.0.1:0"
	}
	gl, err := net.Listen("tcp", grpcListenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen for grpc: %w", err)
	}
	s.grpcListener = gl
	s.grpcAddr = gl.Addr().String()

	s.grpcServer = grpc.NewServer(grpc.ForceServerCodec(UlpCodec{}))
	// Register generic stream handler for StreamUlpMethodName
	streamDesc := &grpc.StreamDesc{
		StreamName:    "StreamUlp",
		Handler:       s.handleGrpcStream,
		ServerStreams: true,
		ClientStreams: true,
	}
	s.grpcServer.RegisterService(&grpc.ServiceDesc{
		ServiceName: "ulpgw.UlpGatewayService",
		HandlerType: (*any)(nil),
		Methods:     nil,
		Streams:     []grpc.StreamDesc{*streamDesc},
		Metadata:    "ulpgw.proto",
	}, struct{}{})

	go func() {
		_ = s.grpcServer.Serve(s.grpcListener)
	}()

	// 2. Start UE edge listener if requested
	if ueListenAddr != "" {
		ul, err := net.Listen("tcp", ueListenAddr)
		if err != nil {
			s.grpcServer.Stop()
			return fmt.Errorf("failed to listen for ue connections: %w", err)
		}
		s.ueListener = ul
		s.ueAddr = ul.Addr().String()
		go s.serveUeConnections()
	}

	return nil
}

// GrpcAddr returns the listening address of the gRPC service
func (s *UlpGatewayServer) GrpcAddr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.grpcAddr
}

// UeAddr returns the listening address for UE terminals
func (s *UlpGatewayServer) UeAddr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ueAddr
}

func (s *UlpGatewayServer) handleGrpcStream(srv any, stream grpc.ServerStream) error {
	outCh := make(chan *UlpFrame, 64)

	s.mu.Lock()
	s.activeStreams[stream] = outCh
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.activeStreams, stream)
		close(outCh)
		s.mu.Unlock()
	}()

	errCh := make(chan error, 2)

	// Writer goroutine to stream
	go func() {
		for frame := range outCh {
			if err := stream.SendMsg(frame); err != nil {
				errCh <- err
				return
			}
		}
	}()

	// Reader goroutine from stream
	go func() {
		for {
			var frame UlpFrame
			if err := stream.RecvMsg(&frame); err != nil {
				errCh <- err
				return
			}

			if frame.IsHeartbeat {
				// Immediately echo heartbeat pong back to SLP client
				pong := NewHeartbeatFrame()
				select {
				case outCh <- pong:
				default:
				}
				continue
			}

			// Data frame from SLP (e.g. SUPL END response)
			sessKey := fmt.Sprintf("%x", frame.SessionID)
			s.mu.RLock()
			ch, ok := s.pendingUeResp[sessKey]
			if !ok && frame.UeID != "" {
				ch, ok = s.pendingUeResp[frame.UeID]
			}
			s.mu.RUnlock()

			if ok && ch != nil {
				select {
				case ch <- frame.Payload:
				default:
				}
			}
		}
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
}

// ForwardUeMessage pushes a message from UE into an active SLP stream
func (s *UlpGatewayServer) ForwardUeMessage(sessionID []byte, ueID string, payload []byte) (<-chan []byte, error) {
	s.mu.Lock()
	if len(s.activeStreams) == 0 {
		s.mu.Unlock()
		return nil, errors.New("no active SLP gRPC stream connected to gateway")
	}

	// Pick an active stream channel
	var targetCh chan *UlpFrame
	for _, ch := range s.activeStreams {
		targetCh = ch
		break
	}

	respCh := make(chan []byte, 1)
	key := fmt.Sprintf("%x", sessionID)
	if len(sessionID) == 0 && ueID != "" {
		key = ueID
	}
	s.pendingUeResp[key] = respCh
	s.mu.Unlock()

	frame := NewDataFrame(sessionID, ueID, payload)
	select {
	case targetCh <- frame:
		return respCh, nil
	default:
		s.mu.Lock()
		delete(s.pendingUeResp, key)
		s.mu.Unlock()
		return nil, errors.New("SLP outbound queue full")
	}
}

func (s *UlpGatewayServer) serveUeConnections() {
	for {
		conn, err := s.ueListener.Accept()
		if err != nil {
			s.mu.Lock()
			isClosed := s.closed
			s.mu.Unlock()
			if isClosed {
				return
			}
			continue
		}
		go s.handleUeConn(conn)
	}
}

func (s *UlpGatewayServer) handleUeConn(conn net.Conn) {
	defer conn.Close()

	// Read 2-byte length prefix (OMA SUPL framing)
	var lenBuf [2]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return
	}
	pduLen := binary.BigEndian.Uint16(lenBuf[:])
	if pduLen < 2 {
		return
	}

	payload := make([]byte, pduLen)
	// Some specs include length in the total, some don't.
	copy(payload[:2], lenBuf[:])
	if _, err := io.ReadFull(conn, payload[2:]); err != nil {
		return
	}

	// Forward to SLP and wait for response
	respCh, err := s.ForwardUeMessage(nil, conn.RemoteAddr().String(), payload)
	if err != nil {
		return
	}

	select {
	case respPayload := <-respCh:
		// Send response back to UE with length prefix
		respLen := uint16(len(respPayload))
		binary.BigEndian.PutUint16(lenBuf[:], respLen)
		_, _ = conn.Write(lenBuf[:])
		_, _ = conn.Write(respPayload)
	case <-time.After(30 * time.Second):
		return
	}
}

// Stop shuts down the gateway server
func (s *UlpGatewayServer) Stop() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	if s.grpcServer != nil {
		s.grpcServer.Stop()
	}
	if s.grpcListener != nil {
		_ = s.grpcListener.Close()
	}
	if s.ueListener != nil {
		_ = s.ueListener.Close()
	}
}
