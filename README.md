# DK Gateway Backend

## Backend-Only Admin Signup

A secure admin signup endpoint is now available in backend only:

- It is **not** exposed in `login.html` or `dashboard.html`.
- Passwords are stored using **bcrypt hashing** (never plaintext).
- Endpoint requires a secret header: `X-Signup-Key`.
- Route is long and hard to guess, configurable via env.

### Environment

Set these variables:

```env
ADMIN_SIGNUP_ROUTE=/api/_internal/admin-signup/<long-random-string>
ADMIN_SIGNUP_KEY=<very-long-random-secret>
```

Notes:

- If `ADMIN_SIGNUP_ROUTE` is empty, the backend derives it from `JWT_SECRET` using SHA-256:
  - `/api/_internal/admin-signup/<sha256(JWT_SECRET + ":admin-signup-route-v1")>`
- If `ADMIN_SIGNUP_KEY` is empty, signup endpoint is disabled.

### Request

```bash
curl -X POST "http://localhost:5001$ADMIN_SIGNUP_ROUTE" \
  -H "Content-Type: application/json" \
  -H "X-Signup-Key: $ADMIN_SIGNUP_KEY" \
  -d '{
    "username": "ops_admin",
    "password": "StrongAdminPass123"
  }'
```

### Behavior

- `201 Created`: admin user created.
- `409 Conflict`: username already exists.
- `400 Bad Request`: invalid username/password format.
- `404 Not Found`: wrong/missing signup key or endpoint disabled.

### Security Notes

- Username validation allows only letters, numbers, `.`, `_`, `-` (3-64 chars).
- Password policy enforces strong baseline (length + upper/lower/digit).
- Request/response log storage now redacts sensitive fields (`password`, `token`, `secret`, `otp`, etc.).

## Login Hardening (Brute Force Mitigation)

Admin login now supports:

- Failed-attempt lockout (`LOGIN_MAX_ATTEMPTS`, `LOGIN_WINDOW_MINUTES`, `LOGIN_LOCK_MINUTES`)
- Optional Cloudflare Turnstile CAPTCHA after repeated failures

Set these environment variables to enable CAPTCHA:

```env
LOGIN_CAPTCHA_SITE_KEY=<turnstile-site-key>
LOGIN_CAPTCHA_SECRET=<turnstile-secret-key>
LOGIN_CAPTCHA_AFTER_FAILURES=3
LOGIN_CAPTCHA_TIMEOUT_SECONDS=3
```

Behavior:

- CAPTCHA is required only after `LOGIN_CAPTCHA_AFTER_FAILURES` failed attempts for the same `username+IP`.
- If Turnstile variables are missing, login continues using lockout-only behavior.

## Slowloris / Connection DoS Hardening

Server-level protections are enabled with bounded defaults:

- Request read timeout (headers + body)
- Response write timeout
- Keep-alive idle timeout
- Maximum concurrent connections
- Maximum concurrent connections per source IP
- Bounded request read buffer (header size cap)

Tune with environment variables:

```env
SERVER_READ_TIMEOUT_SECONDS=10
SERVER_WRITE_TIMEOUT_SECONDS=30
SERVER_IDLE_TIMEOUT_SECONDS=60
SERVER_CONCURRENCY=4096
SERVER_MAX_CONNS_PER_IP=0
SERVER_READ_BUFFER_BYTES=8192
```

Notes:

- Keep `SERVER_READ_TIMEOUT_SECONDS` low enough to block slow header/body drips.
- `SERVER_MAX_CONNS_PER_IP=0` disables app-level per-IP cap (safer behind single reverse proxy IP).
- If the app is behind a reverse proxy/WAF, enforce per-client connection limits at the edge.

## HTTPS / HSTS Enforcement

Transport security hardening is enabled by default:

- HTTP requests are permanently redirected to HTTPS (`301`).
- HTTPS responses include HSTS:
  - `Strict-Transport-Security: max-age=31536000; includeSubDomains; preload`

Control redirect behavior with:

```env
HTTPS_REDIRECT_ENABLED=true
```

Keep this enabled in production and ensure valid TLS certificates are configured at the edge/load balancer.

## Technology Disclosure Hardening

The security headers middleware removes common stack-disclosure headers from responses:

- `Server`
- `X-Powered-By`
- `X-AspNet-Version`
- `X-AspNetMvc-Version`

Note: if a reverse proxy/load balancer adds its own `Server` header (for example Nginx), disable version disclosure at the edge as well (e.g. `server_tokens off;`).

## Frontend Asset Hardening

- The app now serves only explicit frontend asset routes (`/styles.css`, `/app.js`, `/charts.js`, `/modals.js`, `/login.js`) instead of exposing the full `views/` directory.
- This prevents accidental exposure of backup/template files under `views/` (for example `*.bak` files).
- External CDN assets in `login.html` and `dashboard.html` include Subresource Integrity (SRI) and `crossorigin="anonymous"` attributes.

