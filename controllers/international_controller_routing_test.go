package controllers

import (
	"testing"

	"example.com/fiber-mvc/internal/storage"
	"example.com/fiber-mvc/internal/stripe"
)

func TestStripeClientForPaymentOnlyFallsBackForExplicitLegacyRecord(t *testing.T) {
	legacyClient := stripe.NewClient("https://provider.invalid", "legacy-key", nil)
	controller := &InternationalController{StripeClient: legacyClient}

	if client, err := controller.stripeClientForPayment(storage.InternationalPayment{RoutingMode: "legacy"}); err != nil || client != legacyClient {
		t.Fatalf("legacy payment fallback = (%v, %v), want configured legacy client", client, err)
	}
	if client, err := controller.stripeClientForPayment(storage.InternationalPayment{RoutingMode: "merchant"}); err == nil || client != nil {
		t.Fatalf("incomplete merchant payment fallback = (%v, %v), want rejection", client, err)
	}
}
