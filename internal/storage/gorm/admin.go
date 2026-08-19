package gormdb

import (
	"errors"
	"strings"
	"unicode"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type AdminUser struct {
	ID           uint64 `gorm:"primaryKey"`
	Username     string `gorm:"uniqueIndex"`
	PasswordHash string
}

var dummyPasswordHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

var ErrAdminUserExists = errors.New("admin user already exists")

// TableName maps AdminUser model to existing admin_users table.
func (AdminUser) TableName() string { return "admin_users" }

// EnsureAdminUser creates a default admin account when missing.
// Why needed: keeps admin login functional after fresh deploy/migration.
// Called from: main() startup after OpenAndMigrate.
func EnsureAdminUser(db *gorm.DB, username, password, passwordHash string) error {
	if db == nil {
		return errors.New("db is nil")
	}
	username = strings.TrimSpace(username)
	if err := ValidateAdminUsername(username); err != nil {
		return err
	}

	var count int64
	if err := db.Model(&AdminUser{}).Where("username = ?", username).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	passwordHash = strings.TrimSpace(passwordHash)
	if passwordHash != "" {
		if _, err := bcrypt.Cost([]byte(passwordHash)); err != nil {
			return errors.New("invalid ADMIN_PASSWORD_HASH")
		}
		return db.Create(&AdminUser{Username: username, PasswordHash: passwordHash}).Error
	}

	if err := ValidateAdminPassword(password); err != nil {
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	return db.Create(&AdminUser{Username: username, PasswordHash: string(hash)}).Error
}

// CreateAdminUser creates a new admin user with a bcrypt-hashed password.
// Returns ErrAdminUserExists when username already exists.
func CreateAdminUser(db *gorm.DB, username, password string) error {
	if db == nil {
		return errors.New("db is nil")
	}
	username = strings.TrimSpace(username)
	if err := ValidateAdminUsername(username); err != nil {
		return err
	}
	if err := ValidateAdminPassword(password); err != nil {
		return err
	}

	var count int64
	if err := db.Model(&AdminUser{}).Where("username = ?", username).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrAdminUserExists
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	if err := db.Create(&AdminUser{Username: username, PasswordHash: string(hash)}).Error; err != nil {
		// Handle race where duplicate user is created after pre-check.
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return ErrAdminUserExists
		}
		return err
	}
	return nil
}

// VerifyAdminUser checks username/password against stored bcrypt hash.
// Called from: AuthController.Login when DB-backed auth is enabled.
// Returns false,nil for password mismatch; error for query/db failures.
func VerifyAdminUser(db *gorm.DB, username, password string) (bool, error) {
	if db == nil {
		return false, errors.New("db is nil")
	}
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return false, nil
	}
	var user AdminUser
	if err := db.Where("username = ?", username).First(&user).Error; err != nil {
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(password))
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return false, nil
	}
	return true, nil
}

// ValidateAdminPassword enforces a minimum baseline policy for bootstrap admin passwords.
func ValidateAdminPassword(password string) error {
	if len(password) < 12 {
		return errors.New("admin password must be at least 12 characters")
	}
	if len(password) > 128 {
		return errors.New("admin password is too long")
	}

	var hasUpper bool
	var hasLower bool
	var hasDigit bool
	for _, r := range password {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	if !hasUpper || !hasLower || !hasDigit {
		return errors.New("admin password must contain uppercase, lowercase, and number")
	}
	return nil
}

// ValidateAdminUsername enforces a safe username format for admin users.
func ValidateAdminUsername(username string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("admin username required")
	}
	if len(username) < 3 || len(username) > 64 {
		return errors.New("admin username must be between 3 and 64 characters")
	}
	for _, r := range username {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '.', r == '-', r == '_':
			continue
		default:
			return errors.New("admin username can only contain letters, numbers, dot, dash, and underscore")
		}
	}
	return nil
}
