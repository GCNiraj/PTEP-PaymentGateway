package controllers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"example.com/fiber-mvc/internal/credentials"
	"example.com/fiber-mvc/internal/storage"
	"example.com/fiber-mvc/internal/validation"
)

// MerchantRoutingAdminController owns all recipient mappings and credential
// lifecycle changes. External callers have no routes to these operations.
type MerchantRoutingAdminController struct {
	Repo   *storage.MerchantRoutingRepository
	Cipher *credentials.Cipher
}

type createRecipientRequest struct {
	Name string `json:"name" validate:"required,min=2,max=100,safetext"`
}

type createMappingRequest struct {
	ExternalAppID             string `json:"external_app_id" validate:"required,max=100"`
	ExternalMerchantReference string `json:"merchant_reference" validate:"required,merchantref"`
	RecipientID               string `json:"recipient_id" validate:"required,max=100"`
}

type createCredentialRequest struct {
	RecipientID   string            `json:"recipient_id"`
	Provider      string            `json:"provider"`
	Credentials   map[string]string `json:"credentials"`
	RotatedFromID string            `json:"rotated_from_id"`
}

func NewMerchantRoutingAdminController(repo *storage.MerchantRoutingRepository, cipher *credentials.Cipher) *MerchantRoutingAdminController {
	return &MerchantRoutingAdminController{Repo: repo, Cipher: cipher}
}

func (ctl *MerchantRoutingAdminController) CreateRecipient(c *fiber.Ctx) error {
	var req createRecipientRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "recipient name is required"})
	}
	req.Name = strings.TrimSpace(req.Name)
	if err := validation.Struct(&req); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	recipient := storage.PaymentRecipient{ID: uuid.NewString(), Name: req.Name, IsActive: true}
	if err := ctl.Repo.CreateRecipient(c.Context(), recipient, adminActor(c)); err != nil {
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "failed to create recipient"})
	}
	return c.Status(http.StatusCreated).JSON(fiber.Map{"message": "recipient created", "data": fiber.Map{"recipient_id": recipient.ID}})
}

func (ctl *MerchantRoutingAdminController) ListRecipients(c *fiber.Ctx) error {
	items, err := ctl.Repo.ListRecipients(c.Context())
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "failed to list recipients"})
	}
	return c.JSON(fiber.Map{"data": items})
}

func (ctl *MerchantRoutingAdminController) SetRecipientActive(c *fiber.Ctx) error {
	active, ok := parseActiveBody(c)
	if !ok {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "is_active is required"})
	}
	if err := ctl.Repo.SetRecipientActive(c.Context(), c.Params("id"), active, adminActor(c)); err != nil {
		return statusForAdminResourceError(c, err)
	}
	return c.JSON(fiber.Map{"message": "recipient updated"})
}

func (ctl *MerchantRoutingAdminController) CreateMapping(c *fiber.Ctx) error {
	var req createMappingRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "external_app_id, merchant_reference, and recipient_id are required"})
	}
	req.ExternalAppID = strings.TrimSpace(req.ExternalAppID)
	req.ExternalMerchantReference = strings.TrimSpace(req.ExternalMerchantReference)
	req.RecipientID = strings.TrimSpace(req.RecipientID)
	if err := validation.Struct(&req); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if err := ctl.Repo.CreateMapping(c.Context(), storage.IntegrationRecipientMapping{ExternalAppID: req.ExternalAppID, ExternalMerchantReference: req.ExternalMerchantReference, RecipientID: req.RecipientID, IsActive: true}, adminActor(c)); err != nil {
		return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "failed to create merchant mapping"})
	}
	return c.Status(http.StatusCreated).JSON(fiber.Map{"message": "merchant mapping created"})
}

func (ctl *MerchantRoutingAdminController) ListMappings(c *fiber.Ctx) error {
	items, err := ctl.Repo.ListMappings(c.Context(), c.Query("external_app_id"))
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "failed to list merchant mappings"})
	}
	return c.JSON(fiber.Map{"data": items})
}

func (ctl *MerchantRoutingAdminController) SetMappingActive(c *fiber.Ctx) error {
	active, ok := parseActiveBody(c)
	if !ok {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "is_active is required"})
	}
	if err := ctl.Repo.SetMappingActive(c.Context(), c.Params("id"), active, adminActor(c)); err != nil {
		return statusForAdminResourceError(c, err)
	}
	return c.JSON(fiber.Map{"message": "merchant mapping updated"})
}

