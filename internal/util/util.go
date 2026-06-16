package util

// Utility helpers (retry, rate-limit, etc.).

// Retry is a small helper placeholder.
func Retry(fn func() error) error {
	// TODO: add backoff/retry
	return fn()
}
