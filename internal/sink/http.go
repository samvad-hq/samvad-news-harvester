package sink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/samvad-hq/samvad-news-harvester/internal/news"
)

// maxErrorBodyBytes bounds how much of a failing response is quoted back
// in the error.
const maxErrorBodyBytes = 512

// HTTP delivers events to a webhook.
type HTTP struct {
	id       string
	method   string
	url      string
	redacted string
	headers  map[string]string
	client   *http.Client
}

// NewHTTP returns a webhook sink.
func NewHTTP(id string, cfg HTTPConfig) (*HTTP, error) {
	method := cfg.Method
	if method == "" {
		method = defaultHTTPMethod
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	return &HTTP{
		id:       id,
		method:   method,
		url:      cfg.URL,
		redacted: redactURL(cfg.URL),
		headers:  cfg.Headers,
		client:   &http.Client{Timeout: timeout},
	}, nil
}

// Name returns the sink ID.
func (h *HTTP) Name() string { return h.id }

// Send posts the event as JSON. A non-2xx response is an error, with a
// bounded snippet of the body so the cause is visible in the log.
func (h *HTTP) Send(ctx context.Context, evt news.Event) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, h.method, h.url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("post to %s: %w", h.redacted, stripRequestURL(err))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return fmt.Errorf("webhook returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// stripRequestURL unwraps a *url.Error to the network failure it carries.
//
// net/http.Client.Do wraps every transport failure in a *url.Error whose
// own Error() string embeds the full request URL verbatim — so redacting
// h.url before formatting our own message is not enough on its own; %w
// on the raw err would still print the secret path through the wrapped
// error's text. The network error url.Error carries (a dial failure, a
// context error) never contains the URL, so unwrapping to it keeps
// errors.Is/As working — including the context-cancellation case this
// sink is tested against — while dropping the leak. Anything that is not
// a *url.Error is returned unchanged, which is what err already was
// before this fix.
func stripRequestURL(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err
	}
	return err
}

// redactURL returns a form of raw safe to put in a log or error message.
// For a Slack, Discord or Microsoft Teams incoming webhook the URL itself
// is the bearer credential — the token lives in the path — so this keeps
// only the scheme and host, which is enough for an operator to tell one
// failing webhook from another, and drops everything that could be a
// secret: userinfo, query string, and the path itself, which is replaced
// with a marker rather than reproduced. A URL with no path (or just "/")
// carries nothing to redact and is returned as-is.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<unparseable url>"
	}
	if u.Path == "" || u.Path == "/" {
		return fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, u.Path)
	}
	return fmt.Sprintf("%s://%s/…", u.Scheme, u.Host)
}
