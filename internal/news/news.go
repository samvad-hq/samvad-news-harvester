package news

import "time"

// Article is one story discovered at a source. URL is always canonical;
// OriginalURL records the form the sitemap used when the two differ, so a
// consumer can trace an event back to the publisher's own link.
type Article struct {
	SourceID    string    `json:"source_id"`
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	OriginalURL string    `json:"original_url,omitempty"`
	Description string    `json:"description,omitempty"`
	ImageURL    string    `json:"image_url,omitempty"`
	Keywords    []string  `json:"keywords,omitempty"`
	PublishedAt time.Time `json:"published_at"`
}

// Event is the payload delivered to every sink.
//
// Delivery is at-least-once: an article is recorded as seen only once every
// sink has accepted it, so a partial failure replays the event to the sinks
// that already succeeded. Consumers must key on Article.ID.
type Event struct {
	SourceID    string    `json:"source_id"`
	SourceName  string    `json:"source_name"`
	Article     Article   `json:"article"`
	CollectedAt time.Time `json:"collected_at"`
}

// NewEvent wraps an article for delivery, stamping the collection time in UTC.
func NewEvent(sourceID, sourceName string, a Article) Event {
	return Event{
		SourceID:    sourceID,
		SourceName:  sourceName,
		Article:     a,
		CollectedAt: time.Now().UTC(),
	}
}
