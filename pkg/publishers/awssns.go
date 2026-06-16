package publishers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"
)

// snsClient defines the minimal subset of the SNS client used by the AWS sender.
type snsClient interface {
	Publish(ctx context.Context, params *sns.PublishInput, optFns ...func(*sns.Options)) (*sns.PublishOutput, error)
}

// awsSNSSender implements queueSender for AWS SNS.
type awsSNSSender struct {
	topicARN string
	client   snsClient
	log      Logger
}

// newAWSSNSSender builds an SNS sender with static credentials.
func newAWSSNSSender(ctx context.Context, cfg *AWSSNSPublisherConfig, log Logger) (queueSender, error) {
	if cfg == nil {
		return nil, fmt.Errorf("aws sns configuration is missing")
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

	return &awsSNSSender{
		topicARN: cfg.TopicARN,
		client:   sns.NewFromConfig(awsCfg),
		log:      ensureLogger(log),
	}, nil
}

// Send publishes the event to the configured SNS topic.
func (s *awsSNSSender) Send(ctx context.Context, evt Event) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	input := &sns.PublishInput{
		TopicArn: aws.String(s.topicARN),
		Message:  aws.String(string(payload)),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"provider_id": {
				DataType:    aws.String("String"),
				StringValue: aws.String(evt.ProviderID),
			},
		},
	}

	resp, err := s.client.Publish(ctx, input)
	if err != nil {
		s.log.ErrorObj("sns publisher send failed", "publisher_sns_error", map[string]any{
			"error": err.Error(),
		})
		return fmt.Errorf("send message to sns: %w", err)
	}
	s.log.DebugObj("sns publisher delivered event", "publisher_sns_delivery", map[string]any{
		"message_id": aws.ToString(resp.MessageId),
	})
	return nil
}
