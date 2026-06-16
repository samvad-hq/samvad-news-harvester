package publishers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRegistryEnabledFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "publishers.yaml")
	raw := `
publishers:
  - id: http1
    type: http
    enabled: false
    http:
      url: https://example.com
  - id: http2
    type: http
    enabled: true
    http:
      url: https://example.com/2
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	enabled := reg.Enabled()
	if len(enabled) != 1 || enabled[0].ID != "http2" {
		t.Fatalf("expected only http2 enabled, got %#v", enabled)
	}
}

func TestValidatePublisherConfigRejectsMissingHTTP(t *testing.T) {
	err := validatePublisherConfig(PublisherConfig{
		ID:   "h1",
		Type: TypeHTTP,
	})
	if err == nil {
		t.Fatalf("expected validation error for missing http block")
	}
}
