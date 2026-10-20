package uper

import (
	"fmt"
	"io"
)

// WriteLengthDeterminant encodes an unconstrained length determinant (ITU-T X.691 §11.9.3).
func (w *BitWriter) WriteLengthDeterminant(length int) error {
	if length < 0 {
		return fmt.Errorf("negative length determinant: %d", length)
	}

	if length <= 127 {
		// 1 octet: 0 followed by 7 bits
		w.WriteBits(uint64(length), 8)
		return nil
	}

	if length <= 16383 {
		// 2 octets: 10 followed by 14 bits
		val := (2 << 14) | uint64(length)
		w.WriteBits(val, 16)
		return nil
	}

	// Fragmentation for lengths >= 16K (ITU-T X.691 §11.9.3.8)
	// For SUPL/telecom PDUs, messages larger than 16K are chunked by caller in OpenType or OctetString.
	// But for a single length determinant, standard 16K chunks:
	chunkFactor := length / 16384
	if chunkFactor > 4 {
		chunkFactor = 4
	}
	val := (3 << 6) | uint64(chunkFactor)
	w.WriteBits(val, 8)
	return nil
}

// ReadLengthDeterminant decodes an unconstrained length determinant (ITU-T X.691 §11.9.3).
func (r *BitReader) ReadLengthDeterminant() (int, error) {
	if r.BitsLeft() < 8 {
		return 0, io.ErrUnexpectedEOF
	}

	b, err := r.ReadBits(8)
	if err != nil {
		return 0, err
	}

	if (b & 0x80) == 0 {
		// 1 octet: 0xxxxxxx
		return int(b & 0x7F), nil
	}

	if (b & 0xC0) == 0x80 {
		// 2 octets: 10xxxxxx xxxxxxxx
		nextByte, err := r.ReadBits(8)
		if err != nil {
			return 0, err
		}
		val := (int(b&0x3F) << 8) | int(nextByte)
		return val, nil
	}

	// Fragmented: 11xxxxxx
	chunkFactor := int(b & 0x3F)
	if chunkFactor < 1 || chunkFactor > 4 {
		return 0, fmt.Errorf("invalid fragmentation chunk factor: %d", chunkFactor)
	}
	return chunkFactor * 16384, nil
}

// WriteOpenType encodes an Open Type value (ITU-T X.691 §11.2).
// In UPER, the open type payload is byte-oriented, prefixed by an unconstrained
// length determinant (in octets), and followed by the octets as a bit field.
func (w *BitWriter) WriteOpenType(payload []byte) error {
	length := len(payload)
	if length <= 16383 {
		if err := w.WriteLengthDeterminant(length); err != nil {
			return err
		}
		w.WriteBytes(payload)
		return nil
	}

	// Fragmented open type
	rem := payload
	for len(rem) >= 16384 {
		chunkFactor := len(rem) / 16384
		if chunkFactor > 4 {
			chunkFactor = 4
		}
		chunkSize := chunkFactor * 16384
		val := (3 << 6) | uint64(chunkFactor)
		w.WriteBits(val, 8)
		w.WriteBytes(rem[:chunkSize])
		rem = rem[chunkSize:]
	}

	// Write remaining
	if err := w.WriteLengthDeterminant(len(rem)); err != nil {
		return err
	}
	w.WriteBytes(rem)
	return nil
}

// ReadOpenType decodes an Open Type value (ITU-T X.691 §11.2).
func (r *BitReader) ReadOpenType() ([]byte, error) {
	lenDet, err := r.ReadLengthDeterminant()
	if err != nil {
		return nil, err
	}

	if lenDet < 16384 {
		return r.ReadBytes(lenDet)
	}

	// Fragmented read
	var result []byte
	chunk := lenDet
	for {
		data, err := r.ReadBytes(chunk)
		if err != nil {
			return nil, err
		}
		result = append(result, data...)
		if chunk < 16384 {
			break
		}
		chunk, err = r.ReadLengthDeterminant()
		if err != nil {
			return nil, err
		}
		if chunk == 0 {
			break
		}
	}
	return result, nil
}