func (ctl *MerchantRoutingAdminController) CreateCredential(c *fiber.Ctx) error {
	var req createCredentialRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}
	if ctl == nil || ctl.Repo == nil || ctl.Cipher == nil || strings.TrimSpace(req.RecipientID) == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid gateway credential configuration"})
	}
	for key, value := range req.Credentials {
		req.Credentials[key] = strings.TrimSpace(value)
	}
	if !validCredentialPayload(req.Provider, req.Credentials) {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": credentialPayloadProblem(req.Provider, req.Credentials)})
	}
	if err := validateCredentialFormat(req.Provider, req.Credentials); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	credential, err := ctl.Repo.CreateDraftCredential(c.Context(), strings.TrimSpace(req.RecipientID), strings.TrimSpace(req.Provider), "env:PAYMENT_CREDENTIALS_MASTER_KEY_B64", adminActor(c), ctl.credentialEncryptor(req.Credentials))
	if err != nil {
		return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "failed to create gateway credential configuration"})
	}
	return c.Status(http.StatusCreated).JSON(fiber.Map{"message": "gateway credential configuration created", "data": safeCredential(credential)})
}

func (ctl *MerchantRoutingAdminController) RotateCredential(c *fiber.Ctx) error {
	old, err := ctl.Repo.GetCredential(c.Context(), c.Params("id"))
	if err != nil {
		return statusForAdminResourceError(c, err)
	}
	var req struct {
		Credentials map[string]string `json:"credentials"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}
	for key, value := range req.Credentials {
		req.Credentials[key] = strings.TrimSpace(value)
	}
	if !validCredentialPayload(old.Provider, req.Credentials) {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid gateway credential configuration"})
	}
	if err := validateCredentialFormat(old.Provider, req.Credentials); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	credential, err := ctl.Repo.RotateCredential(c.Context(), old.ID, "env:PAYMENT_CREDENTIALS_MASTER_KEY_B64", adminActor(c), ctl.credentialEncryptor(req.Credentials))
	if err != nil {
		return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "failed to rotate gateway credential configuration"})
	}
	return c.Status(http.StatusCreated).JSON(fiber.Map{"message": "gateway credential configuration rotated", "data": safeCredential(credential)})
}

func (ctl *MerchantRoutingAdminController) ListCredentials(c *fiber.Ctx) error {
	items, err := ctl.Repo.ListCredentials(c.Context(), c.Query("recipient_id"))
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "failed to list gateway credential configurations"})
	}
	safe := make([]storage.GatewayCredentialConfiguration, 0, len(items))
	for _, item := range items {
		safe = append(safe, safeCredential(item))
	}
	return c.JSON(fiber.Map{"data": safe})
}

func (ctl *MerchantRoutingAdminController) ActivateCredential(c *fiber.Ctx) error {
	if err := ctl.Repo.SetCredentialStatus(c.Context(), c.Params("id"), "active", adminActor(c)); err != nil {
		return statusForAdminResourceError(c, err)
	}
	return c.JSON(fiber.Map{"message": "gateway credential configuration activated"})
}

func (ctl *MerchantRoutingAdminController) DisableCredential(c *fiber.Ctx) error {
	if err := ctl.Repo.SetCredentialStatus(c.Context(), c.Params("id"), "disabled", adminActor(c)); err != nil {
		return statusForAdminResourceError(c, err)
	}
	return c.JSON(fiber.Map{"message": "gateway credential configuration disabled"})
}

func (ctl *MerchantRoutingAdminController) credentialEncryptor(values map[string]string) storage.CredentialEncryptor {
	return func(credential *storage.GatewayCredentialConfiguration) (string, error) {
		payload, err := json.Marshal(values)
		if err != nil {
			return "", err
		}
		defer zero(payload)
		return ctl.Cipher.Encrypt(payload, storage.CredentialAdditionalData(credential.ID, credential.RecipientID, credential.Provider, credential.Version))
	}
}

func safeCredential(credential storage.GatewayCredentialConfiguration) storage.GatewayCredentialConfiguration {
	credential.EncryptedCredentials = ""
	return credential
}

func validProvider(provider string) bool {
	return provider == "dkpg" || provider == "stripe"
}

// Credentials that identify this gateway to a provider, rather than
// distinguishing one recipient from another. They are identical on every
// payment and live in the environment, so a recipient record that carries them
// is describing something it does not get to decide, and is refused rather than
// quietly ignored.
var dkpgAuthKeys = []string{"api_key", "username", "password", "client_id", "client_secret", "source_app", "scopes", "private_key"}

var stripeAuthKeys = []string{"api_key", "agency_name"}

// sharedAuthKeys is what the environment owns for a provider.
func sharedAuthKeys(provider string) []string {
	if provider == "dkpg" {
		return dkpgAuthKeys
	}
	return stripeAuthKeys
}

// humanList joins field names the way a sentence would: "a, b and c".
func humanList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	default:
		return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
	}
}

// recipientKeys is what a record must carry for a provider: the part that
// actually distinguishes this recipient from the next one.
//
// Both providers know this gateway as a single integrator under a single
// identity. What differs per recipient is only where that recipient's money is
// booked — an account number on the bank side, a sub-merchant on the card side.
func recipientKeys(provider string) []string {
	if provider == "dkpg" {
		// beneficiary_bank is required, though its name undersells it. On
		// pull/initiate it is only a fallback for the remitter's bank code,
		// which the payer normally supplies — but on intra/inquiry it IS the
		// beneficiary's bank code, sent upstream with no fallback behind it. A
		// record without it would take a payment and then send the bank a blank
		// where the destination bank belongs.
		return []string{"beneficiary_account", "beneficiary_name", "beneficiary_bank"}
	}
	return []string{"submerchant_id", "dk_account"}
}

// validCredentialPayload reports whether a credential describes its recipient
// and nothing more.
func validCredentialPayload(provider string, values map[string]string) bool {
	if len(values) == 0 || !validProvider(provider) {
		return false
	}
	for _, key := range recipientKeys(provider) {
		if strings.TrimSpace(values[key]) == "" {
			return false
		}
	}
	for _, key := range sharedAuthKeys(provider) {
		if strings.TrimSpace(values[key]) != "" {
			return false
		}
	}
	return true
}

// supportedBeneficiaryBanks is the set of bank codes this gateway will book a
// beneficiary against. It is deliberately a list rather than a single constant
// so that adding a bank is one entry here, not a change of shape.
//
// It is 1060 and only 1060: every beneficiary on this deployment is at that
// bank. The value is passed straight through to DKPG as bene_bank_code on
// intra/inquiry with nothing behind it, so a typo here is a payment sent to a
// bank that was never meant to receive it — which is why this is checked rather
// than trusted.
var supportedBeneficiaryBanks = []string{"1060"}

func supportedBank(code string) bool {
	for _, supported := range supportedBeneficiaryBanks {
		if code == supported {
			return true
		}
	}
	return false
}

// validateCredentialFormat checks the shape of the values that describe where a
// recipient's money goes.
//
// validCredentialPayload above answers a different question — whether the right
// keys are present and no shared authentication has been smuggled in. It says
// nothing about what is *in* them, so "986543210saasa" was a valid account
// number and "<h1>x</h1>" a valid beneficiary name (ASD Cyber Security, 23
// September 2026, finding V2). These values are encrypted and then sent to a
// bank; a malformed one is discovered at settlement, by which time money has
// moved.
func validateCredentialFormat(provider string, values map[string]string) error {
	if provider == "dkpg" {
		if !validation.AccountNumber(values["beneficiary_account"]) {
			return errors.New("beneficiary_account must be 6 to 20 digits")
		}
		if !supportedBank(values["beneficiary_bank"]) {
			return errors.New("beneficiary_bank must be one of: " + strings.Join(supportedBeneficiaryBanks, ", "))
		}
		if name := values["beneficiary_name"]; len(name) < 2 || len(name) > 100 || !validation.SafeText(name) {
			return errors.New("beneficiary_name must be 2 to 100 characters and may contain only letters, digits, spaces and . , & ' ( ) - _ /")
		}
		return nil
	}

	// Stripe. These are identifiers the card processor issued, not free text.
	for _, key := range []string{"submerchant_id", "dk_account"} {
		value := values[key]
		// MerchantReference caps at 64, so the message must say 64 and not a
		// larger number it would then refuse.
		if len(value) < 2 || !validation.MerchantReference(value) {
			return errors.New(key + " must be 2 to 64 characters of letters, digits, hyphen or underscore")
		}
	}
	return nil
}

// credentialPayloadProblem says what is wrong, because "invalid gateway
// credential configuration" sends somebody to read the source.
func credentialPayloadProblem(provider string, values map[string]string) string {
	if !validProvider(provider) {
		return "provider must be dkpg or stripe"
	}
	label := "DKPG"
	if provider != "dkpg" {
		label = "Stripe"
	}
	for _, key := range sharedAuthKeys(provider) {
		if strings.TrimSpace(values[key]) != "" {
			return label + " authentication comes from this gateway's environment and is the same for every recipient; supply only " +
				strings.Join(recipientKeys(provider), ", ") + " (remove: " + key + ")"
		}
	}
	return humanList(recipientKeys(provider)) + " are required"
}

func adminActor(c *fiber.Ctx) string {
	actor, _ := c.Locals("admin_username").(string)
	return strings.TrimSpace(actor)
}

func parseActiveBody(c *fiber.Ctx) (bool, bool) {
	var req map[string]bool
	if err := c.BodyParser(&req); err != nil {
		return false, false
	}
	value, ok := req["is_active"]
	return value, ok
}

func statusForAdminResourceError(c *fiber.Ctx, err error) error {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, storage.ErrCredentialNotConfigured) {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "resource not found"})
	}
	return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "operation failed"})
}
