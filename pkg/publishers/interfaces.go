package publishers

import "context"

// Publisher sends events to a downstream sink (SQS, HTTP, etc).
type Publisher interface {
	ID() string
	Type() string
	Publish(ctx context.Context, evt Event) error
}
