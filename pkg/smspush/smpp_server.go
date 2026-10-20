package smspush

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
)

// SubmittedMessage holds details of an SMS received by the mock SMSC
type SubmittedMessage struct {
	SourceAddr string
	DestAddr   string
	DataCoding byte
	EsmClass   byte
	Payload    []byte
}

// MockSMSC represents a mock Short Message Service Center for unit/integration testing
type MockSMSC struct {
	listener  net.Listener
	addr      string
	mu        sync.Mutex
	messages  []SubmittedMessage
	msgIDSeq  uint32
	closed    bool
	onSubmit  func(msg SubmittedMessage)
}

// StartMockSMSC starts an in-process mock SMSC on an available port or specified address
func StartMockSMSC(listenAddr string) (*MockSMSC, error) {
	if listenAddr == "" {
		listenAddr = "127.0.0.1:0"
	}
	l, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen mock smsc: %w", err)
	}

	smsc := &MockSMSC{
		listener: l,
		addr:     l.Addr().String(),
	}

	go smsc.serve()
	return smsc, nil
}

// Addr returns the listening address of the mock SMSC
func (s *MockSMSC) Addr() string {
	return s.addr
}

// SetOnSubmitHandler registers a callback invoked when a new SMS is submitted
func (s *MockSMSC) SetOnSubmitHandler(fn func(msg SubmittedMessage)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSubmit = fn
}

// GetSubmittedMessages returns all received messages
func (s *MockSMSC) GetSubmittedMessages() []SubmittedMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := make([]SubmittedMessage, len(s.messages))
	copy(res, s.messages)
	return res
}

func (s *MockSMSC) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.mu.Lock()
			isClosed := s.closed
			s.mu.Unlock()
			if isClosed {
				return
			}
			continue
		}
		go s.handleConn(conn)
	}
}

func readCString(r io.Reader) (string, error) {
	var buf bytes.Buffer
	b := make([]byte, 1)
	for {
		if _, err := r.Read(b); err != nil {
			return "", err
		}
		if b[0] == 0 {
			break
		}
		buf.WriteByte(b[0])
	}
	return buf.String(), nil
}

func (s *MockSMSC) handleConn(conn net.Conn) {
	defer conn.Close()

	for {
		hdr, err := DecodeHeader(conn)
		if err != nil {
			return
		}

		bodyLen := int(hdr.CommandLength) - 16
		bodyBytes := make([]byte, bodyLen)
		if bodyLen > 0 {
			if _, err := io.ReadFull(conn, bodyBytes); err != nil {
				return
			}
		}

		bodyReader := bytes.NewReader(bodyBytes)

		switch hdr.CommandId {
		case CmdBindTransmitter, CmdBindTransceiver:
			// Read system_id
			sysID, _ := readCString(bodyReader)
			_ = sysID

			// Respond with BIND_RESP
			respCmd := CmdBindTransmitterResp
			if hdr.CommandId == CmdBindTransceiver {
				respCmd = CmdBindTransceiverResp
			}

			var respBody bytes.Buffer
			respBody.WriteString("MOCK_SMSC")
			respBody.WriteByte(0)

			respHdr := Header{
				CommandLength:  uint32(16 + respBody.Len()),
				CommandId:      respCmd,
				CommandStatus:  StatusOk,
				SequenceNumber: hdr.SequenceNumber,
			}
			_, _ = conn.Write(append(respHdr.Encode(), respBody.Bytes()...))

		case CmdSubmitSm:
			// Parse SUBMIT_SM fields
			_, _ = readCString(bodyReader) // service_type
			_ = bodyBytes[0]               // ton
			_ = bodyBytes[1]               // npi
			srcAddr, _ := readCString(bodyReader)
			_, _ = bodyReader.ReadByte() // dest ton
			_, _ = bodyReader.ReadByte() // dest npi
			destAddr, _ := readCString(bodyReader)

			esmClass, _ := bodyReader.ReadByte()
			_, _ = bodyReader.ReadByte() // protocol_id
			_, _ = bodyReader.ReadByte() // priority_flag
			_, _ = readCString(bodyReader) // schedule_delivery_time
			_, _ = readCString(bodyReader) // validity_period
			_, _ = bodyReader.ReadByte() // registered_delivery
			_, _ = bodyReader.ReadByte() // replace_if_present
			dataCoding, _ := bodyReader.ReadByte()
			_, _ = bodyReader.ReadByte() // sm_default_msg_id
			smLen, _ := bodyReader.ReadByte()

			payload := make([]byte, smLen)
			_, _ = io.ReadFull(bodyReader, payload)

			// Store message
			msg := SubmittedMessage{
				SourceAddr: srcAddr,
				DestAddr:   destAddr,
				DataCoding: dataCoding,
				EsmClass:   esmClass,
				Payload:    payload,
			}

			s.mu.Lock()
			s.messages = append(s.messages, msg)
			handler := s.onSubmit
			s.mu.Unlock()

			if handler != nil {
				go handler(msg)
			}

			// Generate message_id
			idNum := atomic.AddUint32(&s.msgIDSeq, 1)
			msgID := fmt.Sprintf("SMSC-MSG-%06d", idNum)

			var respBody bytes.Buffer
			respBody.WriteString(msgID)
			respBody.WriteByte(0)

			respHdr := Header{
				CommandLength:  uint32(16 + respBody.Len()),
				CommandId:      CmdSubmitSmResp,
				CommandStatus:  StatusOk,
				SequenceNumber: hdr.SequenceNumber,
			}
			_, _ = conn.Write(append(respHdr.Encode(), respBody.Bytes()...))

		case CmdEnquireLink:
			respHdr := Header{
				CommandLength:  16,
				CommandId:      CmdEnquireLinkResp,
				CommandStatus:  StatusOk,
				SequenceNumber: hdr.SequenceNumber,
			}
			_, _ = conn.Write(respHdr.Encode())

		case CmdUnbind:
			respHdr := Header{
				CommandLength:  16,
				CommandId:      CmdUnbindResp,
				CommandStatus:  StatusOk,
				SequenceNumber: hdr.SequenceNumber,
			}
			_, _ = conn.Write(respHdr.Encode())
			return

		default:
			// Generic Nack
			respHdr := Header{
				CommandLength:  16,
				CommandId:      CmdGenericNack,
				CommandStatus:  0x00000003, // Invalid Command ID
				SequenceNumber: hdr.SequenceNumber,
			}
			_, _ = conn.Write(respHdr.Encode())
		}
	}
}

// Stop shuts down the mock SMSC listener
func (s *MockSMSC) Stop() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.listener.Close()
}
