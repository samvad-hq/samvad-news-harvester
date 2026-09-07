package sink

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/samvad-hq/samvad-news-harvester/internal/news"
)

// sqsAPI is the one SQS call this sink makes. It is declared here, at the
// consumer, and unexported because nothing else needs to name it.
type sqsAPI interface {
	SendMessage(ctx context.Context, in *sqs.SendMessageInput, opts ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}

// SQS delivers events to an Amazon SQS queue.
type SQS struct {
	id       string
	queueURL string
	api      sqsAPI
}

// NewSQS returns an SQS sink using static credentials from configuration.
func NewSQS(ctx context.Context, id string, cfg SQSConfig) (*SQS, error) {
	creds := credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")
	awsCfg, err := awscfg.LoadDefaultConfig(ctx,
		awscfg.WithRegion(cfg.Region),
		awscfg.WithCredentialsProvider(creds),
	)
	if err != nil {
		return nil, fmt.Errorf("sink %s: load aws config: %w", id, err)
	}
	return &SQS{id: id, queueURL: cfg.QueueURL, api: sqs.NewFromConfig(awsCfg)}, nil
}

// Name returns the sink ID.
func (s *SQS) Name() string { return s.id }

// Send delivers one event as a JSON message body, with the source ID as a
// message attribute so consumers can filter without parsing the payload.
func (s *SQS) Send(ctx context.Context, evt news.Event) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	_, err = s.api.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(s.queueURL),
		MessageBody: aws.String(string(payload)),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"source_id": {DataType: aws.String("String"), StringValue: aws.String(evt.SourceID)},
		},
	})
	if err != nil {
		return fmt.Errorf("send to sqs: %w", err)
	}
	return nil
}
