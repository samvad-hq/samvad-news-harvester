package storage

import (
	"testing"
	"time"
)

func TestBoltStoreMarksAndExpiresArticles(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		ArticleTTL:      1 * time.Second,
		CleanupInterval: 1 * time.Second,
	}

	storeRaw, err := openBolt(dir+"/cache.db", opts)
	if err != nil {
		t.Fatalf("openBolt: %v", err)
	}
	store := storeRaw.(*boltStore)
	defer store.Close()

	seen, err := store.SeenArticle("id1")
	if err != nil || seen {
		t.Fatalf("expected unseen article, seen=%v err=%v", seen, err)
	}

	if err := store.MarkArticle("id1"); err != nil {
		t.Fatalf("MarkArticle: %v", err)
	}

	seen, err = store.SeenArticle("id1")
	if err != nil || !seen {
		t.Fatalf("expected article marked as seen, got seen=%v err=%v", seen, err)
	}

	// Fast-forward cleanup cadence and trigger expiry.
	store.lastCleanup.Store(time.Now().Add(-2 * time.Second).Unix())
	time.Sleep(1100 * time.Millisecond)

	seen, err = store.SeenArticle("id1")
	if err != nil {
		t.Fatalf("SeenArticle after expiry: %v", err)
	}
	if seen {
		t.Fatalf("expected entry to expire and be removed")
	}
}

func TestNewStoreSupportsNoop(t *testing.T) {
	store, err := NewStore("none", "", Options{})
	if err != nil {
		t.Fatalf("NewStore none: %v", err)
	}
	if err := store.MarkArticle("x"); err != nil {
		t.Fatalf("noop store MarkArticle: %v", err)
	}
}
