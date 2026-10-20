package uper_test

import (
	"bytes"
	"testing"

	"slp/pkg/uper"
)

// TestX691_Clause11_2_LengthDeterminants verifies ITU-T X.691 §11.9.3 (unconstrained length determinants).
func TestX691_Clause11_2_LengthDeterminants(t *testing.T) {
	cases := []struct {
		length       int
		expectedBits int
		expectedHex  []byte
	}{
		// 1-octet format: 0 <= n <= 127 -> 0 followed by 7 bits
		{length: 0, expectedBits: 8, expectedHex: []byte{0x00}},
		{length: 1, expectedBits: 8, expectedHex: []byte{0x01}},
		{length: 5, expectedBits: 8, expectedHex: []byte{0x05}},
		{length: 127, expectedBits: 8, expectedHex: []byte{0x7F}},

		// 2-octet format: 128 <= n <= 16383 -> 10 followed by 14 bits
		// 128 = 0x80 -> 10000000 10000000 = 0x80, 0x80
		{length: 128, expectedBits: 16, expectedHex: []byte{0x80, 0x80}},
		// 500 = 0x01F4 -> 10000001 11110100 = 0x81, 0xF4
		{length: 500, expectedBits: 16, expectedHex: []byte{0x81, 0xF4}},
		// 16383 = 0x3FFF -> 10111111 11111111 = 0xBF, 0xFF
		{length: 16383, expectedBits: 16, expectedHex: []byte{0xBF, 0xFF}},

		// Fragmentation format: >= 16384 -> 11 followed by 6 bits of chunkFactor (1..4)
		// 16384 = 1 * 16384 -> 11000001 = 0xC1
		{length: 16384, expectedBits: 8, expectedHex: []byte{0xC1}},
		// 32768 = 2 * 16384 -> 11000010 = 0xC2
		{length: 32768, expectedBits: 8, expectedHex: []byte{0xC2}},
	}

	for _, tc := range cases {
		w := uper.NewBitWriter()
		if err := w.WriteLengthDeterminant(tc.length); err != nil {
			t.Fatalf("WriteLengthDeterminant(%d) error: %v", tc.length, err)
		}
		if w.BitsWritten() != tc.expectedBits {
			t.Fatalf("length %d: expected %d bits, got %d", tc.length, tc.expectedBits, w.BitsWritten())
		}
		raw := w.Bytes()
		if !bytes.Equal(raw, tc.expectedHex) {
			t.Fatalf("length %d: expected bytes %X, got %X", tc.length, tc.expectedHex, raw)
		}

		r := uper.NewBitReader(raw)
		decodedLen, err := r.ReadLengthDeterminant()
		if err != nil {
			t.Fatalf("ReadLengthDeterminant() for %d error: %v", tc.length, err)
		}
		if decodedLen != tc.length {
			t.Fatalf("decoded length mismatch: expected %d, got %d", tc.length, decodedLen)
		}
	}
}

// TestX691_Clause11_2_OpenType verifies ITU-T X.691 §11.2 (Open Type encoding).
func TestX691_Clause11_2_OpenType(t *testing.T) {
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}

	w := uper.NewBitWriter()
	if err := w.WriteOpenType(payload); err != nil {
		t.Fatalf("WriteOpenType error: %v", err)
	}

	// Open Type wire format: length determinant (4 octets = 0x04) + payload
	expected := []byte{0x04, 0xDE, 0xAD, 0xBE, 0xEF}
	if !bytes.Equal(w.Bytes(), expected) {
		t.Fatalf("OpenType wire format mismatch: expected %X, got %X", expected, w.Bytes())
	}

	r := uper.NewBitReader(w.Bytes())
	decoded, err := r.ReadOpenType()
	if err != nil {
		t.Fatalf("ReadOpenType error: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("OpenType decoded mismatch: expected %X, got %X", payload, decoded)
	}
}

