package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

type CaptchaVerifier struct {
	SiteKey       string
	Secret        string
	AfterFailures int
	Timeout       time.Duration
	Client        *http.Client
}

type turnstileVerifyResponse struct {
	Success bool `json:"success"`
}

func NewCaptchaVerifier(siteKey, secret string, afterFailures int, timeout time.Duration) *CaptchaVerifier {
	if afterFailures < 0 {
		afterFailures = 0
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &CaptchaVerifier{
		SiteKey:       strings.TrimSpace(siteKey),
		Secret:        strings.TrimSpace(secret),
		AfterFailures: afterFailures,
		Timeout:       timeout,
	}
}

func (v *CaptchaVerifier) Enabled() bool {
	return v != nil && v.SiteKey != "" && v.Secret != ""
}

func (v *CaptchaVerifier) RequiresCaptcha(failures int) bool {
	if !v.Enabled() {
		return false
	}
	threshold := v.AfterFailures
	if threshold < 0 {
		threshold = 0
	}
	return failures >= threshold
}

func (v *CaptchaVerifier) PublicConfig() (enabled bool, siteKey string, afterFailures int) {
	if !v.Enabled() {
		return false, "", 0
	}
	return true, v.SiteKey, v.AfterFailures
}

func (v *CaptchaVerifier) Verify(ctx context.Context, token, remoteIP string) (bool, error) {
	if !v.Enabled() {
		return true, nil
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return false, nil
	}

	form := url.Values{}
	form.Set("secret", v.Secret)
	form.Set("response", token)
	remoteIP = strings.TrimSpace(remoteIP)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}

	reqCtx, cancel := context.WithTimeout(ctx, v.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, turnstileVerifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return false, fmt.Errorf("build captcha verify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := v.Client
	if client == nil {
		client = &http.Client{Timeout: v.Timeout}
	}

	res, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("captcha verify request failed: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("captcha verify status=%d", res.StatusCode)
	}

	var payload turnstileVerifyResponse
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return false, fmt.Errorf("decode captcha verify response: %w", err)
	}
	return payload.Success, nil
}
