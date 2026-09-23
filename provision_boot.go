package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"

	"github.com/google/uuid"

	"example.com/fiber-mvc/internal/credentials"
	"example.com/fiber-mvc/internal/storage"
)

// Seeding who gets paid, at boot.
//
// The payout destinations are encrypted with the master key, which only this
// process holds — so provisioning them from outside means exporting that key
// into somebody's shell, and every attempt to do it by hand is one typo away
// from writing rows the gateway cannot decrypt. The process that owns the key
// does the work instead.
//
// IT ONLY EVER ADDS. Nothing here updates or deletes: a payee that already
// exists is left exactly as it is, because the account somebody corrected
// through the dashboard must not be silently reverted on the next restart.
//
// IT NEVER STOPS THE SERVICE. A gateway that will not start because a seed file
// has a typo in it is worse than a gateway missing a payee. Failures are logged
// and boot continues.

type bootPayee struct {
	Name      string            `json:"name"`
	Reference string            `json:"merchant_reference"`
	DKPG      map[string]string `json:"dkpg"`
	Stripe    map[string]string `json:"stripe"`
}

type bootPlan struct {
	AppID  string      `json:"app_id"`
	Payees []bootPayee `json:"payees"`
}

// provisionAtBoot reads PROVISION_FILE (default provision.json) and makes sure
// everything in it exists. Absent file, nothing happens and nothing is logged:
// most deployments do not want this.
func provisionAtBoot(repo *storage.MerchantRoutingRepository, cipher *credentials.Cipher) {
	path := strings.TrimSpace(os.Getenv("PROVISION_FILE"))
	if path == "" {
		path = "provision.json"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("provision: could not read %s: %v", path, err)
		}
		return
	}
	var plan bootPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		log.Printf("provision: %s is not valid JSON: %v", path, err)
		return
	}
	if strings.TrimSpace(plan.AppID) == "" {
		log.Printf("provision: %s has no app_id — routing is per app, so there is nothing to attach these to", path)
		return
	}
	if repo == nil || cipher == nil {
		log.Printf("provision: merchant routing is not configured; skipping %s", path)
		return
	}

	ctx := context.Background()

	// Checked before anything is written. A mapping points at an app, so on a
	// gateway where that app has not been registered yet — a fresh deployment —
	// every payee would be created and none of them could be paid, with the
	// reason buried in a foreign-key error per payee.
	switch exists, err := repo.AppExists(ctx, plan.AppID); {
	case err != nil:
		log.Printf("provision: could not check app %s: %v", plan.AppID, err)
		return
	case !exists:
		log.Printf("provision: app %s is not registered in this gateway, so nothing was provisioned. "+
			"Register the booking service in the dashboard (Apps), put its app id in %s, and restart.",
			plan.AppID, path)
		return
	}

	for _, payee := range plan.Payees {
		if err := provisionOne(ctx, repo, cipher, plan.AppID, payee); err != nil {
			log.Printf("provision: %s: %v", payee.Name, err)
		}
	}
}

func provisionOne(ctx context.Context, repo *storage.MerchantRoutingRepository,
	cipher *credentials.Cipher, appID string, payee bootPayee) error {

	name := strings.TrimSpace(payee.Name)
	reference := strings.TrimSpace(payee.Reference)
	if name == "" || reference == "" {
		return errBadPayee
	}

	// The recipient. Matched by name, because that is what an operator typed
	// and what the dashboard shows.
	recipients, err := repo.ListRecipients(ctx)
	if err != nil {
		return err
	}
	recipientID := ""
	for _, existing := range recipients {
		if strings.EqualFold(strings.TrimSpace(existing.Name), name) {
			recipientID = existing.ID
			break
		}
	}
	if recipientID == "" {
		recipientID = uuid.NewString()
		if err := repo.CreateRecipient(ctx,
			storage.PaymentRecipient{ID: recipientID, Name: name, IsActive: true}, "boot"); err != nil {
			return err
		}
		log.Printf("provision: created payee %q", name)
	}

	// Its destinations, one per provider that was given.
	for provider, values := range map[string]map[string]string{"dkpg": payee.DKPG, "stripe": payee.Stripe} {
		if len(values) == 0 {
			continue
		}
		if _, err := repo.ResolveActive(ctx, appID, reference, provider); err == nil {
			continue // already payable this way; leave it alone
		}
		credential, err := repo.CreateDraftCredential(ctx, recipientID, provider,
			"env:PAYMENT_CREDENTIALS_MASTER_KEY_B64", "boot",
			func(c *storage.GatewayCredentialConfiguration) (string, error) {
				plaintext, err := json.Marshal(values)
				if err != nil {
					return "", err
				}
				return cipher.Encrypt(plaintext, storage.CredentialAdditionalData(
					c.ID, c.RecipientID, c.Provider, c.Version))
			})
		if err != nil {
			log.Printf("provision: %s %s destination: %v", name, provider, err)
			continue
		}
		if err := repo.SetCredentialStatus(ctx, credential.ID, "active", "boot"); err != nil {
			log.Printf("provision: %s %s activate: %v", name, provider, err)
			continue
		}
		log.Printf("provision: %s can now be paid by %s", name, provider)
	}

	// And the reference the booking platform pays it under.
	mappings, err := repo.ListMappings(ctx, appID)
	if err != nil {
		return err
	}
	for _, mapping := range mappings {
		if mapping.ExternalMerchantReference == reference && mapping.IsActive {
			return nil
		}
	}
	if err := repo.CreateMapping(ctx, storage.IntegrationRecipientMapping{
		ExternalAppID: appID, ExternalMerchantReference: reference,
		RecipientID: recipientID, IsActive: true,
	}, "boot"); err != nil {
		return err
	}
	log.Printf("provision: %s mapped to %s", name, reference)
	return nil
}

var errBadPayee = errorString("needs both a name and a merchant_reference")

type errorString string

func (e errorString) Error() string { return string(e) }
