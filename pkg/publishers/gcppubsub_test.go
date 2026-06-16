package publishers

import (
	"context"
	"os"
	"testing"

	"cloud.google.com/go/pubsub"
	"cloud.google.com/go/pubsub/pstest"
	"github.com/samvad-hq/samvad-news-harvester/internal/domain"
)

func TestGCPPubSubSenderPublishes(t *testing.T) {
	// Use the in-memory Pub/Sub emulator.
	server := pstest.NewServer()
	defer server.Close()
	defer os.Unsetenv("PUBSUB_EMULATOR_HOST")
	os.Setenv("PUBSUB_EMULATOR_HOST", server.Addr)

	ctx := context.Background()
	client, err := pubsub.NewClient(ctx, "test-project")
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	if _, err := client.CreateTopic(ctx, "topic-1"); err != nil {
		t.Fatalf("create topic: %v", err)
	}

	sender, err := newGCPPubSubSender(ctx, &GCPQueueConfig{
		ProjectID: "test-project",
		Topic:     "topic-1",
	}, nil)
	if err != nil {
		t.Fatalf("newGCPPubSubSender: %v", err)
	}

	err = sender.Send(ctx, Event{
		ProviderID: "p1",
		Article:    domain.Article{ID: "a1"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
}
