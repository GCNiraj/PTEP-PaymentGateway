package controllers

import (
	"testing"

	"example.com/fiber-mvc/internal/storage"
)

func TestValidCredentialPayload(t *testing.T) {
	dkpg := map[string]string{
		"api_key": "key", "username": "user", "password": "password", "client_id": "id", "client_secret": "secret", "source_app": "source",
		"beneficiary_account": "account", "beneficiary_name": "name", "beneficiary_bank": "bank",
	}
	if !validCredentialPayload("dkpg", dkpg) {
		t.Fatal("expected complete DKPG credential payload to be valid")
	}
	delete(dkpg, "client_secret")
	if validCredentialPayload("dkpg", dkpg) {
		t.Fatal("expected incomplete DKPG credential payload to be invalid")
	}
	if !validCredentialPayload("stripe", map[string]string{"api_key": "key", "agency_name": "agency", "submerchant_id": "sub", "dk_account": "account"}) {
		t.Fatal("expected complete Stripe credential payload to be valid")
	}
}

func TestSafeCredentialDoesNotExposeCiphertext(t *testing.T) {
	credential := safeCredential(storage.GatewayCredentialConfiguration{EncryptedCredentials: "ciphertext-that-must-not-leak"})
	if credential.EncryptedCredentials != "" {
		t.Fatal("safe credential response exposed encrypted credential payload")
	}
}
