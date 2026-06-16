package publishers

import (
	"context"
	"fmt"
)

// queueSender abstracts provider-specific queue senders.
type queueSender interface {
	Send(ctx context.Context, evt Event) error
}

// queuePublisher dispatches events to a cloud queue provider.
type queuePublisher struct {
	id       string
	typ      string
	provider string
	sender   queueSender
	log      Logger
}

// newQueuePublisher creates a queue publisher for the configured provider.
func newQueuePublisher(ctx context.Context, cfg PublisherConfig, log Logger) (Publisher, error) {
	if cfg.Queue == nil {
		return nil, fmt.Errorf("publisher %q missing queue configuration", cfg.ID)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	var (
		sender queueSender
		err    error
	)

	switch cfg.Queue.Provider {
	case QueueProviderAWSSQS:
		sender, err = newAWSSQSSender(ctx, cfg.Queue.AWS, log)
	case QueueProviderAWSSNS:
		sender, err = newAWSSNSSender(ctx, cfg.Queue.SNS, log)
	case QueueProviderGCP:
		sender, err = newGCPPubSubSender(ctx, cfg.Queue.GCP, log)
	case QueueProviderAzure:
		err = fmt.Errorf("queue provider %q not implemented", cfg.Queue.Provider)
	default:
		err = fmt.Errorf("queue provider %q is not supported", cfg.Queue.Provider)
	}
	if err != nil {
		return nil, err
	}

	return &queuePublisher{
		id:       cfg.ID,
		typ:      cfg.Type,
		provider: cfg.Queue.Provider,
		sender:   sender,
		log:      ensureLogger(log),
	}, nil
}

func (p *queuePublisher) ID() string   { return p.id }
func (p *queuePublisher) Type() string { return p.typ }

// Publish forwards the event to the configured queue provider.
func (p *queuePublisher) Publish(ctx context.Context, evt Event) error {
	if err := p.sender.Send(ctx, evt); err != nil {
		return fmt.Errorf("queue provider %s send failed: %w", p.provider, err)
	}
	return nil
}
