package uper

import (
	"bytes"
	"testing"
)

func TestBitWriterReader(t *testing.T) {
	w := NewBitWriter()
	w.WriteBit(true)
	w.WriteBit(false)
	w.WriteBit(true)
	w.WriteBits(0x0A, 4) // 1010
	w.WriteBit(true)
	// Written: 1, 0, 1, 1, 0, 1, 0, 1 = 0xB5 (181)

	b := w.Bytes()
	if len(b) != 1 || b[0] != 0xB5 {
		t.Fatalf("expected [0xB5], got %X", b)
	}

	r := NewBitReader(b)
	b1, err := r.ReadBit()
	if err != nil || !b1 {
		t.Fatalf("read b1 failed: %v", b1)
	}
	b2, err := r.ReadBit()
	if err != nil || b2 {
		t.Fatalf("read b2 failed: %v", b2)
	}
	b3, err := r.ReadBit()
	if err != nil || !b3 {
		t.Fatalf("read b3 failed: %v", b3)
	}
	v4, err := r.ReadBits(4)
	if err != nil || v4 != 0x0A {
		t.Fatalf("read v4 failed: 0x%X", v4)
	}
	b5, err := r.ReadBit()
	if err != nil || !b5 {
		t.Fatalf("read b5 failed: %v", b5)
	}
	if r.BitsLeft() != 0 {
		t.Fatalf("expected 0 bits left, got %d", r.BitsLeft())
	}
}

func TestConstrainedInt(t *testing.T) {
	testCases := []struct {
		val int64
		lb  int64
		ub  int64
	}{
		{5, 5, 5},
		{0, 0, 1},
		{1, 0, 1},
		{0, 0, 2},
		{1, 0, 2},
		{2, 0, 2},
		{42, 0, 255},
		{12345, 0, 65535},
		{-10, -127, 128},
		{0, -8388608, 8388607},
		{-8388608, -8388608, 8388607},
		{8388607, -8388608, 8388607},
		{4294967295, 0, 4294967295},
		{1234567, 0, 4294967295},
	}

	for _, tc := range testCases {
		w := NewBitWriter()
		if err := w.WriteConstrainedInt(tc.val, tc.lb, tc.ub); err != nil {
			t.Fatalf("encode failed for %d [%d..%d]: %v", tc.val, tc.lb, tc.ub, err)
		}
		data := w.Bytes()
		r := NewBitReader(data)
		got, err := r.ReadConstrainedInt(tc.lb, tc.ub)
		if err != nil {
			t.Fatalf("decode failed for %d [%d..%d]: %v", tc.val, tc.lb, tc.ub, err)
		}
		if got != tc.val {
			t.Fatalf("for [%d..%d] expected %d, got %d", tc.lb, tc.ub, tc.val, got)
		}
	}
}

func TestNormallySmallNonNegativeWholeNumber(t *testing.T) {
	numbers := []uint64{0, 1, 5, 63, 64, 65, 100, 500, 10000}
	for _, n := range numbers {
		w := NewBitWriter()
		if err := w.WriteNormallySmallNonNegativeWholeNumber(n); err != nil {
			t.Fatalf("write normally small %d failed: %v", n, err)
		}
		r := NewBitReader(w.Bytes())
		got, err := r.ReadNormallySmallNonNegativeWholeNumber()
		if err != nil {
			t.Fatalf("read normally small %d failed: %v", n, err)
		}
		if got != n {
			t.Fatalf("expected %d, got %d", n, got)
		}
	}
}

