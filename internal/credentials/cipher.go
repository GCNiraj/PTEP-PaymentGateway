package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const encryptionVersion = 1

// Cipher encrypts gateway credential payloads before they are persisted.
// It intentionally has no database dependency; its key must come from process
// configuration or a secret manager, never PostgreSQL.
type Cipher struct {
	gcm cipher.AEAD
}

type envelope struct {
	Version    int    `json:"version"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

func NewFromBase64(encodedKey string) (*Cipher, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedKey))
	if err != nil {
		return nil, fmt.Errorf("decode payment credential master key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("payment credential master key must decode to 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM: %w", err)
	}
	return &Cipher{gcm: gcm}, nil
}

func (c *Cipher) Encrypt(plaintext, additionalData []byte) (string, error) {
	if c == nil || c.gcm == nil {
		return "", fmt.Errorf("payment credential cipher is not configured")
	}
	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate credential encryption nonce: %w", err)
	}
	ciphertext := c.gcm.Seal(nil, nonce, plaintext, additionalData)
	encoded, err := json.Marshal(envelope{
		Version:    encryptionVersion,
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
	})
	if err != nil {
		return "", fmt.Errorf("encode credential envelope: %w", err)
	}
	return string(encoded), nil
}

func (c *Cipher) Decrypt(encoded string, additionalData []byte) ([]byte, error) {
	if c == nil || c.gcm == nil {
		return nil, fmt.Errorf("payment credential cipher is not configured")
	}
	var data envelope
	if err := json.Unmarshal([]byte(encoded), &data); err != nil {
		return nil, fmt.Errorf("decode credential envelope: %w", err)
	}
	if data.Version != encryptionVersion {
		return nil, fmt.Errorf("unsupported credential encryption version")
	}
	nonce, err := base64.StdEncoding.DecodeString(data.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode credential nonce: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(data.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode credential ciphertext: %w", err)
	}
	plaintext, err := c.gcm.Open(nil, nonce, ciphertext, additionalData)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential payload: %w", err)
	}
	return plaintext, nil
}
