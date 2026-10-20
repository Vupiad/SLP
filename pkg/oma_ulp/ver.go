package oma_ulp

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"slp/pkg/uper"
)

// VerLengthBits is the standard length of the VER field in bits (64 bits = 8 octets)
const VerLengthBits = 64

// VerLengthBytes is the standard length of the VER field in octets
const VerLengthBytes = 8

// ComputeVerRaw calculates HMAC-SHA256-64 over the provided message using key.
// It returns the leftmost 64 bits (8 octets) of the HMAC-SHA256 digest as per OMA SUPL 2.0.
func ComputeVerRaw(key []byte, message []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	sum := mac.Sum(nil)
	res := make([]byte, VerLengthBytes)
	copy(res, sum[:VerLengthBytes])
	return res
}

// ComputeVer calculates the VER field as an ASN.1 *Ver BitString (64 bits).
func ComputeVer(key []byte, message []byte) *Ver {
	raw := ComputeVerRaw(key, message)
	bs := uper.NewBitString(raw, VerLengthBits)
	v := Ver(bs)
	return &v
}

// ComputeVerForPdu encodes the given ULPPDU and computes its 64-bit HMAC-SHA256 VER.
// It returns the Ver struct, the serialized UPER bytes, and any encoding error encountered.
func ComputeVerForPdu(key []byte, pdu *ULPPDU) (*Ver, []byte, error) {
	if pdu == nil {
		return nil, nil, errors.New("pdu is nil")
	}
	encoded, err := Marshal(pdu)
	if err != nil {
		return nil, nil, err
	}
	v := ComputeVer(key, encoded)
	return v, encoded, nil
}

// VerifyVer verifies if the provided *Ver matches the HMAC-SHA256-64 over message using key.
func VerifyVer(ver *Ver, key []byte, message []byte) bool {
	if ver == nil || len(ver.Bytes) < VerLengthBytes {
		return false
	}
	expected := ComputeVerRaw(key, message)
	return hmac.Equal(ver.Bytes[:VerLengthBytes], expected)
}

// VerifyVerAgainstRaw checks whether a received *Ver matches an expected 8-byte raw VER slice.
func VerifyVerAgainstRaw(ver *Ver, expected []byte) bool {
	if ver == nil || len(ver.Bytes) < VerLengthBytes || len(expected) < VerLengthBytes {
		return false
	}
	return hmac.Equal(ver.Bytes[:VerLengthBytes], expected[:VerLengthBytes])
}
