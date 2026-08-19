package config

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	AppName string
	Port    string

	DKPGBaseURL      string
	DKPGAPIKey       string
	DKPGUsername     string
	DKPGPassword     string
	DKPGClientID     string
	DKPGClientSecret string
	DKPGScopes       string
	DKPGSourceApp    string
	DKPGPrivateKey   string

	DatabaseURL string

	DKBeneficiaryAccount string
	DKBeneficiaryName    string
	DKBeneficiaryBank    string
	DKSourceAccount      string
	DKSourceAccountName  string

	DBHost     string
	DBPort     string
	DBUser     string
	DBPass     string
	DBName     string
	DBSSLMode  string
	DBTimezone string

	AdminUsername                   string
	AdminPassword                   string
	AdminPasswordHash               string
	AdminSignupRoute                string
	AdminSignupKey                  string
	JWTSecret                       string // #nosec G117 -- config field name intentionally matches environment variable semantics.
	JWTTTLMinutes                   int
	JWTIssuer                       string
	JWTAudience                     string
	AuthCookieName                  string
	LoginMaxAttempts                int
	LoginWindowMinutes              int
	LoginLockMinutes                int
	LoginCaptchaSiteKey             string
	LoginCaptchaSecret              string
	LoginCaptchaAfterFailures       int
	LoginCaptchaTimeoutSeconds      int
	APIKeyAuthMaxConcurrent         int
	APIKeyAuthInvalidCacheSeconds   int
	APIKeyAuthBusyRetryAfterSeconds int

	// Rate limiting for business, OTP confirm, and DKPG proxy endpoints.
	// Limits are per source IP using a sliding window. Zero or negative values
	// use the defaults baked into NewIPRateLimiter.
	BizRateLimitMax                  int // requests allowed per window on /api/business/* and /api/v1/*
	BizRateLimitWindowSeconds        int // window size in seconds for business endpoints
	OTPRateLimitMax                  int // requests allowed per window on /api/business/pull/confirm only
	OTPRateLimitWindowSeconds        int // window size in seconds for OTP confirm endpoint
	DKPGRateLimitMax                 int // requests allowed per window on /api/dkpg/*
	DKPGRateLimitWindowSeconds       int // window size in seconds for DKPG proxy endpoints
	AdminWriteRateLimitMax           int // requests allowed per window on admin write endpoints (create/update/delete app)
	AdminWriteRateLimitWindowSeconds int // window size in seconds for admin write endpoints
	AppCreateRateLimitMax            int // requests allowed per window on POST /api/admin/apps/create specifically
	AppCreateRateLimitWindowSeconds  int // window size in seconds for app create endpoint

	StripeAPIKey        string
	StripeBaseURL       string
	StripeAgencyName    string
	StripeSubmerchantID string
	StripeDKAccount     string
	StripeSuccessURL    string
	StripeCancelURL     string
	StripeCurrency      string
	// PaymentCredentialMasterKey is a base64-encoded 32-byte AES-256 key used
	// only to encrypt tenant gateway credentials stored in PostgreSQL.
	PaymentCredentialMasterKey string

	SecurityHeadersEnabled bool
	CSPEnabled             bool
	CSPPolicy              string
	CSPReportOnly          bool
	CSPReportURI           string

	HTTPSRedirectEnabled bool // controlled by HTTPS_REDIRECT_ENABLED env var (default: true)

	CSRFCookieName string

	// Server-level DoS/Slowloris hardening.
	ServerReadTimeoutSeconds  int // full request read timeout (headers+body)
	ServerWriteTimeoutSeconds int // response write timeout
	ServerIdleTimeoutSeconds  int // keep-alive idle timeout
	ServerConcurrency         int // max concurrent connections
	ServerMaxConnsPerIP       int // max concurrent connections per source IP (0 disables)
	ServerReadBufferBytes     int // max header buffer / request read buffer per connection
}

