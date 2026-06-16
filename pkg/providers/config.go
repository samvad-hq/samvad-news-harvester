package providers

import "strings"

// ConfigString returns the trimmed string value for key from provider.Config or a fallback.
func ConfigString(cfg Provider, key, fallback string) string {
	if cfg.Config != nil {
		if raw, ok := cfg.Config[key]; ok {
			if val, ok := raw.(string); ok {
				if trimmed := strings.TrimSpace(val); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return fallback
}

const (
	ConfigUserAgentKey      = "user_agent"
	ConfigAcceptKey         = "accept"
	ConfigAcceptLanguageKey = "accept_language"
	ConfigCacheControlKey   = "cache_control"
)

// Headers builds the common request headers from a provider config (skips empty values).
func Headers(cfg Provider) map[string]string {
	headers := make(map[string]string, 4)

	if v := ConfigString(cfg, ConfigUserAgentKey, ""); v != "" {
		headers["User-Agent"] = v
	}
	if v := ConfigString(cfg, ConfigAcceptKey, ""); v != "" {
		headers["Accept"] = v
	}
	if v := ConfigString(cfg, ConfigAcceptLanguageKey, ""); v != "" {
		headers["Accept-Language"] = v
	}
	if v := ConfigString(cfg, ConfigCacheControlKey, ""); v != "" {
		headers["Cache-Control"] = v
	}

	return headers
}
