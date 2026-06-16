package publishers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

const (
	// Supported publisher types.
	TypeQueue = "queue"
	TypeHTTP  = "http"

	// Supported queue providers.
	QueueProviderAWSSQS = "aws-sqs"
	QueueProviderAWSSNS = "aws-sns"
	QueueProviderAzure  = "azure"
	QueueProviderGCP    = "gcp"

	httpDefaultMethod         = "POST"
	httpDefaultTimeoutSeconds = 5
)

// configFile represents the structure of the publishers configuration file.
type configFile struct {
	Publishers []PublisherConfig `json:"publishers" yaml:"publishers"`
}

// PublisherConfig represents a single publisher entry declared in config files.
type PublisherConfig struct {
	ID      string                `json:"id" yaml:"id"`
	Type    string                `json:"type" yaml:"type"`
	Enabled *bool                 `json:"enabled" yaml:"enabled"`
	Queue   *QueuePublisherConfig `json:"queue" yaml:"queue"`
	HTTP    *HTTPPublisherConfig  `json:"http" yaml:"http"`
}

// QueuePublisherConfig allows selecting a cloud queue provider.
type QueuePublisherConfig struct {
	Provider string                 `json:"provider" yaml:"provider"`
	AWS      *AWSSQSPublisherConfig `json:"aws" yaml:"aws"`
	SNS      *AWSSNSPublisherConfig `json:"sns" yaml:"sns"`
	Azure    *AzureQueueConfig      `json:"azure" yaml:"azure"`
	GCP      *GCPQueueConfig        `json:"gcp" yaml:"gcp"`
}

// AWSSQSPublisherConfig holds AWS SQS specific settings.
type AWSSQSPublisherConfig struct {
	QueueURL        string `json:"uri" yaml:"uri"`
	Region          string `json:"region" yaml:"region"`
	AccessKeyID     string `json:"access_key_id" yaml:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key" yaml:"secret_access_key"`
}

// AWSSNSPublisherConfig holds AWS SNS specific settings.
type AWSSNSPublisherConfig struct {
	TopicARN        string `json:"topic_arn" yaml:"topic_arn"`
	Region          string `json:"region" yaml:"region"`
	AccessKeyID     string `json:"access_key_id" yaml:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key" yaml:"secret_access_key"`
}

// AzureQueueConfig holds the minimal Service Bus queue settings.
type AzureQueueConfig struct {
	ConnectionString string `json:"connection_string" yaml:"connection_string"`
	QueueName        string `json:"queue" yaml:"queue"`
}

// GCPQueueConfig holds the minimal Pub/Sub topic settings.
type GCPQueueConfig struct {
	ProjectID       string `json:"project_id" yaml:"project_id"`
	Topic           string `json:"topic" yaml:"topic"`
	CredentialsFile string `json:"credentials_file" yaml:"credentials_file"`
}

// HTTPPublisherConfig holds generic HTTP sink settings.
type HTTPPublisherConfig struct {
	URL            string            `json:"url" yaml:"url"`
	Method         string            `json:"method" yaml:"method"`
	Headers        map[string]string `json:"headers" yaml:"headers"`
	TimeoutSeconds int               `json:"timeout_seconds" yaml:"timeout_seconds"`
}

// ConfigRegistry materializes publisher definitions loaded from config files.
type ConfigRegistry struct {
	mu         sync.RWMutex
	publishers []PublisherConfig
	idx        map[string]PublisherConfig
}

// LoadRegistry loads the publisher registry from a YAML/JSON file.
func LoadRegistry(path string) (*ConfigRegistry, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("publishers file path is empty")
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open publishers file: %w", err)
	}
	defer file.Close()

	raw, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read publishers file: %w", err)
	}

	expanded := []byte(os.ExpandEnv(string(raw)))

	fileReg, err := parsePublisherRegistry(expanded, filepath.Ext(path))
	if err != nil {
		return nil, err
	}
	if len(fileReg.Publishers) == 0 {
		return nil, errors.New("publishers file contains no publishers entries")
	}

	reg := &ConfigRegistry{
		publishers: make([]PublisherConfig, len(fileReg.Publishers)),
		idx:        make(map[string]PublisherConfig, len(fileReg.Publishers)),
	}

	for i := range fileReg.Publishers {
		cfg := sanitizePublisherConfig(fileReg.Publishers[i])
		if err := validatePublisherConfig(cfg); err != nil {
			return nil, fmt.Errorf("publishers[%d]: %w", i, err)
		}
		if _, exists := reg.idx[cfg.ID]; exists {
			return nil, fmt.Errorf("duplicate publisher id %q", cfg.ID)
		}
		reg.publishers[i] = cfg
		reg.idx[cfg.ID] = cfg
	}

	return reg, nil
}

