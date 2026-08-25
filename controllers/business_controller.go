package controllers

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/config"
	"example.com/fiber-mvc/internal/common"
	"example.com/fiber-mvc/internal/dkpg"
	"example.com/fiber-mvc/internal/storage"
	"example.com/fiber-mvc/middleware"
	"example.com/fiber-mvc/models"
)

type BusinessController struct {
	Repo     *storage.Repository
	Cfg      config.Config
	Resolver *GatewayResolver
}

// NewBusinessController constructs the business API controller.
// Called from: main() to wire /api/business routes.
func NewBusinessController(repo *storage.Repository, cfg config.Config, resolver *GatewayResolver) *BusinessController {
	return &BusinessController{Repo: repo, Cfg: cfg, Resolver: resolver}
}

// PullPaymentInitiate starts pull-payment auth flow (OTP initiation).
// Why needed: this is the canonical business API for non-intra pull payments.
// Request fields (accepted aliases):
// - external_reference/order_id, transaction_amount/amount, purpose/payment_desc
// - remitter_account_number, remitter_account_name
// - customer_phone/phone_number/remitter_phone, email_id
// - remitter_bank_code (preferred), remitter_bank_id (legacy alias)
// Server-owned fields:
// - account_number is always set from DK_BENEFICIARY_ACCOUNT (client value ignored)
// - account_name is always set from DK_BENEFICIARY_NAME (client value ignored)
// - remitter_bank_id falls back to DK_BENEFICIARY_BANK when client value is missing
// Response:
// - 400 invalid JSON
// - 409 duplicate external reference
// - 500 DB errors
// - 502 upstream DKPG call error
// - 200 sanitized response envelope on upstream completion
// Called from: POST /api/business/pull/initiate.
// Next flow: client should call PullPaymentConfirm with transaction_id (STAN) + otp.
func (ctl *BusinessController) PullPaymentInitiate(c *fiber.Ctx) error {
	raw := map[string]any{}
	if err := c.BodyParser(&raw); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}
	req := models.PullPaymentInitiateRequest{
		MerchantReference:     readString(raw, "merchant_reference"),
		Reference:             firstNonEmpty(readString(raw, "external_reference"), readString(raw, "order_id")),
		Amount:                firstNonZero(readFloat(raw, "transaction_amount"), readFloat(raw, "amount")),
		TransactionFee:        readFloat(raw, "transaction_fee"),
		Purpose:               firstNonEmpty(readString(raw, "purpose"), readString(raw, "payment_desc"), readString(raw, "description")),
		EmailID:               readString(raw, "email_id"),
		RemitterAccountNumber: readString(raw, "remitter_account_number"),
		RemitterAccountName:   readString(raw, "remitter_account_name"),
		CustomerPhone:         firstNonEmpty(readString(raw, "customer_phone"), readString(raw, "phone_number"), readString(raw, "remitter_phone")),
		RemitterBankCode:      firstNonEmpty(readString(raw, "remitter_bank_code"), readString(raw, "remitter_bank_id")),
	}
	if req.Reference == "" {
		req.Reference = "REF-" + common.NewRequestID()
	}
	appID := middleware.GetAppID(c)
	resolved, client, err := ctl.resolveDKPGForMerchant(appID, req.MerchantReference)
	if err != nil {
		return merchantConfigurationError(c)
	}
	if exists, err := ctl.Repo.OrderIDExistsForApp(common.CtxFromFiber(c), appID, req.Reference); err != nil {
		return sendSanitizedInternalError(c)
	} else if exists {
		return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "external_reference already used"})
	}

	payload := make(map[string]any, len(raw)+4)
	for k, v := range raw {
		payload[k] = v
	}
	// Routing is service-internal and must not alter the upstream DKPG contract.
	delete(payload, "merchant_reference")
	delete(payload, "external_app_id")
	beneficiaryAccountCfg := ctl.Resolver.Value(resolved, "beneficiary_account")
	beneficiaryNameCfg := ctl.Resolver.Value(resolved, "beneficiary_name")
	remitterBankCfg := ctl.Resolver.Value(resolved, "beneficiary_bank")
	if beneficiaryAccountCfg == "" || beneficiaryNameCfg == "" {
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{
			"error": "server beneficiary configuration is missing",
		})
	}
	remitterBankID := firstNonEmpty(req.RemitterBankCode, remitterBankCfg)
	if remitterBankID == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{
			"error": "remitter_bank_code is required",
		})
	}

	requestID := common.NewRequestID()
	stan := firstNonEmpty(readString(raw, "stan_number"), common.GenerateSTAN(ctl.Resolver.Value(resolved, "source_app")))
	txnTime := time.Now().UTC()
	if dt := readString(raw, "transaction_datetime"); dt != "" {
		if parsed, err := time.Parse(time.RFC3339, dt); err == nil {
			txnTime = parsed
		}
	}
	payload["request_id"] = requestID
	payload["transaction_datetime"] = txnTime.Format(time.RFC3339)
	payload["stan_number"] = stan
	// Always use server-owned beneficiary values; ignore client-provided values.
	payload["account_number"] = beneficiaryAccountCfg
	payload["account_name"] = beneficiaryNameCfg
	payload["remitter_bank_id"] = remitterBankID
	payload["source_app"] = ctl.Resolver.Value(resolved, "source_app")
	delete(payload, "remitter_bank_code")
	if _, ok := payload["transaction_amount"]; !ok && req.Amount > 0 {
		payload["transaction_amount"] = req.Amount
	}
	if _, ok := payload["purpose"]; !ok && req.Purpose != "" {
		payload["purpose"] = req.Purpose
	}
	// Fallback for compatibility if downstream expects payment_desc
	payload["payment_desc"] = req.Purpose

	if _, ok := payload["customer_phone"]; !ok && req.CustomerPhone != "" {
		payload["customer_phone"] = req.CustomerPhone
	}
	// Fallback for compatibility
	payload["phone_number"] = req.CustomerPhone

	payload["external_reference"] = req.Reference
	// Fallback
	payload["order_id"] = req.Reference

	if err := ctl.Repo.CreatePullPayment(common.CtxFromFiber(c), storage.PullPaymentRecord{
		ExternalAppID:                    appID,
		ExternalMerchantReference:        req.MerchantReference,
		RecipientID:                      resolved.Routing.RecipientID,
		GatewayProvider:                  resolved.Routing.Credential.Provider,
		GatewayCredentialConfigurationID: resolved.Routing.Credential.ID,
		GatewayCredentialVersion:         resolved.Routing.Credential.Version,
		RoutingMode:                      "merchant",
		OrderID:                          req.Reference,
		STAN:                             stan,
		BFSTxnID:                         "",
		Amount:                           req.Amount,
		TransactionFee:                   req.TransactionFee,
		RemitterAccount:                  req.RemitterAccountNumber,
		RemitterBank:                     remitterBankID,
		BeneficiaryAccount:               beneficiaryAccountCfg,
		RemitterName:                     req.RemitterAccountName,
		RemitterPhone:                    req.CustomerPhone,
		EmailID:                          req.EmailID,
		TransactionDatetime:              txnTime,
		PaymentDesc:                      req.Purpose,
		Currency:                         "BTN",
		Status:                           "PENDING",
		ErrorCode:                        "",
		ErrorMessage:                     "",
	}); err != nil {
		if storage.IsDuplicateOrderIDError(err) {
			return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "external_reference already used"})
		}
		return sendSanitizedInternalError(c)
	}

	resBytes, err := client.AccountAuthPullPayment(common.CtxFromFiber(c), payload)
	if err != nil {
		_ = ctl.Repo.UpdatePullPaymentStatus(common.CtxFromFiber(c), appID, stan, "FAILED", "", err.Error())
		return sendSanitizedUpstreamError(c)
	}

	bfsTxnID := common.ExtractBFSTxnID(resBytes)
	code, message := common.ExtractResponseCode(resBytes)
	status := "OTP_SENT"
	if code != "" && code != "0000" {
		status = "FAILED"
	}
	if bfsTxnID != "" {
		_ = ctl.Repo.UpdateBFSTxnID(common.CtxFromFiber(c), appID, stan, bfsTxnID)
	}
	if bfsOrderNo := common.ExtractBFSOrderNo(resBytes); bfsOrderNo != "" {
		_ = ctl.Repo.UpdateBFSOrderNo(common.CtxFromFiber(c), appID, stan, bfsOrderNo)
	}
	_ = ctl.Repo.UpdatePullPaymentStatus(common.CtxFromFiber(c), appID, stan, status, code, message)

	return sendSanitizedDKResponse(c, resBytes, fiber.Map{
		"transaction_id":     stan,
		"external_reference": req.Reference,
		"status":             status,
		"next_step":          "confirm_otp",
	})
}

