package dkpg

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"example.com/fiber-mvc/internal/storage"
)

type Client struct {
	http         *http.Client
	baseURL      string
	apiKey       string
	sourceApp    string
	username     string
	password     string
	clientID     string
	clientSecret string
	scopes       string

	tokenMu     sync.Mutex
	token       string
	tokenExpiry time.Time

	keyMu         sync.Mutex
	privateKey    *rsa.PrivateKey
	privateKeyPEM string
	logRepo       *storage.LogRepository
}

type TokenResponse struct {
	ResponseCode        string `json:"response_code"`
	ResponseMessage     string `json:"response_massage"`
	ResponseDescription string `json:"response_description"`
	ResponseData        struct {
		AccessToken string `json:"access_token"` // #nosec G117 -- name reflects upstream DKPG API response contract.
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	} `json:"response_data"`
}

// NewClient creates DKPG HTTP client with credential/config dependencies.
// Called from: main() during service initialization.
func NewClient(baseURL, apiKey, sourceApp, username, password, clientID, clientSecret, scopes, privateKeyPEM string, logRepo *storage.LogRepository) *Client {
	return &Client{
		http: &http.Client{
			Timeout: 10 * time.Minute,
		},
		baseURL:       strings.TrimRight(baseURL, "/"),
		apiKey:        apiKey,
		sourceApp:     sourceApp,
		username:      username,
		password:      password,
		clientID:      clientID,
		clientSecret:  clientSecret,
		scopes:        scopes,
		privateKeyPEM: strings.TrimSpace(privateKeyPEM),
		logRepo:       logRepo,
	}
}

// SourceApp returns configured DK source_app value.
// Called from: controllers when generating STAN values.
func (c *Client) SourceApp() string {
	return c.sourceApp
}

// FetchToken requests raw token payload from DK auth endpoint.
// Why needed: debug endpoint support and token troubleshooting.
// Called from: DKPGController.FetchToken.
func (c *Client) FetchToken(ctx context.Context) ([]byte, error) {
	form := url.Values{}
	requestID := newRequestID()
	form.Set("username", c.username)
	form.Set("password", c.password)
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	form.Set("grant_type", "password")
	form.Set("scopes", c.scopes)
	form.Set("source_app", c.sourceApp)
	form.Set("request_id", requestID)
	reqBody := form.Encode()
	start := time.Now().UTC()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/auth/token", strings.NewReader(reqBody))
	if err != nil {
		c.logInternalCall(ctx, http.MethodPost, "/v1/auth/token", requestID, reqBody, err.Error(), 0, start)
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-gravitee-api-key", c.apiKey)

	// #nosec G704 -- baseURL is operator-configured for the DKPG upstream; endpoint path is fixed.
	res, err := c.http.Do(req)
	if err != nil {
		c.logInternalCall(ctx, http.MethodPost, "/v1/auth/token", requestID, reqBody, err.Error(), 0, start)
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		c.logInternalCall(ctx, http.MethodPost, "/v1/auth/token", requestID, reqBody, err.Error(), res.StatusCode, start)
		return nil, err
	}
	c.logInternalCall(ctx, http.MethodPost, "/v1/auth/token", requestID, reqBody, string(body), res.StatusCode, start)

	return body, nil
}

// ensureToken returns a cached access token or refreshes it from DK.
// Why needed: all signed downstream calls require Authorization bearer token.
// Called from: FetchPrivateKey and signedPost.
func (c *Client) ensureToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.token != "" && time.Now().Before(c.tokenExpiry.Add(-30*time.Second)) {
		return c.token, nil
	}

	form := url.Values{}
	requestID := newRequestID()
	form.Set("username", c.username)
	form.Set("password", c.password)
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	form.Set("grant_type", "password")
	form.Set("scopes", c.scopes)
	form.Set("source_app", c.sourceApp)
	form.Set("request_id", requestID)
	reqBody := form.Encode()
	start := time.Now().UTC()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/auth/token", strings.NewReader(reqBody))
	if err != nil {
		c.logInternalCall(ctx, http.MethodPost, "/v1/auth/token", requestID, reqBody, err.Error(), 0, start)
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-gravitee-api-key", c.apiKey)

	// #nosec G704 -- baseURL is operator-configured for the DKPG upstream; endpoint path is fixed.
	res, err := c.http.Do(req)
	if err != nil {
		c.logInternalCall(ctx, http.MethodPost, "/v1/auth/token", requestID, reqBody, err.Error(), 0, start)
		return "", err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		c.logInternalCall(ctx, http.MethodPost, "/v1/auth/token", requestID, reqBody, err.Error(), res.StatusCode, start)
		return "", err
	}
	c.logInternalCall(ctx, http.MethodPost, "/v1/auth/token", requestID, reqBody, string(body), res.StatusCode, start)

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("token request failed: status=%d body=%s", res.StatusCode, string(body))
	}

	var tokenRes TokenResponse
	if err := json.Unmarshal(body, &tokenRes); err != nil {
		return "", err
	}
	if tokenRes.ResponseData.AccessToken == "" {
		return "", fmt.Errorf("missing access token")
	}

	c.token = tokenRes.ResponseData.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(tokenRes.ResponseData.ExpiresIn) * time.Second)

	return c.token, nil
}

