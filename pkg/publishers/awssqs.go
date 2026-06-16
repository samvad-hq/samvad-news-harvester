package publishers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// sqsClient defines the minimal subset of the SQS client used by the AWS sender.
type sqsClient interface {
	SendMessage(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}

// awsSQSSender implements queueSender for AWS SQS.
type awsSQSSender struct {
	queueURL string
	client   sqsClient
	log      Logger
}

// newAWSSQSSender builds an SQS sender with static credentials.
func newAWSSQSSender(ctx context.Context, cfg *AWSSQSPublisherConfig, log Logger) (queueSender, error) {
	if cfg == nil {
		return nil, fmt.Errorf("aws queue configuration is missing")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	creds := credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")
	awsCfg, err := awscfg.LoadDefaultConfig(ctx,
		awscfg.WithRegion(cfg.Region),
		awscfg.WithCredentialsProvider(creds),
	)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	return &awsSQSSender{
		queueURL: cfg.QueueURL,
		client:   sqs.NewFromConfig(awsCfg),
		log:      ensureLogger(log),
	}, nil
}

// Send publishes the event to the configured SQS queue.
func (s *awsSQSSender) Send(ctx context.Context, evt Event) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	input := &sqs.SendMessageInput{
		QueueUrl:    aws.String(s.queueURL),
		MessageBody: aws.String(string(payload)),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"provider_id": {
				DataType:    aws.String("String"),
				StringValue: aws.String(evt.ProviderID),
			},
		},
	}

	resp, err := s.client.SendMessage(ctx, input)
	if err != nil {
		s.log.ErrorObj("sqs publisher send failed", "publisher_sqs_error", map[string]any{
			"error": err.Error(),
		})
		return fmt.Errorf("send message to sqs: %w", err)
	}
	s.log.DebugObj("sqs publisher delivered event", "publisher_sqs_delivery", map[string]any{
		"message_id": aws.ToString(resp.MessageId),
	})
	return nil
}