// Load reads application configuration from environment variables with defaults.
// Why needed: central source for runtime settings used across startup wiring.
// Called from: main() at process bootstrap.
func Load() Config {
	// Load private key using the robust multi-source loader
	privateKey, err := LoadPrivateKey()
	if err != nil {
		// Log error but continue - main() essentially does this anyway via sdk client check
		// or we can let it fail later. Better to log visibility.
		// Since this function signature doesn't return error, we print to stderr.
		_, _ = os.Stderr.WriteString("Error loading private key: " + err.Error() + "\n")
	}

	jwtSecret := getenv("JWT_SECRET", "")
	adminSignupRoute := normalizePath(getenv("ADMIN_SIGNUP_ROUTE", ""))
	if adminSignupRoute == "" {
		adminSignupRoute = derivedSignupRoute(jwtSecret)
	}

	return Config{
		AppName:              getenv("APP_NAME", "DK Payment Gateway Backend"),
		Port:                 getenv("PORT", "5001"),
		DKPGBaseURL:          getenv("DKPG_BASE_URL", "https://internal-gateway.uat.digitalkidu.bt/api/dkpg"),
		DKPGAPIKey:           getenv("DKPG_API_KEY", ""),
		DKPGUsername:         getenv("DKPG_USERNAME", ""),
		DKPGPassword:         getenv("DKPG_PASSWORD", ""),
		DKPGClientID:         getenv("DKPG_CLIENT_ID", ""),
		DKPGClientSecret:     getenv("DKPG_CLIENT_SECRET", ""),
		DKPGScopes:           getenv("DKPG_SCOPES", "keys:read"),
		DKPGSourceApp:        getenv("DKPG_SOURCE_APP", "SRC_AVS_0201"),
		DKPGPrivateKey:       privateKey,
		DatabaseURL:          getenv("DATABASE_URL", ""),
		DKBeneficiaryAccount: getenv("DK_BENEFICIARY_ACCOUNT", ""),
		DKBeneficiaryName:    getenv("DK_BENEFICIARY_NAME", ""),
		DKBeneficiaryBank:    getenv("DK_BENEFICIARY_BANK", "1060"),
		DKSourceAccount:      getenv("DK_SOURCE_ACCOUNT", ""),
		DKSourceAccountName:  getenv("DK_SOURCE_ACCOUNT_NAME", ""),

		DBHost:     getenv("DB_HOST", ""),
		DBPort:     getenv("DB_PORT", "5432"),
		DBUser:     getenv("DB_USER", ""),
		DBPass:     getenv("DB_PASS", ""),
		DBName:     getenv("DB_NAME", ""),
		DBSSLMode:  getenv("DB_SSLMODE", "disable"),
		DBTimezone: getenv("DB_TIMEZONE", ""),

		AdminUsername:                   getenv("ADMIN_USERNAME", "admin"),
		AdminPassword:                   getenv("ADMIN_PASSWORD", ""),
		AdminPasswordHash:               getenv("ADMIN_PASSWORD_HASH", ""),
		AdminSignupRoute:                adminSignupRoute,
		AdminSignupKey:                  getenv("ADMIN_SIGNUP_KEY", ""),
		JWTSecret:                       jwtSecret,
		JWTTTLMinutes:                   atoi(getenv("JWT_TTL_MINUTES", "60"), 60),
		JWTIssuer:                       getenv("JWT_ISSUER", "dk-gateway-admin"),
		JWTAudience:                     getenv("JWT_AUDIENCE", "dk-gateway-admin"),
		AuthCookieName:                  getenv("AUTH_COOKIE_NAME", "admin_session"),
		LoginMaxAttempts:                atoi(getenv("LOGIN_MAX_ATTEMPTS", "5"), 5),
		LoginWindowMinutes:              atoi(getenv("LOGIN_WINDOW_MINUTES", "15"), 15),
		LoginLockMinutes:                atoi(getenv("LOGIN_LOCK_MINUTES", "15"), 15),
		LoginCaptchaSiteKey:             getenv("LOGIN_CAPTCHA_SITE_KEY", ""),
		LoginCaptchaSecret:              getenv("LOGIN_CAPTCHA_SECRET", ""),
		LoginCaptchaAfterFailures:       atoi(getenv("LOGIN_CAPTCHA_AFTER_FAILURES", "3"), 3),
		LoginCaptchaTimeoutSeconds:      atoi(getenv("LOGIN_CAPTCHA_TIMEOUT_SECONDS", "3"), 3),
		APIKeyAuthMaxConcurrent:         atoi(getenv("API_KEY_AUTH_MAX_CONCURRENT", "16"), 16),
		APIKeyAuthInvalidCacheSeconds:   atoi(getenv("API_KEY_AUTH_INVALID_CACHE_SECONDS", "2"), 2),
		APIKeyAuthBusyRetryAfterSeconds: atoi(getenv("API_KEY_AUTH_BUSY_RETRY_AFTER_SECONDS", "1"), 1),

		BizRateLimitMax:            atoi(getenv("BIZ_RATE_LIMIT_MAX", "60"), 60),
		BizRateLimitWindowSeconds:  atoi(getenv("BIZ_RATE_LIMIT_WINDOW_SECONDS", "60"), 60),
		OTPRateLimitMax:            atoi(getenv("OTP_RATE_LIMIT_MAX", "10"), 10),
		OTPRateLimitWindowSeconds:  atoi(getenv("OTP_RATE_LIMIT_WINDOW_SECONDS", "60"), 60),
		DKPGRateLimitMax:           atoi(getenv("DKPG_RATE_LIMIT_MAX", "30"), 30),
		DKPGRateLimitWindowSeconds: atoi(getenv("DKPG_RATE_LIMIT_WINDOW_SECONDS", "60"), 60),

		AdminWriteRateLimitMax:           atoi(getenv("ADMIN_WRITE_RATE_LIMIT_MAX", "20"), 20),
		AdminWriteRateLimitWindowSeconds: atoi(getenv("ADMIN_WRITE_RATE_LIMIT_WINDOW_SECONDS", "60"), 60),
		AppCreateRateLimitMax:            atoi(getenv("APP_CREATE_RATE_LIMIT_MAX", "5"), 5),
		AppCreateRateLimitWindowSeconds:  atoi(getenv("APP_CREATE_RATE_LIMIT_WINDOW_SECONDS", "60"), 60),

		StripeAPIKey:               getenv("STRIPE_API_KEY", ""),
		StripeBaseURL:              getenv("STRIPE_BASE_URL", "https://internal-gateway.sit.digitalkidu.bt:8082/uat/stripe/"),
		StripeAgencyName:           getenv("STRIPE_AGENCY_NAME", ""),
		StripeSubmerchantID:        getenv("STRIPE_SUBMERCHANT_ID", ""),
		StripeDKAccount:            getenv("STRIPE_DK_ACCOUNT", ""),
		StripeSuccessURL:           getenv("STRIPE_SUCCESS_URL", ""),
		StripeCancelURL:            getenv("STRIPE_CANCEL_URL", ""),
		StripeCurrency:             strings.ToUpper(getenv("STRIPE_CURRENCY", "USD")),
		PaymentCredentialMasterKey: getenv("PAYMENT_CREDENTIALS_MASTER_KEY_B64", ""),
		SecurityHeadersEnabled:     parseBool(getenv("SECURITY_HEADERS_ENABLED", "true"), true),
		CSPEnabled:                 parseBool(getenv("CSP_ENABLED", "true"), true),
		CSPPolicy:                  getenv("CSP_POLICY", ""),
		CSPReportOnly:              parseBool(getenv("CSP_REPORT_ONLY", "false"), false),
		CSPReportURI:               getenv("CSP_REPORT_URI", ""),
		HTTPSRedirectEnabled:       parseBool(getenv("HTTPS_REDIRECT_ENABLED", "true"), true),
		CSRFCookieName:             getenv("CSRF_COOKIE_NAME", "csrf_token"),

		ServerReadTimeoutSeconds:  atoiMin(getenv("SERVER_READ_TIMEOUT_SECONDS", "10"), 10, 1),
		ServerWriteTimeoutSeconds: atoiMin(getenv("SERVER_WRITE_TIMEOUT_SECONDS", "30"), 30, 1),
		ServerIdleTimeoutSeconds:  atoiMin(getenv("SERVER_IDLE_TIMEOUT_SECONDS", "60"), 60, 1),
		ServerConcurrency:         atoiMin(getenv("SERVER_CONCURRENCY", "4096"), 4096, 1),
		ServerMaxConnsPerIP:       atoiMin(getenv("SERVER_MAX_CONNS_PER_IP", "0"), 0, 0),
		ServerReadBufferBytes:     atoiMin(getenv("SERVER_READ_BUFFER_BYTES", "8192"), 8192, 1024),
	}
}

