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

func (r *GatewayResolver) DKPGClient(resolved *ResolvedGateway) (*dkpg.Client, error) {
	if resolved == nil || resolved.Routing == nil || resolved.Routing.Credential.Provider != "dkpg" {
		return nil, ErrMerchantNotConfigured
	}
	v := resolved.Values
	for _, key := range []string{"api_key", "username", "password", "client_id", "client_secret", "source_app", "beneficiary_account", "beneficiary_name", "beneficiary_bank"} {
		if strings.TrimSpace(v[key]) == "" {
			return nil, fmt.Errorf("DKPG credential configuration is incomplete")
		}
	}
	return dkpg.NewClient(r.Cfg.DKPGBaseURL, v["api_key"], v["source_app"], v["username"], v["password"], v["client_id"], v["client_secret"], v["scopes"], v["private_key"], r.LogRepo), nil
}

func (r *GatewayResolver) StripeClient(resolved *ResolvedGateway) (*stripe.Client, error) {
	if resolved == nil || resolved.Routing == nil || resolved.Routing.Credential.Provider != "stripe" || strings.TrimSpace(resolved.Values["api_key"]) == "" {
		return nil, ErrMerchantNotConfigured
	}
	return stripe.NewClient(r.Cfg.StripeBaseURL, resolved.Values["api_key"], r.LogRepo), nil
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
