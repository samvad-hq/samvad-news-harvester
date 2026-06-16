package providers

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/pkg/httpclient"
)

// fetcherRegistry implements FetcherRegistry.
type fetcherRegistry struct {
	fetchersByID   map[string]Fetcher
	fetchersByType map[string]Fetcher
	mu             sync.RWMutex
}

// NewFetcherRegistry builds a registry for the provided fetcher implementations keyed by provider id.
func NewFetcherRegistry(fetchers ...Fetcher) FetcherRegistry {
	return NewTypeFetcherRegistry(nil, fetchers...)
}

// NewTypeFetcherRegistry builds a registry with optional type-based fetchers and provider-specific fetchers.
func NewTypeFetcherRegistry(typeFetchers map[string]Fetcher, fetchers ...Fetcher) FetcherRegistry {
	reg := &fetcherRegistry{
		fetchersByID:   make(map[string]Fetcher),
		fetchersByType: make(map[string]Fetcher),
	}

	for _, f := range fetchers {
		reg.registerIDFetcher(f)
	}
	for typ, f := range typeFetchers {
		reg.registerTypeFetcher(typ, f)
	}

	return reg
}

// registerIDFetcher registers a fetcher by its provider ID.
func (r *fetcherRegistry) registerIDFetcher(f Fetcher) {
	if f == nil {
		return
	}
	key := strings.ToLower(strings.TrimSpace(f.ID()))
	if key == "" {
		return
	}

	r.mu.Lock()
	r.fetchersByID[key] = f
	r.mu.Unlock()
}

// registerTypeFetcher registers a fetcher by provider type.
func (r *fetcherRegistry) registerTypeFetcher(typ string, f Fetcher) {
	if f == nil {
		return
	}
	key := strings.ToLower(strings.TrimSpace(typ))
	if key == "" {
		return
	}

	r.mu.Lock()
	r.fetchersByType[key] = f
	r.mu.Unlock()
}

// FetcherFor selects the fetcher for the given provider based on its id or type.
func (r *fetcherRegistry) FetcherFor(cfg Provider) (Fetcher, error) {
	if r == nil {
		return nil, fmt.Errorf("fetcher registry is nil")
	}
	if strings.TrimSpace(cfg.ID) == "" {
		return nil, fmt.Errorf("provider id is empty")
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	idKey := strings.ToLower(strings.TrimSpace(cfg.ID))
	if f, ok := r.fetchersByID[idKey]; ok {
		return f, nil
	}

	typeKey := strings.ToLower(strings.TrimSpace(cfg.Type))
	if typeKey != "" {
		if f, ok := r.fetchersByType[typeKey]; ok {
			return f, nil
		}
	}

	return nil, fmt.Errorf("no fetcher registered for provider %q (type %q)", cfg.ID, cfg.Type)
}

// defaultFetchTimeout bounds every provider fetch (direct or proxied).
const defaultFetchTimeout = 15 * time.Second

// DefaultHTTPClient returns a tuned http.Client for provider fetchers.
func DefaultHTTPClient() HTTPClient { return httpclient.NewRestyClient(defaultFetchTimeout) }

const ProviderTypeGoogleNews = "google_news_sitemap"

// DefaultFetcherRegistry wires up known provider fetchers.
func DefaultFetcherRegistry(client HTTPClient) FetcherRegistry {
	if client == nil {
		client = DefaultHTTPClient()
	}

	typeFetchers := map[string]Fetcher{
		ProviderTypeGoogleNews: NewGoogleNewsFetcher(client),
	}

	return NewTypeFetcherRegistry(typeFetchers)
}
