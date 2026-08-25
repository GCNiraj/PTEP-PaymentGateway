package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/internal/common"
	"example.com/fiber-mvc/internal/storage"
)

type APIKeyAuthOptions struct {
	MaxConcurrent int
	// Short negative cache to avoid repeated bcrypt scans for the same invalid key.
	InvalidKeyCacheTTL time.Duration
	// RetryAfter is returned only when the verifier is saturated.
	RetryAfter time.Duration
}

type apiKeyAuthVerifier struct {
	repo            *storage.AppsRepository
	sem             chan struct{}
	invalidCacheTTL time.Duration
	retryAfter      time.Duration

	mu           sync.Mutex
	invalidUntil map[string]time.Time
}

func newAPIKeyAuthVerifier(repo *storage.AppsRepository, opts APIKeyAuthOptions) *apiKeyAuthVerifier {
	maxConcurrent := opts.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 16
	}
	invalidTTL := opts.InvalidKeyCacheTTL
	if invalidTTL <= 0 {
		invalidTTL = 2 * time.Second
	}
	retryAfter := opts.RetryAfter
	if retryAfter <= 0 {
		retryAfter = time.Second
	}
	return &apiKeyAuthVerifier{
		repo:            repo,
		sem:             make(chan struct{}, maxConcurrent),
		invalidCacheTTL: invalidTTL,
		retryAfter:      retryAfter,
		invalidUntil:    make(map[string]time.Time),
	}
}

func (v *apiKeyAuthVerifier) tryAcquire() bool {
	if v == nil || v.sem == nil {
		return true
	}
	select {
	case v.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (v *apiKeyAuthVerifier) release() {
	if v == nil || v.sem == nil {
		return
	}
	select {
	case <-v.sem:
	default:
	}
}

func (v *apiKeyAuthVerifier) invalidCacheKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])
}

func (v *apiKeyAuthVerifier) isKnownInvalid(apiKey string) bool {
	if v == nil || v.invalidCacheTTL <= 0 || strings.TrimSpace(apiKey) == "" {
		return false
	}
	now := time.Now().UTC()
	key := v.invalidCacheKey(apiKey)
	v.mu.Lock()
	defer v.mu.Unlock()
	until, ok := v.invalidUntil[key]
	if !ok {
		return false
	}
	if now.After(until) {
		delete(v.invalidUntil, key)
		return false
	}
	return true
}

func (v *apiKeyAuthVerifier) rememberInvalid(apiKey string) {
	if v == nil || v.invalidCacheTTL <= 0 || strings.TrimSpace(apiKey) == "" {
		return
	}
	key := v.invalidCacheKey(apiKey)
	v.mu.Lock()
	v.invalidUntil[key] = time.Now().UTC().Add(v.invalidCacheTTL)
	// Opportunistic cleanup to keep the map bounded during attack bursts.
	if len(v.invalidUntil) > 2048 {
		now := time.Now().UTC()
		for k, until := range v.invalidUntil {
			if now.After(until) {
				delete(v.invalidUntil, k)
			}
		}
	}
	v.mu.Unlock()
}

func (v *apiKeyAuthVerifier) clearInvalid(apiKey string) {
	if v == nil || strings.TrimSpace(apiKey) == "" {
		return
	}
	key := v.invalidCacheKey(apiKey)
	v.mu.Lock()
	delete(v.invalidUntil, key)
	v.mu.Unlock()
}

func (v *apiKeyAuthVerifier) findMatchingApp(c *fiber.Ctx, apiKey string) (*storage.ExternalApp, error, bool) {
	if v == nil || v.repo == nil || !v.repo.Enabled() {
		return nil, nil, false
	}
	if v.isKnownInvalid(apiKey) {
		return nil, nil, false
	}
	if !v.tryAcquire() {
		return nil, nil, true
	}
	defer v.release()

	apps, err := v.repo.ListForAuth(c.Context())
	if err != nil {
		return nil, err, false
	}
	for _, app := range apps {
		if common.VerifyAPIKey(apiKey, app.APIKey) {
			matched := app
			v.clearInvalid(apiKey)
			return &matched, nil, false
		}
	}
	v.rememberInvalid(apiKey)
	return nil, nil, false
}

func setRetryAfter(c *fiber.Ctx, d time.Duration) {
	if c == nil || d <= 0 {
		return
	}
	secs := int(d / time.Second)
	if secs <= 0 {
		secs = 1
	}
	c.Set("Retry-After", strconv.Itoa(secs))
}

