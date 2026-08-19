package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/config"
	"example.com/fiber-mvc/internal/common"
	"example.com/fiber-mvc/internal/dkpg"
	"example.com/fiber-mvc/internal/storage"
)

type DKPGController struct {
	Client *dkpg.Client
	Repo   *storage.Repository
	Cfg    config.Config
}

// NewDKPGController builds the raw DKPG proxy controller.
// Called from: main() to wire /api/dkpg routes.
func NewDKPGController(client *dkpg.Client, repo *storage.Repository, cfg config.Config) *DKPGController {
	return &DKPGController{Client: client, Repo: repo, Cfg: cfg}
}

// FetchToken proxies DK auth token request using backend credentials.
// Why needed: diagnostic endpoint for token acquisition without exposing secrets.
// Response: 502 on upstream errors, otherwise 200 raw DK payload.
// Called from: POST /api/dkpg/auth/token.
func (ctl *DKPGController) FetchToken(c *fiber.Ctx) error {
	payload, err := ctl.Client.FetchToken(common.CtxFromFiber(c))
	if err != nil {
		return c.Status(http.StatusBadGateway).JSON(fiber.Map{
			"error": err.Error(),
		})
	}
	return c.Status(http.StatusOK).Send(payload)
}

// FetchKey proxies DK sign-key retrieval.
// Request fields: optional request_id (auto-generated if empty).
// Response: 400 invalid JSON, 502 upstream errors, 200 raw key payload.
// Called from: POST /api/dkpg/sign/key.
func (ctl *DKPGController) FetchKey(c *fiber.Ctx) error {
	body := map[string]any{}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}
	requestID, _ := body["request_id"].(string)
	if requestID == "" {
		requestID = "auto-" + c.IP()
	}

	keyText, err := ctl.Client.FetchPrivateKey(common.CtxFromFiber(c), requestID)
	if err != nil {
		return c.Status(http.StatusBadGateway).JSON(fiber.Map{
			"error": err.Error(),
		})
	}
	return c.Status(http.StatusOK).SendString(keyText)
}

// AccountAuthPullPayment proxies account_auth/pull-payment with preprocessing.
// Why needed: auto-fills stan_number and transaction_datetime, accepts user remitter bank code
// (with server fallback), and optionally checks duplicate external reference.
// Response: standard signed proxy responses from signedProxyWithPayload.
// Called from: POST /api/dkpg/account-auth/pull-payment.
func (ctl *DKPGController) AccountAuthPullPayment(c *fiber.Ctx) error {
	payload := map[string]any{}
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}

	if getString(payload, "transaction_datetime") == "" {
		payload["transaction_datetime"] = time.Now().UTC().Format(time.RFC3339)
	}
	if getString(payload, "stan_number") == "" {
		payload["stan_number"] = common.GenerateSTAN(ctl.Client.SourceApp())
	}
	remitterBank := firstNonEmptyString(
		getString(payload, "remitter_bank_id"),
		getString(payload, "remitter_bank_code"),
		getString(payload, "code"),
		strings.TrimSpace(ctl.Cfg.DKBeneficiaryBank),
		"1060",
	)
	payload["remitter_bank_id"] = remitterBank
	delete(payload, "remitter_bank_code")
	delete(payload, "code")
	if ctl.Repo != nil {
		ref := getString(payload, "external_reference")
		if ref == "" {
			ref = getString(payload, "order_id")
		}
		if ref != "" {
			if exists, err := ctl.Repo.OrderIDExists(common.CtxFromFiber(c), ref); err != nil {
				return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
			} else if exists {
				return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "external_reference/order_id already used"})
			}
		}
	}

	return ctl.signedProxyWithPayload(c, payload, ctl.Client.AccountAuthPullPayment)
}

// DebitRequestPullPayment forwards raw debit-request/pull-payment payload.
// Called from: POST /api/dkpg/debit-request/pull-payment.
func (ctl *DKPGController) DebitRequestPullPayment(c *fiber.Ctx) error {
	return ctl.signedProxy(c, ctl.Client.DebitRequestPullPayment)
}

// BeneficiaryAccountInquiry forwards intra inquiry payload to DK.
// Called from: POST /api/dkpg/beneficiary/account-inquiry.
func (ctl *DKPGController) BeneficiaryAccountInquiry(c *fiber.Ctx) error {
	return ctl.signedProxy(c, ctl.Client.BeneficiaryAccountInquiry)
}

// InitiateTransaction forwards intra transfer initiation payload to DK.
// Called from: POST /api/dkpg/initiate/transaction.
func (ctl *DKPGController) InitiateTransaction(c *fiber.Ctx) error {
	return ctl.signedProxy(c, ctl.Client.InitiateTransaction)
}

// TransactionStatus forwards same-day status check payload to DK.
// Called from: POST /api/dkpg/transaction/status.
func (ctl *DKPGController) TransactionStatus(c *fiber.Ctx) error {
	return ctl.signedProxy(c, ctl.Client.TransactionStatus)
}

// TransactionsStatus forwards non same-day status check payload to DK.
// Called from: POST /api/dkpg/transactions/status.
func (ctl *DKPGController) TransactionsStatus(c *fiber.Ctx) error {
	return ctl.signedProxy(c, ctl.Client.TransactionsStatus)
}

