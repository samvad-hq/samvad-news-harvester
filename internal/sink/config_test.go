package sink_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/sink"
	"github.com/stretchr/testify/require"
)

func writeSinks(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sinks.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestLoadFileReadsEverySinkType(t *testing.T) {
	t.Setenv("TEST_WEBHOOK_URL", "https://hooks.example/news")

	path := writeSinks(t, `
sinks:
  - id: local-log
    type: log
    log:
      level: info
  - id: webhook
    type: http
    enabled: false
    http:
      url: ${TEST_WEBHOOK_URL}
      timeout: 10s
      headers:
        Authorization: Bearer secret
`)

	got, err := sink.LoadFile(path)
	require.NoError(t, err)
	require.Len(t, got, 2)

	require.Equal(t, sink.TypeLog, got[0].Type)
	require.True(t, got[0].IsEnabled(), "a sink with no enabled field defaults to on")

	require.Equal(t, sink.TypeHTTP, got[1].Type)
	require.False(t, got[1].IsEnabled())
	require.Equal(t, "https://hooks.example/news", got[1].HTTP.URL)
	require.Equal(t, 10*time.Second, got[1].HTTP.Timeout)
	require.Equal(t, "POST", got[1].HTTP.Method, "method defaults to POST")
}

func TestLoadFileRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		contains string
	}{
		{
			name:     "missing id",
			body:     "sinks:\n  - type: log\n    log: {}\n",
			contains: "id is required",
		},
		{
			name:     "unknown type",
			body:     "sinks:\n  - id: x\n    type: kafka\n",
			contains: "unsupported type",
		},
		{
			name:     "http without a block",
			body:     "sinks:\n  - id: x\n    type: http\n",
			contains: "http block is required",
		},
		{
			name:     "http without a url",
			body:     "sinks:\n  - id: x\n    type: http\n    http:\n      method: POST\n",
			contains: "http.url is required",
		},
		{
			name:     "http with an unexpanded env var",
			body:     "sinks:\n  - id: x\n    type: http\n    http:\n      url: \"\"\n",
			contains: "http.url is required",
		},
		{
			name:     "sqs missing region",
			body:     "sinks:\n  - id: x\n    type: aws-sqs\n    sqs:\n      queue_url: https://sqs.example/q\n",
			contains: "sqs.region is required",
		},
		{
			name:     "duplicate ids",
			body:     "sinks:\n  - id: x\n    type: log\n    log: {}\n  - id: x\n    type: log\n    log: {}\n",
			contains: "duplicate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := sink.LoadFile(writeSinks(t, tt.body))
			require.ErrorContains(t, err, tt.contains)
		})
	}
}

// TestLoadFileRejectsAMalformedWebhookURLWithoutLeakingItsSecret pins the
// fix for the credential leak the re-review found: a mistyped scheme
// (here "htp") fails url.Parse's scheme check, and the old error message
// reproduced cfg.URL verbatim via %q. For a Slack-style webhook the
// token lives in the path itself, so that error handed the secret to
// anyone who ran -validate and saw it on stderr, or to anything that
// captured the log.
func TestLoadFileRejectsAMalformedWebhookURLWithoutLeakingItsSecret(t *testing.T) {
	t.Parallel()

	const secret = "SUPERSECRETTOKEN"
	_, err := sink.LoadFile(writeSinks(t, "sinks:\n  - id: x\n    type: http\n    http:\n"+
		"      url: htp://hooks.slack.com/services/T00000000/B00000000/"+secret+"\n"))
	require.Error(t, err)
	require.ErrorContains(t, err, "must be an absolute http or https url")
	require.NotContains(t, err.Error(), secret)
}

func TestDisabledSinksAreStillValidated(t *testing.T) {
	t.Parallel()

	// A disabled sink with a broken config should fail loudly now rather
	// than the first time someone enables it in production.
	_, err := sink.LoadFile(writeSinks(t, "sinks:\n  - id: x\n    type: http\n    enabled: false\n"))
	require.Error(t, err)
}
