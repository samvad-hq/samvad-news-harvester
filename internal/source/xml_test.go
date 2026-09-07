package source

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseURLSet(t *testing.T) {
	t.Parallel()

	raw := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"
        xmlns:news="http://www.google.com/schemas/sitemap-news/0.9"
        xmlns:image="http://www.google.com/schemas/sitemap-image/1.1">
  <url>
    <loc>https://example.com/story-one</loc>
    <lastmod>2026-09-06T10:10:57+05:30</lastmod>
    <news:news>
      <news:publication_date>2026-09-06T04:00:00Z</news:publication_date>
      <news:title>Story One</news:title>
      <news:keywords>politics, delhi</news:keywords>
    </news:news>
    <image:image><image:loc>https://example.com/one.jpg</image:loc></image:image>
  </url>
  <url>
    <loc>https://example.com/story-two</loc>
  </url>
</urlset>`)

	entries, err := parseURLSet(raw)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	require.Equal(t, "https://example.com/story-one", entries[0].Loc)
	require.Equal(t, "Story One", entries[0].News.Title)
	require.Equal(t, "politics, delhi", entries[0].News.Keywords)
	require.Equal(t, "https://example.com/one.jpg", entries[0].Images[0].Loc)
	require.Empty(t, entries[1].News.Title)
}

func TestParseSitemapIndexKeepsLastmod(t *testing.T) {
	t.Parallel()

	raw := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap>
    <loc>https://example.com/news-1.xml</loc>
    <lastmod>2026-09-06T10:10:57+05:30</lastmod>
  </sitemap>
  <sitemap>
    <loc>  https://example.com/news-2.xml  </loc>
  </sitemap>
  <sitemap><loc></loc></sitemap>
</sitemapindex>`)

	entries, err := parseSitemapIndex(raw)
	require.NoError(t, err)
	require.Len(t, entries, 2, "blank locs are dropped")
	require.Equal(t, "https://example.com/news-1.xml", entries[0].Loc)
	require.Equal(t, "https://example.com/news-2.xml", entries[1].Loc)
	require.False(t, entries[0].LastMod.IsZero())
	require.True(t, entries[1].LastMod.IsZero())
}

func TestParseToleratesMalformedEntities(t *testing.T) {
	t.Parallel()

	// The Hindu's sitemap has emitted a bare "&K;" entity. A strict decoder
	// fails the whole document and drops the source's entire crawl.
	raw := []byte(`<urlset xmlns:news="http://www.google.com/schemas/sitemap-news/0.9">
  <url>
    <loc>https://example.com/a</loc>
    <news:news><news:title>Wipro &K; Infosys</news:title></news:news>
  </url>
</urlset>`)

	entries, err := parseURLSet(raw)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Contains(t, entries[0].News.Title, "Wipro")
}

func TestParsePublicationDate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected time.Time
		ok       bool
	}{
		{
			name:     "rfc3339",
			input:    "2026-09-06T04:00:00Z",
			expected: time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC),
			ok:       true,
		},
		{
			name:     "rfc3339 with offset is converted to utc",
			input:    "2026-09-06T10:10:57+05:30",
			expected: time.Date(2026, 9, 6, 4, 40, 57, 0, time.UTC),
			ok:       true,
		},
		{
			name:     "date only",
			input:    "2026-09-06",
			expected: time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC),
			ok:       true,
		},
		{
			name:     "rfc1123z",
			input:    "Sun, 06 Sep 2026 04:00:00 +0000",
			expected: time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC),
			ok:       true,
		},
		{name: "empty", input: "", ok: false},
		{name: "nonsense", input: "yesterday", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parsePublicationDate(tt.input)
			require.Equal(t, tt.ok, ok)
			if tt.ok {
				require.True(t, tt.expected.Equal(got), "expected %s, got %s", tt.expected, got)
				require.Equal(t, time.UTC, got.Location())
			}
		})
	}
}

func TestParseKeywords(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{"politics", "delhi"}, parseKeywords(" politics , delhi "))
	require.Nil(t, parseKeywords(""))
	require.Nil(t, parseKeywords(" , , "))
}

func TestRootKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      string
		wantKind documentKind
		wantRoot string
	}{
		{
			name:     "urlset",
			raw:      `<urlset><url><loc>https://example.com/a</loc></url></urlset>`,
			wantKind: kindURLSet,
			wantRoot: "urlset",
		},
		{
			name:     "sitemapindex",
			raw:      `<sitemapindex><sitemap><loc>https://example.com/s.xml</loc></sitemap></sitemapindex>`,
			wantKind: kindIndex,
			wantRoot: "sitemapindex",
		},
		{
			name:     "html error page",
			raw:      `<html><head><title>Access Denied</title></head><body>blocked</body></html>`,
			wantKind: kindUnknown,
			wantRoot: "html",
		},
		{
			name:     "rss feed",
			raw:      `<?xml version="1.0"?><rss version="2.0"><channel><title>Feed</title></channel></rss>`,
			wantKind: kindUnknown,
			wantRoot: "rss",
		},
		{
			name:     "atom feed",
			raw:      `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Feed</title></feed>`,
			wantKind: kindUnknown,
			wantRoot: "feed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			kind, root, err := rootKind([]byte(tt.raw))
			require.NoError(t, err)
			require.Equal(t, tt.wantKind, kind)
			require.Equal(t, tt.wantRoot, root)
		})
	}
}