## Sensitive Data in URLs (Admin Dashboard)

To avoid exposing transaction identifiers in URL query strings, sensitive admin
search flows use POST JSON endpoints:

- `POST /api/admin/transactions/search`
- `POST /api/admin/logs/search`
- `POST /api/admin/international/transactions/search`

These endpoints:

- Keep search terms out of browser/proxy URL logs.
- Require authenticated admin session and CSRF token.
- Return `Cache-Control: no-store`.
- Reject `search` in legacy GET query endpoints to enforce POST-based search.

## Content Security Policy (CSP)

Security headers middleware now supports CSP enforcement and report-only rollout.

Default CSP policy (applied when `CSP_POLICY` is empty):

```text
default-src 'self';
base-uri 'self';
form-action 'self';
frame-ancestors 'self';
object-src 'none';
script-src 'self' https://cdn.jsdelivr.net https://challenges.cloudflare.com;
style-src 'self' https://cdn.jsdelivr.net https://fonts.googleapis.com;
img-src 'self' data:;
font-src 'self' https://cdn.jsdelivr.net https://fonts.gstatic.com data:;
connect-src 'self' https://challenges.cloudflare.com;
frame-src 'self' https://challenges.cloudflare.com;
```

Environment variables:

```env
CSP_ENABLED=true
CSP_REPORT_ONLY=false
# Optional override of the full policy string
CSP_POLICY=
# Optional report endpoint; appended as report-uri when missing in policy
CSP_REPORT_URI=
```

## Multi-Merchant Gateway Routing

`X-API-Key` authenticates the external platform (`external_apps`), not an
individual hotelier or payment recipient. Every externally initiated payment
must include a body-level `merchant_reference`. The service resolves it only
through an active administrator-managed mapping scoped to the authenticated
platform:

```text
X-API-Key -> external app -> merchant_reference -> recipient -> active credential configuration
```

`merchant_reference` is never an internal recipient ID or credential ID. A
reference registered for one external app cannot be used with another app's API
key. Unknown, inactive, or unconfigured references return:

```json
{
  "success": false,
  "code": "MERCHANT_NOT_CONFIGURED",
  "message": "merchant_reference is missing or not configured for this integration"
}
```

Use `merchant_reference` on `POST /api/business/pull/initiate`,
`POST /api/business/intra/inquiry`, and `POST /api/v1/payments`:

```json
{
  "merchant_reference": "hotel-aurora",
  "reference_id": "booking-1042",
  "amount": 125.00,
  "description": "Reservation payment",
  "success_url": "https://platform.example/payments/success",
  "cancel_url": "https://platform.example/payments/cancel"
}
```

OTP confirmation, status checks, and international reconciliation retrieve the
original transaction under the authenticated app and reuse its recorded gateway
credential configuration. Credential rotation therefore affects only newly
initiated payments.

### Credential Administration

Set this required process secret before starting the service:

```env
# base64 encoding of exactly 32 random bytes; keep outside PostgreSQL
PAYMENT_CREDENTIALS_MASTER_KEY_B64=
```

Administrators configure recipients and mappings through the protected,
CSRF-protected `/api/admin` endpoints:

- `POST|GET /api/admin/recipients`
- `PUT /api/admin/recipients/:id`
- `POST|GET /api/admin/recipient-mappings`
- `PUT /api/admin/recipient-mappings/:id`
- `POST|GET /api/admin/gateway-credentials`
- `POST /api/admin/gateway-credentials/:id/activate`
- `POST /api/admin/gateway-credentials/:id/disable`
- `POST /api/admin/gateway-credentials/:id/rotate`

Gateway configuration bodies are encrypted with AES-256-GCM before storage and
are never returned by the administration API.

A configuration says where one recipient's money is booked, and nothing else.
Each provider knows this service as a single integrator under a single identity,
so authentication is held in the environment and is identical on every payment:
`DKPG_*` for the bank, `STRIPE_BASE_URL` / `STRIPE_API_KEY` /
`STRIPE_AGENCY_NAME` for cards.

- `dkpg` requires `beneficiary_account`, `beneficiary_name` and
  `beneficiary_bank`. The last reads as optional on `pull/initiate`, where it
  only backs up the remitter's bank code — but `intra/inquiry` sends it as
  `bene_bank_code`, the beneficiary's own bank, with nothing behind it.
- `stripe` requires `submerchant_id` and `dk_account`.

A configuration that also carries authentication is **rejected**, not ignored,
and the error names the offending key: a record that appears to hold its own
credentials while not using them is worse than one that never claimed to.
