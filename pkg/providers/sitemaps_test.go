package providers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/pkg/httpclient"
)

// fakeResponse lets us stub the httpclient.Client interface.
type fakeResponse struct {
	body       []byte
	statusCode int
}

func (f fakeResponse) Body() []byte    { return f.body }
func (f fakeResponse) StatusCode() int { return f.statusCode }

// fakeHTTPClient returns canned responses per URL to avoid network calls.
type fakeHTTPClient struct {
	responses map[string]fakeResponse
	calls     []string
}

func (f *fakeHTTPClient) Get(_ context.Context, url string, _ map[string]string) (httpclient.Response, error) {
	f.calls = append(f.calls, url)
	resp, ok := f.responses[url]
	if !ok {
		return nil, errors.New("not found")
	}
	return resp, nil
}

func TestParseGoogleNewsSitemap(t *testing.T) {
	xml := []byte(`
<urlset xmlns:news="http://www.google.com/schemas/sitemap-news/0.9" xmlns:image="http://www.google.com/schemas/sitemap-image/1.1">
  <url>
    <loc>https://example.com/a</loc>
    <news:news>
      <news:publication_date>2024-01-01T00:00:00Z</news:publication_date>
      <news:keywords>foo, bar</news:keywords>
      <news:title>Hello</news:title>
    </news:news>
    <image:image>
      <image:loc>https://example.com/a.jpg</image:loc>
    </image:image>
  </url>
  <url>
    <loc>   </loc>
  </url>
</urlset>`)

	entries, err := parseGoogleNewsSitemap(xml)
	if err != nil {
		t.Fatalf("parseGoogleNewsSitemap: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 url entries, got %d", len(entries))
	}

	articles := buildArticlesFromSitemap("provider-x", entries)
	if len(articles) != 1 {
		t.Fatalf("expected 1 article after filtering empty loc, got %d", len(articles))
	}

	art := articles[0]
	if art.ProviderID != "provider-x" {
		t.Errorf("ProviderID = %s want provider-x", art.ProviderID)
	}
	if art.Title != "Hello" {
		t.Errorf("Title = %s want Hello", art.Title)
	}
	if got := art.PublishedAt.UTC(); !got.Equal(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("PublishedAt = %v", got)
	}
	if len(art.Keywords) != 2 || art.Keywords[0] != "foo" || art.Keywords[1] != "bar" {
		t.Errorf("Keywords = %#v", art.Keywords)
	}
	if art.ImageURL != "https://example.com/a.jpg" {
		t.Errorf("ImageURL = %s", art.ImageURL)
	}
	if art.ID == "" {
		t.Errorf("expected hashed ID to be set")
	}
}

// Some publisher sitemaps (e.g. The Hindu) contain malformed character
// entities such as a bare "&K;". Strict XML decoding fails the whole document;
// the lenient decoder must parse the well-formed entries and leave the bad
// entity as literal text.
func TestParseGoogleNewsSitemap_MalformedEntity(t *testing.T) {
	xml := []byte(`
<urlset xmlns:news="http://www.google.com/schemas/sitemap-news/0.9" xmlns:image="http://www.google.com/schemas/sitemap-image/1.1">
  <url>
    <loc>https://example.com/a</loc>
    <news:news>
      <news:publication_date>2024-01-01T00:00:00Z</news:publication_date>
      <news:title>Bad &K; entity here</news:title>
    </news:news>
  </url>
</urlset>`)

	entries, err := parseGoogleNewsSitemap(xml)
	if err != nil {
		t.Fatalf("parseGoogleNewsSitemap should tolerate malformed entities: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 url entry, got %d", len(entries))
	}
	if !strings.Contains(entries[0].News.Title, "&K;") {
		t.Errorf("Title = %q, want the literal &K; preserved", entries[0].News.Title)
	}
}

func TestParseSitemapIndex(t *testing.T) {
	data := []byte(`
<sitemapindex>
  <sitemap><loc>https://example.com/s1.xml</loc></sitemap>
  <sitemap><loc> </loc></sitemap>
  <sitemap><loc>https://example.com/s2.xml</loc></sitemap>
</sitemapindex>`)

	urls, err := parseSitemapIndex(data)
	if err != nil {
		t.Fatalf("parseSitemapIndex: %v", err)
	}
	if len(urls) != 2 {
		t.Fatalf("expected 2 urls, got %d", len(urls))
	}
}

func TestFetchGoogleNewsURLsFollowsIndexes(t *testing.T) {
	indexXML := []byte(`
<sitemapindex>
  <sitemap><loc>https://example.com/leaf.xml</loc></sitemap>
</sitemapindex>`)
	leafXML := []byte(`
<urlset>
  <url>
    <loc>https://example.com/article</loc>
    <news>
      <publication_date>2024-01-01T00:00:00Z</publication_date>
      <keywords>foo</keywords>
      <title>Hello</title>
    </news>
  </url>
</urlset>`)

	client := &fakeHTTPClient{
		responses: map[string]fakeResponse{
			"https://example.com/root.xml": {body: indexXML, statusCode: http.StatusOK},
			"https://example.com/leaf.xml": {body: leafXML, statusCode: http.StatusOK},
		},
	}

	fetcher := &googleNewsFetcher{client: client}
	cfg := Provider{
		ID:        "p1",
		Type:      ProviderTypeGoogleNews,
		SourceURL: "https://example.com/root.xml",
	}

	urls, err := fetcher.fetchGoogleNewsURLs(context.Background(), cfg, cfg.SourceURL, nil, nil)
	if err != nil {
		t.Fatalf("fetchGoogleNewsURLs: %v", err)
	}
	if len(urls) != 1 {
		t.Fatalf("expected 1 url, got %d", len(urls))
	}
	if urls[0].Loc != "https://example.com/article" {
		t.Fatalf("unexpected loc %q", urls[0].Loc)
	}
	if len(client.calls) != 2 {
		t.Fatalf("expected 2 HTTP calls (index + leaf), got %d", len(client.calls))
	}
}

func TestFetchSitemapHandlesNon200(t *testing.T) {
	client := &fakeHTTPClient{
		responses: map[string]fakeResponse{
			"https://example.com/root.xml": {body: []byte("oops"), statusCode: http.StatusBadRequest},
		},
	}

	_, err := fetchSitemap(context.Background(), client, "https://example.com/root.xml", "p1", nil)
	if err == nil || !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("expected status error, got %v", err)
	}
}

func TestParseHelpers(t *testing.T) {
	kw := parseKeywords(" a, b , ,c ")
	if len(kw) != 3 || kw[0] != "a" || kw[2] != "c" {
		t.Errorf("parseKeywords = %#v", kw)
	}

	if kw := parseKeywords("   "); kw != nil {
		t.Errorf("expected nil keywords on blank input")
	}

	tm := parsePublicationDate("not-a-date")
	if !tm.IsZero() {
		t.Errorf("expected zero time on invalid input, got %v", tm)
	}

	resp := responseSnippet(make([]byte, 600))
	if len(resp) == 0 {
		t.Errorf("expected truncated response snippet")
	}
}
