// Package crypto handles payload encryption and decryption.
// Uses AES-256-GCM for authenticated encryption.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

const (
	// KeySize is the AES-256 key size in bytes.
	KeySize = 32

	// NonceSize is the GCM nonce size.
	NonceSize = 12
)

// DeriveKey derives a 32-byte AES-256 key from an arbitrary passphrase.
// Uses SHA-256 as the KDF (simple; for production use Argon2).
func DeriveKey(passphrase string) []byte {
	h := sha256.Sum256([]byte(passphrase))
	return h[:]
}

// Encrypt encrypts plaintext using AES-256-GCM with the given key.
// The key should be 32 bytes (use DeriveKey for passphrases).
// Returns: nonce || ciphertext || tag
func Encrypt(plaintext, key []byte) ([]byte, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes (got %d)", KeySize, len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Seal appends the encrypted data (ciphertext + tag) to nonce
	ciphertext := aesGCM.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// Decrypt decrypts data using AES-256-GCM.
// Expects: nonce || ciphertext || tag
func Decrypt(data, key []byte) ([]byte, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes (got %d)", KeySize, len(key))
	}

	if len(data) < NonceSize+1 {
		return nil, errors.New("ciphertext too short")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce := data[:NonceSize]
	ciphertext := data[NonceSize:]

	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	return plaintext, nil
}

// EncryptHex encrypts plaintext and returns hex-encoded output.
func EncryptHex(plaintext []byte, keyHex string) (string, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return "", fmt.Errorf("invalid key hex: %w", err)
	}
	ct, err := Encrypt(plaintext, key)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(ct), nil
}

// DecryptHex decrypts hex-encoded data and returns plaintext.
func DecryptHex(dataHex string, key []byte) ([]byte, error) {
	data, err := hex.DecodeString(dataHex)
	if err != nil {
		return nil, fmt.Errorf("invalid hex data: %w", err)
	}
	return Decrypt(data, key)
}

// HexToKey decodes a hex string into a 32-byte key.
func HexToKey(hexStr string) ([]byte, error) {
	key, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("invalid hex: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes (got %d)", KeySize, len(key))
	}
	return key, nil
}

// HexToKeyWithFallback accepts either a 64-char hex key (32 bytes) or any
// passphrase and derives a key using SHA-256.
func HexToKeyWithFallback(input string) []byte {
	if len(input) == 64 {
		if key, err := hex.DecodeString(input); err == nil && len(key) == KeySize {
			return key
		}
	}
	return DeriveKey(input)
}

// GenerateKey generates a random 32-byte AES-256 key and returns it as hex.
func GenerateKey() (string, error) {
	key := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", fmt.Errorf("failed to generate key: %w", err)
	}
	return hex.EncodeToString(key), nil
}
