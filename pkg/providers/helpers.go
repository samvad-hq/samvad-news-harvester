package providers

import (
	"context"
	"crypto/sha1" //nolint:gosec // non-cryptographic id generation
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Adda-Baaj/taja-khobor/internal/domain"
	"github.com/Adda-Baaj/taja-khobor/pkg/httpclient"
)

func hashURL(u string) string {
	sum := sha1.Sum([]byte(u))
	return hex.EncodeToString(sum[:])
}

func responseSnippet(body []byte) string {
	const maxLen = 512
	s := strings.TrimSpace(string(body))
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	if s == "" {
		return "<empty>"
	}
	return s
}

type googleNewsSitemap struct {
	URLs []googleNewsURL `xml:"url"`
}

type googleNewsURL struct {
	Loc    string            `xml:"loc"`
	News   googleNewsDetail  `xml:"news"`
	Images []googleNewsImage `xml:"image:image"`
}

type googleNewsDetail struct {
	PublicationDate string `xml:"publication_date"`
	Keywords        string `xml:"keywords"`
	Title           string `xml:"title"`
}

type googleNewsImage struct {
	Loc   string `xml:"image:loc"`
	Title string `xml:"image:title"`
}

func parseGoogleNewsSitemap(data []byte) ([]googleNewsURL, error) {
	var sitemap googleNewsSitemap
	if err := xml.Unmarshal(data, &sitemap); err != nil {
		return nil, err
	}
	return sitemap.URLs, nil
}

func buildArticlesFromSitemap(urls []googleNewsURL) []domain.Article {
	articles := make([]domain.Article, 0, len(urls))
	for _, entry := range urls {
		loc := strings.TrimSpace(entry.Loc)
		if loc == "" {
			continue
		}

		keywords := parseKeywords(entry.News.Keywords)
		publishedAt := parsePublicationDate(entry.News.PublicationDate)
		title := strings.TrimSpace(entry.News.Title)
		imageURL := firstImageURL(entry.Images)

		articles = append(articles, domain.Article{
			ID:          hashURL(loc),
			Title:       title,
			URL:         loc,
			ImageURL:    imageURL,
			Keywords:    keywords,
			PublishedAt: publishedAt,
		})
	}
	return articles
}

func firstImageURL(images []googleNewsImage) string {
	for _, img := range images {
		if loc := strings.TrimSpace(img.Loc); loc != "" {
			return loc
		}
	}
	return ""
}

func parseKeywords(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	keywords := make([]string, 0, len(parts))
	for _, part := range parts {
		if kw := strings.TrimSpace(part); kw != "" {
			keywords = append(keywords, kw)
		}
	}

	if len(keywords) == 0 {
		return nil
	}
	return keywords
}

func parsePublicationDate(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}

	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}

	return time.Time{}
}

func fetchSitemap(ctx context.Context, client httpclient.Client, url, providerID string, headers map[string]string) ([]byte, error) {
	resp, err := client.Get(ctx, url, headers)
	if err != nil {
		return nil, fmt.Errorf("fetch %s sitemap: %w", providerID, err)
	}

	body := resp.Body()
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("%s sitemap returned status %d body: %s", providerID, resp.StatusCode(), responseSnippet(body))
	}

	return body, nil
}
