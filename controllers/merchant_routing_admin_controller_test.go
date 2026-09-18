package controllers

import (
	"strings"
	"testing"

	"example.com/fiber-mvc/internal/storage"
)

// A DKPG recipient record says where money lands, and nothing else.
//
// The bank issues this gateway one identity, so authentication lives in the
// environment and is identical for every recipient. A record that carries it is
// claiming to decide something it does not decide, and is refused rather than
// ignored — silently dropping it is how somebody comes to believe one hotel is
// paying through a different account than it is.
func TestDKPGCredentialCarriesOnlyTheDestination(t *testing.T) {
	destination := func() map[string]string {
		return map[string]string{
			"beneficiary_account": "110158212197",
			"beneficiary_name":    "Hotel Amochu View",
			"beneficiary_bank":    "1040",
		}
	}
	if !validCredentialPayload("dkpg", destination()) {
		t.Fatal("expected a beneficiary-only DKPG payload to be valid")
	}

	// All three are required. beneficiary_bank reads as optional on
	// pull/initiate, where it only backs up the remitter's bank code — but on
	// intra/inquiry it is the beneficiary's own bank code with nothing behind
	// it, so a record without it would send the bank a blank destination.
	for _, missing := range []string{"beneficiary_account", "beneficiary_name", "beneficiary_bank"} {
		incomplete := destination()
		delete(incomplete, missing)
		if validCredentialPayload("dkpg", incomplete) {
			t.Fatalf("expected a DKPG payload without %s to be invalid", missing)
		}
	}
}

func TestDKPGCredentialRefusesSharedAuthentication(t *testing.T) {
	for _, key := range dkpgAuthKeys {
		payload := map[string]string{
			"beneficiary_account": "110158212197",
			"beneficiary_name":    "Hotel Amochu View",
			"beneficiary_bank":    "1040",
			key:                   "supplied-by-mistake",
		}
		if validCredentialPayload("dkpg", payload) {
			t.Fatalf("expected a DKPG payload carrying %s to be refused", key)
		}
		// The refusal has to name the offending key, or whoever sent it is left
		// guessing which of nine fields the gateway objected to.
		if problem := credentialPayloadProblem("dkpg", payload); !strings.Contains(problem, key) {
			t.Fatalf("expected the refusal for %s to name it, got: %s", key, problem)
		}
	}
}

// Stripe divides the same way. One agency and one key identify this gateway to
// the provider; what distinguishes a recipient is which sub-merchant the money
// is booked to.
func TestStripeCredentialCarriesOnlyTheSubMerchant(t *testing.T) {
	subMerchant := map[string]string{"submerchant_id": "MERCHANT_001", "dk_account": "MERCHANT_001"}
	if !validCredentialPayload("stripe", subMerchant) {
		t.Fatal("expected a sub-merchant-only Stripe payload to be valid")
	}
	for _, missing := range []string{"submerchant_id", "dk_account"} {
		incomplete := map[string]string{"submerchant_id": "MERCHANT_001", "dk_account": "MERCHANT_001"}
		delete(incomplete, missing)
		if validCredentialPayload("stripe", incomplete) {
			t.Fatalf("expected a Stripe payload without %s to be invalid", missing)
		}
	}
}

func TestStripeCredentialRefusesSharedAuthentication(t *testing.T) {
	for _, key := range stripeAuthKeys {
		payload := map[string]string{
			"submerchant_id": "MERCHANT_001", "dk_account": "MERCHANT_001",
			key: "supplied-by-mistake",
		}
		if validCredentialPayload("stripe", payload) {
			t.Fatalf("expected a Stripe payload carrying %s to be refused", key)
		}
		if problem := credentialPayloadProblem("stripe", payload); !strings.Contains(problem, key) {
			t.Fatalf("expected the refusal for %s to name it, got: %s", key, problem)
		}
	}
}

func TestSafeCredentialDoesNotExposeCiphertext(t *testing.T) {
	credential := safeCredential(storage.GatewayCredentialConfiguration{EncryptedCredentials: "ciphertext-that-must-not-leak"})
	if credential.EncryptedCredentials != "" {
		t.Fatal("safe credential response exposed encrypted credential payload")
	}
}

func TestHumanListReadsAsASentence(t *testing.T) {
	cases := map[string][]string{
		"":              {},
		"a":             {"a"},
		"a and b":       {"a", "b"},
		"a, b and c":    {"a", "b", "c"},
		"a, b, c and d": {"a", "b", "c", "d"},
	}
	for want, items := range cases {
		if got := humanList(items); got != want {
			t.Fatalf("humanList(%v) = %q, want %q", items, got, want)
		}
	}
}
