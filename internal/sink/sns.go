package sink

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/samvad-hq/samvad-news-harvester/internal/news"
)

// snsAPI is the one SNS call this sink makes.
type snsAPI interface {
	Publish(ctx context.Context, in *sns.PublishInput, opts ...func(*sns.Options)) (*sns.PublishOutput, error)
}

// SNS delivers events to an Amazon SNS topic.
type SNS struct {
	id       string
	topicARN string
	api      snsAPI
}

// NewSNS returns an SNS sink using static credentials from configuration.
func NewSNS(ctx context.Context, id string, cfg SNSConfig) (*SNS, error) {
	creds := credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")
	awsCfg, err := awscfg.LoadDefaultConfig(ctx,
		awscfg.WithRegion(cfg.Region),
		awscfg.WithCredentialsProvider(creds),
	)
	if err != nil {
		return nil, fmt.Errorf("sink %s: load aws config: %w", id, err)
	}
	return &SNS{id: id, topicARN: cfg.TopicARN, api: sns.NewFromConfig(awsCfg)}, nil
}

// Name returns the sink ID.
func (s *SNS) Name() string { return s.id }

// Send publishes one event as a JSON message.
func (s *SNS) Send(ctx context.Context, evt news.Event) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	_, err = s.api.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(s.topicARN),
		Message:  aws.String(string(payload)),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"source_id": {DataType: aws.String("String"), StringValue: aws.String(evt.SourceID)},
		},
	})
	if err != nil {
		return fmt.Errorf("publish to sns: %w", err)
	}
	return nil
}
