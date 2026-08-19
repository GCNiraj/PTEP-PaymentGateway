package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrRecipientMappingNotFound = errors.New("recipient mapping not found")
	ErrRecipientMappingInactive = errors.New("recipient mapping inactive")
	ErrCredentialNotConfigured  = errors.New("gateway credential not configured")
)

type PaymentRecipient struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type IntegrationRecipientMapping struct {
	ID                        string    `json:"id"`
	ExternalAppID             string    `json:"external_app_id"`
	ExternalMerchantReference string    `json:"external_merchant_reference"`
	RecipientID               string    `json:"recipient_id"`
	RecipientName             string    `json:"recipient_name,omitempty"`
	IsActive                  bool      `json:"is_active"`
	CreatedAt                 time.Time `json:"created_at"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

type GatewayCredentialConfiguration struct {
	ID                   string     `json:"id"`
	RecipientID          string     `json:"recipient_id"`
	Provider             string     `json:"provider"`
	Version              int        `json:"version"`
	Status               string     `json:"status"`
	EncryptedCredentials string     `json:"-"`
	EncryptionKeyID      string     `json:"encryption_key_id"`
	RotatedFromID        string     `json:"rotated_from_id,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	ActivatedAt          *time.Time `json:"activated_at,omitempty"`
	DisabledAt           *time.Time `json:"disabled_at,omitempty"`
}

type ResolvedGatewayConfiguration struct {
	ExternalAppID             string
	ExternalMerchantReference string
	RecipientID               string
	RecipientName             string
	Credential                GatewayCredentialConfiguration
}

type MerchantRoutingRepository struct {
	DB *sql.DB
}

func NewMerchantRoutingRepository(db *DB) *MerchantRoutingRepository {
	if db == nil {
		return &MerchantRoutingRepository{}
	}
	return &MerchantRoutingRepository{DB: db.Conn}
}

func (r *MerchantRoutingRepository) Enabled() bool { return r != nil && r.DB != nil }

func (r *MerchantRoutingRepository) CreateRecipient(ctx context.Context, recipient PaymentRecipient, actor string) error {
	if !r.Enabled() {
		return errors.New("merchant routing repository is not configured")
	}
	_, err := r.DB.ExecContext(ctx, `
		INSERT INTO payment_recipients (id, name, is_active, created_by, updated_by)
		VALUES (COALESCE(NULLIF($1, ''), gen_random_uuid()), $2, $3, $4, $4)
	`, strings.TrimSpace(recipient.ID), strings.TrimSpace(recipient.Name), recipient.IsActive, strings.TrimSpace(actor))
	return err
}

func (r *MerchantRoutingRepository) ListRecipients(ctx context.Context) ([]PaymentRecipient, error) {
	if !r.Enabled() {
		return nil, errors.New("merchant routing repository is not configured")
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT id::text, name, is_active, created_at, updated_at FROM payment_recipients ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]PaymentRecipient, 0)
	for rows.Next() {
		var recipient PaymentRecipient
		if err := rows.Scan(&recipient.ID, &recipient.Name, &recipient.IsActive, &recipient.CreatedAt, &recipient.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, recipient)
	}
	return result, rows.Err()
}

