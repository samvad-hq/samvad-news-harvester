package sink

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/config"
)

// Sink types accepted by the type field.
const (
	TypeLog    = "log"
	TypeHTTP   = "http"
	TypeSQS    = "aws-sqs"
	TypeSNS    = "aws-sns"
	TypePubSub = "gcp-pubsub"
)

const defaultHTTPMethod = "POST"
const defaultHTTPTimeout = 10 * time.Second

// Config is one sink as declared in the sinks file.
//
// The type is flat: the old shape wrapped the three cloud sinks in a
// "queue" type with a nested provider field, which bought an extra config
// struct and an extra indirection for no gain.
type Config struct {
	ID      string `yaml:"id" json:"id"`
	Type    string `yaml:"type" json:"type"`
	Enabled *bool  `yaml:"enabled" json:"enabled"`

	Log    *LogConfig    `yaml:"log" json:"log"`
	HTTP   *HTTPConfig   `yaml:"http" json:"http"`
	SQS    *SQSConfig    `yaml:"sqs" json:"sqs"`
	SNS    *SNSConfig    `yaml:"sns" json:"sns"`
	PubSub *PubSubConfig `yaml:"pubsub" json:"pubsub"`
}

// LogConfig configures the log sink, which writes each event to the
// service's own logger. It needs no credentials, which makes it the
// default in the example configuration: a fresh clone produces visible
// output without an account anywhere.
type LogConfig struct {
	// Level is debug, info, warn or error. Defaults to info.
	Level string `yaml:"level" json:"level"`
}

// HTTPConfig configures a webhook sink.
type HTTPConfig struct {
	URL     string            `yaml:"url" json:"url"`
	Method  string            `yaml:"method" json:"method"`
	Timeout time.Duration     `yaml:"timeout" json:"timeout"`
	Headers map[string]string `yaml:"headers" json:"headers"`
}

// SQSConfig configures an AWS SQS sink.
type SQSConfig struct {
	QueueURL        string `yaml:"queue_url" json:"queue_url"`
	Region          string `yaml:"region" json:"region"`
	AccessKeyID     string `yaml:"access_key_id" json:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key" json:"secret_access_key"`
}

// SNSConfig configures an AWS SNS sink.
type SNSConfig struct {
	TopicARN        string `yaml:"topic_arn" json:"topic_arn"`
	Region          string `yaml:"region" json:"region"`
	AccessKeyID     string `yaml:"access_key_id" json:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key" json:"secret_access_key"`
}

// PubSubConfig configures a Google Cloud Pub/Sub sink.
type PubSubConfig struct {
	ProjectID       string `yaml:"project_id" json:"project_id"`
	Topic           string `yaml:"topic" json:"topic"`
	CredentialsFile string `yaml:"credentials_file" json:"credentials_file"`
}

// Ident returns the sink ID, satisfying config.Item.
func (c Config) Ident() string { return c.ID }

// IsEnabled reports whether the sink should be built. A sink with no
// enabled field is on.
func (c Config) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// Validate reports every problem with the entry at once.
//
// Disabled sinks are validated too, so a broken configuration fails at
// startup rather than the first time someone flips it on in production.
func (c Config) Validate() error {
	var errs []error

	if strings.TrimSpace(c.ID) == "" {
		errs = append(errs, errors.New("id is required"))
	}

	switch c.Type {
	case TypeLog:
		// The log block is optional; its zero value is valid.
	case TypeHTTP:
		errs = append(errs, validateHTTP(c.HTTP)...)
	case TypeSQS:
		errs = append(errs, validateSQS(c.SQS)...)
	case TypeSNS:
		errs = append(errs, validateSNS(c.SNS)...)
	case TypePubSub:
		errs = append(errs, validatePubSub(c.PubSub)...)
	case "":
		errs = append(errs, errors.New("type is required"))
	default:
		errs = append(errs, fmt.Errorf("unsupported type %q (use %s, %s, %s, %s or %s)",
			c.Type, TypeLog, TypeHTTP, TypeSQS, TypeSNS, TypePubSub))
	}

	return errors.Join(errs...)
}