// PullPaymentConfirm finalizes pull-payment using OTP verification.
// Why needed: second leg of pull-payment flow to move from OTP_SENT to terminal state.
// Request fields:
// - transaction_id/reference/stan_number, otp/bfs_remitter_Otp
// - bfs_txn_id (optional if already stored in DB)
// - bfs_orderNo/bfs_order_no (optional; looked up from DB, fallback auto-generated)
// Response:
// - 400 invalid JSON, missing required fields, or missing bfs_txn_id lookup
// - 502 upstream DKPG call error
// - 200 sanitized response envelope on upstream completion
// Called from: POST /api/business/pull/confirm.
// Next flow: updates local transaction status and confirm metadata in storage.Repository.
func (ctl *BusinessController) PullPaymentConfirm(c *fiber.Ctx) error {
	raw := map[string]any{}
	if err := c.BodyParser(&raw); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}
	req := models.PullPaymentConfirmRequest{
		TransactionID: firstNonEmpty(readString(raw, "transaction_id"), readString(raw, "reference"), readString(raw, "stan_number")),
		OTP:           firstNonEmpty(readString(raw, "otp"), readString(raw, "bfs_remitter_Otp")),
		OrderID:       readString(raw, "order_id"),
		BFSOrderNo:    firstNonEmpty(readString(raw, "bfs_orderNo"), readString(raw, "bfs_order_no")),
		BFSTxnID:      readString(raw, "bfs_txn_id"),
	}

	// Map TransactionID to Reference for lookups
	req.Reference = req.TransactionID

	if req.Reference == "" || req.OTP == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "transaction_id and otp are required"})
	}
	routing, err := ctl.Repo.GetTransactionRoutingBySTAN(common.CtxFromFiber(c), middleware.GetAppID(c), req.Reference)
	if err != nil || routing.GatewayCredentialConfigurationID == "" {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "transaction not found"})
	}
	client, err := ctl.resolveDKPGForRecordedCredential(routing.GatewayCredentialConfigurationID)
	if err != nil {
		return merchantConfigurationError(c)
	}
	bfsTxnID := routing.BFSTxnID

	if bfsTxnID == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "missing bfs_txn_id for transaction"})
	}

	bfsOrderNo := routing.BFSOrderNo
	if bfsOrderNo == "" {
		bfsOrderNo = common.NewBFSOrderNo(req.Reference)
	}

	requestID := common.NewRequestID()
	payload := map[string]any{
		"request_id":       requestID,
		"bfs_TxnId":        bfsTxnID,
		"bfs_remitter_Otp": req.OTP,
	}
	if bfsOrderNo != "" {
		payload["bfs_orderNo"] = bfsOrderNo
	}

	resBytes, err := client.DebitRequestPullPayment(common.CtxFromFiber(c), payload)
	if err != nil {
		_ = ctl.Repo.UpdatePullPaymentStatus(common.CtxFromFiber(c), middleware.GetAppID(c), req.Reference, "FAILED", "", err.Error())
		return sendSanitizedUpstreamError(c)
	}

	code, message := common.ExtractResponseCode(resBytes)
	status := "COMPLETED"
	if code != "" && code != "0000" {
		status = "FAILED"
	}
	appID := middleware.GetAppID(c)
	_ = ctl.Repo.UpdateConfirmMeta(common.CtxFromFiber(c), appID, req.Reference, requestID, bfsOrderNo)
	_ = ctl.Repo.UpdatePullPaymentStatus(common.CtxFromFiber(c), appID, req.Reference, status, code, message)

	return sendSanitizedDKResponse(c, resBytes, fiber.Map{
		"transaction_id": req.Reference,
		"status":         status,
	})
}