func TestOpenType(t *testing.T) {
	payloads := [][]byte{
		{},
		[]byte("hello"),
		bytes.Repeat([]byte{0xAB, 0xCD}, 100),
		bytes.Repeat([]byte{0x55}, 1000),
	}

	for _, p := range payloads {
		w := NewBitWriter()
		// Write prefix bit to test unaligned open type
		w.WriteBit(true)
		if err := w.WriteOpenType(p); err != nil {
			t.Fatalf("write open type failed: %v", err)
		}
		w.WriteBit(false)

		r := NewBitReader(w.Bytes())
		prefix, err := r.ReadBit()
		if err != nil || !prefix {
			t.Fatalf("read prefix failed: %v", err)
		}
		got, err := r.ReadOpenType()
		if err != nil {
			t.Fatalf("read open type failed: %v", err)
		}
		if !bytes.Equal(got, p) {
			t.Fatalf("expected payload len %d, got %d", len(p), len(got))
		}
		suffix, err := r.ReadBit()
		if err != nil || suffix {
			t.Fatalf("read suffix failed: %v", err)
		}
	}
}

func TestBitString(t *testing.T) {
	// Fixed size: 64 bits
	bs := NewBitString([]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF}, 64)
	w := NewBitWriter()
	if err := w.WriteBitString(bs, 64, 64); err != nil {
		t.Fatalf("write fixed bit string: %v", err)
	}
	r := NewBitReader(w.Bytes())
	got, err := r.ReadBitString(64, 64)
	if err != nil {
		t.Fatalf("read fixed bit string: %v", err)
	}
	if got.BitLength != 64 || !bytes.Equal(got.Bytes, bs.Bytes) {
		t.Fatalf("mismatch: got %v, expected %v", got, bs)
	}

	// Variable size: 1..8 bits
	bsVar := NewBitString([]byte{0xA0}, 4) // '1010'
	w = NewBitWriter()
	if err := w.WriteBitString(bsVar, 1, 8); err != nil {
		t.Fatalf("write var bit string: %v", err)
	}
	r = NewBitReader(w.Bytes())
	gotVar, err := r.ReadBitString(1, 8)
	if err != nil {
		t.Fatalf("read var bit string: %v", err)
	}
	if gotVar.BitLength != 4 || gotVar.GetBit(0) != true || gotVar.GetBit(1) != false {
		t.Fatalf("mismatch var bit string: got %v", gotVar)
	}
}

func TestOctetString(t *testing.T) {
	// Fixed size: 4 octets
	data := []byte{192, 168, 1, 1}
	w := NewBitWriter()
	if err := w.WriteOctetString(data, 4, 4); err != nil {
		t.Fatalf("write octet string: %v", err)
	}
	r := NewBitReader(w.Bytes())
	got, err := r.ReadOctetString(4, 4)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("mismatch: got %v, want %v", got, data)
	}

	// Variable size: 1..50 octets
	vData := []byte("testing variable length octet string")
	w = NewBitWriter()
	if err := w.WriteOctetString(vData, 1, 50); err != nil {
		t.Fatalf("write var octet string: %v", err)
	}
	r = NewBitReader(w.Bytes())
	gotV, err := r.ReadOctetString(1, 50)
	if err != nil || !bytes.Equal(gotV, vData) {
		t.Fatalf("mismatch var octet string: got %s, want %s", gotV, vData)
	}
}

func TestVisibleStringWithAlphabet(t *testing.T) {
	// FQDN alphabet: "a".."z" | "A".."Z" | "0".."9" | ".-"
	alpha := BuildAlphabet([]string{"a-z", "A-Z", "0-9", ".-"})
	if len(alpha) != 64 {
		t.Fatalf("expected 64 runes in FQDN alphabet, got %d", len(alpha))
	}

	fqdn := "slp.location.operator.com"
	w := NewBitWriter()
	if err := w.WriteVisibleString(fqdn, alpha, 1, 255); err != nil {
		t.Fatalf("write fqdn: %v", err)
	}

	// 64 runes = 6 bits per rune. Length is 25 runes = 25 * 6 = 150 bits + 8 bits length = 158 bits
	r := NewBitReader(w.Bytes())
	got, err := r.ReadVisibleString(alpha, 1, 255)
	if err != nil {
		t.Fatalf("read fqdn: %v", err)
	}
	if got != fqdn {
		t.Fatalf("expected %q, got %q", fqdn, got)
	}
}
