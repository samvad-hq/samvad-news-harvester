package publishers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/samvad-hq/samvad-news-harvester/internal/domain"
)

type fakeSQSClient struct {
	input *sqs.SendMessageInput
	err   error
}

func (f *fakeSQSClient) SendMessage(_ context.Context, params *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	f.input = params
	if f.err != nil {
		return nil, f.err
	}
	return &sqs.SendMessageOutput{MessageId: aws.String("msg-123")}, nil
}

func TestAWSSQSSenderSendSuccess(t *testing.T) {
	client := &fakeSQSClient{}
	sender := &awsSQSSender{
		queueURL: "https://example.com/queue",
		client:   client,
		log:      noopLogger{},
	}

	err := sender.Send(context.Background(), Event{
		ProviderID: "provider-1",
		Article:    domain.Article{ID: "a1"},
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if client.input == nil {
		t.Fatalf("client was not called")
	}
	if got := aws.ToString(client.input.QueueUrl); got != "https://example.com/queue" {
		t.Fatalf("QueueUrl = %s", got)
	}
	attr, ok := client.input.MessageAttributes["provider_id"]
	if !ok || attr.StringValue == nil || aws.ToString(attr.StringValue) != "provider-1" {
		t.Fatalf("provider_id attribute missing or wrong: %#v", attr)
	}
	if attr.DataType == nil || aws.ToString(attr.DataType) != "String" {
		t.Fatalf("DataType should be String, got %#v", attr.DataType)
	}
	if client.input.MessageBody == nil || !strings.Contains(aws.ToString(client.input.MessageBody), `"provider_id":"provider-1"`) {
		t.Fatalf("MessageBody missing provider_id: %s", aws.ToString(client.input.MessageBody))
	}
}

func TestAWSSQSSenderSendError(t *testing.T) {
	client := &fakeSQSClient{err: errors.New("boom")}
	sender := &awsSQSSender{
		queueURL: "https://example.com/queue",
		client:   client,
		log:      noopLogger{},
	}

	err := sender.Send(context.Background(), Event{
		ProviderID: "provider-1",
		Article:    domain.Article{ID: "a1"},
	})
	if err == nil {
		t.Fatalf("expected error from Send")
	}
}