// IntraInquiry performs beneficiary inquiry for intra-bank transfer flow.
// Why needed: DK requires inquiry_id from this call before initiating transfer.
// Request fields: external_reference/order_id, transaction_amount/amount, remitter_account_number.
// Optional request fields: source_account_name/remitter_account_name.
// Response:
// - 400 invalid JSON or missing required fields
// - 502 upstream/network failure
// - 200 sanitized response envelope when inquiry reaches upstream
// Called from: POST /api/business/intra/inquiry.
// Next flow: client uses returned inquiry_id in IntraTransfer.
func (ctl *BusinessController) IntraInquiry(c *fiber.Ctx) error {
	raw := map[string]any{}
	if err := c.BodyParser(&raw); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}
	req := models.IntraInquiryRequest{
		MerchantReference:     readString(raw, "merchant_reference"),
		Reference:             firstNonEmpty(readString(raw, "external_reference"), readString(raw, "order_id")),
		TransactionAmount:     firstNonZero(readFloat(raw, "transaction_amount"), readFloat(raw, "amount")),
		RemitterAccountNumber: readString(raw, "remitter_account_number"),
	}

	missing := make([]string, 0)
	if req.Reference == "" {
		missing = append(missing, "external_reference")
	}
	if req.TransactionAmount <= 0 {
		missing = append(missing, "transaction_amount")
	}
	if req.RemitterAccountNumber == "" {
		missing = append(missing, "remitter_account_number")
	}
	if len(missing) > 0 {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "missing required fields", "missing_fields": missing})
	}
	resolved, client, err := ctl.resolveDKPGForMerchant(middleware.GetAppID(c), req.MerchantReference)
	if err != nil {
		return merchantConfigurationError(c)
	}

	// Prevent duplicate external_reference usage across inquiry and transfer records.
	if exists, err := ctl.Repo.OrderIDExistsForApp(common.CtxFromFiber(c), middleware.GetAppID(c), req.Reference); err != nil {
		return sendSanitizedInternalError(c)
	} else if exists {
		return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "external_reference already used"})
	}
	appID := middleware.GetAppID(c)
	if exists, err := ctl.Repo.IntraInquiryOrderIDExistsForApp(common.CtxFromFiber(c), appID, req.Reference); err != nil {
		return sendSanitizedInternalError(c)
	} else if exists {
		return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "external_reference already used"})
	}

	// Step 1: Account Inquiry
	requestID := common.NewRequestID()
	sourceAccountName := firstNonEmpty(
		readString(raw, "source_account_name"),
		readString(raw, "remitter_account_name"),
		ctl.Resolver.Value(resolved, "source_account_name"),
	)
	inquiryPayload := map[string]any{
		"request_id":           requestID,
		"amount":               strconv.FormatFloat(req.TransactionAmount, 'f', 2, 64),
		"currency":             "BTN",
		"bene_bank_code":       ctl.Resolver.Value(resolved, "beneficiary_bank"),
		"bene_account_number":  ctl.Resolver.Value(resolved, "beneficiary_account"),
		"source_account_name":  sourceAccountName,
		"soure_account_number": req.RemitterAccountNumber,
	}

	inquiryResBytes, err := client.BeneficiaryAccountInquiry(common.CtxFromFiber(c), inquiryPayload)
	if err != nil {
		// Store failed inquiry attempt
		_ = ctl.Repo.CreateIntraInquiry(common.CtxFromFiber(c), storage.IntraInquiryRecord{
			ExternalAppID:                    appID,
			ExternalMerchantReference:        req.MerchantReference,
			RecipientID:                      resolved.Routing.RecipientID,
			GatewayProvider:                  resolved.Routing.Credential.Provider,
			GatewayCredentialConfigurationID: resolved.Routing.Credential.ID,
			GatewayCredentialVersion:         resolved.Routing.Credential.Version,
			RoutingMode:                      "merchant",
			OrderID:                          req.Reference,
			BeneficiaryAccount:               ctl.Resolver.Value(resolved, "beneficiary_account"),
			Amount:                           req.TransactionAmount,
			Status:                           "FAILED",
			ErrorCode:                        "network_error",
			ErrorMessage:                     err.Error(),
		})
		return sendSanitizedUpstreamError(c)
	}

	inquiryID := extractInquiryID(inquiryResBytes)
	code, msg := common.ExtractResponseCode(inquiryResBytes)

	// Determine status
	status := "SUCCESS"
	if code != "0000" {
		status = "FAILED"
	}

	// Store inquiry in database (all attempts, success or failure)
	if dbErr := ctl.Repo.CreateIntraInquiry(common.CtxFromFiber(c), storage.IntraInquiryRecord{
		InquiryID:                        inquiryID,
		ExternalAppID:                    appID,
		ExternalMerchantReference:        req.MerchantReference,
		RecipientID:                      resolved.Routing.RecipientID,
		GatewayProvider:                  resolved.Routing.Credential.Provider,
		GatewayCredentialConfigurationID: resolved.Routing.Credential.ID,
		GatewayCredentialVersion:         resolved.Routing.Credential.Version,
		RoutingMode:                      "merchant",
		OrderID:                          req.Reference,
		BeneficiaryAccount:               ctl.Resolver.Value(resolved, "beneficiary_account"),
		Amount:                           req.TransactionAmount,
		Status:                           status,
		ErrorCode:                        code,
		ErrorMessage:                     msg,
	}); dbErr != nil {
		if storage.IsDuplicateIntraInquiryOrderIDError(dbErr) {
			return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "external_reference already used"})
		}
		log.Printf("ERROR: Failed to store inquiry in DB: %v", dbErr)
		return sendSanitizedInternalError(c)
	}

	// Return response (even if failed)
	if inquiryID == "" {
		return c.Status(http.StatusBadGateway).JSON(fiber.Map{
			"success": false,
			"code":    "INVALID_UPSTREAM_RESPONSE",
			"message": "Inquiry could not be completed at this time.",
		})
	}

	return sendSanitizedDKResponse(c, inquiryResBytes, fiber.Map{
		"inquiry_id":         inquiryID,
		"external_reference": req.Reference,
		"status":             status,
	})
}

