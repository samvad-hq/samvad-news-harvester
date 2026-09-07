package source_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/samvad-hq/samvad-news-harvester/internal/source"
	"github.com/stretchr/testify/require"
)

func writeSources(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sources.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestLoadFile(t *testing.T) {
	t.Parallel()

	path := writeSources(t, `
sources:
  - id: thehindu
    name: The Hindu
    type: news_sitemap
    url: https://www.thehindu.com/sitemap/googlenews/all/all.xml
    headers:
      user_agent: test-agent
      accept: application/xml
`)

	got, err := source.LoadFile(path)
	require.NoError(t, err)
	require.Len(t, got, 1)

	src := got[0]
	require.Equal(t, "thehindu", src.ID)
	require.Equal(t, "The Hindu", src.Name)
	require.Equal(t, source.TypeNewsSitemap, src.Type)
	require.Equal(t, "test-agent", src.Headers.UserAgent)
	require.Equal(t, "thehindu", src.Ident())
}

func TestLoadFileExpandsProxyFromEnv(t *testing.T) {
	t.Setenv("TEST_PROXY", "http://proxy.example:8080")

	path := writeSources(t, `
sources:
  - id: ndtv
    name: NDTV
    type: news_sitemap
    url: https://www.ndtv.com/sitemap/google-news-sitemap
    proxy: ${TEST_PROXY}
    headers:
      user_agent: test-agent
`)

	got, err := source.LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, "http://proxy.example:8080", got[0].Proxy)
}

func TestLoadFileRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		contains string
	}{
		{
			name:     "missing id",
			body:     "sources:\n  - name: X\n    type: news_sitemap\n    url: https://x.example/s.xml\n    headers:\n      user_agent: a\n",
			contains: "id is required",
		},
		{
			name:     "missing name",
			body:     "sources:\n  - id: x\n    type: news_sitemap\n    url: https://x.example/s.xml\n    headers:\n      user_agent: a\n",
			contains: "name is required",
		},
		{
			name:     "unknown type",
			body:     "sources:\n  - id: x\n    name: X\n    type: rss\n    url: https://x.example/s.xml\n    headers:\n      user_agent: a\n",
			contains: "unsupported type",
		},
		{
			name:     "missing user agent",
			body:     "sources:\n  - id: x\n    name: X\n    type: news_sitemap\n    url: https://x.example/s.xml\n",
			contains: "headers.user_agent is required",
		},
		{
			name:     "non-http url",
			body:     "sources:\n  - id: x\n    name: X\n    type: news_sitemap\n    url: ftp://x.example/s.xml\n    headers:\n      user_agent: a\n",
			contains: "url",
		},
		{
			name:     "bad proxy url",
			body:     "sources:\n  - id: x\n    name: X\n    type: news_sitemap\n    url: https://x.example/s.xml\n    proxy: \"not a url\"\n    headers:\n      user_agent: a\n",
			contains: "proxy",
		},
		{
			name:     "duplicate ids",
			body:     "sources:\n  - id: x\n    name: X\n    type: news_sitemap\n    url: https://x.example/a.xml\n    headers:\n      user_agent: a\n  - id: x\n    name: Y\n    type: news_sitemap\n    url: https://x.example/b.xml\n    headers:\n      user_agent: a\n",
			contains: "duplicate",
		},
		{
			name:     "empty file",
			body:     "sources: []\n",
			contains: "no entries",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := source.LoadFile(writeSources(t, tt.body))
			require.ErrorContains(t, err, tt.contains)
		})
	}
}

// TestLoadFileRejectsABadProxyURLWithoutLeakingItsPassword pins finding
// #3: a proxy URL is a normal place to carry basic-auth credentials, and
// the old error message echoed the raw URL with %q on a validation
// failure.
func TestLoadFileRejectsABadProxyURLWithoutLeakingItsPassword(t *testing.T) {
	t.Parallel()

	path := writeSources(t, `
sources:
  - id: x
    name: X
    type: news_sitemap
    url: https://x.example/s.xml
    proxy: "http://user:hunter2@"
    headers:
      user_agent: a
`)

	_, err := source.LoadFile(path)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "hunter2")
}

func TestHeadersMapSkipsEmptyValues(t *testing.T) {
	t.Parallel()

	h := source.Headers{UserAgent: "agent", AcceptLanguage: "en-IN"}
	got := h.Map()

	require.Equal(t, map[string]string{
		"User-Agent":      "agent",
		"Accept-Language": "en-IN",
	}, got)
}
