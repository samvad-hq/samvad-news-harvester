package publishers

import (
	"context"
	"encoding/json"
	"fmt"

	"cloud.google.com/go/pubsub"
	"google.golang.org/api/option"
)

// gcpPubSubSender implements queueSender for Google Cloud Pub/Sub.
type gcpPubSubSender struct {
	topic *pubsub.Topic
	log   Logger
}

// newGCPPubSubSender builds a Pub/Sub sender using the provided config.
func newGCPPubSubSender(ctx context.Context, cfg *GCPQueueConfig, log Logger) (queueSender, error) {
	if cfg == nil {
		return nil, fmt.Errorf("gcp queue configuration is missing")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	var opts []option.ClientOption
	if cfg.CredentialsFile != "" {
		opts = append(opts, option.WithCredentialsFile(cfg.CredentialsFile))
	}

	client, err := pubsub.NewClient(ctx, cfg.ProjectID, opts...)
	if err != nil {
		return nil, fmt.Errorf("create pubsub client: %w", err)
	}

	topic := client.Topic(cfg.Topic)

	return &gcpPubSubSender{
		topic: topic,
		log:   ensureLogger(log),
	}, nil
}

// Send publishes the event to the configured Pub/Sub topic.
func (s *gcpPubSubSender) Send(ctx context.Context, evt Event) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	msg := &pubsub.Message{
		Data: payload,
		Attributes: map[string]string{
			"provider_id": evt.ProviderID,
		},
	}

	res := s.topic.Publish(ctx, msg)
	msgID, err := res.Get(ctx)
	if err != nil {
		s.log.ErrorObj("gcp pubsub publisher send failed", "publisher_gcp_pubsub_error", map[string]any{
			"error": err.Error(),
		})
		return fmt.Errorf("send message to pubsub: %w", err)
	}

	s.log.DebugObj("gcp pubsub publisher delivered event", "publisher_gcp_pubsub_delivery", map[string]any{
		"message_id": msgID,
	})
	return nil
}