// StatusSameDay queries DK for same-day transaction status.
// Request fields: external_reference/order_id (preferred) or transaction_id, optional request_id.
// Beneficiary account is always taken from server configuration.
// Response: 400 validation errors, 502 upstream failures, else 200 sanitized response envelope.
// Called from: POST /api/business/status/same-day.
func (ctl *BusinessController) StatusSameDay(c *fiber.Ctx) error {
	raw := map[string]any{}
	if err := c.BodyParser(&raw); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}
	req := models.StatusSameDayRequest{
		TransactionID:     readString(raw, "transaction_id"),
		ExternalReference: firstNonEmpty(readString(raw, "external_reference"), readString(raw, "order_id")),
	}
	if req.TransactionID == "" && req.ExternalReference == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "external_reference (or transaction_id) is required"})
	}

	appID := middleware.GetAppID(c)
	transactionID := req.TransactionID
	var routing *storage.TransactionGatewayRouting
	if req.ExternalReference != "" {
		var err error
		routing, err = ctl.Repo.GetTransactionRoutingByOrderID(common.CtxFromFiber(c), appID, req.ExternalReference)
		if err != nil {
			if err == sql.ErrNoRows {
				return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "external_reference not found"})
			}
			return sendSanitizedInternalError(c)
		}
		if strings.TrimSpace(routing.BFSTxnID) == "" {
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "transaction_id not available yet for this external_reference"})
		}
		transactionID = routing.BFSTxnID
	} else {
		var err error
		routing, err = ctl.Repo.GetTransactionRoutingByBFSTxnID(common.CtxFromFiber(c), appID, transactionID)
		if err != nil {
			return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "transaction not found"})
		}
	}
	if strings.TrimSpace(transactionID) == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "transaction_id could not be resolved"})
	}

	resolved, err := ctl.Resolver.ResolveCredential(routing.GatewayCredentialConfigurationID)
	if err != nil || resolved.Routing.Credential.Provider != "dkpg" {
		return merchantConfigurationError(c)
	}
	client, err := ctl.Resolver.DKPGClient(resolved)
	if err != nil {
		return merchantConfigurationError(c)
	}
	beneAccountNumber := ctl.Resolver.Value(resolved, "beneficiary_account")
	if beneAccountNumber == "" {
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "server beneficiary account configuration is missing"})
	}

	requestID := common.NewRequestID()
	payload := map[string]any{
		"request_id":          requestID,
		"transaction_id":      transactionID,
		"bene_account_number": beneAccountNumber,
	}

	resBytes, err := client.TransactionStatus(common.CtxFromFiber(c), payload)
	if err != nil {
		return sendSanitizedUpstreamError(c)
	}

	data := fiber.Map{
		"transaction_id": transactionID,
	}
	if status := extractTransactionStatus(resBytes); status != "" {
		data["transaction_status"] = status
	}
	return sendSanitizedDKResponse(c, resBytes, data)
}

