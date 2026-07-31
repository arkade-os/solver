package swap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPriceCacheGet(t *testing.T) {
	ctx := context.Background()

	t.Run("miss then hit", func(t *testing.T) {
		url, hits := feedServer(t, func() (string, int) { return `{"price":42}`, http.StatusOK })
		cache := newPriceCache()

		for range 2 {
			price, err := cache.get(ctx, url, "/price", time.Minute)
			require.NoError(t, err)
			require.Equal(t, 42.0, price)
		}
		require.EqualValues(t, 1, *hits, "second call must be served from cache")
	})

	t.Run("refetch after ttl", func(t *testing.T) {
		body := `{"price":42}`
		url, hits := feedServer(t, func() (string, int) { return body, http.StatusOK })
		cache := newPriceCache()

		_, err := cache.get(ctx, url, "/price", time.Millisecond)
		require.NoError(t, err)

		time.Sleep(5 * time.Millisecond)
		body = `{"price":99}`

		price, err := cache.get(ctx, url, "/price", time.Millisecond)
		require.NoError(t, err)
		require.Equal(t, 99.0, price)
		require.EqualValues(t, 2, *hits)
	})

	t.Run("stale cache on fetch error", func(t *testing.T) {
		ok := true
		url, _ := feedServer(t, func() (string, int) {
			if ok {
				return `{"price":42}`, http.StatusOK
			}
			return "boom", http.StatusInternalServerError
		})
		cache := newPriceCache()
		ttl := 20 * time.Millisecond

		_, err := cache.get(ctx, url, "/price", ttl)
		require.NoError(t, err)

		time.Sleep(30 * time.Millisecond)
		ok = false

		price, err := cache.get(ctx, url, "/price", ttl)
		require.ErrorContains(t, err, "using stale cache")
		require.Equal(t, 42.0, price)
	})

	t.Run("cache too stale to serve", func(t *testing.T) {
		ok := true
		url, _ := feedServer(t, func() (string, int) {
			if ok {
				return `{"price":42}`, http.StatusOK
			}
			return "boom", http.StatusInternalServerError
		})
		cache := newPriceCache()
		ttl := time.Millisecond

		_, err := cache.get(ctx, url, "/price", ttl)
		require.NoError(t, err)

		time.Sleep(20 * time.Millisecond)
		ok = false

		price, err := cache.get(ctx, url, "/price", ttl)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "using stale cache")
		require.Zero(t, price)
	})

	t.Run("fetch error without cache", func(t *testing.T) {
		url, _ := feedServer(t, func() (string, int) { return "boom", http.StatusInternalServerError })
		cache := newPriceCache()

		price, err := cache.get(ctx, url, "/price", time.Minute)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "using stale cache")
		require.Zero(t, price)
	})

	t.Run("url whitespace is trimmed", func(t *testing.T) {
		url, hits := feedServer(t, func() (string, int) { return `{"price":7.5}`, http.StatusOK })
		cache := newPriceCache()

		for _, u := range []string{"  " + url + "  ", url} {
			price, err := cache.get(ctx, u, "/price", time.Minute)
			require.NoError(t, err)
			require.Equal(t, 7.5, price)
		}
		require.EqualValues(t, 1, *hits, "trimmed URLs share one cache entry")
	})

	t.Run("price path is part of the key", func(t *testing.T) {
		url, hits := feedServer(t, func() (string, int) { return `{"a":1,"b":2}`, http.StatusOK })
		cache := newPriceCache()

		a, err := cache.get(ctx, url, "/a", time.Minute)
		require.NoError(t, err)
		require.Equal(t, 1.0, a)

		b, err := cache.get(ctx, url, "/b", time.Minute)
		require.NoError(t, err)
		require.Equal(t, 2.0, b)
		require.EqualValues(t, 2, *hits)
	})
}

func TestValidatePrice(t *testing.T) {
	tests := []struct {
		name        string
		offerPrice  float64
		feedPrice   float64
		slippageBps uint32
		dir         Direction
		want        bool
	}{
		{name: "sell exact match", offerPrice: 100, feedPrice: 100, slippageBps: 100, dir: Sell, want: true},
		{name: "sell at upper bound", offerPrice: 101, feedPrice: 100, slippageBps: 100, dir: Sell, want: true},
		{name: "sell above upper bound", offerPrice: 101.1, feedPrice: 100, slippageBps: 100, dir: Sell, want: false},
		{name: "sell far below feed is a gift", offerPrice: 1, feedPrice: 100, slippageBps: 10, dir: Sell, want: true},
		{name: "buy at lower bound", offerPrice: 99, feedPrice: 100, slippageBps: 100, dir: Buy, want: true},
		{name: "buy below lower bound", offerPrice: 98.9, feedPrice: 100, slippageBps: 100, dir: Buy, want: false},
		{name: "buy far above feed is a gift", offerPrice: 10_000, feedPrice: 100, slippageBps: 10, dir: Buy, want: true},
		{name: "no match never validates", offerPrice: 100, feedPrice: 100, slippageBps: 100, dir: NoMatch, want: false},
		// a zero feed price zeroes the margin; without a guard Buy would accept any offer
		{name: "zero feed price never validates on buy", offerPrice: 100, feedPrice: 0, slippageBps: 10, dir: Buy, want: false},
		{name: "zero feed price never validates on sell", offerPrice: 100, feedPrice: 0, slippageBps: 10, dir: Sell, want: false},
		{name: "negative feed price never validates", offerPrice: 100, feedPrice: -1, slippageBps: 10, dir: Buy, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validatePrice(tt.offerPrice, tt.feedPrice, tt.slippageBps, tt.dir)
			require.Equal(t, tt.want, got)
		})
	}
}

// feedServer serves the body returned by next and counts the requests it gets.
func feedServer(t *testing.T, next func() (string, int)) (url string, hits *int32) {
	hits = new(int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(hits, 1)
		body, code := next()
		w.WriteHeader(code)
		// nolint:errcheck
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, hits
}
