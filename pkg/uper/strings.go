package uper

import (
	"fmt"
	"sort"
)

// Default VisibleString alphabet (ASCII 32 to 126).
var defaultVisibleStringAlphabet []rune

func init() {
	defaultVisibleStringAlphabet = make([]rune, 126-32+1)
	for i := 32; i <= 126; i++ {
		defaultVisibleStringAlphabet[i-32] = rune(i)
	}
}

// WriteBitString encodes a BIT STRING with size constraint [lb, ub].
func (w *BitWriter) WriteBitString(bs BitString, lb int, ub int) error {
	if lb == ub {
		if bs.BitLength != lb {
			// adjust or pad/truncate
			if bs.BitLength < lb {
				bs = NewBitString(bs.Bytes, lb)
			}
		}
		// Fixed length: encode exactly lb bits
		for i := 0; i < lb; i++ {
			w.WriteBit(bs.GetBit(i))
		}
		return nil
	}

	// Variable length: encode length then bits
	if bs.BitLength < lb || bs.BitLength > ub {
		return fmt.Errorf("bit string length %d out of bounds [%d, %d]", bs.BitLength, lb, ub)
	}
	if err := w.WriteConstrainedInt(int64(bs.BitLength), int64(lb), int64(ub)); err != nil {
		return err
	}
	for i := 0; i < bs.BitLength; i++ {
		w.WriteBit(bs.GetBit(i))
	}
	return nil
}

// ReadBitString decodes a BIT STRING with size constraint [lb, ub].
func (r *BitReader) ReadBitString(lb int, ub int) (BitString, error) {
	bitLen := lb
	if lb != ub {
		l, err := r.ReadConstrainedInt(int64(lb), int64(ub))
		if err != nil {
			return BitString{}, err
		}
		bitLen = int(l)
	}

	byteCount := (bitLen + 7) / 8
	buf := make([]byte, byteCount)
	bs := BitString{
		Bytes:     buf,
		BitLength: bitLen,
	}
	for i := 0; i < bitLen; i++ {
		b, err := r.ReadBit()
		if err != nil {
			return BitString{}, err
		}
		bs.SetBit(i, b)
	}
	return bs, nil
}

// WriteOctetString encodes an OCTET STRING with size constraint [lb, ub].
func (w *BitWriter) WriteOctetString(b []byte, lb int, ub int) error {
	length := len(b)
	if lb == ub {
		if length != lb {
			return fmt.Errorf("octet string length %d does not match fixed size %d", length, lb)
		}
		w.WriteBytes(b)
		return nil
	}

	if length < lb || length > ub {
		return fmt.Errorf("octet string length %d out of bounds [%d, %d]", length, lb, ub)
	}
	if err := w.WriteConstrainedInt(int64(length), int64(lb), int64(ub)); err != nil {
		return err
	}
	w.WriteBytes(b)
	return nil
}

// ReadOctetString decodes an OCTET STRING with size constraint [lb, ub].
func (r *BitReader) ReadOctetString(lb int, ub int) ([]byte, error) {
	length := lb
	if lb != ub {
		l, err := r.ReadConstrainedInt(int64(lb), int64(ub))
		if err != nil {
			return nil, err
		}
		length = int(l)
	}
	return r.ReadBytes(length)
}

// WriteIA5String encodes an IA5String with size constraint [lb, ub].
func (w *BitWriter) WriteIA5String(s string, lb int, ub int) error {
	length := len(s)
	if lb == ub {
		if length != lb {
			return fmt.Errorf("IA5String length %d does not match fixed size %d", length, lb)
		}
	} else {
		if length < lb || length > ub {
			return fmt.Errorf("IA5String length %d out of bounds [%d, %d]", length, lb, ub)
		}
		if err := w.WriteConstrainedInt(int64(length), int64(lb), int64(ub)); err != nil {
			return err
		}
	}

	for _, ch := range []byte(s) {
		if ch > 127 {
			return fmt.Errorf("invalid IA5String character: 0x%02X", ch)
		}
		w.WriteBits(uint64(ch), 7)
	}
	return nil
}

