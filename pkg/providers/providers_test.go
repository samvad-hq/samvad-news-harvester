package providers

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/domain"
)

func writeTempFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func TestLoadRegistryValidatesAndSanitizes(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "providers.yaml", `
providers:
  - id: foo
    name: Foo
    type: google_news_sitemap
    source_url: https://example.com
    response_format: xml
    request_delay_ms: 0
`)

	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	all := reg.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(all))
	}
	got := all[0]
	if got.Type != ProviderTypeGoogleNews {
		t.Errorf("Type = %s want %s", got.Type, ProviderTypeGoogleNews)
	}
	if got.RequestDelay() != time.Duration(defaultRequestDelayMs)*time.Millisecond {
		t.Errorf("RequestDelay defaulted incorrectly: %v", got.RequestDelay())
	}
	if headers := Headers(got); headers == nil {
		t.Errorf("Headers should return an initialized map, got nil")
	}
}

func TestLoadRegistryRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "providers.yaml", `
providers:
  - id: ""
    name: Foo
    type: google_news_sitemap
    source_url: https://example.com
    response_format: xml
`)

	if _, err := LoadRegistry(path); err == nil {
		t.Fatalf("expected validation error for blank id")
	}
}

func TestFetcherRegistryResolution(t *testing.T) {
	reg := NewTypeFetcherRegistry(map[string]Fetcher{
		"custom": &stubFetcher{id: "custom"},
	})

	cfg := Provider{ID: "p1", Type: "custom"}
	f, err := reg.FetcherFor(cfg)
	if err != nil {
		t.Fatalf("FetcherFor: %v", err)
	}
	if f.ID() != "custom" {
		t.Fatalf("expected fetcher 'custom', got %q", f.ID())
	}

	_, err = reg.FetcherFor(Provider{})
	if err == nil {
		t.Fatalf("expected error on empty provider id")
	}
}

func TestLoadRegistryExpandsProxyEnv(t *testing.T) {
	t.Setenv("TEST_PROXY_URL", "http://10.0.0.1:8080")
	dir := t.TempDir()
	path := writeTempFile(t, dir, "providers.yaml", `
providers:
  - id: blocked
    name: Blocked
    type: google_news_sitemap
    source_url: https://example.com
    response_format: xml
    proxy: ${TEST_PROXY_URL}
  - id: open
    name: Open
    type: google_news_sitemap
    source_url: https://example.com
    response_format: xml
`)

	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	blocked, _ := reg.ByID("blocked")
	if blocked.Proxy != "http://10.0.0.1:8080" {
		t.Errorf("proxy env not expanded: %q", blocked.Proxy)
	}
	open, _ := reg.ByID("open")
	if open.Proxy != "" {
		t.Errorf("unflagged provider should have no proxy, got %q", open.Proxy)
	}
}

type stubFetcher struct {
	id       string
	articles []string
	err      error
}

func (s *stubFetcher) ID() string { return s.id }

func (s *stubFetcher) Fetch(_ context.Context, _ Provider) ([]domain.Article, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []domain.Article
	for _, id := range s.articles {
		out = append(out, domain.Article{ID: id})
	}
	return out, nil
}
