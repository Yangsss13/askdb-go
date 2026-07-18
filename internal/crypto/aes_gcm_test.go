package crypto

import (
	"strings"
	"testing"
)

func newTestCipher(t *testing.T) *Cipher {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	c, err := NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	return c
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	c := newTestCipher(t)
	plain := "super-secret-password"
	aad := AAD(42)

	ct, err := c.Encrypt(plain, aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !strings.HasPrefix(ct, "v1:") {
		t.Errorf("ciphertext missing v1: prefix: %q", ct)
	}

	got, err := c.Decrypt(ct, aad)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != plain {
		t.Errorf("got %q, want %q", got, plain)
	}
}

func TestEncryptProducesUniqueCiphertexts(t *testing.T) {
	c := newTestCipher(t)
	aad := AAD(1)

	ct1, _ := c.Encrypt("password", aad)
	ct2, _ := c.Encrypt("password", aad)
	if ct1 == ct2 {
		t.Error("two encryptions of the same plaintext produced the same ciphertext (nonce reuse?)")
	}
}

func TestDecryptTamperedCiphertext(t *testing.T) {
	c := newTestCipher(t)
	aad := AAD(7)

	ct, _ := c.Encrypt("password", aad)
	// Flip the last byte in the base64 payload.
	bs := []byte(ct)
	bs[len(bs)-1] ^= 0xFF
	_, err := c.Decrypt(string(bs), aad)
	if err != ErrDecryptFailed {
		t.Errorf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptWrongAAD(t *testing.T) {
	c := newTestCipher(t)

	ct, _ := c.Encrypt("password", AAD(1))
	// Using AAD for a different row must fail — cross-row copy attack.
	_, err := c.Decrypt(ct, AAD(2))
	if err != ErrDecryptFailed {
		t.Errorf("expected ErrDecryptFailed for wrong AAD, got %v", err)
	}
}

func TestDecryptMissingVersionPrefix(t *testing.T) {
	c := newTestCipher(t)
	_, err := c.Decrypt("bm9wcmVmaXg=", AAD(1))
	if err != ErrDecryptFailed {
		t.Errorf("expected ErrDecryptFailed for missing prefix, got %v", err)
	}
}

func TestDecryptEmptyString(t *testing.T) {
	c := newTestCipher(t)
	_, err := c.Decrypt("", AAD(1))
	if err != ErrDecryptFailed {
		t.Errorf("expected ErrDecryptFailed for empty input, got %v", err)
	}
}

func TestNewCipherKeyTooShort(t *testing.T) {
	_, err := NewCipher(make([]byte, 16))
	if err == nil {
		t.Error("expected error for 16-byte key")
	}
}

func TestNewCipherKeyTooLong(t *testing.T) {
	_, err := NewCipher(make([]byte, 64))
	if err == nil {
		t.Error("expected error for 64-byte key")
	}
}

func TestAADFormat(t *testing.T) {
	if got := AAD(99); got != "data-source:99" {
		t.Errorf("AAD(99) = %q, want %q", got, "data-source:99")
	}
}