// ReadIA5String decodes an IA5String with size constraint [lb, ub].
func (r *BitReader) ReadIA5String(lb int, ub int) (string, error) {
	length := lb
	if lb != ub {
		l, err := r.ReadConstrainedInt(int64(lb), int64(ub))
		if err != nil {
			return "", err
		}
		length = int(l)
	}

	buf := make([]byte, length)
	for i := 0; i < length; i++ {
		ch, err := r.ReadBits(7)
		if err != nil {
			return "", err
		}
		buf[i] = byte(ch)
	}
	return string(buf), nil
}

// BuildAlphabet creates a sorted rune slice representing the permitted alphabet.
func BuildAlphabet(ranges []string) []rune {
	runeSet := make(map[rune]bool)
	for _, item := range ranges {
		if len(item) == 3 && item[1] == '-' { // "a-z"
			start := rune(item[0])
			end := rune(item[2])
			for r := start; r <= end; r++ {
				runeSet[r] = true
			}
		} else {
			for _, r := range item {
				runeSet[r] = true
			}
		}
	}

	alphabet := make([]rune, 0, len(runeSet))
	for r := range runeSet {
		alphabet = append(alphabet, r)
	}
	sort.Slice(alphabet, func(i, j int) bool {
		return alphabet[i] < alphabet[j]
	})
	return alphabet
}

// WriteVisibleString encodes a VisibleString with alphabet and size constraints.
func (w *BitWriter) WriteVisibleString(s string, alphabet []rune, lb int, ub int) error {
	if len(alphabet) == 0 {
		alphabet = defaultVisibleStringAlphabet
	}

	length := len(s)
	if lb == ub {
		if length != lb {
			return fmt.Errorf("VisibleString length %d does not match fixed size %d", length, lb)
		}
	} else {
		if length < lb || length > ub {
			return fmt.Errorf("VisibleString length %d out of bounds [%d, %d]", length, lb, ub)
		}
		if err := w.WriteConstrainedInt(int64(length), int64(lb), int64(ub)); err != nil {
			return err
		}
	}

	numBits := BitsNeeded(uint64(len(alphabet)))

	// Pre-map rune to index for O(1) lookup
	alphaMap := make(map[rune]uint64, len(alphabet))
	for idx, r := range alphabet {
		alphaMap[r] = uint64(idx)
	}

	for _, ch := range s {
		idx, ok := alphaMap[ch]
		if !ok {
			return fmt.Errorf("character %q not in permitted alphabet", ch)
		}
		w.WriteBits(idx, numBits)
	}
	return nil
}

// ReadVisibleString decodes a VisibleString with alphabet and size constraints.
func (r *BitReader) ReadVisibleString(alphabet []rune, lb int, ub int) (string, error) {
	if len(alphabet) == 0 {
		alphabet = defaultVisibleStringAlphabet
	}

	length := lb
	if lb != ub {
		l, err := r.ReadConstrainedInt(int64(lb), int64(ub))
		if err != nil {
			return "", err
		}
		length = int(l)
	}

	numBits := BitsNeeded(uint64(len(alphabet)))
	runes := make([]rune, length)
	for i := 0; i < length; i++ {
		idx, err := r.ReadBits(numBits)
		if err != nil {
			return "", err
		}
		if int(idx) >= len(alphabet) {
			return "", fmt.Errorf("alphabet index %d out of range %d", idx, len(alphabet))
		}
		runes[i] = alphabet[idx]
	}
	return string(runes), nil
}

// WriteUTCTime encodes a UTCTime string (ITU-T X.691 treats as VisibleString).
func (w *BitWriter) WriteUTCTime(s string) error {
	// Standard UTCTime is e.g. "YYMMDDhhmm[ss]Z" (11 or 13 or 15 or 17 characters)
	// Encoded as unconstrained or semi-constrained VisibleString:
	length := len(s)
	if err := w.WriteLengthDeterminant(length); err != nil {
		return err
	}
	for _, ch := range []byte(s) {
		w.WriteBits(uint64(ch), 7)
	}
	return nil
}

// ReadUTCTime decodes a UTCTime string.
func (r *BitReader) ReadUTCTime() (string, error) {
	length, err := r.ReadLengthDeterminant()
	if err != nil {
		return "", err
	}
	buf := make([]byte, length)
	for i := 0; i < length; i++ {
		ch, err := r.ReadBits(7)
		if err != nil {
			return "", err
		}
		buf[i] = byte(ch)
	}
	return string(buf), nil
}