// StatusLater queries DK for historical (non same-day) transaction status.
// Request fields: external_reference/order_id (preferred) or transaction_id, transaction_date/trasnaction_date.
// Beneficiary account is always taken from server configuration.
// Response: 400 validation errors, 502 upstream failures, else 200 sanitized response envelope.
// Called from: POST /api/business/status/later.
func (ctl *BusinessController) StatusLater(c *fiber.Ctx) error {
	raw := map[string]any{}
	if err := c.BodyParser(&raw); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}
	req := models.StatusLaterRequest{
		TransactionID:     readString(raw, "transaction_id"),
		ExternalReference: firstNonEmpty(readString(raw, "external_reference"), readString(raw, "order_id")),
		TransactionDate:   firstNonEmpty(readString(raw, "transaction_date"), readString(raw, "trasnaction_date")),
	}
	if req.TransactionDate == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "transaction_date is required"})
	}
	if req.TransactionID == "" && req.ExternalReference == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "external_reference (or transaction_id) is required"})
	}

	appID := middleware.GetAppID(c)
	transactionID := req.TransactionID
	var routing *storage.TransactionGatewayRouting
	if req.ExternalReference != "" {
		var err error
		routing, err = ctl.Repo.GetTransactionRoutingByOrderID(common.CtxFromFiber(c), appID, req.ExternalReference)
		if err != nil {
			if err == sql.ErrNoRows {
				return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "external_reference not found"})
			}
			return sendSanitizedInternalError(c)
		}
		if strings.TrimSpace(routing.BFSTxnID) == "" {
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "transaction_id not available yet for this external_reference"})
		}
		transactionID = routing.BFSTxnID
	} else {
		var err error
		routing, err = ctl.Repo.GetTransactionRoutingByBFSTxnID(common.CtxFromFiber(c), appID, transactionID)
		if err != nil {
			return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "transaction not found"})
		}
	}
	if strings.TrimSpace(transactionID) == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "transaction_id could not be resolved"})
	}

	resolved, err := ctl.Resolver.ResolveCredential(routing.GatewayCredentialConfigurationID)
	if err != nil || resolved.Routing.Credential.Provider != "dkpg" {
		return merchantConfigurationError(c)
	}
	client, err := ctl.Resolver.DKPGClient(resolved)
	if err != nil {
		return merchantConfigurationError(c)
	}
	beneAccountNumber := ctl.Resolver.Value(resolved, "beneficiary_account")
	if beneAccountNumber == "" {
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "server beneficiary account configuration is missing"})
	}

	requestID := common.NewRequestID()
	payload := map[string]any{
		"request_id":          requestID,
		"transaction_id":      transactionID,
		"transaction_date":    req.TransactionDate,
		"bene_account_number": beneAccountNumber,
	}

	resBytes, err := client.TransactionsStatus(common.CtxFromFiber(c), payload)
	if err != nil {
		return sendSanitizedUpstreamError(c)
	}

	data := fiber.Map{
		"transaction_id":   transactionID,
		"transaction_date": req.TransactionDate,
	}
	if status := extractTransactionStatus(resBytes); status != "" {
		data["transaction_status"] = status
	}
	return sendSanitizedDKResponse(c, resBytes, data)
}

