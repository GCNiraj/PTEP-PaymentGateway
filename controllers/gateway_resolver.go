package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"example.com/fiber-mvc/config"
	"example.com/fiber-mvc/internal/credentials"
	"example.com/fiber-mvc/internal/dkpg"
	"example.com/fiber-mvc/internal/storage"
	"example.com/fiber-mvc/internal/stripe"
)

// GatewayResolver resolves only administrator-approved recipient mappings. It
// never accepts internal recipient or credential identifiers from API callers.
type GatewayResolver struct {
	Repo    *storage.MerchantRoutingRepository
	Cipher  *credentials.Cipher
	Cfg     config.Config
	LogRepo *storage.LogRepository
}

type ResolvedGateway struct {
	Routing *storage.ResolvedGatewayConfiguration
	Values  map[string]string
}

var ErrMerchantNotConfigured = errors.New("merchant reference is not configured")

func (r *GatewayResolver) Resolve(appID, merchantReference, provider string) (*ResolvedGateway, error) {
	if r == nil || r.Repo == nil || r.Cipher == nil {
		return nil, ErrMerchantNotConfigured
	}
	if strings.TrimSpace(appID) == "" || strings.TrimSpace(merchantReference) == "" {
		return nil, ErrMerchantNotConfigured
	}
	routing, err := r.Repo.ResolveActive(context.Background(), appID, merchantReference, provider)
	if err != nil {
		if errors.Is(err, storage.ErrRecipientMappingNotFound) || errors.Is(err, storage.ErrCredentialNotConfigured) {
			return nil, ErrMerchantNotConfigured
		}
		return nil, fmt.Errorf("resolve merchant reference: %w", err)
	}
	return r.decrypt(routing)
}

func (r *GatewayResolver) ResolveCredential(credentialID string) (*ResolvedGateway, error) {
	if r == nil || r.Repo == nil || r.Cipher == nil || strings.TrimSpace(credentialID) == "" {
		return nil, ErrMerchantNotConfigured
	}
	credential, err := r.Repo.GetCredential(context.Background(), credentialID)
	if err != nil {
		return nil, err
	}
	return r.decrypt(&storage.ResolvedGatewayConfiguration{RecipientID: credential.RecipientID, Credential: *credential})
}

func (r *GatewayResolver) decrypt(routing *storage.ResolvedGatewayConfiguration) (*ResolvedGateway, error) {
	if routing == nil {
		return nil, ErrMerchantNotConfigured
	}
	plaintext, err := r.Cipher.Decrypt(routing.Credential.EncryptedCredentials, storage.CredentialAdditionalData(routing.Credential.ID, routing.Credential.RecipientID, routing.Credential.Provider, routing.Credential.Version))
	if err != nil {
		return nil, fmt.Errorf("decrypt gateway configuration: %w", err)
	}
	defer zero(plaintext)
	values := map[string]string{}
	if err := json.Unmarshal(plaintext, &values); err != nil {
		return nil, fmt.Errorf("decode gateway configuration: %w", err)
	}
	return &ResolvedGateway{Routing: routing, Values: values}, nil
}

// ErrGatewayNotConfigured means this gateway has no identity of its own with
// the provider. Distinct from ErrMerchantNotConfigured, which means the caller
// named a merchant nobody has set up: one is our misconfiguration, the other is
// theirs, and answering the same way for both is how a typo spends an afternoon
// being mistaken for a missing mapping.
var ErrGatewayNotConfigured = errors.New("this gateway's provider credentials are not configured in the environment")

// requireEnv reports the first named value that is empty.
func requireEnv(values map[string]string) error {
	for name, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: %s is empty", ErrGatewayNotConfigured, name)
		}
	}
	return nil
}

// SourceApp is this gateway's registered application code with the bank. It
// identifies *us* to DKPG and is the same on every payment, so it comes from
// the environment rather than from a recipient's record.
func (r *GatewayResolver) SourceApp() string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.Cfg.DKPGSourceApp)
}

