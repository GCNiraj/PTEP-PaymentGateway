package storage

import (
	"context"
	"database/sql"
	"strings"
)

// TransactionGatewayRouting is the immutable tenant and credential selection
// captured at payment initiation. It is used for all follow-up operations.
type TransactionGatewayRouting struct {
	ExternalAppID                    string
	ExternalMerchantReference        string
	RecipientID                      string
	GatewayProvider                  string
	GatewayCredentialConfigurationID string
	GatewayCredentialVersion         int
	OrderID                          string
	STAN                             string
	BFSTxnID                         string
	BFSOrderNo                       string
}

func (r *Repository) GetTransactionRoutingBySTAN(ctx context.Context, appID, stan string) (*TransactionGatewayRouting, error) {
	return r.getTransactionRouting(ctx, `stan_number=$2`, appID, stan)
}

func (r *Repository) GetTransactionRoutingByOrderID(ctx context.Context, appID, orderID string) (*TransactionGatewayRouting, error) {
	return r.getTransactionRouting(ctx, `order_id=$2`, appID, orderID)
}

func (r *Repository) GetTransactionRoutingByBFSTxnID(ctx context.Context, appID, bfsTxnID string) (*TransactionGatewayRouting, error) {
	return r.getTransactionRouting(ctx, `bfs_txn_id=$2`, appID, bfsTxnID)
}

func (r *Repository) getTransactionRouting(ctx context.Context, selector, appID, value string) (*TransactionGatewayRouting, error) {
	if !r.Enabled() {
		return nil, sql.ErrNoRows
	}
	query := `SELECT external_app_id, COALESCE(external_merchant_reference, ''), COALESCE(recipient_id::text, ''),
		COALESCE(gateway_provider, ''), COALESCE(gateway_credential_configuration_id::text, ''), COALESCE(gateway_credential_version, 0),
		COALESCE(order_id, ''), COALESCE(stan_number, ''), COALESCE(bfs_txn_id, ''), COALESCE(bfs_order_no, '')
		FROM payment_transactions WHERE external_app_id=$1 AND ` + selector + ` ORDER BY created_at DESC LIMIT 1`
	var routing TransactionGatewayRouting
	err := r.DB.QueryRowContext(ctx, query, strings.TrimSpace(appID), strings.TrimSpace(value)).Scan(
		&routing.ExternalAppID, &routing.ExternalMerchantReference, &routing.RecipientID, &routing.GatewayProvider,
		&routing.GatewayCredentialConfigurationID, &routing.GatewayCredentialVersion, &routing.OrderID, &routing.STAN, &routing.BFSTxnID, &routing.BFSOrderNo,
	)
	if err != nil {
		return nil, err
	}
	return &routing, nil
}
