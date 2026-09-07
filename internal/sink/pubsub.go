package sink

import (
	"context"
	"encoding/json"
	"fmt"

	"cloud.google.com/go/pubsub"
	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"google.golang.org/api/option"
)

// PubSub delivers events to a Google Cloud Pub/Sub topic.
//
// topic.Publish queues the message with the client's internal bundler and
// returns immediately; Send then blocks on the result until the library
// confirms delivery or the context ends.
//
// The pipeline delivers a source's articles concurrently, up to
// DELIVERY_CONCURRENCY at once, so the bundler generally has several
// messages in flight per topic to coalesce. Setting DELIVERY_CONCURRENCY
// to 1 makes delivery sequential again and leaves the bundler nothing to
// batch except what overlapping sources happen to contribute.
type PubSub struct {
	id     string
	client *pubsub.Client
	topic  *pubsub.Topic
}

// NewPubSub returns a Pub/Sub sink. A blank credentials file falls back to
// application default credentials.
func NewPubSub(ctx context.Context, id string, cfg PubSubConfig) (*PubSub, error) {
	var opts []option.ClientOption
	if cfg.CredentialsFile != "" {
		opts = append(opts, option.WithCredentialsFile(cfg.CredentialsFile))
	}

	client, err := pubsub.NewClient(ctx, cfg.ProjectID, opts...)
	if err != nil {
		return nil, fmt.Errorf("sink %s: create pubsub client: %w", id, err)
	}
	return &PubSub{id: id, client: client, topic: client.Topic(cfg.Topic)}, nil
}

// Name returns the sink ID.
func (p *PubSub) Name() string { return p.id }

// Send publishes one event, blocking until the library confirms it. See
// the type doc for when the library's bundler actually has more than one
// message to coalesce.
func (p *PubSub) Send(ctx context.Context, evt news.Event) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	result := p.topic.Publish(ctx, &pubsub.Message{
		Data:       payload,
		Attributes: map[string]string{"source_id": evt.SourceID},
	})
	if _, err := result.Get(ctx); err != nil {
		return fmt.Errorf("publish to pubsub: %w", err)
	}
	return nil
}

// Close stops the topic's publisher goroutines and releases the client.
func (p *PubSub) Close() error {
	p.topic.Stop()
	return p.client.Close()
}
