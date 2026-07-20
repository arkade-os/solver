package swap

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock PriceFeed
// ---------------------------------------------------------------------------

type mockPriceFeed struct {
	prices map[string]float64
	calls  int
	err    error
}

func (m *mockPriceFeed) Fetch(ctx context.Context, feedURL string) (float64, error) {
	m.calls++
	if m.err != nil {
		return 0, m.err
	}
	price, ok := m.prices[feedURL]
	if !ok {
		return 0, fmt.Errorf("unknown feed: %s", feedURL)
	}
	return price, nil
}

// ---------------------------------------------------------------------------
// priceCache.get tests
// ---------------------------------------------------------------------------

func TestPriceCache_CacheMiss(t *testing.T) {
	feed := &mockPriceFeed{prices: map[string]float64{"http://price": 42.0}}
	cache := newPriceCache(feed, 5*time.Minute)

	price, err := cache.get(context.Background(), "http://price")
	require.NoError(t, err)
	require.Equal(t, 42.0, price)
	require.Equal(t, 1, feed.calls, "should have called feed once on miss")
}

func TestPriceCache_CacheHit(t *testing.T) {
	feed := &mockPriceFeed{prices: map[string]float64{"http://price": 42.0}}
	cache := newPriceCache(feed, 5*time.Minute)

	// First call — miss
	_, err := cache.get(context.Background(), "http://price")
	require.NoError(t, err)

	// Second call — should be a hit
	price, err := cache.get(context.Background(), "http://price")
	require.NoError(t, err)
	require.Equal(t, 42.0, price)
	require.Equal(t, 1, feed.calls, "second call should use cache, not re-fetch")
}

func TestPriceCache_Expired(t *testing.T) {
	feed := &mockPriceFeed{prices: map[string]float64{"http://price": 42.0}}
	// Very short TTL so the entry expires quickly
	cache := newPriceCache(feed, 1*time.Millisecond)

	// Populate the cache
	_, err := cache.get(context.Background(), "http://price")
	require.NoError(t, err)
	require.Equal(t, 1, feed.calls)

	// Wait for TTL to expire
	time.Sleep(5 * time.Millisecond)

	// Update the feed price
	feed.prices["http://price"] = 99.0

	// Should re-fetch because entry is stale
	price, err := cache.get(context.Background(), "http://price")
	require.NoError(t, err)
	require.Equal(t, 99.0, price, "should return updated price after TTL expiry")
	require.Equal(t, 2, feed.calls, "should have re-fetched after expiry")
}

func TestPriceCache_FetchErrorWithStaleCache(t *testing.T) {
	feed := &mockPriceFeed{prices: map[string]float64{"http://price": 42.0}}
	cache := newPriceCache(feed, 1*time.Millisecond)

	// Populate the cache with a good price
	_, err := cache.get(context.Background(), "http://price")
	require.NoError(t, err)

	// Wait for TTL to expire
	time.Sleep(5 * time.Millisecond)

	// Make the feed fail
	feed.err = fmt.Errorf("upstream error")

	// Should return stale price with a "using stale cache" error
	price, err := cache.get(context.Background(), "http://price")
	require.Error(t, err)
	require.Contains(t, err.Error(), "using stale cache")
	require.Equal(t, 42.0, price, "should return stale price when feed fails")
}

func TestPriceCache_FetchErrorNoCache(t *testing.T) {
	feed := &mockPriceFeed{err: fmt.Errorf("upstream unavailable")}
	cache := newPriceCache(feed, 5*time.Minute)

	price, err := cache.get(context.Background(), "http://price")
	require.Error(t, err)
	require.Equal(t, 0.0, price)
	require.NotContains(t, err.Error(), "using stale cache", "should not mention stale cache when there is none")
}

func TestPriceCache_WhitespaceTrimmedKey(t *testing.T) {
	feed := &mockPriceFeed{prices: map[string]float64{"http://price": 7.5}}
	cache := newPriceCache(feed, 5*time.Minute)

	// First call with spaces
	price1, err := cache.get(context.Background(), "  http://price  ")
	require.NoError(t, err)
	require.Equal(t, 7.5, price1)
	require.Equal(t, 1, feed.calls)

	// Second call without spaces — should hit the same cache entry
	price2, err := cache.get(context.Background(), "http://price")
	require.NoError(t, err)
	require.Equal(t, 7.5, price2)
	require.Equal(t, 1, feed.calls, "trimmed URLs should share the same cache entry")
}
