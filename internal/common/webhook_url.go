package common

import (
	"net/netip"
	"net/url"
	"strings"
)

type WebhookURLAssessment struct {
	Normalized string
	Issues     []string
}

// AssessWebhookURL validates a webhook URL without changing any DB field contracts.
// Empty values are treated as allowed to preserve current optional behavior.
func AssessWebhookURL(raw string) WebhookURLAssessment {
	trimmed := strings.TrimSpace(raw)
	out := WebhookURLAssessment{Normalized: trimmed}
	if trimmed == "" {
		return out
	}

	u, err := url.Parse(trimmed)
	if err != nil || u == nil {
		out.Issues = append(out.Issues, "parse error")
		return out
	}
	if u.Scheme == "" || !strings.EqualFold(u.Scheme, "https") {
		out.Issues = append(out.Issues, "scheme must be https")
	}
	if u.User != nil {
		out.Issues = append(out.Issues, "credentials in URL are not allowed")
	}
	if strings.TrimSpace(u.Hostname()) == "" {
		out.Issues = append(out.Issues, "host is required")
	}
	if u.Fragment != "" {
		out.Issues = append(out.Issues, "URL fragments are not allowed")
	}

	host := strings.TrimSpace(u.Hostname())
	if strings.EqualFold(host, "localhost") {
		out.Issues = append(out.Issues, "localhost is not allowed")
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsUnspecified() {
			out.Issues = append(out.Issues, "internal IP ranges are not allowed")
		}
	}

	// Normalize stable parts while preserving path/query semantics.
	if u.Scheme != "" {
		u.Scheme = strings.ToLower(u.Scheme)
	}
	if host != "" {
		normalizedHost := strings.ToLower(host)
		if port := strings.TrimSpace(u.Port()); port != "" {
			u.Host = normalizedHost + ":" + port
		} else {
			u.Host = normalizedHost
		}
	}
	out.Normalized = strings.TrimSpace(u.String())
	return out
}