// TestX691_Clause11_5_ConstrainedIntegers verifies ITU-T X.691 §11.5 bit fields.
func TestX691_Clause11_5_ConstrainedIntegers(t *testing.T) {
	// 1. Range = 1 (R = ub - lb + 1 = 1): 0 bits encoded (§11.5.3)
	{
		w := uper.NewBitWriter()
		if err := w.WriteConstrainedInt(42, 42, 42); err != nil {
			t.Fatalf("WriteConstrainedInt(42, 42, 42) error: %v", err)
		}
		if w.BitsWritten() != 0 {
			t.Fatalf("Range=1 should consume 0 bits, consumed %d", w.BitsWritten())
		}
		r := uper.NewBitReader(w.Bytes())
		val, err := r.ReadConstrainedInt(42, 42)
		if err != nil || val != 42 {
			t.Fatalf("Range=1 decode failed: val=%d, err=%v", val, err)
		}
	}

	// 2. Range = 2 (lb=0, ub=1): ceil(log2(2)) = 1 bit (§11.5.4)
	{
		w := uper.NewBitWriter()
		_ = w.WriteConstrainedInt(0, 0, 1) // 0b
		_ = w.WriteConstrainedInt(1, 0, 1) // 1b
		if w.BitsWritten() != 2 {
			t.Fatalf("expected 2 bits, got %d", w.BitsWritten())
		}
		// In first byte, bits are 0 1 000000 -> 0x40
		if w.Bytes()[0] != 0x40 {
			t.Fatalf("expected byte 0x40, got 0x%02X", w.Bytes()[0])
		}
	}

	// 3. Range = 3 (lb=0, ub=2): ceil(log2(3)) = 2 bits (§11.5.4)
	{
		w := uper.NewBitWriter()
		_ = w.WriteConstrainedInt(0, 0, 2) // 00b
		_ = w.WriteConstrainedInt(1, 0, 2) // 01b
		_ = w.WriteConstrainedInt(2, 0, 2) // 10b
		if w.BitsWritten() != 6 {
			t.Fatalf("expected 6 bits, got %d", w.BitsWritten())
		}
		// 00 01 10 00 = 0x18
		if w.Bytes()[0] != 0x18 {
			t.Fatalf("expected byte 0x18, got 0x%02X", w.Bytes()[0])
		}
	}

	// 4. Range = 256 (lb=0, ub=255): 8 bits (§11.5.5)
	{
		w := uper.NewBitWriter()
		_ = w.WriteConstrainedInt(0xAB, 0, 255)
		if w.BitsWritten() != 8 {
			t.Fatalf("expected 8 bits, got %d", w.BitsWritten())
		}
		if w.Bytes()[0] != 0xAB {
			t.Fatalf("expected byte 0xAB, got 0x%02X", w.Bytes()[0])
		}
	}

	// 5. Range = 257 (lb=0, ub=256): ceil(log2(257)) = 9 bits (§11.5.6)
	{
		w := uper.NewBitWriter()
		_ = w.WriteConstrainedInt(256, 0, 256) // 1 00000000b (9 bits)
		if w.BitsWritten() != 9 {
			t.Fatalf("expected 9 bits, got %d", w.BitsWritten())
		}
		// 10000000 00000000 -> 0x80, 0x00
		if w.Bytes()[0] != 0x80 || w.Bytes()[1] != 0x00 {
			t.Fatalf("expected 0x8000, got %X", w.Bytes())
		}
	}

	// 6. Range = 65536 (lb=0, ub=65535): ceil(log2(65536)) = 16 bits (§11.5.6)
	{
		w := uper.NewBitWriter()
		_ = w.WriteConstrainedInt(0x1234, 0, 65535)
		if w.BitsWritten() != 16 {
			t.Fatalf("expected 16 bits, got %d", w.BitsWritten())
		}
		if w.Bytes()[0] != 0x12 || w.Bytes()[1] != 0x34 {
			t.Fatalf("expected 0x1234, got %X", w.Bytes())
		}
	}

	// 7. Range > 64K: (lb=0, ub=100000), rangeVal = 100001 (ITU-T X.691 §10.5.7(b) UNALIGNED)
	// In the UNALIGNED variant, length determinant is omitted and the value is encoded as
	// a bit-field of length ceil(log2(100001)) = 17 bits.
	// For value 500: offset=500 in 17 bits.
	{
		w := uper.NewBitWriter()
		if err := w.WriteConstrainedInt(500, 0, 100000); err != nil {
			t.Fatalf("WriteConstrainedInt(500, 0, 100000) error: %v", err)
		}
		if w.BitsWritten() != 17 {
			t.Fatalf("expected 17 bits for range > 64K in UPER, got %d", w.BitsWritten())
		}
		r := uper.NewBitReader(w.Bytes())
		val, err := r.ReadConstrainedInt(0, 100000)
		if err != nil || val != 500 {
			t.Fatalf("decoded val=%d, err=%v", val, err)
		}
	}
}

// TestX691_Clause11_6_NormallySmallWholeNumber verifies ITU-T X.691 §11.6.
func TestX691_Clause11_6_NormallySmallWholeNumber(t *testing.T) {
	// Values <= 63: bit 0 followed by 6-bit value (7 bits total)
	{
		w := uper.NewBitWriter()
		_ = w.WriteNormallySmallNonNegativeWholeNumber(0) // 0 000000b
		if w.BitsWritten() != 7 {
			t.Fatalf("expected 7 bits, got %d", w.BitsWritten())
		}
		r := uper.NewBitReader(w.Bytes())
		val, err := r.ReadNormallySmallNonNegativeWholeNumber()
		if err != nil || val != 0 {
			t.Fatalf("decoded %d, err=%v", val, err)
		}
	}

	{
		w := uper.NewBitWriter()
		_ = w.WriteNormallySmallNonNegativeWholeNumber(63) // 0 111111b
		if w.BitsWritten() != 7 {
			t.Fatalf("expected 7 bits, got %d", w.BitsWritten())
		}
		// 01111110 = 0x7E
		if w.Bytes()[0] != 0x7E {
			t.Fatalf("expected 0x7E, got 0x%02X", w.Bytes()[0])
		}
		r := uper.NewBitReader(w.Bytes())
		val, err := r.ReadNormallySmallNonNegativeWholeNumber()
		if err != nil || val != 63 {
			t.Fatalf("decoded %d, err=%v", val, err)
		}
	}

	// Values >= 64: bit 1 followed by semi-constrained integer
	{
		w := uper.NewBitWriter()
		_ = w.WriteNormallySmallNonNegativeWholeNumber(100)
		r := uper.NewBitReader(w.Bytes())
		bit, _ := r.ReadBit()
		if !bit {
			t.Fatalf("expected bit 1 for value >= 64")
		}
		r2 := uper.NewBitReader(w.Bytes())
		val, err := r2.ReadNormallySmallNonNegativeWholeNumber()
		if err != nil || val != 100 {
			t.Fatalf("decoded %d, err=%v", val, err)
		}
	}
}

