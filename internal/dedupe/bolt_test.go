package dedupe_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/dedupe"
	"github.com/stretchr/testify/require"
)

func openTestBolt(t *testing.T, ttl, cleanup time.Duration) *dedupe.Bolt {
	t.Helper()
	store, err := dedupe.OpenBolt(filepath.Join(t.TempDir(), "dedupe.db"), ttl, cleanup)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func TestUnseenReturnsEverythingOnAnEmptyStore(t *testing.T) {
	t.Parallel()

	store := openTestBolt(t, time.Hour, time.Hour)
	got, err := store.Unseen(context.Background(), []string{"a", "b", "c"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c"}, got)
}

func TestMarkThenUnseenFiltersMarkedIDs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestBolt(t, time.Hour, time.Hour)

	require.NoError(t, store.Mark(ctx, []string{"a", "c"}))

	got, err := store.Unseen(ctx, []string{"a", "b", "c", "d"})
	require.NoError(t, err)
	require.Equal(t, []string{"b", "d"}, got)
}

func TestUnseenPreservesInputOrderAndDropsDuplicates(t *testing.T) {
	t.Parallel()

	store := openTestBolt(t, time.Hour, time.Hour)
	got, err := store.Unseen(context.Background(), []string{"c", "a", "c", "b", "a"})
	require.NoError(t, err)
	require.Equal(t, []string{"c", "a", "b"}, got)
}

func TestExpiredIDsReadAsUnseen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	// A TTL in the past means every write is already expired.
	store := openTestBolt(t, -time.Second, time.Hour)

	require.NoError(t, store.Mark(ctx, []string{"a"}))

	got, err := store.Unseen(ctx, []string{"a"})
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, got, "an expired id must read as unseen")
}

func TestMarkIsIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestBolt(t, time.Hour, time.Hour)

	require.NoError(t, store.Mark(ctx, []string{"a"}))
	require.NoError(t, store.Mark(ctx, []string{"a"}))

	got, err := store.Unseen(ctx, []string{"a"})
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestEmptyInputIsANoOp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestBolt(t, time.Hour, time.Hour)

	require.NoError(t, store.Mark(ctx, nil))
	got, err := store.Unseen(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestStateSurvivesReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dedupe.db")

	first, err := dedupe.OpenBolt(path, time.Hour, time.Hour)
	require.NoError(t, err)
	require.NoError(t, first.Mark(ctx, []string{"a"}))
	require.NoError(t, first.Close())

	second, err := dedupe.OpenBolt(path, time.Hour, time.Hour)
	require.NoError(t, err)
	defer func() { require.NoError(t, second.Close()) }()

	got, err := second.Unseen(ctx, []string{"a", "b"})
	require.NoError(t, err)
	require.Equal(t, []string{"b"}, got)
}

func TestCleanupRemovesExpiredKeys(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestBolt(t, -time.Second, time.Nanosecond)

	require.NoError(t, store.Mark(ctx, []string{"a", "b"}))

	removed, err := store.Cleanup(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, removed)

	removed, err = store.Cleanup(ctx)
	require.NoError(t, err)
	require.Zero(t, removed)
}

func TestConcurrentUseIsSafe(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestBolt(t, time.Hour, time.Hour)

	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids := make([]string, 50)
			for i := range ids {
				ids[i] = fmt.Sprintf("w%d-%d", worker, i)
			}
			require.NoError(t, store.Mark(ctx, ids))
			_, err := store.Unseen(ctx, ids)
			require.NoError(t, err)
		}()
	}
	wg.Wait()
}

func TestOperationsHonourCancellation(t *testing.T) {
	t.Parallel()

	store := openTestBolt(t, time.Hour, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := store.Unseen(ctx, []string{"a"})
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, store.Mark(ctx, []string{"a"}), context.Canceled)
}

func TestOpenBoltRejectsABlankPath(t *testing.T) {
	t.Parallel()

	_, err := dedupe.OpenBolt("", time.Hour, time.Hour)
	require.Error(t, err)
}

func TestNoopPassesEverythingThrough(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	var store dedupe.Noop

	got, err := store.Unseen(ctx, []string{"a", "b"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, got)
	require.NoError(t, store.Mark(ctx, []string{"a"}))
	require.NoError(t, store.Close())
}
