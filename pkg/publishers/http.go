package publishers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/samvad-hq/samvad-news-harvester/pkg/httpclient"
)

// httpPublisher implements the Publisher interface for HTTP endpoints.
type httpPublisher struct {
	id      string
	method  string
	url     string
	headers map[string]string
	client  *resty.Client
	typ     string
	log     Logger
}

// newHTTPPublisher creates a new HTTP publisher with the given configuration.
func newHTTPPublisher(_ context.Context, cfg PublisherConfig, log Logger) (Publisher, error) {
	if cfg.HTTP == nil {
		return nil, fmt.Errorf("publisher %q missing http configuration", cfg.ID)
	}

	client := httpclient.NewRestyHTTPClient(time.Duration(cfg.HTTP.TimeoutSeconds) * time.Second)

	return &httpPublisher{
		id:      cfg.ID,
		typ:     TypeHTTP,
		method:  cfg.HTTP.Method,
		url:     cfg.HTTP.URL,
		headers: cfg.HTTP.Headers,
		client:  client,
		log:     ensureLogger(log),
	}, nil
}

func (h *httpPublisher) ID() string   { return h.id }
func (h *httpPublisher) Type() string { return h.typ }

// Publish sends the event to the configured HTTP endpoint.
func (h *httpPublisher) Publish(ctx context.Context, evt Event) error {
	req := h.client.R().
		SetContext(ctx).
		SetBody(evt)

	if len(h.headers) > 0 {
		req.SetHeaders(h.headers)
	}

	req.SetHeader("Content-Type", "application/json")

	resp, err := req.Execute(h.method, h.url)
	if err != nil {
		h.log.ErrorObj("http publisher request failed", "publisher_http_error", map[string]any{
			"publisher_id": h.id,
			"error":        err.Error(),
		})
		return fmt.Errorf("http request: %w", err)
	}
	if resp.IsError() {
		snippet := readBodySnippet(resp.Body())
		h.log.WarnObj("http publisher response error", "publisher_http_error", map[string]any{
			"publisher_id": h.id,
			"status_code":  resp.StatusCode(),
			"body_snippet": snippet,
		})
		return fmt.Errorf("http response status %d: %s", resp.StatusCode(), snippet)
	}
	h.log.DebugObj("http publisher delivered event", "publisher_http_delivery", map[string]any{
		"publisher_id": h.id,
		"status_code":  resp.StatusCode(),
	})
	return nil
}

// readBodySnippet returns a trimmed snippet of the response body for error messages.
func readBodySnippet(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if len(body) > 512 {
		body = body[:512]
	}
	return strings.TrimSpace(string(body))
}