// ResolvedDatabaseURL returns DATABASE_URL directly or builds one from DB_* fields.
// Why needed: supports both single DSN and split field configuration styles.
// Called from: main() before DB initialization.
func (c Config) ResolvedDatabaseURL() string {
	if strings.TrimSpace(c.DatabaseURL) != "" {
		return c.DatabaseURL
	}
	if strings.TrimSpace(c.DBHost) == "" || strings.TrimSpace(c.DBUser) == "" || strings.TrimSpace(c.DBName) == "" {
		return ""
	}

	user := url.UserPassword(c.DBUser, c.DBPass)
	u := &url.URL{
		Scheme: "postgres",
		User:   user,
		Host:   c.DBHost + ":" + c.DBPort,
		Path:   c.DBName,
	}

	q := url.Values{}
	if c.DBSSLMode != "" {
		q.Set("sslmode", c.DBSSLMode)
	}
	if c.DBTimezone != "" {
		q.Set("timezone", c.DBTimezone)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// getenv returns trimmed env value or fallback when unset/blank.
// Called from: Load.
func getenv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

// atoi converts string to int with fallback on parse failure.
// Called from: Load for JWT_TTL_MINUTES parsing.
func atoi(s string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fallback
	}
	return v
}

func atoiMin(s string, fallback, min int) int {
	v := atoi(s, fallback)
	if v < min {
		return min
	}
	return v
}

func parseBool(s string, fallback bool) bool {
	v, err := strconv.ParseBool(strings.TrimSpace(s))
	if err != nil {
		return fallback
	}
	return v
}

func normalizeMode(s, fallback string, allowed ...string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return fallback
	}
	for _, v := range allowed {
		if s == v {
			return s
		}
	}
	return fallback
}

func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func derivedSignupRoute(seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return "/api/_internal/admin-signup/disabled"
	}
	sum := sha256.Sum256([]byte(seed + ":admin-signup-route-v1"))
	return "/api/_internal/admin-signup/" + hex.EncodeToString(sum[:])
}
