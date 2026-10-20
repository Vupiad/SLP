package ulpgw

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"google.golang.org/grpc/encoding"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// UlpFrame represents a message exchanged between ULP Gateway and SLP
type UlpFrame struct {
	SessionID   []byte // 4-byte slpSessionId
	UeID        string // Target MSISDN or IP
	Payload     []byte // UPER-encoded ULP PDU (e.g. SUPL POS INIT or SUPL END)
	IsHeartbeat bool   // Keepalive ping/pong flag
	Timestamp   int64  // Unix nano timestamp
}

// ProtoReflect is implemented to satisfy proto.Message
func (m *UlpFrame) ProtoReflect() protoreflect.Message {
	return nil
}

// Reset is implemented to satisfy proto.Message
func (m *UlpFrame) Reset() {
	*m = UlpFrame{}
}

// String provides a human-readable representation
func (m *UlpFrame) String() string {
	return fmt.Sprintf("UlpFrame{SessionID: %x, UeID: %s, PayloadLen: %d, Heartbeat: %v}",
		m.SessionID, m.UeID, len(m.Payload), m.IsHeartbeat)
}

// Marshal encodes UlpFrame into standard protobuf wire format:
// Tag 1 (bytes): SessionID
// Tag 2 (string): UeID
// Tag 3 (bytes): Payload
// Tag 4 (varint): IsHeartbeat
// Tag 5 (varint): Timestamp
func (m *UlpFrame) Marshal() ([]byte, error) {
	var buf []byte

	// Field 1: SessionID (tag = 1, type = 2: bytes)
	if len(m.SessionID) > 0 {
		buf = appendVarint(buf, (1<<3)|2)
		buf = appendVarint(buf, uint64(len(m.SessionID)))
		buf = append(buf, m.SessionID...)
	}

	// Field 2: UeID (tag = 2, type = 2: bytes)
	if len(m.UeID) > 0 {
		buf = appendVarint(buf, (2<<3)|2)
		buf = appendVarint(buf, uint64(len(m.UeID)))
		buf = append(buf, m.UeID...)
	}

	// Field 3: Payload (tag = 3, type = 2: bytes)
	if len(m.Payload) > 0 {
		buf = appendVarint(buf, (3<<3)|2)
		buf = appendVarint(buf, uint64(len(m.Payload)))
		buf = append(buf, m.Payload...)
	}

	// Field 4: IsHeartbeat (tag = 4, type = 0: varint)
	if m.IsHeartbeat {
		buf = appendVarint(buf, (4<<3)|0)
		buf = append(buf, 1)
	}

	// Field 5: Timestamp (tag = 5, type = 0: varint)
	if m.Timestamp != 0 {
		buf = appendVarint(buf, (5<<3)|0)
		buf = appendVarint(buf, uint64(m.Timestamp))
	}

	return buf, nil
}

// Unmarshal decodes protobuf wire format into UlpFrame
func (m *UlpFrame) Unmarshal(data []byte) error {
	m.Reset()
	idx := 0
	for idx < len(data) {
		tagVarint, n := binary.Uvarint(data[idx:])
		if n <= 0 {
			return errors.New("invalid protobuf field tag")
		}
		idx += n

		fieldNum := tagVarint >> 3
		wireType := tagVarint & 0x07

		switch wireType {
		case 0: // Varint
			val, vn := binary.Uvarint(data[idx:])
			if vn <= 0 {
				return errors.New("invalid varint value")
			}
			idx += vn
			if fieldNum == 4 {
				m.IsHeartbeat = (val != 0)
			} else if fieldNum == 5 {
				m.Timestamp = int64(val)
			}
		case 2: // Length-delimited
			length, ln := binary.Uvarint(data[idx:])
			if ln <= 0 {
				return errors.New("invalid length-delimited field length")
			}
			idx += ln
			if idx+int(length) > len(data) {
				return io.ErrUnexpectedEOF
			}
			bytesVal := data[idx : idx+int(length)]
			idx += int(length)

			switch fieldNum {
			case 1:
				m.SessionID = make([]byte, len(bytesVal))
				copy(m.SessionID, bytesVal)
			case 2:
				m.UeID = string(bytesVal)
			case 3:
				m.Payload = make([]byte, len(bytesVal))
				copy(m.Payload, bytesVal)
			}
		default:
			return fmt.Errorf("unsupported wire type: %d", wireType)
		}
	}
	return nil
}

func appendVarint(buf []byte, v uint64) []byte {
	var temp [10]byte
	n := binary.PutUvarint(temp[:], v)
	return append(buf, temp[:n]...)
}

// UlpCodec implements gRPC encoding.Codec
type UlpCodec struct{}

const UlpCodecName = "ulp-proto"

func (UlpCodec) Marshal(v any) ([]byte, error) {
	if frame, ok := v.(*UlpFrame); ok {
		return frame.Marshal()
	}
	return nil, fmt.Errorf("cannot marshal type %T, expected *UlpFrame", v)
}

func (UlpCodec) Unmarshal(data []byte, v any) error {
	if frame, ok := v.(*UlpFrame); ok {
		return frame.Unmarshal(data)
	}
	return fmt.Errorf("cannot unmarshal into type %T, expected *UlpFrame", v)
}

func (UlpCodec) Name() string {
	return UlpCodecName
}

func init() {
	encoding.RegisterCodec(UlpCodec{})
}

// NewHeartbeatFrame creates a keepalive ping/pong frame
func NewHeartbeatFrame() *UlpFrame {
	return &UlpFrame{
		IsHeartbeat: true,
		Timestamp:   time.Now().UnixNano(),
	}
}

// NewDataFrame creates a data frame containing a UPER-encoded PDU
func NewDataFrame(sessionID []byte, ueID string, payload []byte) *UlpFrame {
	return &UlpFrame{
		SessionID: sessionID,
		UeID:      ueID,
		Payload:   payload,
		Timestamp: time.Now().UnixNano(),
	}
}
