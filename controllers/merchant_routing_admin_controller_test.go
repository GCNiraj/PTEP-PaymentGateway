package controllers

import "testing"

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
