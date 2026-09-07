package news_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/stretchr/testify/require"
)

func TestNewEventStampsCollectedAtInUTC(t *testing.T) {
	t.Parallel()

	before := time.Now().UTC()
	evt := news.NewEvent("thehindu", "The Hindu", news.Article{ID: "abc"})
	after := time.Now().UTC()

	require.Equal(t, "thehindu", evt.SourceID)
	require.Equal(t, "The Hindu", evt.SourceName)
	require.Equal(t, "abc", evt.Article.ID)
	require.Equal(t, time.UTC, evt.CollectedAt.Location())
	require.False(t, evt.CollectedAt.Before(before))
	require.False(t, evt.CollectedAt.After(after))
}

func TestEventJSONShape(t *testing.T) {
	t.Parallel()

	evt := news.Event{
		SourceID:   "thehindu",
		SourceName: "The Hindu",
		Article: news.Article{
			SourceID:    "thehindu",
			ID:          "abc",
			Title:       "Headline",
			URL:         "https://www.thehindu.com/story",
			PublishedAt: time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC),
		},
		CollectedAt: time.Date(2026, 9, 6, 5, 0, 0, 0, time.UTC),
	}

	raw, err := json.Marshal(evt)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))

	require.Equal(t, "thehindu", got["source_id"])
	require.Equal(t, "The Hindu", got["source_name"])
	require.Equal(t, "2026-09-06T05:00:00Z", got["collected_at"])

	article, ok := got["article"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "abc", article["id"])
	require.Equal(t, "https://www.thehindu.com/story", article["url"])

	// Empty optional fields are omitted so consumers see a compact payload.
	require.NotContains(t, article, "description")
	require.NotContains(t, article, "image_url")
	require.NotContains(t, article, "keywords")
	require.NotContains(t, article, "original_url")
}