// DKPGClient builds a bank client for one resolved recipient.
//
// WHO IS CALLING vs WHO IS PAID. These are two different questions and they now
// have two different answers. The bank knows one integrator — this gateway —
// and issues it one set of credentials; those live in the environment. Each
// recipient record says only where the money lands.
//
// It used to be one answer: every recipient carried a full copy of the bank
// credentials alongside its own account number. That meant the same password
// encrypted once per hotel, a rotation that was N operations with no way to
// tell a half-finished one from a finished one, and a typo in any copy failing
// exactly like a merchant that was never configured.
//
// A recipient's stored blob may still contain those old auth fields. They are
// inert — nothing below reads them — and CreateCredential now refuses new ones,
// so they drain away as credentials are rotated.
func (r *GatewayResolver) DKPGClient(resolved *ResolvedGateway) (*dkpg.Client, error) {
	if resolved == nil || resolved.Routing == nil || resolved.Routing.Credential.Provider != "dkpg" {
		return nil, ErrMerchantNotConfigured
	}
	// Where the money goes. Per recipient, and the only reason this record exists.
	for _, key := range []string{"beneficiary_account", "beneficiary_name", "beneficiary_bank"} {
		if strings.TrimSpace(resolved.Values[key]) == "" {
			return nil, fmt.Errorf("DKPG credential configuration is incomplete: %s is required", key)
		}
	}
	// Who is asking. Shared, from the environment.
	cfg := r.Cfg
	if err := requireEnv(map[string]string{
		"DKPG_BASE_URL": cfg.DKPGBaseURL, "DKPG_API_KEY": cfg.DKPGAPIKey,
		"DKPG_USERNAME": cfg.DKPGUsername, "DKPG_PASSWORD": cfg.DKPGPassword,
		"DKPG_CLIENT_ID": cfg.DKPGClientID, "DKPG_CLIENT_SECRET": cfg.DKPGClientSecret,
		"DKPG_SOURCE_APP": cfg.DKPGSourceApp,
	}); err != nil {
		return nil, err
	}
	return dkpg.NewClient(cfg.DKPGBaseURL, cfg.DKPGAPIKey, cfg.DKPGSourceApp, cfg.DKPGUsername,
		cfg.DKPGPassword, cfg.DKPGClientID, cfg.DKPGClientSecret, cfg.DKPGScopes,
		cfg.DKPGPrivateKey, r.LogRepo), nil
}

// AgencyName is the agency this gateway presents to the card provider. Like
// SourceApp on the bank side, it describes us rather than any one recipient.
func (r *GatewayResolver) AgencyName() string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.Cfg.StripeAgencyName)
}

// StripeClient builds a card client for one resolved recipient.
//
// The same division as DKPG above, for the same reason. The provider knows this
// gateway as one agency under one key; what distinguishes a recipient is which
// sub-merchant the money is booked to. So the key and the agency name come from
// the environment, and the record carries submerchant_id and dk_account.
//
// Older records may still hold a copy of the key and agency name. They are
// inert, and CreateCredential refuses new ones.
func (r *GatewayResolver) StripeClient(resolved *ResolvedGateway) (*stripe.Client, error) {
	if resolved == nil || resolved.Routing == nil || resolved.Routing.Credential.Provider != "stripe" {
		return nil, ErrMerchantNotConfigured
	}
	// Which sub-merchant the money is booked to. Per recipient.
	if strings.TrimSpace(resolved.Values["submerchant_id"]) == "" ||
		strings.TrimSpace(resolved.Values["dk_account"]) == "" {
		return nil, fmt.Errorf("Stripe credential configuration is incomplete: submerchant_id and dk_account are required")
	}
	if err := requireEnv(map[string]string{
		"STRIPE_BASE_URL": r.Cfg.StripeBaseURL, "STRIPE_API_KEY": r.Cfg.StripeAPIKey,
		"STRIPE_AGENCY_NAME": r.Cfg.StripeAgencyName,
	}); err != nil {
		return nil, err
	}
	return stripe.NewClient(r.Cfg.StripeBaseURL, r.Cfg.StripeAPIKey, r.LogRepo), nil
}

func (r *GatewayResolver) Value(resolved *ResolvedGateway, key string) string {
	if resolved == nil {
		return ""
	}
	return strings.TrimSpace(resolved.Values[key])
}

func zero(data []byte) {
	for i := range data {
		data[i] = 0
	}
}
