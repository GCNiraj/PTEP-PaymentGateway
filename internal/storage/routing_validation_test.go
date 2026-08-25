package storage

import "testing"

func TestValidateMerchantRoutingRejectsIncompleteOrLegacyNewRecords(t *testing.T) {
	if err := validateMerchantRouting("merchant", "app-a", "hotel-a", "recipient-a", "dkpg", "credential-a", 1); err != nil {
		t.Fatalf("complete merchant routing rejected: %v", err)
	}
	if err := validateMerchantRouting("merchant", "app-a", "", "recipient-a", "dkpg", "credential-a", 1); err == nil {
		t.Fatal("missing merchant reference accepted")
	}
	if err := validateMerchantRouting("legacy", "app-a", "hotel-a", "recipient-a", "dkpg", "credential-a", 1); err == nil {
		t.Fatal("new legacy routing accepted")
	}
}
