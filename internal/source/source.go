// Package source describes where the harvester looks for news and knows
// how to read those documents. A source is one publisher endpoint — today
// always a news sitemap — plus the request headers and optional proxy it
// needs.
package source

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/samvad-hq/samvad-news-harvester/internal/config"
)

// TypeNewsSitemap is the only source type. The XML namespace is Google's,
// but the document is a news sitemap in both the specification and every
// publisher's own naming.
const TypeNewsSitemap = "news_sitemap"

// Headers are the request headers sent to a source. They are typed rather
// than a map so a misspelled key fails at load time instead of silently
// sending no header at all.
type Headers struct {
	// UserAgent is required. Publishers block unidentified crawlers, and
	// several of the configured sources return 403 without one.
	UserAgent      string `yaml:"user_agent" json:"user_agent"`
	Accept         string `yaml:"accept" json:"accept"`
	AcceptLanguage string `yaml:"accept_language" json:"accept_language"`
	CacheControl   string `yaml:"cache_control" json:"cache_control"`
}

// Map renders the headers for an HTTP request, omitting empty values.
func (h Headers) Map() map[string]string {
	out := make(map[string]string, 4)
	for name, value := range map[string]string{
		"User-Agent":      h.UserAgent,
		"Accept":          h.Accept,
		"Accept-Language": h.AcceptLanguage,
		"Cache-Control":   h.CacheControl,
	} {
		if v := strings.TrimSpace(value); v != "" {
			out[name] = v
		}
	}
	return out
}

// Config is one source as declared in the sources file.
type Config struct {
	// ID is the stable short name used in logs and on every event.
	ID string `yaml:"id" json:"id"`
	// Name is the publisher's display name.
	Name string `yaml:"name" json:"name"`
	// Type selects the fetcher. Only TypeNewsSitemap is supported.
	Type string `yaml:"type" json:"type"`
	// URL is the sitemap endpoint.
	URL string `yaml:"url" json:"url"`
	// Proxy optionally routes this source's requests through a proxy.
	// ${VAR} is expanded at load time, so the URL need not be committed.
	// Two of the configured publishers answer 403 without one.
	Proxy string `yaml:"proxy" json:"proxy"`
	// Headers are sent with every request to this source.
	Headers Headers `yaml:"headers" json:"headers"`
}

// Ident returns the source ID, satisfying config.Item.
func (c Config) Ident() string { return c.ID }

// Validate reports every problem with the entry at once.
func (c Config) Validate() error {
	var errs []error

	if strings.TrimSpace(c.ID) == "" {
		errs = append(errs, errors.New("id is required"))
	}
	if strings.TrimSpace(c.Name) == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if c.Type != TypeNewsSitemap {
		errs = append(errs, fmt.Errorf("unsupported type %q (only %q is supported)", c.Type, TypeNewsSitemap))
	}
	if err := validateHTTPURL("url", c.URL); err != nil {
		errs = append(errs, err)
	}
	if strings.TrimSpace(c.Headers.UserAgent) == "" {
		errs = append(errs, errors.New("headers.user_agent is required"))
	}
	if p := strings.TrimSpace(c.Proxy); p != "" {
		if err := validateHTTPURL("proxy", p); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// validateHTTPURL checks that raw is an absolute http or https URL.
func validateHTTPURL(field, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("%s is required", field)
	}
	u, err := url.Parse(raw)
	if err != nil {
		// raw is deliberately not included: it cannot be reproduced safely
		// once it has failed to parse into a structure we could strip
		// credentials from, and this field also validates proxy, where a
		// basic-auth password is a normal thing for the raw string to carry.
		return fmt.Errorf("%s is not a url: %w", field, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s %q must be http or https", field, stripUserinfo(u))
	}
	if u.Host == "" {
		return fmt.Errorf("%s %q must include a host", field, stripUserinfo(u))
	}
	return nil
}

// stripUserinfo returns u without any embedded credentials, for safe use
// in a validation error. This field validates both url and proxy, and a
// proxy URL is a normal place to carry basic-auth credentials — url.Parse
// has already given a structured URL by the point this is called, so
// there is no need to fall back to the raw string that might contain them.
func stripUserinfo(u *url.URL) string {
	stripped := *u
	stripped.User = nil
	return stripped.String()
}

// LoadFile reads and validates the sources file. YAML and JSON are both
// accepted, chosen by file extension.
func LoadFile(path string) ([]Config, error) {
	var file struct {
		Sources []Config `yaml:"sources" json:"sources"`
	}
	if err := config.DecodeFile(path, &file); err != nil {
		return nil, err
	}

	for i := range file.Sources {
		file.Sources[i] = sanitize(file.Sources[i])
	}
	if err := config.ValidateList(file.Sources); err != nil {
		return nil, fmt.Errorf("sources %s: %w", path, err)
	}
	return file.Sources, nil
}

// sanitize trims whitespace and normalises the type before validation, so
// a stray trailing space in YAML is not a configuration error.
func sanitize(c Config) Config {
	c.ID = strings.TrimSpace(c.ID)
	c.Name = strings.TrimSpace(c.Name)
	c.Type = strings.ToLower(strings.TrimSpace(c.Type))
	c.URL = strings.TrimSpace(c.URL)
	c.Proxy = strings.TrimSpace(c.Proxy)
	return c
}