// FetchPrivateKey fetches RSA signing key text from DK sign/key endpoint.
// Why needed: backend must sign DK requests when local private key is absent.
// Called from: DKPGController.FetchKey and ensurePrivateKey fallback path.
func (c *Client) FetchPrivateKey(ctx context.Context, requestID string) (string, error) {
	token, err := c.ensureToken(ctx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(requestID) == "" {
		requestID = newRequestID()
	}

	payload := map[string]any{
		"request_id": requestID,
		"source_app": c.sourceApp,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	reqBody := string(body)
	start := time.Now().UTC()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/sign/key", bytes.NewReader(body))
	if err != nil {
		c.logInternalCall(ctx, http.MethodPost, "/v1/sign/key", requestID, reqBody, err.Error(), 0, start)
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-gravitee-api-key", c.apiKey)
	req.Header.Set("Authorization", "Bearer "+token)

	// #nosec G704 -- baseURL is operator-configured for the DKPG upstream; endpoint path is fixed.
	res, err := c.http.Do(req)
	if err != nil {
		c.logInternalCall(ctx, http.MethodPost, "/v1/sign/key", requestID, reqBody, err.Error(), 0, start)
		return "", err
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		c.logInternalCall(ctx, http.MethodPost, "/v1/sign/key", requestID, reqBody, err.Error(), res.StatusCode, start)
		return "", err
	}
	c.logInternalCall(ctx, http.MethodPost, "/v1/sign/key", requestID, reqBody, string(raw), res.StatusCode, start)

	return string(raw), nil
}

// ensurePrivateKey loads/caches RSA private key from configured PEM or DK API.
// Why needed: signedPost requires RSA private key to create DK-Signature.
// Called from: signedPost.
func (c *Client) ensurePrivateKey(ctx context.Context) (*rsa.PrivateKey, error) {
	c.keyMu.Lock()
	defer c.keyMu.Unlock()

	if c.privateKey != nil {
		return c.privateKey, nil
	}

	pemText := strings.TrimSpace(c.privateKeyPEM)
	if pemText == "" {
		var err error
		pemText, err = c.FetchPrivateKey(ctx, newRequestID())
		if err != nil {
			return nil, err
		}
	}

	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("invalid PEM returned from sign key API")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		c.privateKey = key
		return key, nil
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}

	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("unsupported private key type")
	}

	c.privateKey = rsaKey
	return rsaKey, nil
}

// signedPost sends a signed DK request with token + signature headers.
// Why needed: DK endpoints require DK-Timestamp, DK-Nonce, DK-Signature, source_app.
// Called from: all typed DK business methods below.
// Response: raw upstream body; transport/signing/token errors are returned.
func (c *Client) signedPost(ctx context.Context, path string, payload any) ([]byte, error) {
	token, err := c.ensureToken(ctx)
	if err != nil {
		return nil, err
	}

	privateKey, err := c.ensurePrivateKey(ctx)
	if err != nil {
		return nil, err
	}

	canonical, err := canonicalJSON(payload)
	if err != nil {
		return nil, err
	}

	bodyB64 := base64.StdEncoding.EncodeToString([]byte(canonical))
	timestamp := time.Now().UTC().Format(time.RFC3339)
	nonce := newNonce()

	claims := jwt.MapClaims{
		"data":      bodyB64,
		"timestamp": timestamp,
		"nonce":     nonce,
	}

	tokenJWT := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tokenJWT.SignedString(privateKey)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	requestID := extractRequestID(payload, body)
	start := time.Now().UTC()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		c.logInternalCall(ctx, http.MethodPost, path, requestID, string(body), err.Error(), 0, start)
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-gravitee-api-key", c.apiKey)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("DK-Timestamp", timestamp)
	req.Header.Set("DK-Nonce", nonce)
	req.Header.Set("DK-Signature", "DKSignature "+signed)
	req.Header.Set("source_app", c.sourceApp)

	// #nosec G704 -- path values are internal constants passed by typed client methods.
	res, err := c.http.Do(req)
	if err != nil {
		c.logInternalCall(ctx, http.MethodPost, path, requestID, string(body), err.Error(), 0, start)
		return nil, err
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		c.logInternalCall(ctx, http.MethodPost, path, requestID, string(body), err.Error(), res.StatusCode, start)
		return nil, err
	}
	c.logInternalCall(ctx, http.MethodPost, path, requestID, string(body), string(raw), res.StatusCode, start)

	return raw, nil
}

// AccountAuthPullPayment calls DK /v1/account_auth/pull-payment.
// Called from: business and DKPG controllers.
func (c *Client) AccountAuthPullPayment(ctx context.Context, payload any) ([]byte, error) {
	return c.signedPost(ctx, "/v1/account_auth/pull-payment", payload)
}

// DebitRequestPullPayment calls DK /v1/debit_request/pull-payment.
// Called from: business and DKPG controllers.
func (c *Client) DebitRequestPullPayment(ctx context.Context, payload any) ([]byte, error) {
	return c.signedPost(ctx, "/v1/debit_request/pull-payment", payload)
}

// BeneficiaryAccountInquiry calls DK /v1/beneficiary/account_inquiry.
// Called from: business intra inquiry and DKPG proxy endpoints.
func (c *Client) BeneficiaryAccountInquiry(ctx context.Context, payload any) ([]byte, error) {
	return c.signedPost(ctx, "/v1/beneficiary/account_inquiry", payload)
}

// InitiateTransaction calls DK /v1/initiate/transaction.
// Called from: business intra transfer and DKPG proxy endpoints.
func (c *Client) InitiateTransaction(ctx context.Context, payload any) ([]byte, error) {
	return c.signedPost(ctx, "/v1/initiate/transaction", payload)
}

// TransactionStatus calls DK /v1/transaction/status (same-day check).
// Called from: business status same-day and DKPG proxy endpoints.
func (c *Client) TransactionStatus(ctx context.Context, payload any) ([]byte, error) {
	return c.signedPost(ctx, "/v1/transaction/status", payload)
}

// TransactionsStatus calls DK /v1/transactions/status (historical check).
// Called from: business status later and DKPG proxy endpoints.
func (c *Client) TransactionsStatus(ctx context.Context, payload any) ([]byte, error) {
	return c.signedPost(ctx, "/v1/transactions/status", payload)
}

// IntraTransactionStatus calls DK /v1/intra-transaction/status.
// Called from: business status intra and DKPG proxy endpoints.
func (c *Client) IntraTransactionStatus(ctx context.Context, payload any) ([]byte, error) {
	return c.signedPost(ctx, "/v1/intra-transaction/status", payload)
}

// newNonce creates a nonce value derived from request-id entropy.
// Called from: signedPost for DK-Nonce header.
func newNonce() string {
	return strings.ReplaceAll(newRequestID(), "-", "")
}

// newRequestID creates an internal compact request id for DK client operations.
// Called from: ensureToken, ensurePrivateKey, and newNonce.
func newRequestID() string {
	return strings.ReplaceAll(fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().Unix()), "-", "")
}

