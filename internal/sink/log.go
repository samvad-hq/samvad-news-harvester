package sink

import (
	"context"
	"log/slog"
	"strings"

	"github.com/samvad-hq/samvad-news-harvester/internal/news"
)

// Log writes each event to the service's own logger. It needs no
// credentials and no network, which is what lets a fresh clone produce
// visible output on the first run.
type Log struct {
	id     string
	level  slog.Level
	logger *slog.Logger
}

// NewLog returns a log sink. An unrecognised level falls back to info.
func NewLog(id string, cfg LogConfig, logger *slog.Logger) *Log {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(cfg.Level)) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	return &Log{id: id, level: level, logger: logger}
}

// Name returns the sink ID.
func (l *Log) Name() string { return l.id }

// Send writes one line per event. It cannot fail.
func (l *Log) Send(ctx context.Context, evt news.Event) error {
	l.logger.Log(ctx, l.level, "article",
		"sink", l.id,
		"source_id", evt.SourceID,
		"source_name", evt.SourceName,
		"article_id", evt.Article.ID,
		"title", evt.Article.Title,
		"url", evt.Article.URL,
		"published_at", evt.Article.PublishedAt,
	)
	return nil
}