// StatusIntra queries DK for intra-transaction status by reference number.
// Request fields: external_reference/order_id (preferred) or reference_no, transaction_date.
// Beneficiary account is always taken from server configuration.
// Response: 400 validation errors, 502 upstream failures, else 200 sanitized response envelope.
// Called from: POST /api/business/status/intra.
func (ctl *BusinessController) StatusIntra(c *fiber.Ctx) error {
	raw := map[string]any{}
	if err := c.BodyParser(&raw); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}
	req := models.StatusIntraRequest{
		ExternalReference: firstNonEmpty(readString(raw, "external_reference"), readString(raw, "order_id")),
		ReferenceNo:       readString(raw, "reference_no"),
		TransactionDate:   readString(raw, "transaction_date"),
	}
	if req.TransactionDate == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "transaction_date is required"})
	}
	if req.ReferenceNo == "" && req.ExternalReference == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "external_reference (or reference_no) is required"})
	}

	appID := middleware.GetAppID(c)
	referenceNo := req.ReferenceNo
	var routing *storage.TransactionGatewayRouting
	if req.ExternalReference != "" {
		var err error
		routing, err = ctl.Repo.GetTransactionRoutingByOrderID(common.CtxFromFiber(c), appID, req.ExternalReference)
		if err != nil {
			if err == sql.ErrNoRows {
				return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "external_reference not found"})
			}
			return sendSanitizedInternalError(c)
		}
		if strings.TrimSpace(routing.STAN) == "" {
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "reference_no not available yet for this external_reference"})
		}
		referenceNo = routing.STAN
	} else {
		var err error
		routing, err = ctl.Repo.GetTransactionRoutingBySTAN(common.CtxFromFiber(c), appID, referenceNo)
		if err != nil {
			return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "transaction not found"})
		}
	}
	if strings.TrimSpace(referenceNo) == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "reference_no could not be resolved"})
	}

	resolved, err := ctl.Resolver.ResolveCredential(routing.GatewayCredentialConfigurationID)
	if err != nil || resolved.Routing.Credential.Provider != "dkpg" {
		return merchantConfigurationError(c)
	}
	client, err := ctl.Resolver.DKPGClient(resolved)
	if err != nil {
		return merchantConfigurationError(c)
	}
	beneAccountNumber := ctl.Resolver.Value(resolved, "beneficiary_account")
	if beneAccountNumber == "" {
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "server beneficiary account configuration is missing"})
	}

	requestID := common.NewRequestID()
	payload := map[string]any{
		"request_id":          requestID,
		"reference_no":        referenceNo,
		"transaction_date":    req.TransactionDate,
		"bene_account_number": beneAccountNumber,
	}

	resBytes, err := client.IntraTransactionStatus(common.CtxFromFiber(c), payload)
	if err != nil {
		return sendSanitizedUpstreamError(c)
	}

	data := fiber.Map{
		"reference_no":     referenceNo,
		"transaction_date": req.TransactionDate,
	}
	if req.ExternalReference != "" {
		data["external_reference"] = req.ExternalReference
	}
	if status := extractTransactionStatus(resBytes); status != "" {
		data["transaction_status"] = status
	}
	return sendSanitizedDKResponse(c, resBytes, data)
}

