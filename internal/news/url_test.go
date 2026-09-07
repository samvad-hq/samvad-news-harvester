package news_test

import (
	"testing"

	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/stretchr/testify/require"
)

func TestCanonicalURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "already canonical is unchanged",
			input:    "https://www.thehindu.com/news/national/story",
			expected: "https://www.thehindu.com/news/national/story",
		},
		{
			name:     "host and scheme are lowercased",
			input:    "HTTPS://WWW.TheHindu.com/News/Story",
			expected: "https://www.thehindu.com/News/Story",
		},
		{
			name:     "default https port is dropped",
			input:    "https://example.com:443/story",
			expected: "https://example.com/story",
		},
		{
			name:     "default http port is dropped",
			input:    "http://example.com:80/story",
			expected: "http://example.com/story",
		},
		{
			name:     "non-default port is kept",
			input:    "https://example.com:8443/story",
			expected: "https://example.com:8443/story",
		},
		{
			name:     "fragment is dropped",
			input:    "https://example.com/story#section-2",
			expected: "https://example.com/story",
		},
		{
			name:     "trailing slash is stripped",
			input:    "https://example.com/story/",
			expected: "https://example.com/story",
		},
		{
			name:     "bare root keeps its slash",
			input:    "https://example.com/",
			expected: "https://example.com/",
		},
		{
			name:     "utm parameters are stripped",
			input:    "https://example.com/story?utm_source=twitter&utm_medium=social",
			expected: "https://example.com/story",
		},
		{
			name:     "click identifiers are stripped",
			input:    "https://example.com/story?fbclid=abc&gclid=def&msclkid=ghi",
			expected: "https://example.com/story",
		},
		{
			name:     "meaningful parameters survive and are sorted",
			input:    "https://example.com/story?page=2&id=7&utm_source=x",
			expected: "https://example.com/story?id=7&page=2",
		},
		{
			name:     "repeated parameter values are sorted",
			input:    "https://example.com/s?tag=z&tag=a",
			expected: "https://example.com/s?tag=a&tag=z",
		},
		{
			name:     "surrounding whitespace is trimmed",
			input:    "  https://example.com/story  ",
			expected: "https://example.com/story",
		},
		{
			name:     "percent-encoded slash in path survives",
			input:    "https://example.com/stor%2Fy/",
			expected: "https://example.com/stor%2Fy",
		},
		{
			name:     "percent-encoded space in path survives",
			input:    "https://example.com/a%20b/",
			expected: "https://example.com/a%20b",
		},
		{
			name:     "multiple trailing slashes are all stripped",
			input:    "https://example.com/story//",
			expected: "https://example.com/story",
		},
		{
			name:     "triple slashes become root",
			input:    "https://example.com/story///",
			expected: "https://example.com/story",
		},
		{
			name:     "bare domain normalizes to root",
			input:    "https://example.com",
			expected: "https://example.com/",
		},
		{
			name:     "double slash path normalizes to root",
			input:    "https://example.com//",
			expected: "https://example.com/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := news.CanonicalURL(tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.expected, got)
		})
	}
}

func TestCanonicalURLRejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "whitespace only", input: "   "},
		{name: "relative path", input: "/news/story"},
		{name: "no host", input: "https://"},
		{name: "unsupported scheme", input: "ftp://example.com/story"},
		{name: "javascript scheme", input: "javascript:alert(1)"},
		{name: "unparseable", input: "https://exa mple.com/\x7f"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := news.CanonicalURL(tt.input)
			require.Error(t, err)
		})
	}
}

func TestCanonicalURLErrorWrapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		wraps bool
	}{
		{name: "relative path wraps ErrNotAbsoluteURL", input: "/news/story", wraps: true},
		{name: "no host wraps ErrNotAbsoluteURL", input: "https://", wraps: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := news.CanonicalURL(tt.input)
			require.Error(t, err)
			if tt.wraps {
				require.ErrorIs(t, err, news.ErrNotAbsoluteURL)
			}
		})
	}
}

func TestCanonicalURLIsStableAcrossVariants(t *testing.T) {
	t.Parallel()

	variants := []string{
		"https://www.thehindu.com/news/story",
		"https://www.thehindu.com/news/story/",
		"https://WWW.thehindu.com/news/story",
		"https://www.thehindu.com/news/story?utm_source=whatsapp",
		"https://www.thehindu.com/news/story#top",
		"https://www.thehindu.com:443/news/story",
		"https://www.thehindu.com/news/story//",
	}

	first, err := news.CanonicalURL(variants[0])
	require.NoError(t, err)
	firstID := news.ID(first)

	for _, v := range variants[1:] {
		got, err := news.CanonicalURL(v)
		require.NoError(t, err)
		require.Equal(t, first, got, "variant %q should canonicalise identically", v)
		require.Equal(t, firstID, news.ID(got))
	}
}

func TestID(t *testing.T) {
	t.Parallel()

	id := news.ID("https://example.com/story")
	require.Len(t, id, 64, "sha256 hex is 64 characters")
	require.Equal(t, id, news.ID("https://example.com/story"), "must be deterministic")
	require.NotEqual(t, id, news.ID("https://example.com/other"))
}
