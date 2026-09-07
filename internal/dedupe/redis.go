package dedupe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// keyPrefix namespaces this service's keys so a Redis instance shared
	// with anything else stays legible and collision-free.
	keyPrefix = "dedupe:article:"

	// mgetChunk bounds how many keys go into one MGET. A single source can
	// carry thousands of IDs, and one unbounded command would hold the
	// server for the whole batch and risk the proto write buffer.
	mgetChunk = 1000
)

// Redis is a Redis-backed store of delivered article IDs.
//
// It exists for hosts with no persistent disk — a container platform with
// an ephemeral filesystem loses the bbolt file on every redeploy, and the
// next crawl then republishes every article as if it had never been seen.
//
// Expiry is Redis's own: every key is written with the configured TTL, so
// there is no sweeper and DEDUPE_CLEANUP_INTERVAL does not apply to this
// backend.
//
// This is a store for one harvester process. Two processes sharing one
// Redis can both read an article as unseen before either delivers it,
// because delivery is marked after the fact rather than claimed before —
// the same at-least-once contract the whole service has.
type Redis struct {
	client *redis.Client
	ttl    time.Duration
}

// OpenRedis connects to the Redis instance at url and verifies it answers
// before returning, so a wrong address fails at startup rather than on the
// first crawl.
//
// No error from this package quotes url. A Redis URL routinely carries a
// password, and repeating it in an error would put that password into the
// logs of a service whose whole job is to run unattended.
func OpenRedis(ctx context.Context, url string, ttl time.Duration) (*Redis, error) {
	if strings.TrimSpace(url) == "" {
		return nil, errors.New("dedupe: DEDUPE_REDIS_URL is required for the redis backend")
	}
	if ttl <= 0 {
		return nil, errors.New("dedupe: redis store requires a positive ttl")
	}

	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, errors.New("dedupe: DEDUPE_REDIS_URL is not a valid redis url (expected redis://host:port or rediss://host:port)")
	}

	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("dedupe: redis at %s did not answer: %w", opts.Addr, err)
	}
	return &Redis{client: client, ttl: ttl}, nil
}

// Close releases the connection pool.
func (r *Redis) Close() error { return r.client.Close() }

// Unseen returns the subset of ids that have not been delivered, in the
// order given, with duplicates within ids collapsed. An expired entry
// counts as unseen, because Redis has already removed it.
//
// The batch is answered with MGET rather than one lookup per article, in
// chunks of mgetChunk keys.
func (r *Redis) Unseen(ctx context.Context, ids []string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	// Collapse duplicates first, so the same id is never asked about twice
	// and the returned order is the order it was first seen in.
	unique := make([]string, 0, len(ids))
	asked := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := asked[id]; dup {
			continue
		}
		asked[id] = struct{}{}
		unique = append(unique, id)
	}

	out := make([]string, 0, len(unique))
	for start := 0; start < len(unique); start += mgetChunk {
		end := min(start+mgetChunk, len(unique))
		chunk := unique[start:end]

		keys := make([]string, len(chunk))
		for i, id := range chunk {
			keys[i] = keyPrefix + id
		}

		values, err := r.client.MGet(ctx, keys...).Result()
		if err != nil {
			return nil, fmt.Errorf("dedupe: redis mget: %w", err)
		}
		// MGet returns one entry per key, nil where the key is absent.
		for i, value := range values {
			if value == nil {
				out = append(out, chunk[i])
			}
		}
	}
	return out, nil
}

// Mark records ids as delivered, each with the configured TTL. The whole
// batch is pipelined, so a source costs one round trip rather than one per
// article.
func (r *Redis) Mark(ctx context.Context, ids []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}

	pipe := r.client.Pipeline()
	queued := 0
	for _, id := range ids {
		if id == "" {
			continue
		}
		pipe.Set(ctx, keyPrefix+id, "1", r.ttl)
		queued++
	}
	if queued == 0 {
		return nil
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("dedupe: redis mark: %w", err)
	}
	return nil
}
