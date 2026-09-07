package sink

// Tests in this file are in-package: they substitute the minimal AWS call
// interfaces, which are unexported because nothing outside this package
// has any reason to name them.

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/stretchr/testify/require"
)

type fakeSQS struct {
	input *sqs.SendMessageInput
	err   error
}

func (f *fakeSQS) SendMessage(_ context.Context, in *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	f.input = in
	if f.err != nil {
		return nil, f.err
	}
	return &sqs.SendMessageOutput{MessageId: aws.String("msg-1")}, nil
}

func TestSQSSendsTheEventAsJSON(t *testing.T) {
	t.Parallel()

	api := &fakeSQS{}
	s := &SQS{id: "sqs", queueURL: "https://sqs.example/q", api: api}

	evt := news.NewEvent("thehindu", "The Hindu", news.Article{ID: "abc"})
	require.NoError(t, s.Send(context.Background(), evt))

	require.Equal(t, "sqs", s.Name())
	require.Equal(t, "https://sqs.example/q", aws.ToString(api.input.QueueUrl))
	require.Contains(t, aws.ToString(api.input.MessageBody), `"id":"abc"`)
	require.Equal(t, "thehindu", aws.ToString(api.input.MessageAttributes["source_id"].StringValue))
}

func TestSQSPropagatesTheError(t *testing.T) {
	t.Parallel()

	boom := errors.New("throttled")
	s := &SQS{id: "sqs", queueURL: "q", api: &fakeSQS{err: boom}}

	require.ErrorIs(t, s.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"})), boom)
}

type fakeSNS struct {
	input *sns.PublishInput
	err   error
}

func (f *fakeSNS) Publish(_ context.Context, in *sns.PublishInput, _ ...func(*sns.Options)) (*sns.PublishOutput, error) {
	f.input = in
	if f.err != nil {
		return nil, f.err
	}
	return &sns.PublishOutput{MessageId: aws.String("msg-1")}, nil
}

func TestSNSPublishesTheEventAsJSON(t *testing.T) {
	t.Parallel()

	api := &fakeSNS{}
	s := &SNS{id: "sns", topicARN: "arn:aws:sns:::topic", api: api}

	require.NoError(t, s.Send(context.Background(), news.NewEvent("thehindu", "The Hindu", news.Article{ID: "abc"})))

	require.Equal(t, "arn:aws:sns:::topic", aws.ToString(api.input.TopicArn))
	require.Contains(t, aws.ToString(api.input.Message), `"id":"abc"`)
	require.Equal(t, "thehindu", aws.ToString(api.input.MessageAttributes["source_id"].StringValue))
}

func TestSNSPropagatesTheError(t *testing.T) {
	t.Parallel()

	boom := errors.New("no such topic")
	s := &SNS{id: "sns", topicARN: "arn", api: &fakeSNS{err: boom}}

	require.ErrorIs(t, s.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"})), boom)
}
