package sink_test

import (
	"context"
	"testing"

	"github.com/samvad-hq/samvad-news-harvester/internal/sink"
	"github.com/stretchr/testify/require"
)

func TestBuildSkipsDisabledSinks(t *testing.T) {
	t.Parallel()

	enabled := true
	disabled := false

	sinks, closeAll, err := sink.Build(context.Background(), []sink.Config{
		{ID: "on", Type: sink.TypeLog, Enabled: &enabled, Log: &sink.LogConfig{}},
		{ID: "off", Type: sink.TypeLog, Enabled: &disabled, Log: &sink.LogConfig{}},
		{ID: "default-on", Type: sink.TypeLog},
	}, discardLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeAll()) })

	names := make([]string, 0, len(sinks))
	for _, s := range sinks {
		names = append(names, s.Name())
	}
	require.ElementsMatch(t, []string{"on", "default-on"}, names)
}

func TestBuildRejectsAConfigWithNoEnabledSinks(t *testing.T) {
	t.Parallel()

	disabled := false
	_, _, err := sink.Build(context.Background(), []sink.Config{
		{ID: "off", Type: sink.TypeLog, Enabled: &disabled},
	}, discardLogger())
	require.ErrorContains(t, err, "no enabled sinks")
}

func TestBuildRejectsAnUnknownType(t *testing.T) {
	t.Parallel()

	_, _, err := sink.Build(context.Background(), []sink.Config{
		{ID: "x", Type: "kafka"},
	}, discardLogger())
	require.Error(t, err)
}

// TestBuildStopsAtTheFirstFailureAfterBuildingAnEarlierSink exercises the
// partial-failure path: an enabled sink built successfully, followed by
// one that fails to build. Build must still report the error naming the
// bad type, must not panic while unwinding what it already built, and
// must not hand back a usable sink list or closer alongside an error.
func TestBuildStopsAtTheFirstFailureAfterBuildingAnEarlierSink(t *testing.T) {
	t.Parallel()

	sinks, closeAll, err := sink.Build(context.Background(), []sink.Config{
		{ID: "ok", Type: sink.TypeLog},
		{ID: "bad", Type: "kafka"},
	}, discardLogger())

	require.ErrorContains(t, err, "kafka")
	require.Nil(t, sinks)
	require.Nil(t, closeAll)
}
