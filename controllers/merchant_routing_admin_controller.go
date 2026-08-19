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
)

// MerchantRoutingAdminController owns all recipient mappings and credential
// lifecycle changes. External callers have no routes to these operations.
type MerchantRoutingAdminController struct {
	Repo   *storage.MerchantRoutingRepository
	Cipher *credentials.Cipher
}

type createRecipientRequest struct {
	Name string `json:"name"`
}

type createMappingRequest struct {
	ExternalAppID             string `json:"external_app_id"`
	ExternalMerchantReference string `json:"merchant_reference"`
	RecipientID               string `json:"recipient_id"`
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
	if err := c.BodyParser(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "recipient name is required"})
	}
	recipient := storage.PaymentRecipient{ID: uuid.NewString(), Name: strings.TrimSpace(req.Name), IsActive: true}
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
	if err := c.BodyParser(&req); err != nil || strings.TrimSpace(req.ExternalAppID) == "" || strings.TrimSpace(req.ExternalMerchantReference) == "" || strings.TrimSpace(req.RecipientID) == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "external_app_id, merchant_reference, and recipient_id are required"})
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
	credential, err := ctl.newCredential(c, req)
	if err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid gateway credential configuration"})
	}
	if err := ctl.Repo.CreateCredential(c.Context(), credential, adminActor(c)); err != nil {
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
	credential, err := ctl.newCredential(c, createCredentialRequest{RecipientID: old.RecipientID, Provider: old.Provider, Credentials: req.Credentials, RotatedFromID: old.ID})
	if err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid gateway credential configuration"})
	}
	if err := ctl.Repo.CreateCredential(c.Context(), credential, adminActor(c)); err != nil {
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

func (ctl *MerchantRoutingAdminController) newCredential(c *fiber.Ctx, req createCredentialRequest) (storage.GatewayCredentialConfiguration, error) {
	if ctl == nil || ctl.Repo == nil || ctl.Cipher == nil || !validCredentialPayload(req.Provider, req.Credentials) || strings.TrimSpace(req.RecipientID) == "" {
		return storage.GatewayCredentialConfiguration{}, errors.New("invalid credential")
	}
	version, err := ctl.Repo.NextCredentialVersion(c.Context(), req.RecipientID, req.Provider)
	if err != nil {
		return storage.GatewayCredentialConfiguration{}, err
	}
	credential := storage.GatewayCredentialConfiguration{ID: uuid.NewString(), RecipientID: strings.TrimSpace(req.RecipientID), Provider: strings.TrimSpace(req.Provider), Version: version, Status: "draft", EncryptionKeyID: "env:PAYMENT_CREDENTIALS_MASTER_KEY_B64", RotatedFromID: strings.TrimSpace(req.RotatedFromID)}
	payload, err := json.Marshal(req.Credentials)
	if err != nil {
		return storage.GatewayCredentialConfiguration{}, err
	}
	defer zero(payload)
	encrypted, err := ctl.Cipher.Encrypt(payload, storage.CredentialAdditionalData(credential.ID, credential.RecipientID, credential.Provider, credential.Version))
	if err != nil {
		return storage.GatewayCredentialConfiguration{}, err
	}
	credential.EncryptedCredentials = encrypted
	return credential, nil
}

func safeCredential(credential storage.GatewayCredentialConfiguration) storage.GatewayCredentialConfiguration {
	credential.EncryptedCredentials = ""
	return credential
}

func validProvider(provider string) bool {
	return provider == "dkpg" || provider == "stripe"
}

func validCredentialPayload(provider string, values map[string]string) bool {
	if len(values) == 0 || !validProvider(provider) {
		return false
	}
	required := []string{"api_key"}
	if provider == "dkpg" {
		required = append(required, "username", "password", "client_id", "client_secret", "source_app", "beneficiary_account", "beneficiary_name", "beneficiary_bank")
	} else {
		required = append(required, "agency_name", "submerchant_id", "dk_account")
	}
	for _, key := range required {
		if strings.TrimSpace(values[key]) == "" {
			return false
		}
	}
	return true
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