// IntraTransactionStatus forwards intra status check payload to DK.
// Called from: POST /api/dkpg/intra-transaction/status.
func (ctl *DKPGController) IntraTransactionStatus(c *fiber.Ctx) error {
	return ctl.signedProxy(c, ctl.Client.IntraTransactionStatus)
}

// signedProxy parses JSON body and forwards it to signedProxyWithPayload.
// Response:
// - 400 for invalid JSON
// - otherwise delegated result from signedProxyWithPayload
// Called from: most DKPGController endpoint methods.
func (ctl *DKPGController) signedProxy(c *fiber.Ctx, call func(ctx context.Context, payload any) ([]byte, error)) error {
	payload := map[string]any{}
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}
	return ctl.signedProxyWithPayload(c, payload, call)
}

// signedProxyWithPayload executes common DK call path with optional DB side-effects.
// Why needed: centralizes create/update of payment tracking rows for payloads with stan_number.
// Flow:
// - optionally persists initial transaction snapshot
// - performs signed upstream request
// - updates status/bfs_txn_id when possible
// Response: 500 persistence errors, 502 upstream errors, 200 raw DK response.
func (ctl *DKPGController) signedProxyWithPayload(c *fiber.Ctx, payload map[string]any, call func(ctx context.Context, payload any) ([]byte, error)) error {
	stan := getString(payload, "stan_number")
	if stan != "" && ctl.Repo != nil {
		txnTime := time.Now().UTC()
		if ts := getString(payload, "transaction_datetime"); ts != "" {
			if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
				txnTime = parsed
			}
		}

		ref := getString(payload, "external_reference")
		if ref == "" {
			ref = getString(payload, "order_id")
		}

		phone := getString(payload, "customer_phone")
		if phone == "" {
			phone = getString(payload, "phone_number")
		}

		desc := getString(payload, "purpose")
		if desc == "" {
			desc = getString(payload, "payment_desc")
		}

		if err := ctl.Repo.CreatePullPayment(common.CtxFromFiber(c), storage.PullPaymentRecord{
			ExternalAppID:       getString(payload, "external_app_id"),
			OrderID:             ref,
			InquiryID:           getString(payload, "inquiry_id"),
			STAN:                stan,
			BFSTxnID:            getString(payload, "bfs_txn_id"),
			BFSRequestID:        getString(payload, "request_id"),
			BFSOrderNo:          getString(payload, "bfs_orderNo"),
			Amount:              getFloat(payload, "transaction_amount"),
			TransactionFee:      getFloat(payload, "transaction_fee"),
			RemitterAccount:     getString(payload, "remitter_account_number"),
			RemitterName:        getString(payload, "remitter_account_name"),
			RemitterPhone:       phone,
			EmailID:             getString(payload, "email_id"),
			RemitterBank:        getString(payload, "remitter_bank_id"),
			BeneficiaryAccount:  getString(payload, "account_number"),
			PaymentDesc:         desc,
			Currency:            "BTN",
			Status:              "PENDING",
			TransactionDatetime: txnTime,
		}); err != nil {
			return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
	}

	res, err := call(common.CtxFromFiber(c), payload)
	if err != nil {
		if stan != "" && ctl.Repo != nil {
			_ = ctl.Repo.UpdatePullPaymentStatus(common.CtxFromFiber(c), stan, "FAILED", "", err.Error())
		}
		return c.Status(http.StatusBadGateway).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	if stan != "" && ctl.Repo != nil {
		code, message := common.ExtractResponseCode(res)
		status := "OTP_SENT"
		if code != "" && code != "0000" {
			status = "FAILED"
		}
		_ = ctl.Repo.UpdatePullPaymentStatus(common.CtxFromFiber(c), stan, status, code, message)
		if bfs := common.ExtractBFSTxnID(res); bfs != "" {
			_ = ctl.Repo.UpdateBFSTxnID(common.CtxFromFiber(c), stan, bfs)
		}
	}
	return c.Status(http.StatusOK).Send(res)
}

// getString extracts a trimmed string value from map payload.
// Called from: DK proxy normalization and repository mapping.
func getString(payload map[string]any, key string) string {
	if v, ok := payload[key]; ok {
		switch t := v.(type) {
		case string:
			return strings.TrimSpace(t)
		case []byte:
			return strings.TrimSpace(string(t))
		case json.Number:
			return strings.TrimSpace(t.String())
		case int:
			return strconv.Itoa(t)
		case int32:
			return strconv.FormatInt(int64(t), 10)
		case int64:
			return strconv.FormatInt(t, 10)
		case float64:
			return strings.TrimSpace(strconv.FormatFloat(t, 'f', -1, 64))
		case float32:
			return strings.TrimSpace(strconv.FormatFloat(float64(t), 'f', -1, 32))
		}
	}
	return ""
}

// getFloat extracts numeric payload fields across number/string forms.
// Called from: DK proxy persistence mapping.
func getFloat(payload map[string]any, key string) float64 {
	if v, ok := payload[key]; ok {
		switch t := v.(type) {
		case float64:
			return t
		case int:
			return float64(t)
		case int64:
			return float64(t)
		case json.Number:
			if f, err := t.Float64(); err == nil {
				return f
			}
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
				return f
			}
		}
	}
	return 0
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}
