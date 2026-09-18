package controllers

import (
	"errors"
	"testing"

	"example.com/fiber-mvc/config"
	"example.com/fiber-mvc/internal/storage"
)

// The bank knows one integrator. These tests pin that down, because the shape
// they replaced — a full copy of the bank credentials inside every hotel's
// record — was indistinguishable from this one until a rotation went wrong.

func dkpgEnv() config.Config {
	return config.Config{
		DKPGBaseURL: "https://internal-gateway.uat.digitalkidu.bt/api/dkpg",
		DKPGAPIKey:  "gravitee-key", DKPGUsername: "PG_AVS", DKPGPassword: "p@PG1234",
		DKPGClientID: "PG_AVS_123", DKPGClientSecret: "PG-Requestor-TestSecret123",
		DKPGSourceApp: "SRC_AVS_0201", DKPGScopes: "keys:read",
		StripeBaseURL: "https://internal-gateway.sit.digitalkidu.bt:8082/uat/stripe/",
		StripeAPIKey:  "sk-test", StripeAgencyName: "PTEP",
	}
}

func stripeDestination(values map[string]string) *ResolvedGateway {
	return &ResolvedGateway{
		Routing: &storage.ResolvedGatewayConfiguration{
			Credential: storage.GatewayCredentialConfiguration{Provider: "stripe"},
		},
		Values: values,
	}
}