// APIKeyAuth creates middleware that validates API keys for external app access.
// Why needed: secures business endpoints so only registered apps can make payments.
// Called from: routes.Register for /api/business/* endpoints.
// Flow: extracts X-API-Key header → finds app → verifies key → stores app context.
func APIKeyAuth(appsRepo *storage.AppsRepository) fiber.Handler {
	return APIKeyAuthWithOptions(appsRepo, APIKeyAuthOptions{})
}

// APIKeyAuthWithOptions provides the same auth flow with non-breaking hardening knobs.
func APIKeyAuthWithOptions(appsRepo *storage.AppsRepository, opts APIKeyAuthOptions) fiber.Handler {
	verifier := newAPIKeyAuthVerifier(appsRepo, opts)
	return func(c *fiber.Ctx) error {
		// Skip if repository is not enabled
		if appsRepo == nil || !appsRepo.Enabled() {
			return c.Next()
		}

		// Extract API key from header
		apiKey := strings.TrimSpace(c.Get("X-API-Key"))
		if apiKey == "" {
			return c.Status(401).JSON(fiber.Map{
				"error": "API key required",
				"hint":  "Include X-API-Key header with your request",
			})
		}

		matchedApp, err, saturated := verifier.findMatchingApp(c, apiKey)
		if saturated {
			setRetryAfter(c, verifier.retryAfter)
			return c.Status(503).JSON(fiber.Map{
				"error": "API key validation busy, retry shortly",
			})
		}
		if err != nil {
			return c.Status(500).JSON(fiber.Map{
				"error": "failed to validate API key",
			})
		}

		if matchedApp == nil {
			return c.Status(401).JSON(fiber.Map{
				"error": "invalid API key",
			})
		}

		// Check if app is active
		if !matchedApp.IsActive {
			return c.Status(403).JSON(fiber.Map{
				"error": "app is inactive",
				"hint":  "Contact administrator to reactivate your app",
			})
		}

		// Store app context in locals for controllers to use
		c.Locals("app_id", matchedApp.ID)
		c.Locals("app_name", matchedApp.Name)

		// Update last used timestamp (async, don't wait)
		go func() {
			_ = appsRepo.UpdateLastUsed(c.Context(), matchedApp.ID)
		}()

		return c.Next()
	}
}

// OptionalAPIKeyAuth is a variant that allows requests without API keys.
// Why needed: for endpoints that work with or without app context.
// Called from: routes where API key is optional but beneficial.
func OptionalAPIKeyAuth(appsRepo *storage.AppsRepository) fiber.Handler {
	return OptionalAPIKeyAuthWithOptions(appsRepo, APIKeyAuthOptions{})
}

// OptionalAPIKeyAuthWithOptions adds the same hardening as APIKeyAuth but preserves optional behavior.
func OptionalAPIKeyAuthWithOptions(appsRepo *storage.AppsRepository, opts APIKeyAuthOptions) fiber.Handler {
	verifier := newAPIKeyAuthVerifier(appsRepo, opts)
	return func(c *fiber.Ctx) error {
		// Skip if repository is not enabled
		if appsRepo == nil || !appsRepo.Enabled() {
			return c.Next()
		}

		// Extract API key from header
		apiKey := strings.TrimSpace(c.Get("X-API-Key"))
		if apiKey == "" {
			// No API key provided, continue without app context
			return c.Next()
		}

		matchedApp, err, saturated := verifier.findMatchingApp(c, apiKey)
		if saturated {
			// Preserve optional semantics: continue without app context when verifier is busy.
			return c.Next()
		}
		if err != nil {
			// Don't fail the request, just continue without app context
			return c.Next()
		}

		if matchedApp != nil && matchedApp.IsActive {
			// Store app context in locals
			c.Locals("app_id", matchedApp.ID)
			c.Locals("app_name", matchedApp.Name)

			// Update last used timestamp (async)
			go func() {
				_ = appsRepo.UpdateLastUsed(c.Context(), matchedApp.ID)
			}()
		}

		return c.Next()
	}
}

// GetAppID retrieves the app ID from request context.
// Why needed: controllers need to access authenticated app info.
// Called from: business controllers to associate transactions with apps.
func GetAppID(c *fiber.Ctx) string {
	if appID, ok := c.Locals("app_id").(string); ok {
		return appID
	}
	return ""
}

// GetAppName retrieves the app name from request context.
// Why needed: for logging and display purposes.
// Called from: controllers and logging middleware.
func GetAppName(c *fiber.Ctx) string {
	if appName, ok := c.Locals("app_name").(string); ok {
		return appName
	}
	return ""
}