func (r *MerchantRoutingRepository) SetRecipientActive(ctx context.Context, id string, active bool, actor string) error {
	if !r.Enabled() {
		return errors.New("merchant routing repository is not configured")
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE payment_recipients SET is_active=$1, updated_at=now(), updated_by=$2 WHERE id=$3`, active, strings.TrimSpace(actor), strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *MerchantRoutingRepository) CreateMapping(ctx context.Context, mapping IntegrationRecipientMapping, actor string) error {
	if !r.Enabled() {
		return errors.New("merchant routing repository is not configured")
	}
	_, err := r.DB.ExecContext(ctx, `
		INSERT INTO integration_recipient_mappings
		(external_app_id, external_merchant_reference, recipient_id, is_active, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $5)
	`, strings.TrimSpace(mapping.ExternalAppID), strings.TrimSpace(mapping.ExternalMerchantReference), strings.TrimSpace(mapping.RecipientID), mapping.IsActive, strings.TrimSpace(actor))
	return err
}

func (r *MerchantRoutingRepository) ListMappings(ctx context.Context, appID string) ([]IntegrationRecipientMapping, error) {
	if !r.Enabled() {
		return nil, errors.New("merchant routing repository is not configured")
	}
	args := []any{}
	query := `SELECT m.id::text, m.external_app_id, m.external_merchant_reference, m.recipient_id::text, r.name, m.is_active, m.created_at, m.updated_at
		FROM integration_recipient_mappings m JOIN payment_recipients r ON r.id=m.recipient_id`
	if strings.TrimSpace(appID) != "" {
		query += ` WHERE m.external_app_id=$1`
		args = append(args, strings.TrimSpace(appID))
	}
	query += ` ORDER BY m.external_app_id, m.external_merchant_reference`
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]IntegrationRecipientMapping, 0)
	for rows.Next() {
		var mapping IntegrationRecipientMapping
		if err := rows.Scan(&mapping.ID, &mapping.ExternalAppID, &mapping.ExternalMerchantReference, &mapping.RecipientID, &mapping.RecipientName, &mapping.IsActive, &mapping.CreatedAt, &mapping.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, mapping)
	}
	return result, rows.Err()
}

func (r *MerchantRoutingRepository) SetMappingActive(ctx context.Context, id string, active bool, actor string) error {
	if !r.Enabled() {
		return errors.New("merchant routing repository is not configured")
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE integration_recipient_mappings SET is_active=$1, updated_at=now(), updated_by=$2 WHERE id=$3`, active, strings.TrimSpace(actor), strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *MerchantRoutingRepository) NextCredentialVersion(ctx context.Context, recipientID, provider string) (int, error) {
	if !r.Enabled() {
		return 0, errors.New("merchant routing repository is not configured")
	}
	var version int
	err := r.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM gateway_credential_configurations WHERE recipient_id=$1 AND provider=$2`, strings.TrimSpace(recipientID), strings.TrimSpace(provider)).Scan(&version)
	return version, err
}

func (r *MerchantRoutingRepository) CreateCredential(ctx context.Context, credential GatewayCredentialConfiguration, actor string) error {
	if !r.Enabled() {
		return errors.New("merchant routing repository is not configured")
	}
	_, err := r.DB.ExecContext(ctx, `
		INSERT INTO gateway_credential_configurations
		(id, recipient_id, provider, version, status, encrypted_credentials, encryption_key_id, rotated_from_id, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,'')::uuid,$9,$9)
	`, credential.ID, credential.RecipientID, credential.Provider, credential.Version, credential.Status, credential.EncryptedCredentials, credential.EncryptionKeyID, credential.RotatedFromID, strings.TrimSpace(actor))
	return err
}

func (r *MerchantRoutingRepository) ListCredentials(ctx context.Context, recipientID string) ([]GatewayCredentialConfiguration, error) {
	if !r.Enabled() {
		return nil, errors.New("merchant routing repository is not configured")
	}
	args := []any{}
	query := `SELECT id::text, recipient_id::text, provider, version, status, encryption_key_id, COALESCE(rotated_from_id::text, ''), created_at, updated_at, activated_at, disabled_at FROM gateway_credential_configurations`
	if strings.TrimSpace(recipientID) != "" {
		query += ` WHERE recipient_id=$1`
		args = append(args, strings.TrimSpace(recipientID))
	}
	query += ` ORDER BY recipient_id, provider, version DESC`
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]GatewayCredentialConfiguration, 0)
	for rows.Next() {
		var credential GatewayCredentialConfiguration
		if err := rows.Scan(&credential.ID, &credential.RecipientID, &credential.Provider, &credential.Version, &credential.Status, &credential.EncryptionKeyID, &credential.RotatedFromID, &credential.CreatedAt, &credential.UpdatedAt, &credential.ActivatedAt, &credential.DisabledAt); err != nil {
			return nil, err
		}
		result = append(result, credential)
	}
	return result, rows.Err()
}

// GetCredential retrieves an immutable historical configuration. It deliberately
// does not require active status because pending transactions must keep using the
// configuration selected when they were initiated, even after rotation.
func (r *MerchantRoutingRepository) GetCredential(ctx context.Context, id string) (*GatewayCredentialConfiguration, error) {
	if !r.Enabled() {
		return nil, ErrCredentialNotConfigured
	}
	var credential GatewayCredentialConfiguration
	err := r.DB.QueryRowContext(ctx, `
		SELECT id::text, recipient_id::text, provider, version, status, encrypted_credentials,
		       encryption_key_id, COALESCE(rotated_from_id::text, ''), created_at, updated_at, activated_at, disabled_at
		FROM gateway_credential_configurations WHERE id=$1
	`, strings.TrimSpace(id)).Scan(
		&credential.ID, &credential.RecipientID, &credential.Provider, &credential.Version, &credential.Status, &credential.EncryptedCredentials,
		&credential.EncryptionKeyID, &credential.RotatedFromID, &credential.CreatedAt, &credential.UpdatedAt, &credential.ActivatedAt, &credential.DisabledAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCredentialNotConfigured
	}
	if err != nil {
		return nil, err
	}
	return &credential, nil
}

func (r *MerchantRoutingRepository) SetCredentialStatus(ctx context.Context, id, status, actor string) error {
	if !r.Enabled() {
		return errors.New("merchant routing repository is not configured")
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var recipientID, provider string
	if err := tx.QueryRowContext(ctx, `SELECT recipient_id::text, provider FROM gateway_credential_configurations WHERE id=$1 FOR UPDATE`, strings.TrimSpace(id)).Scan(&recipientID, &provider); err != nil {
		return err
	}
	if status == "active" {
		if _, err := tx.ExecContext(ctx, `UPDATE gateway_credential_configurations SET status='retired', updated_at=now(), updated_by=$1 WHERE recipient_id=$2 AND provider=$3 AND status='active'`, strings.TrimSpace(actor), recipientID, provider); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE gateway_credential_configurations SET status=$1, activated_at=CASE WHEN $1='active' THEN now() ELSE activated_at END, disabled_at=CASE WHEN $1='disabled' THEN now() ELSE disabled_at END, updated_at=now(), updated_by=$2 WHERE id=$3`, status, strings.TrimSpace(actor), strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (r *MerchantRoutingRepository) ResolveActive(ctx context.Context, appID, merchantReference, provider string) (*ResolvedGatewayConfiguration, error) {
	if !r.Enabled() {
		return nil, ErrRecipientMappingNotFound
	}
	appID, merchantReference, provider = strings.TrimSpace(appID), strings.TrimSpace(merchantReference), strings.TrimSpace(provider)
	var resolved ResolvedGatewayConfiguration
	err := r.DB.QueryRowContext(ctx, `
		SELECT m.external_app_id, m.external_merchant_reference, r.id::text, r.name,
		       c.id::text, c.recipient_id::text, c.provider, c.version, c.status, c.encrypted_credentials,
		       c.encryption_key_id, COALESCE(c.rotated_from_id::text, ''), c.created_at, c.updated_at, c.activated_at, c.disabled_at
		FROM integration_recipient_mappings m
		JOIN payment_recipients r ON r.id=m.recipient_id
		JOIN gateway_credential_configurations c ON c.recipient_id=r.id AND c.provider=$3 AND c.status='active'
		WHERE m.external_app_id=$1 AND m.external_merchant_reference=$2 AND m.is_active AND r.is_active
	`, appID, merchantReference, provider).Scan(
		&resolved.ExternalAppID, &resolved.ExternalMerchantReference, &resolved.RecipientID, &resolved.RecipientName,
		&resolved.Credential.ID, &resolved.Credential.RecipientID, &resolved.Credential.Provider, &resolved.Credential.Version, &resolved.Credential.Status, &resolved.Credential.EncryptedCredentials,
		&resolved.Credential.EncryptionKeyID, &resolved.Credential.RotatedFromID, &resolved.Credential.CreatedAt, &resolved.Credential.UpdatedAt, &resolved.Credential.ActivatedAt, &resolved.Credential.DisabledAt,
	)
	if err == nil {
		return &resolved, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRecipientMappingNotFound
	}
	return nil, fmt.Errorf("resolve recipient gateway configuration: %w", err)
}

func CredentialAdditionalData(id, recipientID, provider string, version int) []byte {
	return []byte(strings.Join([]string{strings.TrimSpace(id), strings.TrimSpace(recipientID), strings.TrimSpace(provider), fmt.Sprintf("%d", version)}, ":"))
}
