package publishers

import (
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/domain"
)

// Event represents the payload published downstream.
type Event struct {
	ProviderID   string         `json:"provider_id"`
	ProviderName string         `json:"provider_name"`
	Article      domain.Article `json:"article"`
	CollectedAt  time.Time      `json:"collected_at"`
}

// NewEvent constructs an Event for the given provider + article.
func NewEvent(providerID, providerName string, article domain.Article) Event {
	return Event{
		ProviderID:   providerID,
		ProviderName: providerName,
		Article:      article,
		CollectedAt:  time.Now().UTC(),
	}
}