// extractInquiryID reads response_data.inquiry_id from upstream DK response JSON.
// Called from: IntraInquiry to persist and validate inquiry state.
func extractInquiryID(raw []byte) string {
	var res struct {
		ResponseData struct {
			InquiryID string `json:"inquiry_id"`
		} `json:"response_data"`
	}
	_ = json.Unmarshal(raw, &res)
	return strings.TrimSpace(res.ResponseData.InquiryID)
}

// extractTransactionStatus returns a best-effort status value from upstream response_data.
// Called from: sanitized status-check responses.
func extractTransactionStatus(raw []byte) string {
	_, _, data := parseDKResponse(raw)
	if len(data) == 0 {
		return ""
	}
	return firstNonEmpty(
		readString(data, "transaction_status"),
		readString(data, "status"),
		readString(data, "txn_status"),
		readString(data, "payment_status"),
		readString(data, "transaction_state"),
		readString(data, "state"),
	)
}

// parseDKResponse normalizes wrapped and non-wrapped DK response shapes.
// Supports:
// - {"response_code":"...","response_message":"...","response_data":{...}}
// - {"response":{"response_code":"...","response_message":"...","response_data":{...}}}
func parseDKResponse(raw []byte) (string, string, map[string]any) {
	root := map[string]any{}
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", "", nil
	}

	source := root
	if wrapped, ok := root["response"].(map[string]any); ok {
		source = wrapped
	}

	code := readString(source, "response_code")
	message := firstNonEmpty(readString(source, "response_message"), readString(source, "response_description"))
	responseData, _ := source["response_data"].(map[string]any)
	return code, message, responseData
}

// sendSanitizedUpstreamError returns a generic gateway error response.
// Why needed: avoids leaking transport/internal upstream details to external apps.
func sendSanitizedUpstreamError(c *fiber.Ctx) error {
	return c.Status(http.StatusBadGateway).JSON(fiber.Map{
		"success": false,
		"code":    "UPSTREAM_UNAVAILABLE",
		"message": "Payment service is temporarily unavailable. Please try again.",
	})
}

// sendSanitizedInternalError returns a generic internal error for external clients.
// Why needed: prevents exposing internal DB/query details.
func sendSanitizedInternalError(c *fiber.Ctx) error {
	return c.Status(http.StatusInternalServerError).JSON(fiber.Map{
		"success": false,
		"code":    "INTERNAL_ERROR",
		"message": "Request could not be processed at this time.",
	})
}

