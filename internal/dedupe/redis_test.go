package dedupe_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/samvad-hq/samvad-news-harvester/internal/dedupe"
	"github.com/stretchr/testify/require"
)

// openTestRedis starts an in-process Redis and returns a store pointed at
// it, along with the server so a test can advance its clock. miniredis
// keeps the suite hermetic: no network, no container, no shared state
// between tests — the same property httptest.Server gives the fetch paths.
func openTestRedis(t *testing.T, ttl time.Duration) (*dedupe.Redis, *miniredis.Miniredis) {
	t.Helper()

	server := miniredis.RunT(t)
	store, err := dedupe.OpenRedis(context.Background(), "redis://"+server.Addr(), ttl)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store, server
}

func TestRedisUnseenReturnsEverythingOnAnEmptyStore(t *testing.T) {
	t.Parallel()

	store, _ := openTestRedis(t, time.Hour)
	got, err := store.Unseen(context.Background(), []string{"a", "b", "c"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c"}, got)
}

func TestRedisMarkThenUnseenFiltersMarkedIDs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _ := openTestRedis(t, time.Hour)

	require.NoError(t, store.Mark(ctx, []string{"a", "c"}))

	got, err := store.Unseen(ctx, []string{"a", "b", "c", "d"})
	require.NoError(t, err)
	require.Equal(t, []string{"b", "d"}, got)
}

func TestRedisUnseenPreservesInputOrderAndDropsDuplicates(t *testing.T) {
	t.Parallel()

	store, _ := openTestRedis(t, time.Hour)
	got, err := store.Unseen(context.Background(), []string{"c", "a", "c", "b", "a"})
	require.NoError(t, err)
	require.Equal(t, []string{"c", "a", "b"}, got)
}

// Redis expires keys itself, which is the whole reason this backend has no
// sweeper. Advancing miniredis's clock past the TTL must make a marked ID
// read as unseen again.
func TestRedisExpiredIDsReadAsUnseen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, server := openTestRedis(t, time.Minute)

	require.NoError(t, store.Mark(ctx, []string{"a"}))

	got, err := store.Unseen(ctx, []string{"a"})
	require.NoError(t, err)
	require.Empty(t, got, "a freshly marked id is seen")

	server.FastForward(2 * time.Minute)

	got, err = store.Unseen(ctx, []string{"a"})
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, got, "an expired id must read as unseen")
}

func TestRedisMarkIsIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _ := openTestRedis(t, time.Hour)

	require.NoError(t, store.Mark(ctx, []string{"a", "a", "b"}))
	require.NoError(t, store.Mark(ctx, []string{"a"}))

	got, err := store.Unseen(ctx, []string{"a", "b", "c"})
	require.NoError(t, err)
	require.Equal(t, []string{"c"}, got)
}

func TestRedisEmptyInputIsANoOp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _ := openTestRedis(t, time.Hour)

	require.NoError(t, store.Mark(ctx, nil))
	got, err := store.Unseen(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

// A source can carry thousands of IDs, and Unseen chunks its MGET to keep
// any one command bounded. This crosses several chunk boundaries so a
// chunking bug cannot hide behind a batch that fits in one command.
func TestRedisHandlesBatchesLargerThanOneChunk(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _ := openTestRedis(t, time.Hour)

	const total = 2500
	ids := make([]string, 0, total)
	for i := range total {
		ids = append(ids, fmt.Sprintf("id-%04d", i))
	}

	// Mark every even ID; the odd ones must come back unseen, in order.
	marked := make([]string, 0, total/2)
	for i := 0; i < total; i += 2 {
		marked = append(marked, ids[i])
	}
	require.NoError(t, store.Mark(ctx, marked))

	got, err := store.Unseen(ctx, ids)
	require.NoError(t, err)
	require.Len(t, got, total/2)
	require.Equal(t, ids[1], got[0])
	require.Equal(t, ids[total-1], got[len(got)-1])
}

func TestRedisConcurrentUseIsSafe(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _ := openTestRedis(t, time.Hour)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("id-%d", i)
			require.NoError(t, store.Mark(ctx, []string{id}))
			_, err := store.Unseen(ctx, []string{id})
			require.NoError(t, err)
		}()
	}
	wg.Wait()
}

func TestRedisOperationsHonourCancellation(t *testing.T) {
	t.Parallel()

	store, _ := openTestRedis(t, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := store.Unseen(ctx, []string{"a"})
	require.Error(t, err)
	require.Error(t, store.Mark(ctx, []string{"a"}))
}

func TestOpenRedisRejectsABlankURL(t *testing.T) {
	t.Parallel()

	_, err := dedupe.OpenRedis(context.Background(), "   ", time.Hour)
	require.Error(t, err)
}

// A malformed URL must be reported without echoing it: a Redis URL
// carries a password, and an error is the one place it must never land.
func TestOpenRedisDoesNotEchoTheURL(t *testing.T) {
	t.Parallel()

	const secret = "sup3rs3cret"
	_, err := dedupe.OpenRedis(context.Background(), "not-a-url://user:"+secret+"@host:6379", time.Hour)
	require.Error(t, err)
	require.NotContains(t, err.Error(), secret, "the password must never reach an error string")
	require.Contains(t, err.Error(), "DEDUPE_REDIS_URL")
}

func TestOpenRedisFailsWhenTheServerIsUnreachable(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	addr := server.Addr()
	server.Close()

	_, err := dedupe.OpenRedis(context.Background(), "redis://"+addr, time.Hour)
	require.Error(t, err, "a dead server must fail at startup, not on the first crawl")
}
