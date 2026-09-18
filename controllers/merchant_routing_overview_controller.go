package controllers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"example.com/fiber-mvc/config"
	"example.com/fiber-mvc/internal/credentials"
	"example.com/fiber-mvc/internal/storage"
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
		return nil, errPlatformRefused
	}
	var body struct {
		Properties []platformProperty `json:"properties"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&body); err != nil {
		return nil, err
	}
	return body.Properties, nil
}

var errPlatformRefused = fiber.NewError(http.StatusBadGateway, "the booking platform refused the request")

// Overview lists every property and whether this gateway can pay it.
// GET /api/admin/merchant-routing/overview?provider=dkpg&app_id=APP-xxxxxx
//
// SCOPED TO ONE APP, ALWAYS. Routing is keyed on (app, merchant_reference): two
// integrations may each pay the same property through a recipient of their own,
// and that is deliberate. Answering without naming an app therefore has no
// single answer — an earlier version of this collapsed them and showed whichever
// mapping happened to come last, which meant a screen that could name the wrong
// bank account with complete confidence. It now refuses instead, and hands back
// the list of apps so the caller can pick.
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

	if !mc.platformConfigured() {
		return c.JSON(fiber.Map{
			"connected": false,
			"rows":      []routingRow{},
			"message":   "This gateway is not connected to the booking platform, so it cannot list properties. Set PLATFORM_BASE_URL and PLATFORM_API_KEY.",
		})
	}
	properties, err := mc.properties(c.Context())
	if err != nil {
		return c.Status(http.StatusBadGateway).JSON(fiber.Map{
			"connected": false,
			"error":     "could not reach the booking platform",
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

	// merchant_reference → recipient, within this one app. ListMappings is
	// already filtered by app, and the app is checked again here rather than
	// trusted: a mapping from another integration in this map would attribute
	// somebody else's bank account to this property.
	byReference := map[string]storage.IntegrationRecipientMapping{}
	for _, m := range mappings {
		if m.IsActive && m.ExternalAppID == appID {
			byReference[m.ExternalMerchantReference] = m
		}
	}

	rows := make([]routingRow, 0, len(properties))
	for _, property := range properties {
		row := routingRow{platformProperty: property}

		// A property that does not take money this way needs no routing, and
		// reporting it as missing would bury the ones that do.
		wants := property.AcceptsBank
		if provider == "stripe" {
			wants = property.AcceptsCard
		}

		if strings.TrimSpace(property.MerchantReference) == "" {
			row.Missing = append(row.Missing, "merchant reference (set on the booking platform)")
			rows = append(rows, row)
			continue
		}
		mapping, mapped := byReference[property.MerchantReference]
		if !mapped {
			if wants {
				row.Missing = append(row.Missing, "not mapped in this gateway")
			}
			rows = append(rows, row)
			continue
		}
		row.Recipient = recipientName[mapping.RecipientID]

		// Resolve exactly as a payment would, so this screen cannot claim a
		// property is ready when a payment would be refused.
		resolved, err := mc.Repo.ResolveActive(c.Context(), mapping.ExternalAppID, property.MerchantReference, provider)
		if err != nil {
			if wants {
				row.Missing = append(row.Missing, "no active "+provider+" credential")
			}
			rows = append(rows, row)
			continue
		}
		values, err := mc.decrypt(resolved)
		if err != nil {
			row.Missing = append(row.Missing, "credential cannot be decrypted")
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

	ready := 0
	for _, row := range rows {
		if row.Configured {
			ready++
		}
	}
	return c.JSON(fiber.Map{
		"connected": true, "provider": provider, "appId": appID,
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
	ExternalAppID     string            `json:"external_app_id"`
	MerchantReference string            `json:"merchant_reference"`
	Name              string            `json:"name"`
	Provider          string            `json:"provider"`
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
	if req.ExternalAppID == "" || req.MerchantReference == "" || req.Name == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "external_app_id, merchant_reference and name are required"})
	}
	if !validCredentialPayload(req.Provider, req.Credentials) {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": credentialPayloadProblem(req.Provider, req.Credentials)})
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
