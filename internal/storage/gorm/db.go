package gormdb

import (
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type PaymentTransaction struct {
	ID                  uint64     `gorm:"primaryKey"`
	ExternalAppID       string     `gorm:"column:external_app_id"`
	OrderID             string     `gorm:"column:order_id"`
	InquiryID           string     `gorm:"column:inquiry_id"`
	StanNumber          string     `gorm:"column:stan_number"`
	BFSTxnID            string     `gorm:"column:bfs_txn_id"`
	BFSRequestID        string     `gorm:"column:bfs_request_id"`
	BFSOrderNo          string     `gorm:"column:bfs_order_no"`
	Amount              float64    `gorm:"column:amount"`
	TransactionFee      float64    `gorm:"column:transaction_fee"`
	RemitterAccount     string     `gorm:"column:remitter_account"`
	RemitterName        string     `gorm:"column:remitter_name"`
	RemitterPhone       string     `gorm:"column:remitter_phone"`
	EmailID             string     `gorm:"column:email_id"`
	RemitterBank        string     `gorm:"column:remitter_bank"`
	BeneficiaryAccount  string     `gorm:"column:beneficiary_account"`
	TransactionDatetime time.Time  `gorm:"column:transaction_datetime"`
	PaymentDesc         string     `gorm:"column:payment_desc"`
	Currency            string     `gorm:"column:currency"`
	Status              string     `gorm:"column:status"`
	ErrorCode           string     `gorm:"column:error_code"`
	ErrorMessage        string     `gorm:"column:error_message"`
	CreatedAt           time.Time  `gorm:"column:created_at"`
	UpdatedAt           time.Time  `gorm:"column:updated_at"`
	CompletedAt         *time.Time `gorm:"column:completed_at"`
}

// TableName maps PaymentTransaction model to payment_transactions table.
func (PaymentTransaction) TableName() string { return "payment_transactions" }

type IntraInquiry struct {
	ID                uint64    `gorm:"primaryKey"`
	InquiryID         string    `gorm:"column:inquiry_id;uniqueIndex"`
	OrderID           string    `gorm:"column:order_id"`
	BeneAccountNumber string    `gorm:"column:bene_account_number"`
	Amount            float64   `gorm:"column:amount"`
	Status            string    `gorm:"column:status"`
	ErrorCode         string    `gorm:"column:error_code"`
	ErrorMessage      string    `gorm:"column:error_message"`
	CreatedAt         time.Time `gorm:"column:created_at"`
}

// TableName maps IntraInquiry model to intra_inquiries table.
func (IntraInquiry) TableName() string { return "intra_inquiries" }

type APILog struct {
	ID           uint64    `gorm:"primaryKey"`
	Method       string    `gorm:"column:method"`
	Path         string    `gorm:"column:path"`
	Status       int       `gorm:"column:status"`
	DurationMS   int64     `gorm:"column:duration_ms"`
	IP           string    `gorm:"column:ip"`
	RequestID    string    `gorm:"column:request_id"`
	ActorID      string    `gorm:"column:actor_id"`
	APISurface   string    `gorm:"column:api_surface"`
	Route        string    `gorm:"column:route"`
	AuthResult   string    `gorm:"column:auth_result"`
	RequestBody  string    `gorm:"column:request_body"`
	ResponseBody string    `gorm:"column:response_body"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

// TableName maps APILog model to api_logs table.
func (APILog) TableName() string { return "api_logs" }

type ExternalApp struct {
	ID           string     `gorm:"primaryKey;column:id"`
	Name         string     `gorm:"column:name"`
	APIKey       string     `gorm:"column:api_key;uniqueIndex"` // #nosec G117 -- model field name intentionally matches schema/API semantics.
	APISecret    string     `gorm:"column:api_secret"`          // #nosec G117 -- model field name intentionally matches schema/API semantics.
	WebhookURL   string     `gorm:"column:webhook_url"`
	IsActive     bool       `gorm:"column:is_active;default:true"`
	ContactName  string     `gorm:"column:contact_name"`
	ContactEmail string     `gorm:"column:contact_email"`
	ContactPhone string     `gorm:"column:contact_phone"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at"`
	LastUsedAt   *time.Time `gorm:"column:last_used_at"`
}

// TableName maps ExternalApp model to external_apps table.
func (ExternalApp) TableName() string { return "external_apps" }

// OpenAndMigrate opens a GORM PostgreSQL connection and runs AutoMigrate.
// Why needed: bootstraps schema/table alignment for all GORM-backed models.
// Called from: main() at startup before controllers are registered.
func OpenAndMigrate(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	println("INFO: Running GORM AutoMigrate...")
	// api_logs is managed by raw SQL in internal/storage/logs.go because it uses
	// custom partitioning and retention metadata migrations.
	if err := db.AutoMigrate(&PaymentTransaction{}, &IntraInquiry{}, &ExternalApp{}, &AdminUser{}); err != nil {
		println("ERROR: GORM AutoMigrate failed:", err.Error())
		return nil, err
	}
	println("INFO: GORM AutoMigrate completed successfully")
	return db, nil
}
