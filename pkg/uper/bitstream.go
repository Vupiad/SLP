package uper

import (
	"fmt"
	"io"
)

// BitWriter writes bits to an in-memory buffer.
type BitWriter struct {
	data   []byte
	bitLen int
}

// NewBitWriter creates a new BitWriter.
func NewBitWriter() *BitWriter {
	return &BitWriter{
		data:   make([]byte, 0, 64),
		bitLen: 0,
	}
}

// BitsWritten returns the number of bits written so far.
func (w *BitWriter) BitsWritten() int {
	return w.bitLen
}

// WriteBit writes a single boolean bit.
func (w *BitWriter) WriteBit(b bool) {
	rem := w.bitLen % 8
	if rem == 0 {
		w.data = append(w.data, 0)
	}
	if b {
		w.data[len(w.data)-1] |= (1 << (7 - rem))
	}
	w.bitLen++
}

// WriteBits writes numBits from val (MSB of the specified bit width first).
func (w *BitWriter) WriteBits(val uint64, numBits int) {
	if numBits <= 0 {
		return
	}
	// Fast path: if bitLen is byte-aligned and numBits is a multiple of 8 (up to 64)
	if w.bitLen%8 == 0 && numBits%8 == 0 && numBits <= 64 {
		byteCount := numBits / 8
		for i := byteCount - 1; i >= 0; i-- {
			b := byte((val >> (uint(i) * 8)) & 0xFF)
			w.data = append(w.data, b)
		}
		w.bitLen += numBits
		return
	}

	for i := numBits - 1; i >= 0; i-- {
		bit := ((val >> uint(i)) & 1) != 0
		w.WriteBit(bit)
	}
}

// WriteBytes writes full bytes to the bitstream.
func (w *BitWriter) WriteBytes(bytes []byte) {
	if len(bytes) == 0 {
		return
	}
	if w.bitLen%8 == 0 {
		w.data = append(w.data, bytes...)
		w.bitLen += len(bytes) * 8
		return
	}
	for _, b := range bytes {
		w.WriteBits(uint64(b), 8)
	}
}

// AlignToByte pads with 0 bits until the bit stream is byte-aligned.
func (w *BitWriter) AlignToByte() {
	rem := w.bitLen % 8
	if rem != 0 {
		w.bitLen += (8 - rem)
	}
}

// Bytes returns the byte slice representing the written bits.
// If the written bit count is not a multiple of 8, the last byte is padded with 0 bits.
func (w *BitWriter) Bytes() []byte {
	out := make([]byte, len(w.data))
	copy(out, w.data)
	return out
}

// BitReader reads bits from an in-memory byte buffer.
type BitReader struct {
	data      []byte
	bitPos    int
	totalBits int
}

// NewBitReader creates a new BitReader.
func NewBitReader(data []byte) *BitReader {
	return &BitReader{
		data:      data,
		bitPos:    0,
		totalBits: len(data) * 8,
	}
}

// BitsLeft returns the number of bits remaining to be read.
func (r *BitReader) BitsLeft() int {
	return r.totalBits - r.bitPos
}

// BitPos returns the current bit offset.
func (r *BitReader) BitPos() int {
	return r.bitPos
}

// ReadBit reads a single bit as a bool.
func (r *BitReader) ReadBit() (bool, error) {
	if r.bitPos >= r.totalBits {
		return false, io.ErrUnexpectedEOF
	}
	byteIdx := r.bitPos / 8
	bitIdx := 7 - (r.bitPos % 8)
	val := (r.data[byteIdx] & (1 << bitIdx)) != 0
	r.bitPos++
	return val, nil
}

// ReadBits reads numBits into a uint64.
func (r *BitReader) ReadBits(numBits int) (uint64, error) {
	if numBits < 0 || numBits > 64 {
		return 0, fmt.Errorf("invalid numBits: %d", numBits)
	}
	if numBits == 0 {
		return 0, nil
	}
	if r.BitsLeft() < numBits {
		return 0, io.ErrUnexpectedEOF
	}

	// Fast path: byte-aligned and reading multiples of 8
	if r.bitPos%8 == 0 && numBits%8 == 0 {
		byteCount := numBits / 8
		startByte := r.bitPos / 8
		var res uint64
		for i := 0; i < byteCount; i++ {
			res = (res << 8) | uint64(r.data[startByte+i])
		}
		r.bitPos += numBits
		return res, nil
	}

	var res uint64
	for i := 0; i < numBits; i++ {
		b, err := r.ReadBit()
		if err != nil {
			return 0, err
		}
		res <<= 1
		if b {
			res |= 1
		}
	}
	return res, nil
}

// ReadBytes reads count full bytes from the bitstream.
func (r *BitReader) ReadBytes(count int) ([]byte, error) {
	if count < 0 {
		return nil, fmt.Errorf("invalid count: %d", count)
	}
	if count == 0 {
		return []byte{}, nil
	}
	if r.BitsLeft() < count*8 {
		return nil, io.ErrUnexpectedEOF
	}

	if r.bitPos%8 == 0 {
		start := r.bitPos / 8
		res := make([]byte, count)
		copy(res, r.data[start:start+count])
		r.bitPos += count * 8
		return res, nil
	}

	res := make([]byte, count)
	for i := 0; i < count; i++ {
		val, err := r.ReadBits(8)
		if err != nil {
			return nil, err
		}
		res[i] = byte(val)
	}
	return res, nil
}

// AlignToByte skips bits to align with the next byte boundary.
func (r *BitReader) AlignToByte() {
	rem := r.bitPos % 8
	if rem != 0 {
		r.bitPos += (8 - rem)
	}
}
