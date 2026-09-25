package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v2"
)

// defaultCSPPolicy is the policy for the bundled login and dashboard pages.
//
// cdn.jsdelivr.net was removed from script-src, style-src and font-src once
// Bootstrap, Bootstrap Icons and Chart.js were vendored under /vendor and
// served from this origin (Burp informational, 23 September 2026). Nothing on
// these pages loads from jsdelivr any more, and naming a CDN here would let a
// compromise of it run script inside a payment gateway's console.
//
// challenges.cloudflare.com stays: Turnstile on the login page is loaded,
// connected to and framed from there. The Google Fonts entries stay too.
const defaultCSPPolicy = "default-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'self'; object-src 'none'; script-src 'self' https://challenges.cloudflare.com; style-src 'self' https://fonts.googleapis.com; img-src 'self' data:; font-src 'self' https://fonts.gstatic.com data:; connect-src 'self' https://challenges.cloudflare.com; frame-src 'self' https://challenges.cloudflare.com"
const defaultHSTSValue = "max-age=31536000; includeSubDomains; preload"

var techDisclosureHeaders = []string{
	"Server",
	"X-Powered-By",
	"X-AspNet-Version",
	"X-AspNetMvc-Version",
}

type SecurityHeadersOptions struct {
	Enabled       bool
	CSPEnabled    bool
	CSPPolicy     string
	CSPReportOnly bool
	CSPReportURI  string
}

func normalizeCSP(opts SecurityHeadersOptions) (string, bool) {
	if !opts.Enabled || !opts.CSPEnabled {
		return "", false
	}
	policy := strings.TrimSpace(opts.CSPPolicy)
	if policy == "" {
		policy = defaultCSPPolicy
	}

	reportURI := strings.TrimSpace(opts.CSPReportURI)
	if reportURI != "" {
		lower := strings.ToLower(policy)
		if !strings.Contains(lower, "report-uri") && !strings.Contains(lower, "report-to") {
			if !strings.HasSuffix(policy, ";") {
				policy += ";"
			}
			policy += " report-uri " + reportURI
		}
	}

	return policy, true
}

// SecurityHeaders adds secure baseline HTTP response headers.
//
// Headers applied unconditionally:
//   - X-Content-Type-Options: prevents MIME-type sniffing.
//   - X-Frame-Options: blocks clickjacking via iframes.
//   - Referrer-Policy: limits referrer leakage on cross-origin navigation.
//   - Permissions-Policy: disables unused browser features.
//
// Header applied only on HTTPS responses:
//   - Strict-Transport-Security (HSTS): instructs browsers to always use HTTPS
//     for this origin for the next year (including subdomains) and signals
//     preload intent. Set only on HTTPS to avoid breaking plain-HTTP local
//     development environments, and because browsers ignore HSTS headers
//     sent over HTTP anyway.
func SecurityHeaders(opts SecurityHeadersOptions) fiber.Handler {
	cspPolicy, setCSP := normalizeCSP(opts)

	return func(c *fiber.Ctx) error {
		if !opts.Enabled {
			return c.Next()
		}

		err := c.Next()

		// Remove common technology disclosure headers to reduce stack fingerprinting.
		for _, h := range techDisclosureHeaders {
			c.Response().Header.Del(h)
		}

		c.Set("X-Content-Type-Options", "nosniff")
		c.Set("X-Frame-Options", "SAMEORIGIN")
		c.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		// A handler that set its own CSP keeps it. The site-wide policy is the
		// right default for a page this service wrote; it is the wrong policy
		// for a file somebody uploaded, which a handler serves under a far
		// stricter one (`default-src 'none'; sandbox`). Overwriting that with
		// the looser site policy would silently widen what such a file may do.
		if setCSP && len(c.Response().Header.Peek("Content-Security-Policy")) == 0 {
			if opts.CSPReportOnly {
				c.Set("Content-Security-Policy-Report-Only", cspPolicy)
			} else {
				c.Set("Content-Security-Policy", cspPolicy)
			}
		}

		// Strict-Transport-Security: only meaningful (and only honoured by
		// browsers) when delivered over a secure connection.
		if strings.EqualFold(c.Protocol(), "https") {
			c.Set("Strict-Transport-Security", defaultHSTSValue)
		}

		return err
	}
}
