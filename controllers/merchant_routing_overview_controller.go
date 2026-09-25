package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"example.com/fiber-mvc/config"
	"example.com/fiber-mvc/internal/credentials"
	"example.com/fiber-mvc/internal/storage"
	"example.com/fiber-mvc/internal/validation"
)

// Setting up who gets paid, from the dashboard.
//
// WHY THIS EXISTS. Everything needed to pay a property already had an endpoint
// — a recipient, a credential, an activation, a mapping — and no screen. So it
// was done with curl, four calls at a time, by hand, per property. Seven of
// eight properties went unconfigured for weeks and nothing said so, because
// nothing here knew there were eight.
//
// It could not know. This service holds the bank details; the booking platform
// holds the properties. Neither can answer "who is not set up yet" alone, so
// this asks the platform for the list and joins it against what is configured
// here. The gap is the answer, and it is the only thing worth looking at.
//
// WHAT IT DOES NOT DO. It does not invent a property, and it does not choose a
// merchant reference — both belong to the platform. It fills in the part this
// service owns: the account that receives the money.

// MerchantRoutingOverviewController serves the dashboard's routing tab.
type MerchantRoutingOverviewController struct {
	Repo   *storage.MerchantRoutingRepository
	Cipher *credentials.Cipher
	cfg    config.Config
	client *http.Client
}

