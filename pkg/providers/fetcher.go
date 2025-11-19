package providers

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Adda-Baaj/taja-khobor/pkg/httpclient"
)

type fetcherRegistry struct {
	fetchers map[string]Fetcher
	mu       sync.RWMutex
}

// NewFetcherRegistry builds a registry for the provided fetcher implementations.
func NewFetcherRegistry(fetchers ...Fetcher) FetcherRegistry {
	reg := &fetcherRegistry{
		fetchers: make(map[string]Fetcher, len(fetchers)),
	}

	for _, f := range fetchers {
		if f == nil {
			continue
		}
		reg.fetchers[strings.ToLower(strings.TrimSpace(f.ID()))] = f
	}

	return reg
}

// FetcherFor selects the fetcher for the given provider based on its id.
func (r *fetcherRegistry) FetcherFor(cfg Provider) (Fetcher, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("provider id is empty")
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	key := strings.ToLower(cfg.ID)
	if f, ok := r.fetchers[key]; ok {
		return f, nil
	}

	return nil, fmt.Errorf("no fetcher registered for provider %q", cfg.ID)
}

// DefaultHTTPClient returns a tuned http.Client for provider fetchers.
func DefaultHTTPClient() HTTPClient { return httpclient.NewRestyClient(15 * time.Second) }

// DefaultFetcherRegistry wires up the known provider fetchers.
func DefaultFetcherRegistry(client HTTPClient) FetcherRegistry {
	if client == nil {
		client = DefaultHTTPClient()
	}

	return NewFetcherRegistry(
		NewNDTVFetcher(client),
		NewTOIFetcher(client),
		NewTheHinduFetcher(client),
		NewFinancialExpressFetcher(client),
		NewAnandabazarPatrikaFetcher(client),
		NewEisamayFetcher(client),
		NewAajtakFetcher(client),
		NewJagranFetcher(client),
		NewDinamalarFetcher(client),
		NewDailyThanthiFetcher(client),
	)
}