// parsePublisherRegistry attempts to decode the publishers file content.
func parsePublisherRegistry(data []byte, ext string) (configFile, error) {
	ext = strings.ToLower(strings.TrimSpace(ext))
	decoders := []struct {
		name string
		ext  string
		fn   func([]byte, any) error
	}{
		{name: "yaml", ext: ".yaml", fn: yaml.Unmarshal},
		{name: "yaml", ext: ".yml", fn: yaml.Unmarshal},
		{name: "json", ext: ".json", fn: json.Unmarshal},
	}

	for _, d := range decoders {
		if ext != "" && ext != d.ext {
			continue
		}
		if reg, err := unmarshalPublisherRegistry(d.name, data, d.fn); err == nil {
			return reg, nil
		}
	}

	return configFile{}, errors.New("publishers file format not recognized (expected YAML or JSON)")
}

// unmarshalPublisherRegistry decodes the publishers file using the provided function.
func unmarshalPublisherRegistry(name string, data []byte, fn func([]byte, any) error) (configFile, error) {
	var reg configFile
	if err := fn(data, &reg); err != nil {
		return configFile{}, fmt.Errorf("decode %s publishers: %w", name, err)
	}
	return reg, nil
}

// sanitizePublisherConfig trims and normalizes the publisher config fields.
func sanitizePublisherConfig(cfg PublisherConfig) PublisherConfig {
	cfg.ID = strings.TrimSpace(cfg.ID)
	cfg.Type = strings.ToLower(strings.TrimSpace(cfg.Type))

	if cfg.Enabled == nil {
		def := true
		cfg.Enabled = &def
	}
	if cfg.Queue != nil {
		qc := *cfg.Queue
		qc.Provider = strings.ToLower(strings.TrimSpace(qc.Provider))
		if qc.AWS != nil {
			a := *qc.AWS
			a.QueueURL = strings.TrimSpace(a.QueueURL)
			a.Region = strings.TrimSpace(a.Region)
			a.AccessKeyID = strings.TrimSpace(a.AccessKeyID)
			a.SecretAccessKey = strings.TrimSpace(a.SecretAccessKey)
			qc.AWS = &a
		}
		if qc.SNS != nil {
			s := *qc.SNS
			s.TopicARN = strings.TrimSpace(s.TopicARN)
			s.Region = strings.TrimSpace(s.Region)
			s.AccessKeyID = strings.TrimSpace(s.AccessKeyID)
			s.SecretAccessKey = strings.TrimSpace(s.SecretAccessKey)
			qc.SNS = &s
		}
		if qc.Azure != nil {
			a := *qc.Azure
			a.ConnectionString = strings.TrimSpace(a.ConnectionString)
			a.QueueName = strings.TrimSpace(a.QueueName)
			qc.Azure = &a
		}
		if qc.GCP != nil {
			g := *qc.GCP
			g.ProjectID = strings.TrimSpace(g.ProjectID)
			g.Topic = strings.TrimSpace(g.Topic)
			g.CredentialsFile = strings.TrimSpace(g.CredentialsFile)
			qc.GCP = &g
		}
		cfg.Queue = &qc
	}
	if cfg.HTTP != nil {
		c := *cfg.HTTP
		c.URL = strings.TrimSpace(c.URL)
		c.Method = strings.ToUpper(strings.TrimSpace(c.Method))
		if c.Method == "" {
			c.Method = httpDefaultMethod
		}
		c.Headers = sanitizeHeaders(c.Headers)
		if c.TimeoutSeconds <= 0 {
			c.TimeoutSeconds = httpDefaultTimeoutSeconds
		}
		cfg.HTTP = &c
	}

	return cfg
}

