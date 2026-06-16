package publishers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/samvad-hq/samvad-news-harvester/internal/domain"
)

type fakeSNSClient struct {
	input *sns.PublishInput
	err   error
}

func (f *fakeSNSClient) Publish(_ context.Context, params *sns.PublishInput, _ ...func(*sns.Options)) (*sns.PublishOutput, error) {
	f.input = params
	if f.err != nil {
		return nil, f.err
	}
	return &sns.PublishOutput{MessageId: aws.String("msg-123")}, nil
}

func TestAWSSNSSenderSendSuccess(t *testing.T) {
	client := &fakeSNSClient{}
	sender := &awsSNSSender{
		topicARN: "arn:aws:sns:::topic",
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
	if got := aws.ToString(client.input.TopicArn); got != "arn:aws:sns:::topic" {
		t.Fatalf("TopicArn = %s", got)
	}
	attr, ok := client.input.MessageAttributes["provider_id"]
	if !ok || attr.StringValue == nil || aws.ToString(attr.StringValue) != "provider-1" {
		t.Fatalf("provider_id attribute missing or wrong: %#v", attr)
	}
	if attr.DataType == nil || aws.ToString(attr.DataType) != "String" {
		t.Fatalf("DataType should be String, got %#v", attr.DataType)
	}
	if client.input.Message == nil || !strings.Contains(aws.ToString(client.input.Message), `"provider_id":"provider-1"`) {
		t.Fatalf("Message missing provider_id: %s", aws.ToString(client.input.Message))
	}
}

func TestAWSSNSSenderSendError(t *testing.T) {
	client := &fakeSNSClient{err: errors.New("boom")}
	sender := &awsSNSSender{
		topicARN: "arn:aws:sns:::topic",
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
