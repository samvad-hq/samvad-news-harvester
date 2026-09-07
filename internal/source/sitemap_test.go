package source_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/samvad-hq/samvad-news-harvester/internal/source"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testSource(t *testing.T, url string) source.Config {
	t.Helper()
	return source.Config{
		ID:      "test",
		Name:    "Test Source",
		Type:    source.TypeNewsSitemap,
		URL:     url,
		Headers: source.Headers{UserAgent: "test-agent"},
	}
}

func newFetcher() *source.Sitemap {
	// httptest servers are on loopback, so every test in this file must
	// opt out of the default SSRF guard to reach them.
	clients := httpx.NewProxyCache(5*time.Second, httpx.DefaultMaxBodyBytes, httpx.WithPrivateNetworksAllowed())
	return source.NewSitemap(clients, testLogger())
}

func TestFetchBuildsArticles(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<urlset xmlns:news="http://www.google.com/schemas/sitemap-news/0.9"
			xmlns:image="http://www.google.com/schemas/sitemap-image/1.1">
			<url>
				<loc>https://pub.example/story?utm_source=x</loc>
				<news:news>
					<news:title>Headline</news:title>
					<news:publication_date>2026-09-06T04:00:00Z</news:publication_date>
					<news:keywords>a, b</news:keywords>
				</news:news>
				<image:image><image:loc>https://pub.example/i.jpg</image:loc></image:image>
			</url>
		</urlset>`)
	}))
	defer srv.Close()

	got, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL))
	require.NoError(t, err)
	require.Len(t, got, 1)

	a := got[0]
	require.Equal(t, "test", a.SourceID)
	require.Equal(t, "Headline", a.Title)
	require.Equal(t, "https://pub.example/story", a.URL, "the utm parameter is canonicalised away")
	require.Equal(t, "https://pub.example/story?utm_source=x", a.OriginalURL)
	require.Equal(t, []string{"a", "b"}, a.Keywords)
	require.Equal(t, "https://pub.example/i.jpg", a.ImageURL)
	require.Len(t, a.ID, 64)
}

func TestFetchDeduplicatesWithinOneDocument(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<urlset>
			<url><loc>https://pub.example/story</loc></url>
			<url><loc>https://pub.example/story/</loc></url>
			<url><loc>https://pub.example/story?utm_medium=rss</loc></url>
		</urlset>`)
	}))
	defer srv.Close()

	got, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL))
	require.NoError(t, err)
	require.Len(t, got, 1, "three spellings of one URL yield one article")
}

func TestFetchSkipsUnusableLocations(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<urlset>
			<url><loc></loc></url>
			<url><loc>not-a-url</loc></url>
			<url><loc>javascript:alert(1)</loc></url>
			<url><loc>https://pub.example/good</loc></url>
		</urlset>`)
	}))
	defer srv.Close()

	got, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL))
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "https://pub.example/good", got[0].URL)
}

func TestFetchFollowsASitemapIndex(t *testing.T) {
	t.Parallel()

	var mux *http.ServeMux
	var srv *httptest.Server
	mux = http.NewServeMux()
	srv = httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/index.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `<sitemapindex>
			<sitemap><loc>%s/child-1.xml</loc></sitemap>
			<sitemap><loc>%s/child-2.xml</loc></sitemap>
		</sitemapindex>`, srv.URL, srv.URL)
	})
	mux.HandleFunc("/child-1.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<urlset><url><loc>https://pub.example/one</loc></url></urlset>`)
	})
	mux.HandleFunc("/child-2.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<urlset><url><loc>https://pub.example/two</loc></url></urlset>`)
	})

	got, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL+"/index.xml"))
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestFetchRejectsNonSitemapDocuments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		root string
	}{
		{
			name: "html error page",
			body: `<html><head><title>Access Denied</title></head><body>blocked</body></html>`,
			root: "html",
		},
		{
			name: "rss feed",
			body: `<?xml version="1.0"?><rss version="2.0"><channel><title>Feed</title></channel></rss>`,
			root: "rss",
		},
		{
			name: "atom feed",
			body: `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Feed</title></feed>`,
			root: "feed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			_, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL))
			require.Error(t, err)
			require.ErrorContains(t, err, "is not a sitemap")
			require.ErrorContains(t, err, tt.root)
		})
	}
}

// TestFetchCapsURLsPerDocument pins finding #4: a document over
// maxURLsPerSitemap is truncated to the cap rather than kept in full or
// dropped entirely, so one oversized sitemap cannot balloon the enrich
// stage's cost without bound.
func TestFetchCapsURLsPerDocument(t *testing.T) {
	t.Parallel()

	const overCap = 5001

	var body strings.Builder
	body.WriteString(`<urlset>`)
	for i := range overCap {
		fmt.Fprintf(&body, `<url><loc>https://pub.example/a%d</loc></url>`, i)
	}
	body.WriteString(`</urlset>`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body.String())
	}))
	defer srv.Close()

	got, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL))
	require.NoError(t, err)
	require.Len(t, got, 5000, "a document over the cap is truncated to the cap, not dropped or fully kept")
}

func TestFetchAllowsAGenuinelyEmptyURLSet(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"></urlset>`)
	}))
	defer srv.Close()

	got, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL))
	require.NoError(t, err, "a real sitemap with no stories is not an error")
	require.Empty(t, got)
}

