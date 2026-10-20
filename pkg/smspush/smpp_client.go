package smspush

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// SMPP Command IDs
const (
	CmdGenericNack         uint32 = 0x80000000
	CmdBindTransmitter     uint32 = 0x00000002
	CmdBindTransmitterResp uint32 = 0x80000002
	CmdBindTransceiver     uint32 = 0x00000009
	CmdBindTransceiverResp uint32 = 0x80000009
	CmdUnbind              uint32 = 0x00000006
	CmdUnbindResp          uint32 = 0x80000006
	CmdSubmitSm            uint32 = 0x00000004
	CmdSubmitSmResp        uint32 = 0x80000004
	CmdEnquireLink         uint32 = 0x00000015
	CmdEnquireLinkResp     uint32 = 0x80000015
)

// SMPP Status Codes
const (
	StatusOk uint32 = 0x00000000
)

// SMPP Header
type Header struct {
	CommandLength  uint32
	CommandId      uint32
	CommandStatus  uint32
	SequenceNumber uint32
}

func (h *Header) Encode() []byte {
	b := make([]byte, 16)
	binary.BigEndian.PutUint32(b[0:4], h.CommandLength)
	binary.BigEndian.PutUint32(b[4:8], h.CommandId)
	binary.BigEndian.PutUint32(b[8:12], h.CommandStatus)
	binary.BigEndian.PutUint32(b[12:16], h.SequenceNumber)
	return b
}

func DecodeHeader(r io.Reader) (*Header, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return &Header{
		CommandLength:  binary.BigEndian.Uint32(b[0:4]),
		CommandId:      binary.BigEndian.Uint32(b[4:8]),
		CommandStatus:  binary.BigEndian.Uint32(b[8:12]),
		SequenceNumber: binary.BigEndian.Uint32(b[12:16]),
	}, nil
}

// SMPPConfig holds configuration for the SMPP connection
type SMPPConfig struct {
	Addr        string        // SMSC Host:Port
	SystemID    string        // Client System ID
	Password    string        // Client Password
	SystemType  string        // System Type (e.g. "SUPL")
	SourceAddr  string        // Default Sender Address (e.g. "SLP" or shortcode)
	DialTimeout time.Duration // Timeout for network dial
	ReadTimeout time.Duration // Timeout for network read
}

// SMPPClient manages communication with an SMSC via SMPP v3.4
type SMPPClient struct {
	config  SMPPConfig
	conn    net.Conn
	seq     uint32
	mu      sync.Mutex
	isBound bool
}

// NewSMPPClient initializes a new SMPP client
func NewSMPPClient(cfg SMPPConfig) *SMPPClient {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 5 * time.Second
	}
	return &SMPPClient{
		config: cfg,
	}
}

func (c *SMPPClient) nextSeq() uint32 {
	return atomic.AddUint32(&c.seq, 1)
}

// Connect dials the SMSC and binds as transmitter
func (c *SMPPClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		_ = c.conn.Close()
	}

	d := net.Dialer{Timeout: c.config.DialTimeout}
	conn, err := d.DialContext(ctx, "tcp", c.config.Addr)
	if err != nil {
		return fmt.Errorf("failed to dial smsc %s: %w", c.config.Addr, err)
	}
	c.conn = conn

	// Send BIND_TRANSMITTER
	seq := c.nextSeq()
	var body bytes.Buffer
	body.WriteString(c.config.SystemID)
	body.WriteByte(0)
	body.WriteString(c.config.Password)
	body.WriteByte(0)
	body.WriteString(c.config.SystemType)
	body.WriteByte(0)
	body.WriteByte(0x34) // Interface version 3.4
	body.WriteByte(0x01) // addr_ton (international)
	body.WriteByte(0x01) // addr_npi (ISDN)
	body.WriteString(c.config.SourceAddr)
	body.WriteByte(0)

	totalLen := uint32(16 + body.Len())
	hdr := Header{
		CommandLength:  totalLen,
		CommandId:      CmdBindTransmitter,
		CommandStatus:  0,
		SequenceNumber: seq,
	}

	if _, err := conn.Write(append(hdr.Encode(), body.Bytes()...)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to send bind_transmitter: %w", err)
	}

	// Read BIND_TRANSMITTER_RESP
	respHdr, err := DecodeHeader(conn)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to read bind response header: %w", err)
	}
	if respHdr.CommandId != CmdBindTransmitterResp {
		_ = conn.Close()
		return fmt.Errorf("unexpected bind response command: 0x%08x", respHdr.CommandId)
	}
	if respHdr.CommandStatus != StatusOk {
		_ = conn.Close()
		return fmt.Errorf("bind rejected with status 0x%08x", respHdr.CommandStatus)
	}

	// Read rest of body
	bodyLen := int(respHdr.CommandLength) - 16
	if bodyLen > 0 {
		discard := make([]byte, bodyLen)
		if _, err := io.ReadFull(conn, discard); err != nil {
			_ = conn.Close()
			return err
		}
	}

	c.isBound = true
	return nil
}