func validateHTTP(cfg *HTTPConfig) []error {
	if cfg == nil {
		return []error{errors.New("http block is required for an http sink")}
	}
	var errs []error
	if strings.TrimSpace(cfg.URL) == "" {
		errs = append(errs, errors.New("http.url is required and must not expand to an empty string"))
	} else if u, err := url.Parse(cfg.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		// cfg.URL itself is never reproduced here: for a Slack, Discord
		// or Microsoft Teams incoming webhook the token lives in the
		// path, and this error reaches an operator's terminal on every
		// -validate run with a typo'd scheme. redactURL (see http.go)
		// keeps the scheme and host, which is enough to tell one
		// misconfigured webhook from another.
		errs = append(errs, fmt.Errorf("http.url %q must be an absolute http or https url", redactURL(cfg.URL)))
	}
	if cfg.Timeout < 0 {
		errs = append(errs, errors.New("http.timeout must not be negative"))
	}
	return errs
}

func validateSQS(cfg *SQSConfig) []error {
	if cfg == nil {
		return []error{errors.New("sqs block is required for an aws-sqs sink")}
	}
	return requireFields(map[string]string{
		"sqs.queue_url":         cfg.QueueURL,
		"sqs.region":            cfg.Region,
		"sqs.access_key_id":     cfg.AccessKeyID,
		"sqs.secret_access_key": cfg.SecretAccessKey,
	})
}

func validateSNS(cfg *SNSConfig) []error {
	if cfg == nil {
		return []error{errors.New("sns block is required for an aws-sns sink")}
	}
	return requireFields(map[string]string{
		"sns.topic_arn":         cfg.TopicARN,
		"sns.region":            cfg.Region,
		"sns.access_key_id":     cfg.AccessKeyID,
		"sns.secret_access_key": cfg.SecretAccessKey,
	})
}

func validatePubSub(cfg *PubSubConfig) []error {
	if cfg == nil {
		return []error{errors.New("pubsub block is required for a gcp-pubsub sink")}
	}
	return requireFields(map[string]string{
		"pubsub.project_id": cfg.ProjectID,
		"pubsub.topic":      cfg.Topic,
	})
}

// requireFields reports one error per blank field, sorted by field name so
// the message is stable.
func requireFields(fields map[string]string) []error {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)

	var errs []error
	for _, name := range names {
		if strings.TrimSpace(fields[name]) == "" {
			errs = append(errs, fmt.Errorf("%s is required and must not expand to an empty string", name))
		}
	}
	return errs
}

// LoadFile reads and validates the sinks file.
func LoadFile(path string) ([]Config, error) {
	var file struct {
		Sinks []Config `yaml:"sinks" json:"sinks"`
	}
	if err := config.DecodeFile(path, &file); err != nil {
		return nil, err
	}

	for i := range file.Sinks {
		file.Sinks[i] = sanitize(file.Sinks[i])
	}
	if err := config.ValidateList(file.Sinks); err != nil {
		return nil, fmt.Errorf("sinks %s: %w", path, err)
	}
	return file.Sinks, nil
}

// sanitize trims whitespace and applies HTTP defaults before validation.
func sanitize(c Config) Config {
	c.ID = strings.TrimSpace(c.ID)
	c.Type = strings.ToLower(strings.TrimSpace(c.Type))

	if c.HTTP != nil {
		h := *c.HTTP
		h.URL = strings.TrimSpace(h.URL)
		h.Method = strings.ToUpper(strings.TrimSpace(h.Method))
		if h.Method == "" {
			h.Method = defaultHTTPMethod
		}
		if h.Timeout == 0 {
			h.Timeout = defaultHTTPTimeout
		}
		h.Headers = trimHeaders(h.Headers)
		c.HTTP = &h
	}
	return c
}

// trimHeaders drops blank header names and values, which is what an unset
// ${VAR} expands to.
func trimHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		key, value := strings.TrimSpace(k), strings.TrimSpace(v)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
