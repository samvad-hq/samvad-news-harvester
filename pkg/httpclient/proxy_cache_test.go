package httpclient

import (
	"context"
	"testing"
	"time"
)

type stubClient struct{}

func (stubClient) Get(context.Context, string, map[string]string) (Response, error) {
	return nil, nil
}

func TestProxyCacheReturnsDirectWhenBlank(t *testing.T) {
	direct := stubClient{}
	c := NewProxyCache(5*time.Second, direct)

	if c.For("") != direct {
		t.Error("blank proxy should return the direct client")
	}
	if c.For("   ") != direct {
		t.Error("whitespace proxy should return the direct client")
	}
}

func TestProxyCacheCachesPerURL(t *testing.T) {
	c := NewProxyCache(5*time.Second, stubClient{})

	a1 := c.For("http://proxy.example:8080")
	a2 := c.For("http://proxy.example:8080")
	b := c.For("http://other.example:3128")

	if a1 != a2 {
		t.Error("same proxy URL should return the cached client")
	}
	if a1 == b {
		t.Error("different proxy URLs should return different clients")
	}
}