// SubmitWapPushSMS sends a WAP Push message containing SUPL INIT to a destination MSISDN
func (c *SMPPClient) SubmitWapPushSMS(ctx context.Context, destMsisdn string, wapPushData []byte) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isBound || c.conn == nil {
		return "", errors.New("smpp client not connected or bound")
	}

	if len(wapPushData) > 254 {
		return "", fmt.Errorf("wap push data length %d exceeds max 254 bytes", len(wapPushData))
	}

	seq := c.nextSeq()
	var body bytes.Buffer

	// service_type: "SUPL"
	body.WriteString("SUPL")
	body.WriteByte(0)

	// source_addr_ton, source_addr_npi
	body.WriteByte(0x01) // TON: International
	body.WriteByte(0x01) // NPI: ISDN
	body.WriteString(c.config.SourceAddr)
	body.WriteByte(0)

	// dest_addr_ton, dest_addr_npi
	body.WriteByte(0x01) // TON: International
	body.WriteByte(0x01) // NPI: ISDN
	body.WriteString(destMsisdn)
	body.WriteByte(0)

	// esm_class: 0x40 indicates UDHI (User Data Header Indicator) is set
	body.WriteByte(0x40)

	// protocol_id: 0
	body.WriteByte(0x00)

	// priority_flag: 1 (Urgent/Emergency)
	body.WriteByte(0x01)

	// schedule_delivery_time: null
	body.WriteByte(0x00)

	// validity_period: null
	body.WriteByte(0x00)

	// registered_delivery: 0x01 (requested)
	body.WriteByte(0x01)

	// replace_if_present_flag: 0
	body.WriteByte(0x00)

	// data_coding: 0x04 (8-bit binary data)
	body.WriteByte(0x04)

	// sm_default_msg_id: 0
	body.WriteByte(0x00)

	// sm_length
	body.WriteByte(byte(len(wapPushData)))

	// short_message
	body.Write(wapPushData)

	totalLen := uint32(16 + body.Len())
	hdr := Header{
		CommandLength:  totalLen,
		CommandId:      CmdSubmitSm,
		CommandStatus:  0,
		SequenceNumber: seq,
	}

	_ = c.conn.SetDeadline(time.Now().Add(c.config.ReadTimeout))
	if _, err := c.conn.Write(append(hdr.Encode(), body.Bytes()...)); err != nil {
		c.isBound = false
		return "", fmt.Errorf("failed to send submit_sm: %w", err)
	}

	// Read SUBMIT_SM_RESP
	respHdr, err := DecodeHeader(c.conn)
	if err != nil {
		c.isBound = false
		return "", fmt.Errorf("failed to read submit_sm_resp: %w", err)
	}
	if respHdr.CommandId != CmdSubmitSmResp {
		return "", fmt.Errorf("unexpected command id in response: 0x%08x", respHdr.CommandId)
	}
	if respHdr.CommandStatus != StatusOk {
		return "", fmt.Errorf("submit_sm failed with status 0x%08x", respHdr.CommandStatus)
	}

	// Read message_id
	respBodyLen := int(respHdr.CommandLength) - 16
	msgIdBytes := make([]byte, respBodyLen)
	if _, err := io.ReadFull(c.conn, msgIdBytes); err != nil {
		return "", err
	}
	msgId := string(bytes.TrimRight(msgIdBytes, "\x00"))
	return msgId, nil
}

// EnquireLink sends an SMPP keepalive enquiry
func (c *SMPPClient) EnquireLink(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isBound || c.conn == nil {
		return errors.New("smpp client not connected")
	}

	seq := c.nextSeq()
	hdr := Header{
		CommandLength:  16,
		CommandId:      CmdEnquireLink,
		CommandStatus:  0,
		SequenceNumber: seq,
	}

	_ = c.conn.SetDeadline(time.Now().Add(c.config.ReadTimeout))
	if _, err := c.conn.Write(hdr.Encode()); err != nil {
		c.isBound = false
		return err
	}

	respHdr, err := DecodeHeader(c.conn)
	if err != nil {
		c.isBound = false
		return err
	}
	if respHdr.CommandId != CmdEnquireLinkResp {
		return fmt.Errorf("unexpected enquire_link response: 0x%08x", respHdr.CommandId)
	}
	return nil
}

// Close closes the SMPP connection gracefully
func (c *SMPPClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		if c.isBound {
			// Best effort unbind
			seq := c.nextSeq()
			hdr := Header{
				CommandLength:  16,
				CommandId:      CmdUnbind,
				CommandStatus:  0,
				SequenceNumber: seq,
			}
			_, _ = c.conn.Write(hdr.Encode())
		}
		c.isBound = false
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}