// TestX691_Clause11_9_Boolean verifies ITU-T X.691 §11.9 (Boolean is 1 bit).
func TestX691_Clause11_9_Boolean(t *testing.T) {
	w := uper.NewBitWriter()
	w.WriteBit(true)  // 1
	w.WriteBit(false) // 0
	w.WriteBit(true)  // 1
	if w.BitsWritten() != 3 {
		t.Fatalf("expected 3 bits, got %d", w.BitsWritten())
	}
	// 10100000 = 0xA0
	if w.Bytes()[0] != 0xA0 {
		t.Fatalf("expected 0xA0, got 0x%02X", w.Bytes()[0])
	}
}

// TestX691_Clause14_BitString verifies ITU-T X.691 §14 (BitString).
func TestX691_Clause14_BitString(t *testing.T) {
	// Fixed size: exactly 4 bits, no length determinant
	bs := uper.NewBitString([]byte{0xA0}, 4) // 1010b
	w := uper.NewBitWriter()
	if err := w.WriteBitString(bs, 4, 4); err != nil {
		t.Fatalf("WriteBitString fixed error: %v", err)
	}
	if w.BitsWritten() != 4 {
		t.Fatalf("expected 4 bits, got %d", w.BitsWritten())
	}
	if w.Bytes()[0] != 0xA0 {
		t.Fatalf("expected byte 0xA0, got 0x%02X", w.Bytes()[0])
	}

	r := uper.NewBitReader(w.Bytes())
	decodedBs, err := r.ReadBitString(4, 4)
	if err != nil {
		t.Fatalf("ReadBitString fixed error: %v", err)
	}
	if decodedBs.BitLength != 4 || decodedBs.Bytes[0] != 0xA0 {
		t.Fatalf("decoded mismatch: %+v", decodedBs)
	}
}

// TestX691_Clause15_OctetString verifies ITU-T X.691 §15 (OctetString).
func TestX691_Clause15_OctetString(t *testing.T) {
	// Fixed size 4: exactly 32 bits (4 octets), no length determinant
	data := []byte{0x12, 0x34, 0x56, 0x78}
	w := uper.NewBitWriter()
	if err := w.WriteOctetString(data, 4, 4); err != nil {
		t.Fatalf("WriteOctetString fixed error: %v", err)
	}
	if w.BitsWritten() != 32 {
		t.Fatalf("expected 32 bits, got %d", w.BitsWritten())
	}
	if !bytes.Equal(w.Bytes(), data) {
		t.Fatalf("expected %X, got %X", data, w.Bytes())
	}

	r := uper.NewBitReader(w.Bytes())
	decoded, err := r.ReadOctetString(4, 4)
	if err != nil {
		t.Fatalf("ReadOctetString error: %v", err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatalf("decoded mismatch: %X", decoded)
	}
}

// TestX691_Clause26_PermittedAlphabet verifies ITU-T X.691 §26.
func TestX691_Clause26_PermittedAlphabet(t *testing.T) {
	// Alphabet of 4 characters: "a", "b", "c", "d"
	// ceil(log2(4)) = 2 bits per character:
	// 'a' = 00b, 'b' = 01b, 'c' = 10b, 'd' = 11b
	alphabet := uper.BuildAlphabet([]string{"a-d"})
	if len(alphabet) != 4 {
		t.Fatalf("expected 4 chars in alphabet, got %d", len(alphabet))
	}

	w := uper.NewBitWriter()
	// Write fixed length 3 string: "bad"
	// 'b' (01) + 'a' (00) + 'd' (11) = 01 00 11 -> 6 bits: 01001100 = 0x4C
	if err := w.WriteVisibleString("bad", alphabet, 3, 3); err != nil {
		t.Fatalf("WriteVisibleString error: %v", err)
	}
	if w.BitsWritten() != 6 {
		t.Fatalf("expected 6 bits, got %d", w.BitsWritten())
	}
	if w.Bytes()[0] != 0x4C {
		t.Fatalf("expected byte 0x4C, got 0x%02X", w.Bytes()[0])
	}

	r := uper.NewBitReader(w.Bytes())
	decoded, err := r.ReadVisibleString(alphabet, 3, 3)
	if err != nil {
		t.Fatalf("ReadVisibleString error: %v", err)
	}
	if decoded != "bad" {
		t.Fatalf("decoded string mismatch: expected 'bad', got %q", decoded)
	}
}