func extractRequestID(payload any, body []byte) string {
	if m, ok := payload.(map[string]any); ok {
		if raw, exists := m["request_id"]; exists && raw != nil {
			if s, ok := raw.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	if raw, exists := m["request_id"]; exists && raw != nil {
		if s, ok := raw.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func (c *Client) logInternalCall(ctx context.Context, method, path, requestID, reqBody, resBody string, status int, start time.Time) {
	if c == nil {
		return
	}
	
	duration := time.Since(start).Milliseconds()

	if c.logRepo != nil && c.logRepo.Enabled() && ctx != nil {
		entry := storage.LogEntry{
			Method:       method,
			Path:         strings.TrimSpace(path),
			Status:       status,
			Duration:     duration,
			RequestID:    strings.TrimSpace(requestID),
			ActorID:      "dkpg-client",
			APISurface:   "internal",
			Route:        strings.TrimSpace(path),
			AuthResult:   "authenticated",
		}

		dbResBody := ""
		// The user requested we only store the response body for status checks to save space.
		if strings.Contains(entry.Path, "/status") {
			dbResBody = resBody
		}

		_ = c.logRepo.Insert(ctx, entry, "", dbResBody)
	}

	payload := map[string]any{
		"type":         "internal_call_log",
		"sink":         "stdout",
		"service":      "dkpg",
		"method":       method,
		"path":         strings.TrimSpace(path),
		"status":       status,
		"duration_ms":  duration,
		"request_id":   strings.TrimSpace(requestID),
		"req_len":      len(reqBody),
		"res_len":      len(resBody),
		"api_surface":  "internal",
		"request_path": "dkpg:" + strings.TrimSpace(path),
	}
	if b, err := json.Marshal(payload); err == nil {
		log.Printf("%s", string(b))
		return
	}
	// #nosec G706 -- fallback logging uses trimmed internal fields; primary path uses JSON encoding above.
	log.Printf("internal_call_log service=dkpg method=%s path=%s status=%d request_id=%s", method, strings.TrimSpace(path), status, strings.TrimSpace(requestID))
}

func limitLogBody(s string) string {
	const max = 64 * 1024
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