func TestFetchSkipsFailingIndexChildAndKeepsSiblings(t *testing.T) {
	t.Parallel()

	var srv *httptest.Server
	mux := http.NewServeMux()
	srv = httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/index.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `<sitemapindex>
			<sitemap><loc>%s/bad.xml</loc></sitemap>
			<sitemap><loc>%s/good.xml</loc></sitemap>
		</sitemapindex>`, srv.URL, srv.URL)
	})
	mux.HandleFunc("/bad.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/good.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<urlset><url><loc>https://pub.example/good</loc></url></urlset>`)
	})

	got, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL+"/index.xml"))
	require.NoError(t, err, "one failing child should not fail siblings that parsed fine")
	require.Len(t, got, 1)
	require.Equal(t, "https://pub.example/good", got[0].URL)
}

func TestFetchFailsWhenEveryIndexChildFails(t *testing.T) {
	t.Parallel()

	var srv *httptest.Server
	mux := http.NewServeMux()
	srv = httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/index.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `<sitemapindex>
			<sitemap><loc>%s/bad-1.xml</loc></sitemap>
			<sitemap><loc>%s/bad-2.xml</loc></sitemap>
		</sitemapindex>`, srv.URL, srv.URL)
	})
	mux.HandleFunc("/bad-1.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/bad-2.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	_, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL+"/index.xml"))
	require.Error(t, err)
	require.ErrorContains(t, err, "2")
}

func TestFetchStopsAtIndexCycles(t *testing.T) {
	t.Parallel()

	var srv *httptest.Server
	mux := http.NewServeMux()
	srv = httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/a.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `<sitemapindex><sitemap><loc>%s/b.xml</loc></sitemap></sitemapindex>`, srv.URL)
	})
	mux.HandleFunc("/b.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `<sitemapindex><sitemap><loc>%s/a.xml</loc></sitemap></sitemapindex>`, srv.URL)
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL+"/a.xml"))
		require.NoError(t, err)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Fetch did not terminate on a cyclic sitemap index")
	}
}

// TestFetchSkipsSitemapIndexChildOnADifferentHost pins the complementary
// SSRF check: a sitemap index child on a host unrelated to the index
// document itself is suspicious enough to skip, logging both hosts. The
// mismatched child's host is never resolved or fetched — if it were, this
// test would hang or fail on DNS resolution rather than passing quickly.
func TestFetchSkipsSitemapIndexChildOnADifferentHost(t *testing.T) {
	t.Parallel()

	var srv *httptest.Server
	mux := http.NewServeMux()
	srv = httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/index.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `<sitemapindex>
			<sitemap><loc>%s/good.xml</loc></sitemap>
			<sitemap><loc>https://unrelated-host.example/child.xml</loc></sitemap>
		</sitemapindex>`, srv.URL)
	})
	mux.HandleFunc("/good.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<urlset><url><loc>https://pub.example/good</loc></url></urlset>`)
	})

	got, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL+"/index.xml"))
	require.NoError(t, err, "a skipped mismatched-host child must not fail siblings that parsed fine")
	require.Len(t, got, 1)
	require.Equal(t, "https://pub.example/good", got[0].URL)
}

func TestFetchReportsHTTPErrors(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<html>Access Denied</html>")
	}))
	defer srv.Close()

	_, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL))
	require.ErrorContains(t, err, "403")
}

func TestFetchRejectsAWrongSourceType(t *testing.T) {
	t.Parallel()

	src := testSource(t, "https://pub.example/s.xml")
	src.Type = "something_else"

	_, err := newFetcher().Fetch(context.Background(), src)
	require.Error(t, err)
}

// fixtureRootElement returns the local name of raw's root XML element. It
// is a minimal, test-only reader — the production root check lives in the
// unexported source.rootKind, which this black-box test package cannot
// call directly.
func fixtureRootElement(t *testing.T, raw []byte) string {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		require.NoError(t, err)
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local
		}
	}
}

// TestFetchAgainstRecordedFixtures runs the parser over responses captured
// from the real configured publishers. A publisher changing their XML
// shape is the failure mode that actually pages you, and this is what
// catches it in CI.
func TestFetchAgainstRecordedFixtures(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir("testdata/sitemaps")
	require.NoError(t, err)
	require.NotEmpty(t, entries, "run scripts/capture-fixtures.sh to populate testdata/sitemaps")

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".xml" {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			t.Parallel()

			body, err := os.ReadFile(filepath.Join("testdata/sitemaps", entry.Name()))
			require.NoError(t, err)

			// This test serves only the root document from httptest; a
			// <sitemapindex> fixture's <sitemap><loc> children would be
			// followed literally out to the real, live publisher. No
			// captured fixture is index-shaped today — this guards against
			// one being added without also serving its children locally.
			root := fixtureRootElement(t, body)
			require.Equal(t, "urlset", root,
				"fixture %s has root <%s>; an index fixture needs its <sitemap> children served "+
					"locally by this test, not fetched from the network", entry.Name(), root)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				_, _ = w.Write(body)
			}))
			defer srv.Close()

			got, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL))
			require.NoError(t, err)
			require.NotEmpty(t, got, "fixture parsed to zero articles")

			withTitle, withDate := 0, 0
			for _, a := range got {
				require.NotEmpty(t, a.ID)
				require.NotEmpty(t, a.URL)
				require.Len(t, a.ID, 64)
				if a.Title != "" {
					withTitle++
				}
				if !a.PublishedAt.IsZero() {
					withDate++
				}
			}
			// A low bar — half is plenty — so a publisher that legitimately
			// omits titles or dates on some entries does not break the
			// suite. The point is catching a renamed news:title or
			// news:publication_date, which leaves <loc> untouched and
			// would otherwise pass silently.
			require.Greater(t, withTitle, len(got)/2,
				"only %d of %d articles carry a title — news:title may have moved", withTitle, len(got))
			require.Greater(t, withDate, len(got)/2,
				"only %d of %d articles carry a publish date — news:publication_date may have moved", withDate, len(got))
		})
	}
}
