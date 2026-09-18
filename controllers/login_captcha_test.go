package controllers

import (
	"testing"

	"example.com/fiber-mvc/internal/auth"
)

// The console guards the first knock, not the fourth.
//
// A threshold above zero leaves the opening attempts unchallenged, which is the
// window credential stuffing works in: it does not need many tries against one
// account, it needs one try against many. This is the administration console
// for a service that moves money, so the default is zero.
func TestCaptchaGuardsTheVeryFirstAttempt(t *testing.T) {
	verifier := &auth.CaptchaVerifier{SiteKey: "site", Secret: "secret", AfterFailures: 0}
	if !verifier.Enabled() {
		t.Fatal("a verifier with both keys must be enabled")
	}
	if !verifier.RequiresCaptcha(0) {
		t.Fatal("expected a challenge with zero prior failures")
	}
}

// Without both keys there is no challenge to pass, so a zero threshold must not
// lock every administrator out of a gateway that has no captcha configured.
func TestZeroThresholdDoesNotLockOutAnUnconfiguredGateway(t *testing.T) {
	for _, verifier := range []*auth.CaptchaVerifier{
		{SiteKey: "", Secret: "", AfterFailures: 0},
		{SiteKey: "site", Secret: "", AfterFailures: 0},
		{SiteKey: "", Secret: "secret", AfterFailures: 0},
	} {
		if verifier.Enabled() {
			t.Fatalf("half-configured verifier must not be enabled: %+v", verifier)
		}
		if verifier.RequiresCaptcha(0) || verifier.RequiresCaptcha(99) {
			t.Fatalf("an unconfigured verifier must never demand a token: %+v", verifier)
		}
	}
}
