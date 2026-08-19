package middleware

import (
	"strings"
	"testing"
)

func TestSanitizePayloadRedactsGatewayCredentialContainer(t *testing.T) {
	got := sanitizePayload([]byte(`{"provider":"stripe","credentials":{"api_key":"provider-secret","dk_account":"123"}}`), "application/json")
	if strings.Contains(got, "provider-secret") || strings.Contains(got, `"dk_account":"123"`) {
		t.Fatalf("gateway credential material leaked into sanitized payload: %s", got)
	}
	if !strings.Contains(got, `"credentials":"[REDACTED]"`) {
		t.Fatalf("credential container was not redacted: %s", got)
	}
}
