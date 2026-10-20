package uper

import "fmt"

// BitString represents an ASN.1 BIT STRING value.
type BitString struct {
	Bytes     []byte
	BitLength int
}

// NewBitString creates a new BitString from bytes and a bit length.
func NewBitString(bytes []byte, bitLength int) BitString {
	if bitLength < 0 {
		bitLength = 0
	}
	needed := (bitLength + 7) / 8
	padded := make([]byte, needed)
	copy(padded, bytes)
	return BitString{
		Bytes:     padded,
		BitLength: bitLength,
	}
}

// GetBit returns the bit at the given index (0-indexed, from MSB of byte 0).
func (bs BitString) GetBit(index int) bool {
	if index < 0 || index >= bs.BitLength {
		return false
	}
	byteIdx := index / 8
	bitIdx := 7 - (index % 8)
	return (bs.Bytes[byteIdx] & (1 << bitIdx)) != 0
}

// SetBit sets the bit at the given index.
func (bs *BitString) SetBit(index int, val bool) {
	if index < 0 || index >= bs.BitLength {
		return
	}
	byteIdx := index / 8
	bitIdx := 7 - (index % 8)
	if val {
		bs.Bytes[byteIdx] |= (1 << bitIdx)
	} else {
		bs.Bytes[byteIdx] &^= (1 << bitIdx)
	}
}

// String provides a string representation of the bitstring (e.g. '1010'B).
func (bs BitString) String() string {
	res := make([]byte, bs.BitLength)
	for i := 0; i < bs.BitLength; i++ {
		if bs.GetBit(i) {
			res[i] = '1'
		} else {
			res[i] = '0'
		}
	}
	return string(res)
}

// Null represents an ASN.1 NULL value.
type Null struct{}

// Encoder is the interface for types that can encode themselves into a UPER BitWriter.
type Encoder interface {
	EncodeUPER(w *BitWriter) error
}

// Decoder is the interface for types that can decode themselves from a UPER BitReader.
type Decoder interface {
	DecodeUPER(r *BitReader) error
}

// Marshal encodes an ASN.1 value to UPER byte slice.
func Marshal(v Encoder) ([]byte, error) {
	w := NewBitWriter()
	if err := v.EncodeUPER(w); err != nil {
		return nil, fmt.Errorf("uper marshal error: %w", err)
	}
	return w.Bytes(), nil
}

// Unmarshal decodes a UPER byte slice into an ASN.1 value.
func Unmarshal(data []byte, v Decoder) error {
	r := NewBitReader(data)
	if err := v.DecodeUPER(r); err != nil {
		return fmt.Errorf("uper unmarshal error: %w", err)
	}
	return nil
}
