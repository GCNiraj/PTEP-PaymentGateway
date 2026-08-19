package credentials

import (
	"encoding/base64"
	"testing"
)

func TestCipherEncryptDecryptBindsAdditionalData(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	cipher, err := NewFromBase64(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("NewFromBase64() error = %v", err)
	}

	encoded, err := cipher.Encrypt([]byte(`{"api_key":"secret"}`), []byte("recipient-a:stripe:1"))
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	plain, err := cipher.Decrypt(encoded, []byte("recipient-a:stripe:1"))
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if got, want := string(plain), `{"api_key":"secret"}`; got != want {
		t.Fatalf("plaintext = %q, want %q", got, want)
	}
	if _, err := cipher.Decrypt(encoded, []byte("recipient-b:stripe:1")); err == nil {
		t.Fatal("Decrypt() with different additional data unexpectedly succeeded")
	}
}