// sanitizeHeaders trims and removes empty headers.
func sanitizeHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		if key == "" || val == "" {
			continue
		}
		out[key] = val
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validatePublisherConfig checks that required fields are present.
func validatePublisherConfig(cfg PublisherConfig) error {
	if cfg.ID == "" {
		return errors.New("id is required")
	}
	if cfg.Type == "" {
		return fmt.Errorf("type is required for publisher %q", cfg.ID)
	}
	switch cfg.Type {
	case TypeQueue:
		if cfg.Queue == nil {
			return fmt.Errorf("queue config required for publisher %q", cfg.ID)
		}
		switch cfg.Queue.Provider {
		case QueueProviderAWSSQS:
			if err := validateSQSConfig(cfg.ID, cfg.Queue.AWS); err != nil {
				return err
			}
		case QueueProviderAWSSNS:
			if err := validateSNSConfig(cfg.ID, cfg.Queue.SNS); err != nil {
				return err
			}
		case QueueProviderGCP:
			if err := validateGCPConfig(cfg.ID, cfg.Queue.GCP); err != nil {
				return err
			}
		case QueueProviderAzure:
			return fmt.Errorf("queue provider %q not implemented for publisher %q", cfg.Queue.Provider, cfg.ID)
		default:
			return fmt.Errorf("queue provider %q not supported for publisher %q", cfg.Queue.Provider, cfg.ID)
		}
	case TypeHTTP:
		if cfg.HTTP == nil {
			return fmt.Errorf("http config required for publisher %q", cfg.ID)
		}
		if cfg.HTTP.URL == "" {
			return fmt.Errorf("http.url is required for publisher %q", cfg.ID)
		}
	default:
		return fmt.Errorf("type %q not supported for publisher %q", cfg.Type, cfg.ID)
	}
	return nil
}

func validateSQSConfig(id string, cfg *AWSSQSPublisherConfig) error {
	if cfg == nil {
		return fmt.Errorf("sqs config required for publisher %q", id)
	}
	if cfg.QueueURL == "" {
		return fmt.Errorf("sqs.uri is required for publisher %q", id)
	}
	if cfg.Region == "" {
		return fmt.Errorf("sqs.region is required for publisher %q", id)
	}
	if cfg.AccessKeyID == "" {
		return fmt.Errorf("sqs.access_key_id is required for publisher %q", id)
	}
	if cfg.SecretAccessKey == "" {
		return fmt.Errorf("sqs.secret_access_key is required for publisher %q", id)
	}
	return nil
}

func validateSNSConfig(id string, cfg *AWSSNSPublisherConfig) error {
	if cfg == nil {
		return fmt.Errorf("sns config required for publisher %q", id)
	}
	if cfg.TopicARN == "" {
		return fmt.Errorf("sns.topic_arn is required for publisher %q", id)
	}
	if cfg.Region == "" {
		return fmt.Errorf("sns.region is required for publisher %q", id)
	}
	if cfg.AccessKeyID == "" {
		return fmt.Errorf("sns.access_key_id is required for publisher %q", id)
	}
	if cfg.SecretAccessKey == "" {
		return fmt.Errorf("sns.secret_access_key is required for publisher %q", id)
	}
	return nil
}

func validateGCPConfig(id string, cfg *GCPQueueConfig) error {
	if cfg == nil {
		return fmt.Errorf("gcp config required for publisher %q", id)
	}
	if cfg.ProjectID == "" {
		return fmt.Errorf("gcp.project_id is required for publisher %q", id)
	}
	if cfg.Topic == "" {
		return fmt.Errorf("gcp.topic is required for publisher %q", id)
	}
	return nil
}

// PublisherByID returns the publisher config by id.
func (r *ConfigRegistry) ByID(id string) (PublisherConfig, bool) {
	if r == nil {
		return PublisherConfig{}, false
	}

	id = strings.TrimSpace(id)
	if id == "" {
		return PublisherConfig{}, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg, ok := r.idx[id]
	return cfg, ok
}

// All returns all configured publishers.
func (r *ConfigRegistry) All() []PublisherConfig {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]PublisherConfig, len(r.publishers))
	copy(out, r.publishers)
	return out
}

// Enabled returns publishers that are enabled.
func (r *ConfigRegistry) Enabled() []PublisherConfig {
	if r == nil {
		return nil
	}

	all := r.All()
	if len(all) == 0 {
		return nil
	}

	out := make([]PublisherConfig, 0, len(all))
	for _, cfg := range all {
		if cfg.EnabledValue() {
			out = append(out, cfg)
		}
	}
	return out
}

// EnabledValue returns enabled flag defaulting to true.
func (cfg PublisherConfig) EnabledValue() bool {
	if cfg.Enabled == nil {
		return true
	}
	return *cfg.Enabled
}
