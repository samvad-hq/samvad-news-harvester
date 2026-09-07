package dedupe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	bucketName   = "articles"
	expiryLength = 8
)

// Bolt is a bbolt-backed store of delivered article IDs.
//
// Reads use a read transaction. The previous implementation used a write
// transaction so it could delete expired keys on the way past, which meant
// every lookup contended for bbolt's single writer lock and serialised the
// entire crawl. An expired key now simply reads as unseen and the periodic
// sweep reclaims it.
type Bolt struct {
	db              *bolt.DB
	ttl             time.Duration
	cleanupInterval time.Duration

	cleanupMu   sync.Mutex
	lastCleanup atomic.Int64
}

// OpenBolt opens or creates the store at path. Parent directories are
// created as needed.
func OpenBolt(path string, ttl, cleanupInterval time.Duration) (*Bolt, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("dedupe: bolt store requires a path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("dedupe: create directory for %s: %w", path, err)
		}
	}

	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("dedupe: open %s: %w", path, err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketName))
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("dedupe: create bucket: %w", err)
	}

	store := &Bolt{db: db, ttl: ttl, cleanupInterval: cleanupInterval}
	store.lastCleanup.Store(time.Now().Unix())
	return store, nil
}

// Close releases the underlying database.
func (b *Bolt) Close() error {
	return b.db.Close()
}

// Unseen returns the subset of ids that have not been delivered, in the
// order given, with duplicates within ids collapsed. An expired entry
// counts as unseen.
//
// The whole batch is answered in one read transaction.
func (b *Bolt) Unseen(ctx context.Context, ids []string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	now := time.Now()
	out := make([]string, 0, len(ids))
	asked := make(map[string]struct{}, len(ids))

	err := b.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(bucketName))
		if bucket == nil {
			return errors.New("dedupe: articles bucket is missing")
		}
		for _, id := range ids {
			if _, dup := asked[id]; dup {
				continue
			}
			asked[id] = struct{}{}

			value := bucket.Get([]byte(id))
			if expiry, ok := decodeExpiry(value); ok && expiry.After(now) {
				continue
			}
			out = append(out, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if err := b.maybeCleanup(ctx, now); err != nil {
		return nil, err
	}
	return out, nil
}

// Mark records ids as delivered. The whole batch is one transaction, so a
// source contributes one fsync rather than one per article.
func (b *Bolt) Mark(ctx context.Context, ids []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}

	expiry := make([]byte, expiryLength)
	binary.BigEndian.PutUint64(expiry, uint64(time.Now().Add(b.ttl).Unix())) //nolint:gosec // unix seconds fit

	return b.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(bucketName))
		if bucket == nil {
			return errors.New("dedupe: articles bucket is missing")
		}
		for _, id := range ids {
			if id == "" {
				continue
			}
			if err := bucket.Put([]byte(id), expiry); err != nil {
				return err
			}
		}
		return nil
	})
}

// Cleanup removes every expired entry and returns how many were deleted.
// It is exported so the crawl loop can sweep on its own schedule and so
// the behaviour is directly testable.
func (b *Bolt) Cleanup(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	now := time.Now()
	removed := 0

	err := b.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(bucketName))
		if bucket == nil {
			return errors.New("dedupe: articles bucket is missing")
		}
		cursor := bucket.Cursor()
		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
			expiry, ok := decodeExpiry(v)
			if ok && expiry.After(now) {
				continue
			}
			if err := cursor.Delete(); err != nil {
				return err
			}
			removed++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	b.lastCleanup.Store(now.Unix())
	return removed, nil
}

// maybeCleanup sweeps expired entries at most once per cleanup interval.
func (b *Bolt) maybeCleanup(ctx context.Context, now time.Time) error {
	if now.Sub(time.Unix(b.lastCleanup.Load(), 0)) < b.cleanupInterval {
		return nil
	}

	b.cleanupMu.Lock()
	defer b.cleanupMu.Unlock()

	// Re-check under the lock: another goroutine may have just swept.
	if now.Sub(time.Unix(b.lastCleanup.Load(), 0)) < b.cleanupInterval {
		return nil
	}

	_, err := b.Cleanup(ctx)
	return err
}

// decodeExpiry reads a stored expiry timestamp.
func decodeExpiry(value []byte) (time.Time, bool) {
	if len(value) != expiryLength {
		return time.Time{}, false
	}
	unix := int64(binary.BigEndian.Uint64(value)) //nolint:gosec // written by Mark
	if unix <= 0 {
		return time.Time{}, false
	}
	return time.Unix(unix, 0), true
}
