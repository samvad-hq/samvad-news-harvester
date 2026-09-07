package sink_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/samvad-hq/samvad-news-harvester/internal/sink"
	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeSink records what it was asked to send and can be made to fail.
type fakeSink struct {
	name string
	err  error
	sent []news.Event
}

func (f *fakeSink) Name() string { return f.name }

func (f *fakeSink) Send(_ context.Context, evt news.Event) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, evt)
	return nil
}

func TestFanoutDeliversToEverySink(t *testing.T) {
	t.Parallel()

	a := &fakeSink{name: "a"}
	b := &fakeSink{name: "b"}
	fan := sink.NewFanout([]sink.Sink{a, b}, discardLogger())

	res := fan.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"}))

	require.True(t, res.OK())
	require.NoError(t, res.Err())
	require.ElementsMatch(t, []string{"a", "b"}, res.Delivered)
	require.Empty(t, res.Failed)
	require.Len(t, a.sent, 1)
	require.Len(t, b.sent, 1)
}

// This is the C2 regression test. A partial success must not report OK,
// because the caller marks the article seen on OK and the failing sink
// would then never see it again.
func TestPartialFailureIsNotOK(t *testing.T) {
	t.Parallel()

	boom := errors.New("webhook unreachable")
	ok := &fakeSink{name: "sqs"}
	bad := &fakeSink{name: "webhook", err: boom}
	fan := sink.NewFanout([]sink.Sink{ok, bad}, discardLogger())

	res := fan.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"}))

	require.False(t, res.OK(), "one sink failed, so the article is not delivered")
	require.Equal(t, []string{"sqs"}, res.Delivered)
	require.Len(t, res.Failed, 1)
	require.ErrorIs(t, res.Failed["webhook"], boom)
	require.ErrorIs(t, res.Err(), boom)
}

// TestFanoutLogsEachFailedSink pins the fix for a dead logger: Fanout took
// a *slog.Logger and never called it, so the only record of a rejected
// event was a Result the caller had to inspect correctly. A failure must
// now produce a log line naming the sink and the article.
func TestFanoutLogsEachFailedSink(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	boom := errors.New("webhook unreachable")
	fan := sink.NewFanout([]sink.Sink{&fakeSink{name: "webhook", err: boom}}, logger)

	res := fan.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "art-1"}))
	require.False(t, res.OK())

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	require.Equal(t, "WARN", line["level"])
	require.Equal(t, "sink rejected event", line["msg"])
	require.Equal(t, "webhook", line["sink"])
	require.Equal(t, "art-1", line["article_id"])
	require.Contains(t, line["error"], "webhook unreachable")
}

func TestFanoutReportsEverySinkThatFailed(t *testing.T) {
	t.Parallel()

	first := errors.New("first down")
	second := errors.New("second down")
	fan := sink.NewFanout([]sink.Sink{
		&fakeSink{name: "a", err: first},
		&fakeSink{name: "b", err: second},
	}, discardLogger())

	res := fan.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"}))

	require.False(t, res.OK())
	require.Empty(t, res.Delivered)
	require.Len(t, res.Failed, 2)
	require.ErrorIs(t, res.Err(), first)
	require.ErrorIs(t, res.Err(), second)
}

func TestFanoutWithNoSinksIsNotOK(t *testing.T) {
	t.Parallel()

	fan := sink.NewFanout(nil, discardLogger())
	res := fan.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"}))

	require.False(t, res.OK(), "delivering to nothing is not delivery")
	require.Zero(t, fan.Len())
}

func TestFanoutDropsNilSinks(t *testing.T) {
	t.Parallel()

	fan := sink.NewFanout([]sink.Sink{nil, &fakeSink{name: "a"}, nil}, discardLogger())
	require.Equal(t, 1, fan.Len())
}

func TestFanoutSendsConcurrently(t *testing.T) {
	t.Parallel()

	// Each sink blocks until all three have arrived; a sequential fan-out
	// would deadlock and fail the test by timeout.
	gate := make(chan struct{}, 3)
	release := make(chan struct{})

	blocking := func(name string) sink.Sink { return &gatedSink{name: name, gate: gate, release: release} }
	fan := sink.NewFanout([]sink.Sink{blocking("a"), blocking("b"), blocking("c")}, discardLogger())

	go func() {
		for range 3 {
			<-gate
		}
		close(release)
	}()

	res := fan.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"}))
	require.True(t, res.OK())
}

type gatedSink struct {
	name    string
	gate    chan struct{}
	release chan struct{}
}

func (g *gatedSink) Name() string { return g.name }

func (g *gatedSink) Send(context.Context, news.Event) error {
	g.gate <- struct{}{}
	<-g.release
	return nil
}
