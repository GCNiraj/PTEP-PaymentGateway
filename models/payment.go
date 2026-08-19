package models

type PullPaymentInitiateRequest struct {
	MerchantReference     string  `json:"merchant_reference"`
	Reference             string  `json:"external_reference"`
	Amount                float64 `json:"transaction_amount"`
	Purpose               string  `json:"purpose"`
	TransactionFee        float64 `json:"transaction_fee"`
	EmailID               string  `json:"email_id"`
	RemitterAccountNumber string  `json:"remitter_account_number"`
	RemitterAccountName   string  `json:"remitter_account_name"`
	CustomerPhone         string  `json:"customer_phone"`
	RemitterBankCode      string  `json:"remitter_bank_code"`
}

type PullPaymentConfirmRequest struct {
	TransactionID string `json:"transaction_id"`
	OTP           string `json:"otp"`
	ExternalAppID string `json:"external_app_id"`
	BFSTxnID      string `json:"bfs_txn_id"`
	// Legacy fields for internal use if needed
	Reference  string `json:"reference"`
	OrderID    string `json:"order_id"`
	BFSOrderNo string `json:"bfs_orderNo"`
}

type IntraBankRequest struct {
	ExternalAppID            string  `json:"external_app_id"`
	Reference                string  `json:"external_reference"`
	Amount                   float64 `json:"transaction_amount"`
	RemitterAccountNumber    string  `json:"remitter_account_number"`
	BeneficiaryAccountNumber string  `json:"beneficiary_account_number"`
	Purpose                  string  `json:"purpose"`
	Remarks                  string  `json:"remarks"`
}

type IntraInquiryRequest struct {
	MerchantReference     string  `json:"merchant_reference"`
	Reference             string  `json:"external_reference"`
	TransactionAmount     float64 `json:"transaction_amount"`
	RemitterAccountNumber string  `json:"remitter_account_number"`
}

type IntraTransferRequest struct {
	ExternalAppID         string  `json:"external_app_id"`
	ExternalReference     string  `json:"external_reference"`
	InquiryID             string  `json:"inquiry_id"`
	TransactionAmount     float64 `json:"transaction_amount"`
	RemitterAccountNumber string  `json:"remitter_account_number"`
	RemitterAccountName   string  `json:"remitter_account_name"`
	CustomerPhone         string  `json:"customer_phone"`
	EmailID               string  `json:"email_id"`
	Purpose               string  `json:"purpose"`
	Remarks               string  `json:"remarks"`
}

type IntraVerifyRequest struct {
	Amount              float64 `json:"transaction_amount"`
	Currency            string  `json:"currency"`
	BeneAccountNumber   string  `json:"bene_account_number"`
	SourceAccountNumber string  `json:"source_account_number"`
}

type StatusSameDayRequest struct {
	TransactionID     string `json:"transaction_id"`
	ExternalReference string `json:"external_reference"`
	BeneAccountNumber string `json:"bene_account_number"`
}

type StatusLaterRequest struct {
	TransactionID     string `json:"transaction_id"`
	ExternalReference string `json:"external_reference"`
	TransactionDate   string `json:"transaction_date"`
	BeneAccountNumber string `json:"bene_account_number"`
}

type StatusIntraRequest struct {
	ExternalReference string `json:"external_reference"`
	ReferenceNo       string `json:"reference_no"`
	TransactionDate   string `json:"transaction_date"`
	BeneAccountNumber string `json:"bene_account_number"`
}

// International Payment Models

type CreateInternationalPaymentRequest struct {
	MerchantReference string  `json:"merchant_reference"`
	Amount            float64 `json:"amount"`
	ReferenceID       string  `json:"reference_id"`
	Description       string  `json:"description"`
	SuccessURL        string  `json:"success_url"`
	CancelURL         string  `json:"cancel_url"`
}

type CreateInternationalPaymentResponse struct {
	Success bool                      `json:"success"`
	Message string                    `json:"message,omitempty"`
	Data    *InternationalPaymentData `json:"data,omitempty"`
	Error   *APIError                 `json:"error,omitempty"`
}

type InternationalPaymentData struct {
	PaymentID   string  `json:"payment_id"`
	SessionID   string  `json:"session_id"`
	CheckoutURL string  `json:"checkout_url"`
	Amount      float64 `json:"amount"`
	TotalAmount float64 `json:"total_amount"`
	Currency    string  `json:"currency"`
	ReferenceID string  `json:"reference_id"`
	Status      string  `json:"status"`
	ExpiresAt   string  `json:"expires_at"`
}

type CheckPaymentStatusResponse struct {
	Success bool               `json:"success"`
	Data    *PaymentStatusData `json:"data,omitempty"`
	Error   *APIError          `json:"error,omitempty"`
}

type PaymentStatusData struct {
	PaymentID   string  `json:"payment_id"`
	ReferenceID string  `json:"reference_id"`
	Status      string  `json:"status"`
	Amount      float64 `json:"amount"`
	TotalAmount float64 `json:"total_amount"`
	Currency    string  `json:"currency"`
	CreatedAt   string  `json:"created_at,omitempty"`
	CompletedAt string  `json:"completed_at,omitempty"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