// sendSanitizedDKResponse emits a normalized business response without exposing raw DK payloads.
// Fields:
// - success: true when upstream response_code == 0000
// - code: sanitized business code (not raw DK code)
// - message: user-safe message
// - data: endpoint-specific safe payload
func sendSanitizedDKResponse(c *fiber.Ctx, raw []byte, data fiber.Map) error {
	upstreamCode, _, _ := parseDKResponse(raw)
	resp := fiber.Map{
		"success": strings.TrimSpace(upstreamCode) == "0000",
		"code":    sanitizeBusinessCode(upstreamCode),
		"message": sanitizeBusinessMessage(upstreamCode),
	}
	if len(data) > 0 {
		resp["data"] = data
	}
	return c.Status(http.StatusOK).JSON(resp)
}

// sanitizeBusinessCode maps provider-specific codes to stable app-facing codes.
func sanitizeBusinessCode(upstreamCode string) string {
	switch strings.TrimSpace(upstreamCode) {
	case "0000":
		return "SUCCESS"
	case "2002":
		return "GATEWAY_TIMEOUT"
	case "2011":
		return "BANK_REJECTED"
	case "4001":
		return "INVALID_REQUEST"
	case "5001":
		return "UPSTREAM_ERROR"
	case "2008":
		return "RESTRICTED"
	case "2006":
		return "DUPLICATE_REQUEST"
	case "":
		return "UNKNOWN_ERROR"
	default:
		return "OPERATION_FAILED"
	}
}

// sanitizeBusinessMessage returns user-safe text and never forwards raw upstream details.
func sanitizeBusinessMessage(upstreamCode string) string {
	if strings.TrimSpace(upstreamCode) == "0000" {
		return "Request processed successfully."
	}
	return mapErrorToUserMessage(upstreamCode, "")
}

// mapErrorToUserMessage maps known response codes to user-friendly text.
// Called from: sanitizeBusinessMessage for non-success responses.
func mapErrorToUserMessage(code, fallback string) string {
	messages := map[string]string{
		"2002": "Payment gateway timeout. Please try again.",
		"2011": "Payment rejected by your bank. Please check account details.",
		"4001": "Invalid payment details. Please check and retry.",
		"5001": "System error. Please contact support.",
		"2008": "Transaction restricted. Please contact support.",
		"2006": "Duplicate transaction. Please retry later.",
	}
	if msg, ok := messages[code]; ok {
		return msg
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	return "Request could not be completed at this time."
}

// readString safely converts dynamic request map value to trimmed string.
// Called from: business handlers when parsing flexible JSON fields/aliases.
func readString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	default:
		return strings.TrimSpace(strings.TrimSpace(toJSONScalar(t)))
	}
}

// readFloat safely converts dynamic request map value to float64.
// Called from: business handlers when parsing numeric amount fields.
func readFloat(m map[string]any, key string) float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int32:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	default:
		return 0
	}
}

// toJSONScalar marshals arbitrary value into JSON literal text.
// Called from: readString fallback conversion path.
func toJSONScalar(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// firstNonEmpty returns the first non-blank string from the provided values.
// Called from: request-field alias resolution across business handlers.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// firstNonZero returns the first positive float from the provided values.
// Called from: amount alias resolution across business handlers.
func firstNonZero(values ...float64) float64 {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

func (ctl *BusinessController) resolveDKPGForMerchant(appID, merchantReference string) (*ResolvedGateway, *dkpg.Client, error) {
	if ctl == nil || ctl.Resolver == nil {
		return nil, nil, ErrMerchantNotConfigured
	}
	resolved, err := ctl.Resolver.Resolve(appID, merchantReference, "dkpg")
	if err != nil {
		return nil, nil, err
	}
	client, err := ctl.Resolver.DKPGClient(resolved)
	if err != nil {
		return nil, nil, err
	}
	return resolved, client, nil
}

func (ctl *BusinessController) resolveDKPGForRecordedCredential(credentialID string) (*dkpg.Client, error) {
	if ctl == nil || ctl.Resolver == nil {
		return nil, ErrMerchantNotConfigured
	}
	resolved, err := ctl.Resolver.ResolveCredential(credentialID)
	if err != nil {
		return nil, err
	}
	return ctl.Resolver.DKPGClient(resolved)
}

func merchantConfigurationError(c *fiber.Ctx) error {
	return c.Status(http.StatusBadRequest).JSON(fiber.Map{
		"success": false,
		"code":    "MERCHANT_NOT_CONFIGURED",
		"message": "merchant_reference is missing or not configured for this integration",
	})
}
