package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/joho/godotenv"

	"example.com/fiber-mvc/config"
	"example.com/fiber-mvc/controllers"
	"example.com/fiber-mvc/internal/auth"
	"example.com/fiber-mvc/internal/credentials"
	"example.com/fiber-mvc/internal/storage"
	gormdb "example.com/fiber-mvc/internal/storage/gorm"
	"example.com/fiber-mvc/internal/stripe"
	"example.com/fiber-mvc/middleware"
	"example.com/fiber-mvc/routes"
)

// main bootstraps configuration, database, controllers, middleware, and HTTP server.
// Why needed: orchestrates dependency wiring and starts Fiber listener.
// Request flow starts here via routes.Register and app.Listen.
func main() {
	loadDotEnv()

	cfg := config.Load()
	if strings.TrimSpace(cfg.JWTSecret) == "" {
		log.Fatal("JWT_SECRET must be set")
	}
	if cfg.JWTTTLMinutes <= 0 {
		log.Fatal("JWT_TTL_MINUTES must be greater than 0")
	}

	gormDB, err := gormdb.OpenAndMigrate(cfg.ResolvedDatabaseURL())
	if err != nil {
		log.Fatal(err)
	}

	if err := gormdb.EnsureAdminUser(gormDB, cfg.AdminUsername, cfg.AdminPassword, cfg.AdminPasswordHash); err != nil {
		log.Fatal(err)
	}

	db, err := storage.Connect(cfg.ResolvedDatabaseURL())
	if err != nil {
		log.Fatal(err)
	}
	if err := storage.EnsureIndexes(db); err != nil {
		log.Fatal(err)
	}
	// Run apps schema migration
	if err := storage.EnsureAppsSchema(db); err != nil {
		log.Fatal(err)
	}
	// Run international payments migration
	if err := storage.RunInternationalMigrations(db.Conn); err != nil {
		log.Fatal(err)
	}
	if err := storage.RunMerchantRoutingMigrations(db.Conn); err != nil {
		log.Fatal(err)
	}
	// Ensure api_logs schema/partitioning exists (managed by raw SQL in storage/logs.go)
	if err := storage.EnsureLogsSchema(db); err != nil {
		log.Fatal(err)
	}
	logRepo := storage.NewLogRepository(db)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := logRepo.Close(shutdownCtx); err != nil {
			log.Printf("log repository shutdown error: %v", err)
		}
	}()

	app := fiber.New(fiber.Config{
		AppName:         cfg.AppName,
		ReadTimeout:     time.Duration(cfg.ServerReadTimeoutSeconds) * time.Second,
		WriteTimeout:    time.Duration(cfg.ServerWriteTimeoutSeconds) * time.Second,
		IdleTimeout:     time.Duration(cfg.ServerIdleTimeoutSeconds) * time.Second,
		Concurrency:     cfg.ServerConcurrency,
		ReadBufferSize:  cfg.ServerReadBufferBytes,
		WriteBufferSize: cfg.ServerReadBufferBytes,
		// ProxyHeader tells Fiber to treat X-Forwarded-Proto as the authoritative
		// scheme. This is required so that c.Protocol() — used by HTTPSRedirect
		// and SecurityHeaders — correctly returns "https" when the app sits
		// behind a TLS-terminating reverse proxy.
		ProxyHeader: fiber.HeaderXForwardedProto,
	})
	// Slowloris/DoS hardening at the underlying server layer.
	// MaxConnsPerIP is optional because a single reverse proxy IP can represent all clients.
	if cfg.ServerMaxConnsPerIP > 0 {
		app.Server().MaxConnsPerIP = cfg.ServerMaxConnsPerIP
	}
	log.Printf(
		"server hardening enabled: read_timeout=%ds write_timeout=%ds idle_timeout=%ds concurrency=%d max_conns_per_ip=%d read_buffer_bytes=%d",
		cfg.ServerReadTimeoutSeconds,
		cfg.ServerWriteTimeoutSeconds,
		cfg.ServerIdleTimeoutSeconds,
		cfg.ServerConcurrency,
		cfg.ServerMaxConnsPerIP,
		cfg.ServerReadBufferBytes,
	)
	// HTTPSRedirect must be the very first middleware so that every request
	// (login page, API endpoints, static assets) is upgraded to HTTPS before
	// any processing occurs. The enabled flag defaults to true and can be set
	// to false in .env for local HTTP development.
	app.Use(middleware.HTTPSRedirect(cfg.HTTPSRedirectEnabled))
	app.Use(middleware.SecurityHeaders(middleware.SecurityHeadersOptions{
		Enabled:       cfg.SecurityHeadersEnabled,
		CSPEnabled:    cfg.CSPEnabled,
		CSPPolicy:     cfg.CSPPolicy,
		CSPReportOnly: cfg.CSPReportOnly,
		CSPReportURI:  cfg.CSPReportURI,
	}))
	dbRequestLogger := middleware.RequestLogger(logRepo)
	stdoutRequestLogger := middleware.StdoutRequestLogger()

	// Initialize Stripe client for international payments
	stripeClient := stripe.NewClient(cfg.StripeBaseURL, cfg.StripeAPIKey, logRepo)
	credentialCipher, err := credentials.NewFromBase64(cfg.PaymentCredentialMasterKey)
	if err != nil {
		log.Fatalf("PAYMENT_CREDENTIALS_MASTER_KEY_B64 must be a base64-encoded 32-byte key: %v", err)
	}

	repo := storage.NewRepository(db)
	routingRepo := storage.NewMerchantRoutingRepository(db)

	// Who gets paid, from provision.json if there is one. Adds only, never
	// overwrites, never fatal — see provision_boot.go.
	provisionAtBoot(routingRepo, credentialCipher)
	resolver := &controllers.GatewayResolver{Repo: routingRepo, Cipher: credentialCipher, Cfg: cfg, LogRepo: logRepo}
	businessController := controllers.NewBusinessController(repo, cfg, resolver)

	txListController := controllers.NewTransactionsListController(repo)
	appsController := controllers.NewAppsController(storage.NewAppsRepository(db))
	dbDebugController := controllers.NewDBDebugController(db.Conn)

	jwtMgr := &auth.Manager{
		Secret:   cfg.JWTSecret,
		TTL:      time.Duration(cfg.JWTTTLMinutes) * time.Minute,
		Issuer:   cfg.JWTIssuer,
		Audience: cfg.JWTAudience,
	}
	sessionMgr := auth.NewSessionManager(jwtMgr.TTL)
	loginLimiter := auth.NewLoginLimiter(
		cfg.LoginMaxAttempts,
		time.Duration(cfg.LoginWindowMinutes)*time.Minute,
		time.Duration(cfg.LoginLockMinutes)*time.Minute,
	)
	captchaVerifier := auth.NewCaptchaVerifier(
		cfg.LoginCaptchaSiteKey,
		cfg.LoginCaptchaSecret,
		cfg.LoginCaptchaAfterFailures,
		time.Duration(cfg.LoginCaptchaTimeoutSeconds)*time.Second,
	)
	authController := &controllers.AuthController{
		JWT:            jwtMgr,
		Sessions:       sessionMgr,
		DB:             gormDB,
		Limiter:        loginLimiter,
		Captcha:        captchaVerifier,
		CookieName:     cfg.AuthCookieName,
		CSRFCookieName: cfg.CSRFCookieName,
	}
	if captchaVerifier.Enabled() {
		log.Printf("login CAPTCHA enabled (threshold=%d failed attempts)", cfg.LoginCaptchaAfterFailures)
	} else if strings.TrimSpace(cfg.LoginCaptchaSiteKey) != "" || strings.TrimSpace(cfg.LoginCaptchaSecret) != "" {
		log.Printf("warning: login CAPTCHA is disabled because both LOGIN_CAPTCHA_SITE_KEY and LOGIN_CAPTCHA_SECRET are required")
	}
	adminSignupController := controllers.NewAdminSignupController(gormDB, cfg.AdminSignupKey)
	logsController := controllers.NewLogsController(logRepo)
	adminDebugController := controllers.NewAdminDebugController(gormDB)
	seedController := controllers.NewSeedController(gormDB)

	// Apps management
	appsRepo := storage.NewAppsRepository(db)
	appsManagementController := controllers.NewAppsManagementController(appsRepo)

	// International payments
	intlRepo := storage.NewInternationalRepository(db)
	internationalController := controllers.NewInternationalController(stripeClient, intlRepo, appsRepo, cfg, resolver)
	merchantRoutingAdminController := controllers.NewMerchantRoutingAdminController(routingRepo, credentialCipher)
	// UPI records live in the booking platform; this reads them over its API.
	upiController := controllers.NewUpiController(cfg)
	routingOverviewController := controllers.NewMerchantRoutingOverviewController(routingRepo, credentialCipher, cfg)
	statusSyncCtx, cancelStatusSync := context.WithCancel(context.Background())
	defer cancelStatusSync()
	internationalController.StartPendingStatusSync(statusSyncCtx)

	if strings.TrimSpace(cfg.AdminSignupKey) != "" {
		// We use a shared IP rate limiter (adminWriteRateLimit) which is defined below,
		// but since it's needed here, let's define all limiters first.
		log.Printf("ADMIN_SIGNUP_KEY is empty; backend signup endpoint is disabled")
	}
	adminAuth := middleware.AdminAuth(jwtMgr, sessionMgr, cfg.AuthCookieName)
	apiKeyAuth := middleware.APIKeyAuthWithOptions(appsRepo, middleware.APIKeyAuthOptions{
		MaxConcurrent:      cfg.APIKeyAuthMaxConcurrent,
		InvalidKeyCacheTTL: time.Duration(cfg.APIKeyAuthInvalidCacheSeconds) * time.Second,
		RetryAfter:         time.Duration(cfg.APIKeyAuthBusyRetryAfterSeconds) * time.Second,
	})

	// Rate limiters — one per risk tier, all configurable via environment variables.
	// nil-safe: if NewIPRateLimiter panics it won't; harmless if env vars are not set.
	bizLimiter := auth.NewIPRateLimiter(
		cfg.BizRateLimitMax,
		time.Duration(cfg.BizRateLimitWindowSeconds)*time.Second,
	)
	otpLimiter := auth.NewIPRateLimiter(
		cfg.OTPRateLimitMax,
		time.Duration(cfg.OTPRateLimitWindowSeconds)*time.Second,
	)
	adminWriteLimiter := auth.NewIPRateLimiter(
		cfg.AdminWriteRateLimitMax,
		time.Duration(cfg.AdminWriteRateLimitWindowSeconds)*time.Second,
	)
	appCreateLimiter := auth.NewIPRateLimiter(
		cfg.AppCreateRateLimitMax,
		time.Duration(cfg.AppCreateRateLimitWindowSeconds)*time.Second,
	)
	bizRateLimit := middleware.RateLimit(bizLimiter)
	otpRateLimit := middleware.RateLimit(otpLimiter)
	adminWriteRateLimit := middleware.RateLimit(adminWriteLimiter)
	appCreateRateLimit := middleware.RateLimit(appCreateLimiter)

	// Admin signup endpoint (backend only)
	if strings.TrimSpace(cfg.AdminSignupKey) != "" {
		app.Post(cfg.AdminSignupRoute, adminWriteRateLimit, dbRequestLogger, adminSignupController.Signup)
	} else {
		log.Printf("ADMIN_SIGNUP_KEY is empty; backend signup endpoint is disabled")
	}

	routes.Register(app, businessController, txListController, authController, logsController, appsController, appsManagementController, internationalController, merchantRoutingAdminController, upiController, routingOverviewController, dbRequestLogger, stdoutRequestLogger, adminAuth, apiKeyAuth, adminDebugController, seedController, dbDebugController, bizRateLimit, otpRateLimit, adminWriteRateLimit, appCreateRateLimit)
	app.Get("/", func(c *fiber.Ctx) error {
		return c.Redirect("/login", http.StatusFound)
	})
	app.Get(
		"/dashboard",
		middleware.AdminPageAuth(jwtMgr, sessionMgr, cfg.AuthCookieName, "/login"),
		func(c *fiber.Ctx) error {
			return c.SendFile("./views/dashboard.html")
		},
	)
	// These pages and their assets are served with no Cache-Control and no
	// ETag, only Last-Modified. A browser is allowed to invent its own
	// freshness from that, and the mtimes here are months old, so a stale
	// login.js can be served for weeks without one revalidating request.
	//
	// That failure is invisible: the page renders, the form works, and the
	// only symptom is behaviour from a version of the site that no longer
	// exists — a captcha that never appears, say. Revalidate every time. These
	// are four small files on an administrative console, not a CDN's problem.
	asset := func(path string) fiber.Handler {
		return func(c *fiber.Ctx) error {
			c.Set("Cache-Control", "no-cache, must-revalidate")
			return c.SendFile(path)
		}
	}
	app.Get("/login", asset("./views/login.html"))
	// Serve only the explicit frontend assets needed by login/dashboard pages.
	// This avoids exposing backup/template files under views/ via directory-wide static serving.
	app.Get("/styles.css", asset("./views/styles.css"))
	app.Get("/app.js", asset("./views/app.js"))
	app.Get("/charts.js", asset("./views/charts.js"))
	app.Get("/modals.js", asset("./views/modals.js"))
	app.Get("/login.js", asset("./views/login.js"))

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(sigCh)
		sig := <-sigCh
		log.Printf("shutdown signal received: %s", sig.String())
		cancelStatusSync()
		if err := app.Shutdown(); err != nil {
			log.Printf("fiber shutdown error: %v", err)
		}
	}()

	if err := app.Listen(":" + cfg.Port); err != nil {
		log.Printf("server stopped: %v", err)
	}
}

// loadDotEnv conditionally loads local environment variables for development.
// Why needed: allows operators to configure DKPG credentials and DB settings
// without recompiling the binary.
// Called from: main() during startup.
// Next flow: config.Load() reads these values and wires downstream services.
func loadDotEnv() {
	if _, err := os.Stat(".env"); err == nil {
		if err := godotenv.Load(); err != nil {
			log.Printf("warning: failed to load .env: %v", err)
		}
	}
}