// The card side divides the same way, and each hotel keeps its own sub-merchant.
func TestStripeClientBuildsFromTheEnvironment(t *testing.T) {
	resolver := &GatewayResolver{Cfg: dkpgEnv()}
	for name, sub := range map[string]string{
		"Hotel Amochu View": "MERCHANT_001",
		"Toorsa Farmstay":   "MERCHANT_002",
	} {
		client, err := resolver.StripeClient(stripeDestination(map[string]string{
			"submerchant_id": sub, "dk_account": sub,
		}))
		if err != nil || client == nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if resolver.AgencyName() != "PTEP" {
		t.Fatalf("agency_name must come from the environment, got %q", resolver.AgencyName())
	}
}

func TestStripeMissingSubMerchantIsRefused(t *testing.T) {
	resolver := &GatewayResolver{Cfg: dkpgEnv()}
	if _, err := resolver.StripeClient(stripeDestination(map[string]string{"dk_account": "MERCHANT_001"})); err == nil {
		t.Fatal("expected a Stripe credential with no submerchant_id to be refused")
	}
}

func TestStripeMissingEnvironmentIsItsOwnFailure(t *testing.T) {
	for _, field := range []string{"STRIPE_API_KEY", "STRIPE_AGENCY_NAME", "STRIPE_BASE_URL"} {
		cfg := dkpgEnv()
		switch field {
		case "STRIPE_API_KEY":
			cfg.StripeAPIKey = ""
		case "STRIPE_AGENCY_NAME":
			cfg.StripeAgencyName = ""
		case "STRIPE_BASE_URL":
			cfg.StripeBaseURL = ""
		}
		resolver := &GatewayResolver{Cfg: cfg}
		_, err := resolver.StripeClient(stripeDestination(map[string]string{
			"submerchant_id": "MERCHANT_001", "dk_account": "MERCHANT_001",
		}))
		if !errors.Is(err, ErrGatewayNotConfigured) {
			t.Fatalf("%s empty: expected ErrGatewayNotConfigured, got %v", field, err)
		}
	}
}

// Stale auth in an old Stripe record must not take precedence either.
func TestStaleStripeAuthInARecordIsIgnored(t *testing.T) {
	resolver := &GatewayResolver{Cfg: dkpgEnv()}
	if _, err := resolver.StripeClient(stripeDestination(map[string]string{
		"submerchant_id": "MERCHANT_001", "dk_account": "MERCHANT_001",
		"api_key": "STALE", "agency_name": "STALE",
	})); err != nil {
		t.Fatalf("expected a record with stale auth to still resolve: %v", err)
	}
	if resolver.AgencyName() != "PTEP" {
		t.Fatal("a stale agency_name in the record overrode the environment")
	}
}

func destinationOnly(values map[string]string) *ResolvedGateway {
	return &ResolvedGateway{
		Routing: &storage.ResolvedGatewayConfiguration{
			Credential: storage.GatewayCredentialConfiguration{Provider: "dkpg"},
		},
		Values: values,
	}
}

func TestDKPGClientBuildsFromTheEnvironment(t *testing.T) {
	resolver := &GatewayResolver{Cfg: dkpgEnv()}
	client, err := resolver.DKPGClient(destinationOnly(map[string]string{
		"beneficiary_account": "110158212197", "beneficiary_name": "Hotel Amochu View",
		"beneficiary_bank": "1040",
	}))
	if err != nil {
		t.Fatalf("expected a destination-only credential to resolve, got: %v", err)
	}
	if client == nil {
		t.Fatal("expected a client")
	}
	if resolver.SourceApp() != "SRC_AVS_0201" {
		t.Fatalf("source_app must come from the environment, got %q", resolver.SourceApp())
	}
}

// Two hotels, one bank identity, two destinations. This is the whole point.
func TestEachRecipientKeepsItsOwnDestination(t *testing.T) {
	resolver := &GatewayResolver{Cfg: dkpgEnv()}
	for name, account := range map[string]string{
		"Hotel Amochu View": "110158212197",
		"Toorsa Farmstay":   "110158212198",
	} {
		if _, err := resolver.DKPGClient(destinationOnly(map[string]string{
			"beneficiary_account": account, "beneficiary_name": name, "beneficiary_bank": "1040",
		})); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// Auth left over in an old record is inert, not authoritative. Existing rows
// still carry it; they must not quietly take precedence over the environment.
func TestStaleAuthInARecordIsIgnored(t *testing.T) {
	resolver := &GatewayResolver{Cfg: dkpgEnv()}
	if _, err := resolver.DKPGClient(destinationOnly(map[string]string{
		"beneficiary_account": "110158212197", "beneficiary_name": "Hotel Amochu View",
		"beneficiary_bank": "1040",
		"username":         "STALE", "password": "STALE", "source_app": "STALE",
	})); err != nil {
		t.Fatalf("expected a record with stale auth to still resolve: %v", err)
	}
	if resolver.SourceApp() != "SRC_AVS_0201" {
		t.Fatal("a stale source_app in the record overrode the environment")
	}
}

func TestMissingDestinationIsRefused(t *testing.T) {
	resolver := &GatewayResolver{Cfg: dkpgEnv()}
	// Every field of the destination is required, including the bank code that
	// intra/inquiry sends upstream with no fallback behind it.
	for _, missing := range []string{"beneficiary_account", "beneficiary_name", "beneficiary_bank"} {
		values := map[string]string{
			"beneficiary_account": "110158212197", "beneficiary_name": "Hotel Amochu View",
			"beneficiary_bank": "1040",
		}
		delete(values, missing)
		if _, err := resolver.DKPGClient(destinationOnly(values)); err == nil {
			t.Fatalf("expected a credential with no %s to be refused", missing)
		}
	}
}

// Our misconfiguration must not read as the caller's. A gateway with no bank
// identity is a 503 we own, not a bad merchant_reference they can fix.
func TestMissingEnvironmentIsItsOwnFailure(t *testing.T) {
	for _, field := range []string{"DKPG_USERNAME", "DKPG_PASSWORD", "DKPG_CLIENT_ID", "DKPG_CLIENT_SECRET", "DKPG_SOURCE_APP", "DKPG_API_KEY", "DKPG_BASE_URL"} {
		cfg := dkpgEnv()
		switch field {
		case "DKPG_USERNAME":
			cfg.DKPGUsername = ""
		case "DKPG_PASSWORD":
			cfg.DKPGPassword = ""
		case "DKPG_CLIENT_ID":
			cfg.DKPGClientID = ""
		case "DKPG_CLIENT_SECRET":
			cfg.DKPGClientSecret = ""
		case "DKPG_SOURCE_APP":
			cfg.DKPGSourceApp = ""
		case "DKPG_API_KEY":
			cfg.DKPGAPIKey = ""
		case "DKPG_BASE_URL":
			cfg.DKPGBaseURL = ""
		}
		resolver := &GatewayResolver{Cfg: cfg}
		_, err := resolver.DKPGClient(destinationOnly(map[string]string{
			"beneficiary_account": "110158212197", "beneficiary_name": "Hotel Amochu View",
			"beneficiary_bank": "1040",
		}))
		if !errors.Is(err, ErrGatewayNotConfigured) {
			t.Fatalf("%s empty: expected ErrGatewayNotConfigured, got %v", field, err)
		}
		if errors.Is(err, ErrMerchantNotConfigured) {
			t.Fatalf("%s empty: our misconfiguration must not report as the caller's", field)
		}
	}
}
