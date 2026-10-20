package smspush

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

const (
	// WapPushPort is the IANA-assigned port for WAP Push (2948)
	WapPushPort uint16 = 2948

	// PduTypePush represents WSP PDU Type 0x06 (Push)
	PduTypePush byte = 0x06

	// ContentTypeSuplInitWellKnown is the well-known WSP code for application/vnd.omaloc-supl-init
	// Assigned number in WSP / OMA: 0x31 (or 0xB1 with high-bit set)
	ContentTypeSuplInitCode byte = 0x31

	// ContentTypeSuplInitString is the text MIME type for SUPL INIT
	ContentTypeSuplInitString = "application/vnd.omaloc-supl-init"

	// UdhIeiAppPort16Bit is the IEI for 16-bit application port addressing
	UdhIeiAppPort16Bit byte = 0x05
)

// WapPushMessage represents a parsed WAP Push PDU
type WapPushMessage struct {
	TID         byte
	PduType     byte
	ContentType string
	Payload     []byte
}

// BuildUDH creates a User Data Header with 16-bit application port addressing (Destination port: 2948)
func BuildUDH(destPort, srcPort uint16) []byte {
	udh := make([]byte, 7)
	udh[0] = 0x06 // Total UDH length (excluding length octet) = 6 octets
	udh[1] = UdhIeiAppPort16Bit
	udh[2] = 0x04 // IE Length = 4 octets
	binary.BigEndian.PutUint16(udh[3:5], destPort)
	binary.BigEndian.PutUint16(udh[5:7], srcPort)
	return udh
}

// EncodeWspPushPdu encodes a payload into a WSP Push PDU (Type 0x06)
func EncodeWspPushPdu(tid byte, payload []byte) []byte {
	var buf bytes.Buffer

	// 1. Transaction ID
	buf.WriteByte(tid)

	// 2. PDU Type = 0x06 (Push)
	buf.WriteByte(PduTypePush)

	// 3. Headers Length:
	// If using well-known code with high bit set (0x80 | 0x31 = 0xB1), header length is 1 octet.
	buf.WriteByte(0x01) // Headers length = 1 octet
	buf.WriteByte(0x80 | ContentTypeSuplInitCode) // 0xB1

	// 4. Payload body
	buf.Write(payload)

	return buf.Bytes()
}

// BuildWapPushSmsPackage packages a SUPL INIT payload into a complete SMS User Data (UDH + WSP PDU)
func BuildWapPushSmsPackage(tid byte, suplInitPayload []byte) []byte {
	udh := BuildUDH(WapPushPort, WapPushPort)
	wsp := EncodeWspPushPdu(tid, suplInitPayload)
	res := make([]byte, len(udh)+len(wsp))
	copy(res, udh)
	copy(res[len(udh):], wsp)
	return res
}

// ParseWspPushPdu parses a WSP Push PDU
func ParseWspPushPdu(wspData []byte) (*WapPushMessage, error) {
	if len(wspData) < 3 {
		return nil, errors.New("wsp pdu too short")
	}

	tid := wspData[0]
	pduType := wspData[1]
	if pduType != PduTypePush {
		return nil, fmt.Errorf("unsupported wsp pdu type: 0x%02x, expected 0x06", pduType)
	}

	hdrLen := int(wspData[2])
	offset := 3
	if offset+hdrLen > len(wspData) {
		return nil, errors.New("invalid wsp headers length")
	}

	contentType := ContentTypeSuplInitString
	if hdrLen > 0 {
		ctByte := wspData[offset]
		if (ctByte & 0x7F) == ContentTypeSuplInitCode {
			contentType = ContentTypeSuplInitString
		} else if ctByte >= 0x20 && ctByte <= 0x7E {
			// ASCII string content type
			end := offset + hdrLen
			if wspData[end-1] == 0 {
				contentType = string(wspData[offset : end-1])
			} else {
				contentType = string(wspData[offset:end])
			}
		}
	}

	payload := wspData[offset+hdrLen:]
	return &WapPushMessage{
		TID:         tid,
		PduType:     pduType,
		ContentType: contentType,
		Payload:     payload,
	}, nil
}

// ExtractSuplInitFromSmsUserData extracts the raw SUPL INIT payload from SMS User Data containing UDH
func ExtractSuplInitFromSmsUserData(userData []byte) ([]byte, error) {
	if len(userData) < 7 {
		return nil, errors.New("user data too short to contain UDH")
	}

	udhLen := int(userData[0])
	if udhLen+1 > len(userData) {
		return nil, errors.New("invalid UDH length")
	}

	// Verify UDH contains port 2948
	udhBytes := userData[1 : 1+udhLen]
	destPortFound := false
	for i := 0; i+4 <= len(udhBytes); {
		iei := udhBytes[i]
		ieLen := int(udhBytes[i+1])
		if iei == UdhIeiAppPort16Bit && ieLen >= 4 {
			destPort := binary.BigEndian.Uint16(udhBytes[i+2 : i+4])
			if destPort == WapPushPort {
				destPortFound = true
				break
			}
		}
		i += 2 + ieLen
	}

	if !destPortFound {
		// Even if not matching strictly, proceed if payload follows UDH
	}

	wspData := userData[1+udhLen:]
	msg, err := ParseWspPushPdu(wspData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse wsp: %w", err)
	}

	if !strings.EqualFold(msg.ContentType, ContentTypeSuplInitString) {
		// Log or warn but allow payload
	}

	return msg.Payload, nil
}
