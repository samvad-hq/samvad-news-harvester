package httpclient

import "context"

// Response is a minimal HTTP response contract.
type Response interface {
	Body() []byte
	StatusCode() int
}

// Client abstracts HTTP calls so callers can inject mocks or different transports.
type Client interface {
	Get(ctx context.Context, url string, headers map[string]string) (Response, error)
}
