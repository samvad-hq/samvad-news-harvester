package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Provider represents the configuration for a news provider.
type Provider struct {
	ID             string `json:"id" yaml:"id"`
	Name           string `json:"name" yaml:"name"`
	Type           string `json:"type" yaml:"type"`
	SourceURL      string `json:"source_url" yaml:"source_url"`
	ResponseFormat string `json:"response_format" yaml:"response_format"`
	RequestDelayMs int    `json:"request_delay_ms" yaml:"request_delay_ms"`
	// Proxy optionally routes this provider's requests through the given proxy
	// URL; blank means a direct fetch. Supports ${ENV} expansion so the URL can be
	// supplied via the environment rather than committed (e.g. proxy: ${PROXY_URL}).
	Proxy  string         `json:"proxy" yaml:"proxy"`
	Config map[string]any `json:"config" yaml:"config"`
}

// registryFile models the structure of the providers file.
type registryFile struct {
	Providers []Provider `json:"providers" yaml:"providers"`
}

const defaultRequestDelayMs = 500

// Registry is an in-memory snapshot of provider configs sourced from files.
type Registry struct {
	mu        sync.RWMutex
	providers []Provider
	idx       map[string]Provider
}

// LoadRegistry reads provider definitions from a YAML/JSON file.
func LoadRegistry(path string) (*Registry, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("providers file path is empty")
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open providers file: %w", err)
	}
	defer file.Close()

	raw, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read providers file: %w", err)
	}

	fileReg, err := parseRegistry(raw, filepath.Ext(path))
	if err != nil {
		return nil, err
	}

	if len(fileReg.Providers) == 0 {
		return nil, errors.New("providers file contains no providers entries")
	}

	reg := &Registry{
		providers: make([]Provider, len(fileReg.Providers)),
		idx:       make(map[string]Provider, len(fileReg.Providers)),
	}

	for i := range fileReg.Providers {
		p := sanitizeProvider(fileReg.Providers[i])
		if err := validateProvider(p); err != nil {
			return nil, fmt.Errorf("provider[%d]: %w", i, err)
		}
		if _, exists := reg.idx[p.ID]; exists {
			return nil, fmt.Errorf("duplicate provider id %q", p.ID)
		}
		reg.providers[i] = p
		reg.idx[p.ID] = p
	}

	return reg, nil
}

// All returns a copy of every provider definition.
func (r *Registry) All() []Provider {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Provider, len(r.providers))
	copy(out, r.providers)
	return out
}

// ByID finds a provider by id.
func (r *Registry) ByID(id string) (Provider, bool) {
	if r == nil {
		return Provider{}, false
	}

	id = strings.TrimSpace(id)
	if id == "" {
		return Provider{}, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.idx[id]
	return p, ok
}

// parseRegistry attempts to decode provider definitions from data.
func parseRegistry(data []byte, ext string) (registryFile, error) {
	ext = strings.ToLower(strings.TrimSpace(ext))

	decoders := []struct {
		name string
		ext  string
		fn   unmarshalFn
	}{
		{name: "yaml", ext: ".yaml", fn: yaml.Unmarshal},
		{name: "yaml", ext: ".yml", fn: yaml.Unmarshal},
		{name: "json", ext: ".json", fn: json.Unmarshal},
	}

	for _, d := range decoders {
		if ext != "" && ext != d.ext {
			continue
		}
		if reg, err := unmarshalRegistry(d.name, data, d.fn); err == nil {
			return reg, nil
		}
	}

	return registryFile{}, errors.New("providers file format not recognized (expected YAML or JSON)")
}

type unmarshalFn func([]byte, any) error

// unmarshalRegistry decodes provider definitions using the given unmarshal function.
func unmarshalRegistry(name string, data []byte, fn unmarshalFn) (registryFile, error) {
	var reg registryFile
	if err := fn(data, &reg); err != nil {
		return registryFile{}, fmt.Errorf("decode %s providers: %w", name, err)
	}
	return reg, nil
}

// sanitizeProvider cleans up and normalizes provider fields.
func sanitizeProvider(p Provider) Provider {
	p.ID = strings.TrimSpace(p.ID)
	p.Name = strings.TrimSpace(p.Name)
	p.Type = strings.ToLower(strings.TrimSpace(p.Type))
	p.SourceURL = strings.TrimSpace(p.SourceURL)
	p.ResponseFormat = strings.TrimSpace(p.ResponseFormat)
	// Expand ${ENV} so a proxy URL can come from the environment; an unset var
	// collapses to "" → direct fetch.
	p.Proxy = strings.TrimSpace(os.ExpandEnv(p.Proxy))

	if p.Config == nil {
		p.Config = map[string]any{}
	}
	if p.RequestDelayMs <= 0 {
		p.RequestDelayMs = defaultRequestDelayMs
	}

	return p
}

// validateProvider checks that required provider fields are present.
func validateProvider(p Provider) error {
	if p.ID == "" {
		return errors.New("id is required")
	}
	if p.Name == "" {
		return fmt.Errorf("name is required for provider %q", p.ID)
	}
	if p.Type == "" {
		return fmt.Errorf("type is required for provider %q", p.ID)
	}
	if p.SourceURL == "" {
		return fmt.Errorf("source_url is required for provider %q", p.ID)
	}
	if p.ResponseFormat == "" {
		return fmt.Errorf("response_format is required for provider %q", p.ID)
	}
	return nil
}

// RequestDelay returns the per-request throttle duration for the provider.
func (p Provider) RequestDelay() time.Duration {
	if p.RequestDelayMs <= 0 {
		return time.Duration(defaultRequestDelayMs) * time.Millisecond
	}
	return time.Duration(p.RequestDelayMs) * time.Millisecond
}
