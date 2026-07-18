// Package crypto provides AES-256-GCM encryption for at-rest secrets.
// It is used by the API to encrypt data-source passwords before storage
// and by the Worker to decrypt them before use. No other part of the
// application should hold or log plaintext passwords.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// keyLen is the required AES-256 key size in bytes.
	keyLen = 32

	// versionPrefix is prepended to every ciphertext to allow future key rotation.
	versionPrefix = "v1:"

	// nonceSize is the GCM standard nonce length.
	nonceSize = 12
)

// Cipher wraps an AES-256-GCM block cipher. Create one with NewCipher;
// reuse across requests — it is safe for concurrent use.
type Cipher struct {
	key []byte
}

// NewCipher validates key and returns a ready-to-use Cipher.
// key must be exactly 32 bytes (AES-256). Returns an error otherwise.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != keyLen {
		return nil, fmt.Errorf("crypto: key must be exactly %d bytes, got %d", keyLen, len(key))
	}
	cp := make([]byte, keyLen)
	copy(cp, key)
	return &Cipher{key: cp}, nil
}

// Encrypt encrypts plaintext using AES-256-GCM with the supplied AAD.
// The returned string has the form "v1:<base64(nonce || ciphertext)>".
// AAD binds the ciphertext to a specific row; use a stable identifier
// such as "data-source:<id>". Encrypt panics only on rand.Read failure.
func (c *Cipher) Encrypt(plaintext, aad string) (string, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", fmt.Errorf("crypto: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: new gcm: %w", err)
	}

	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), []byte(aad))
	encoded := base64.StdEncoding.EncodeToString(ciphertext)
	return versionPrefix + encoded, nil
}

// Decrypt reverses Encrypt. It expects a "v1:…" prefixed string and the
// same AAD that was used during encryption. Returns ErrDecryptFailed on
// any authentication or decoding error so callers never see GCM internals.
func (c *Cipher) Decrypt(ciphertext, aad string) (string, error) {
	if !strings.HasPrefix(ciphertext, versionPrefix) {
		return "", ErrDecryptFailed
	}
	encoded := strings.TrimPrefix(ciphertext, versionPrefix)

	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", ErrDecryptFailed
	}
	if len(raw) < nonceSize {
		return "", ErrDecryptFailed
	}

	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", fmt.Errorf("crypto: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: new gcm: %w", err)
	}

	nonce, data := raw[:nonceSize], raw[nonceSize:]
	plain, err := gcm.Open(nil, nonce, data, []byte(aad))
	if err != nil {
		return "", ErrDecryptFailed
	}
	return string(plain), nil
}

// ErrDecryptFailed is returned when decryption or authentication fails.
// Callers must not log the accompanying ciphertext or key material.
var ErrDecryptFailed = errors.New("crypto: decryption failed")

// AAD returns the canonical additional-authenticated-data string for a
// data source. Binding the ciphertext to the row's stable ID prevents
// copying a password column between rows.
func AAD(dataSourceID uint64) string {
	return fmt.Sprintf("data-source:%d", dataSourceID)
}