func NewMerchantRoutingOverviewController(repo *storage.MerchantRoutingRepository, cipher *credentials.Cipher, cfg config.Config) *MerchantRoutingOverviewController {
	return &MerchantRoutingOverviewController{
		Repo: repo, Cipher: cipher, cfg: cfg,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// platformProperty is one property as the booking platform describes it.
type platformProperty struct {
	Property          string `json:"property"`
	Slug              string `json:"slug"`
	MerchantReference string `json:"merchantReference"`
	AcceptsBank       bool   `json:"acceptsBank"`
	AcceptsCard       bool   `json:"acceptsCard"`
	Status            string `json:"status"`
	AccountName       string `json:"accountName"`
	AccountTail       string `json:"accountTail"`
	Bank              string `json:"bank"`
}

// routingRow is one property and what this gateway knows about paying it.
type routingRow struct {
	platformProperty
	Configured bool   `json:"configured"`
	Recipient  string `json:"recipient,omitempty"`
	MappingID  string `json:"mappingId,omitempty"`
	// Whether the booking platform has a property using this reference. False
	// means money routed here would never be asked for — worth saying plainly.
	KnownToPlatform bool `json:"knownToPlatform"`
	// The destination, as far as it can be shown. The account is masked: this
	// screen gets shared and projected, and the last four is enough to check a
	// hotel against what it told the platform.
	BeneficiaryHint string `json:"beneficiaryHint,omitempty"`
	BeneficiaryName string `json:"beneficiaryName,omitempty"`
	BeneficiaryBank string `json:"beneficiaryBank,omitempty"`
	// What is missing, in the order somebody would fix it.
	Missing []string `json:"missing,omitempty"`
}

func (mc *MerchantRoutingOverviewController) platformConfigured() bool {
	return strings.TrimSpace(mc.cfg.PlatformBaseURL) != "" && strings.TrimSpace(mc.cfg.PlatformAPIKey) != ""
}

// properties asks the booking platform which properties exist.
func (mc *MerchantRoutingOverviewController) properties(ctx context.Context) ([]platformProperty, error) {
	base := strings.TrimRight(strings.TrimSpace(mc.cfg.PlatformBaseURL), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/internal/merchant-references", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Internal-Key", strings.TrimSpace(mc.cfg.PlatformAPIKey))
	resp, err := mc.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Say which. A 404 means the platform is reachable but has no such
		// endpoint — an older build. A 401 means the shared key does not match.
		// Both used to arrive here as "could not reach the booking platform",
		// which sends somebody to check firewalls for an afternoon.
		switch resp.StatusCode {
		case http.StatusNotFound:
			return nil, fmt.Errorf("the booking platform answered 404 — it is reachable, but has no /v1/internal/merchant-references. Deploy a build that has it, or check PLATFORM_BASE_URL points at the booking platform rather than something else")
		case http.StatusUnauthorized:
			return nil, fmt.Errorf("the booking platform rejected the key — PLATFORM_API_KEY here must equal INTERNAL_API_KEY there, and must be at least 32 characters")
		default:
			return nil, fmt.Errorf("the booking platform answered %d", resp.StatusCode)
		}
	}
	var body struct {
		Properties []platformProperty `json:"properties"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&body); err != nil {
		return nil, err
	}
	return body.Properties, nil
}

// Overview lists who this gateway can pay.
//
// THE GATEWAY'S OWN ROUTING COMES FIRST, ALWAYS. An earlier version built this
// list from the booking platform's properties and joined the local routing onto
// it. That made the entire screen — including the means to add a payee —
// disappear whenever the platform was unreachable, which is exactly when
// somebody needs to look at it. It also implied the platform owns who gets
// paid. It does not: this gateway does.
//
// So the rows are this gateway's recipients and mappings. The platform, when it
// answers, adds two things and nothing else: the property name behind a
// reference, and the properties it knows that are not mapped here yet. When it
// does not answer, the list still works and says so.
//
// SCOPED TO ONE APP, ALWAYS. Routing is keyed on (app, merchant_reference): two
// integrations may each pay the same property through a recipient of their own.
// Answering without naming an app has no single answer, so it refuses.
func (mc *MerchantRoutingOverviewController) Overview(c *fiber.Ctx) error {
	provider := strings.TrimSpace(c.Query("provider"))
	if provider != "stripe" {
		provider = "dkpg"
	}
	appID := strings.TrimSpace(c.Query("app_id"))
	if appID == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{
			"error": "app_id is required: routing is per app, so 'who gets paid' has no answer until one is named",
		})
	}

	mappings, err := mc.Repo.ListMappings(c.Context(), appID)
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "failed to list merchant mappings"})
	}
	recipients, err := mc.Repo.ListRecipients(c.Context())
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "failed to list recipients"})
	}
	recipientName := map[string]string{}
	for _, r := range recipients {
		recipientName[r.ID] = r.Name
	}

	// The platform is an enrichment, not a dependency. Its absence is reported
	// and the screen carries on.
	platform := map[string]platformProperty{}
	platformError := ""
	if mc.platformConfigured() {
		properties, err := mc.properties(c.Context())
		if err != nil {
			platformError = err.Error()
			log.Printf("merchant routing overview: %v (PLATFORM_BASE_URL=%q)", err, mc.cfg.PlatformBaseURL)
		}
		for _, property := range properties {
			if strings.TrimSpace(property.MerchantReference) != "" {
				platform[property.MerchantReference] = property
			}
		}
	} else {
		platformError = "not connected to the booking platform (PLATFORM_BASE_URL and PLATFORM_API_KEY are unset)"
	}

	rows := []routingRow{}
	mapped := map[string]bool{}

	for _, mapping := range mappings {
		if !mapping.IsActive || mapping.ExternalAppID != appID {
			continue
		}
		mapped[mapping.ExternalMerchantReference] = true
		row := routingRow{
			MappingID: mapping.ID,
			Recipient: recipientName[mapping.RecipientID],
		}
		row.MerchantReference = mapping.ExternalMerchantReference
		row.Property = row.Recipient
		// If the platform knows this reference, its name for the property wins:
		// that is the name a traveller booked, and the one a dispute will use.
		if property, known := platform[mapping.ExternalMerchantReference]; known {
			row.platformProperty = property
			row.KnownToPlatform = true
		} else if platformError == "" {
			row.Missing = append(row.Missing, "no property on the booking platform uses this reference")
		}

		resolved, err := mc.Repo.ResolveActive(c.Context(), appID, mapping.ExternalMerchantReference, provider)
		if err != nil {
			row.Missing = append(row.Missing, "no active "+provider+" destination")
			rows = append(rows, row)
			continue
		}
		values, err := mc.decrypt(resolved)
		if err != nil {
			row.Missing = append(row.Missing, "destination cannot be decrypted")
			rows = append(rows, row)
			continue
		}
		if provider == "dkpg" {
			row.BeneficiaryHint = maskAccount(values["beneficiary_account"])
			row.BeneficiaryName = values["beneficiary_name"]
			row.BeneficiaryBank = values["beneficiary_bank"]
		} else {
			row.BeneficiaryHint = values["submerchant_id"]
			row.BeneficiaryName = values["dk_account"]
		}
		row.Configured = true
		rows = append(rows, row)
	}

	// Properties the platform knows that nothing here pays yet. These are the
	// ones worth acting on, so they sort to the top on the page.
	for reference, property := range platform {
		if mapped[reference] {
			continue
		}
		wants := property.AcceptsBank
		if provider == "stripe" {
			wants = property.AcceptsCard
		}
		if !wants {
			continue
		}
		rows = append(rows, routingRow{
			platformProperty: property,
			KnownToPlatform:  true,
			Missing:          []string{"not set up in this gateway"},
		})
	}

	ready := 0
	for _, row := range rows {
		if row.Configured {
			ready++
		}
	}
	return c.JSON(fiber.Map{
		"connected":     platformError == "",
		"platformError": platformError,
		"platform":      mc.cfg.PlatformBaseURL,
		"provider":      provider, "appId": appID,
		"rows": rows, "ready": ready, "total": len(rows),
	})
}

func (mc *MerchantRoutingOverviewController) decrypt(routing *storage.ResolvedGatewayConfiguration) (map[string]string, error) {
	plaintext, err := mc.Cipher.Decrypt(routing.Credential.EncryptedCredentials,
		storage.CredentialAdditionalData(routing.Credential.ID, routing.Credential.RecipientID, routing.Credential.Provider, routing.Credential.Version))
	if err != nil {
		return nil, err
	}
	defer zero(plaintext)
	values := map[string]string{}
	if err := json.Unmarshal(plaintext, &values); err != nil {
		return nil, err
	}
	return values, nil
}

type provisionRequest struct {
	ExternalAppID     string            `json:"external_app_id" validate:"required,max=100"`
	MerchantReference string            `json:"merchant_reference" validate:"required,merchantref"`
	Name              string            `json:"name" validate:"required,min=2,max=100,safetext"`
	Provider          string            `json:"provider" validate:"required,oneof=dkpg stripe"`
	Credentials       map[string]string `json:"credentials"`
}

// Provision sets a property up to be paid, in one call.
// POST /api/admin/merchant-routing/provision
//
// Four steps that must all happen or none: create the recipient, store its
// destination, activate it, map the reference to it. Done separately — as they
// had to be — a failure halfway leaves a recipient nobody can pay through and
// no sign of which step was missed. This does them in order and undoes what it
// made if a later step fails.
func (mc *MerchantRoutingOverviewController) Provision(c *fiber.Ctx) error {
	var req provisionRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}
	req.ExternalAppID = strings.TrimSpace(req.ExternalAppID)
	req.MerchantReference = strings.TrimSpace(req.MerchantReference)
	req.Name = strings.TrimSpace(req.Name)
	req.Provider = strings.TrimSpace(req.Provider)
	if req.Provider == "" {
		req.Provider = "dkpg"
	}
	if err := validation.Struct(&req); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	// Trim inside the map too: the values are checked below and then encrypted,
	// so a trailing space would be stored and sent to the bank verbatim.
	for key, value := range req.Credentials {
		req.Credentials[key] = strings.TrimSpace(value)
	}
	if !validCredentialPayload(req.Provider, req.Credentials) {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": credentialPayloadProblem(req.Provider, req.Credentials)})
	}
	if err := validateCredentialFormat(req.Provider, req.Credentials); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	// Refuse before creating anything if this reference is already mapped.
	// Otherwise the recipient is made and only then does the mapping collide.
	existing, err := mc.Repo.ListMappings(c.Context(), req.ExternalAppID)
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "failed to read existing mappings"})
	}
	for _, m := range existing {
		if m.ExternalMerchantReference == req.MerchantReference && m.IsActive {
			return c.Status(http.StatusConflict).JSON(fiber.Map{
				"error": "that merchant reference is already mapped; disable the existing mapping first",
			})
		}
	}

	actor := adminActor(c)
	recipient := storage.PaymentRecipient{ID: uuid.NewString(), Name: req.Name, IsActive: true}
	if err := mc.Repo.CreateRecipient(c.Context(), recipient, actor); err != nil {
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "failed to create recipient"})
	}
	// From here on, anything that fails deactivates what was made. The recipient
	// is not deleted — the audit trail of who created what is worth more than a
	// tidy table, and an inactive recipient can pay nobody.
	undo := func() { _ = mc.Repo.SetRecipientActive(context.Background(), recipient.ID, false, actor) }

	credential, err := mc.Repo.CreateDraftCredential(c.Context(), recipient.ID, req.Provider,
		"env:PAYMENT_CREDENTIALS_MASTER_KEY_B64", actor, mc.credentialEncryptor(req.Credentials))
	if err != nil {
		undo()
		return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "failed to store the destination"})
	}
	if err := mc.Repo.SetCredentialStatus(c.Context(), credential.ID, "active", actor); err != nil {
		undo()
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "failed to activate the destination"})
	}
	if err := mc.Repo.CreateMapping(c.Context(), storage.IntegrationRecipientMapping{
		ExternalAppID: req.ExternalAppID, ExternalMerchantReference: req.MerchantReference,
		RecipientID: recipient.ID, IsActive: true,
	}, actor); err != nil {
		undo()
		return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "failed to map the merchant reference"})
	}

	return c.Status(http.StatusCreated).JSON(fiber.Map{
		"message": "property is now set up to be paid",
		"data": fiber.Map{
			"recipient_id":  recipient.ID,
			"credential_id": credential.ID,
		},
	})
}

func (mc *MerchantRoutingOverviewController) credentialEncryptor(values map[string]string) storage.CredentialEncryptor {
	return func(credential *storage.GatewayCredentialConfiguration) (string, error) {
		plaintext, err := json.Marshal(values)
		if err != nil {
			return "", err
		}
		defer zero(plaintext)
		return mc.Cipher.Encrypt(plaintext, storage.CredentialAdditionalData(
			credential.ID, credential.RecipientID, credential.Provider, credential.Version))
	}
}
