package storage

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	articleBucket    = "articles"
	expiryValueBytes = 8
)

// boltStore implements a Store backed by BoltDB.
type boltStore struct {
	db              *bolt.DB
	cleanupMu       sync.Mutex
	lastCleanup     atomic.Int64
	articleTTL      time.Duration
	cleanupInterval time.Duration
}

// openBolt initializes a BoltDB-backed Store.
func openBolt(path string, opts Options) (Store, error) {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create storage directory: %w", err)
		}
	}

	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open bbolt db: %w", err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(articleBucket))
		return err
	}); err != nil {
		db.Close()
		return nil, fmt.Errorf("init bucket: %w", err)
	}

	store := &boltStore{
		db:              db,
		articleTTL:      opts.ArticleTTL,
		cleanupInterval: opts.CleanupInterval,
	}
	store.lastCleanup.Store(time.Now().Unix())
	return store, nil
}

// Close closes the BoltDB store.
func (b *boltStore) Close() error {
	if b == nil || b.db == nil {
		return nil
	}
	return b.db.Close()
}

// SeenArticle checks if an article with the given ID has been seen.
func (b *boltStore) SeenArticle(id string) (bool, error) {
	if b == nil || b.db == nil {
		return false, nil
	}

	if err := b.maybeCleanupExpired(time.Now()); err != nil {
		return false, err
	}

	var exists bool
	err := b.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(articleBucket))
		if bucket == nil {
			return fmt.Errorf("article bucket missing")
		}

		key := []byte(id)
		value := bucket.Get(key)
		if value == nil {
			exists = false
			return nil
		}

		expiry, ok := decodeExpiry(value)
		if !ok || !expiry.After(time.Now()) {
			exists = false
			return bucket.Delete(key)
		}

		exists = true
		return nil
	})
	return exists, err
}

// MarkArticle marks an article with the given ID as seen.
func (b *boltStore) MarkArticle(id string) error {
	if b == nil || b.db == nil {
		return nil
	}

	now := time.Now()
	if err := b.maybeCleanupExpired(now); err != nil {
		return err
	}

	return b.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(articleBucket))
		if bucket == nil {
			return fmt.Errorf("article bucket missing")
		}
		buf := make([]byte, expiryValueBytes)
		binary.BigEndian.PutUint64(buf, uint64(now.Add(b.articleTTL).Unix()))
		return bucket.Put([]byte(id), buf)
	})
}

// maybeCleanupExpired removes expired article hashes on a fixed cadence to avoid unbounded growth.
func (b *boltStore) maybeCleanupExpired(now time.Time) error {
	if b == nil || b.db == nil {
		return nil
	}

	last := time.Unix(b.lastCleanup.Load(), 0)
	if now.Sub(last) < b.cleanupInterval {
		return nil
	}

	b.cleanupMu.Lock()
	defer b.cleanupMu.Unlock()

	last = time.Unix(b.lastCleanup.Load(), 0)
	if now.Sub(last) < b.cleanupInterval {
		return nil
	}

	err := b.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(articleBucket))
		if bucket == nil {
			return fmt.Errorf("article bucket missing")
		}

		cursor := bucket.Cursor()
		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
			expiry, ok := decodeExpiry(v)
			if !ok || !expiry.After(now) {
				if err := cursor.Delete(); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil {
		b.lastCleanup.Store(now.Unix())
	}
	return err
}

// decodeExpiry decodes the expiry time from the stored byte slice.
func decodeExpiry(value []byte) (time.Time, bool) {
	if len(value) != expiryValueBytes {
		return time.Time{}, false
	}
	unix := int64(binary.BigEndian.Uint64(value))
	if unix <= 0 {
		return time.Time{}, false
	}
	return time.Unix(unix, 0), true
}
