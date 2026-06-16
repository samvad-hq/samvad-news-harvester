package domain

import "time"

// Domain contains core models and interfaces.

type Article struct {
	ProviderID  string    `json:"provider_id"`
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	Description string    `json:"description"`
	ImageURL    string    `json:"image_url"`
	Keywords    []string  `json:"keywords"`
	PublishedAt time.Time `json:"published_at"`
}
