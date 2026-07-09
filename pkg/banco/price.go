package banco

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// PriceFeed fetches asset prices from an external source.
type PriceFeed interface {
	Fetch(ctx context.Context, feedURL string) (float64, error)
}

type priceCache struct {
	mu      sync.RWMutex
	feed    PriceFeed
	ttl     time.Duration
	entries map[string]cachedEntry
}

type cachedEntry struct {
	price     float64
	fetchedAt time.Time
}

func newPriceCache(feed PriceFeed, ttl time.Duration) *priceCache {
	return &priceCache{
		feed:    feed,
		ttl:     ttl,
		entries: make(map[string]cachedEntry),
	}
}

// get returns the price for the given feed URL, using the cache when fresh.
func (c *priceCache) get(ctx context.Context, feedURL string) (float64, error) {
	key := strings.TrimSpace(feedURL)
	now := time.Now()

	c.mu.RLock()
	cached, ok := c.entries[key]
	c.mu.RUnlock()

	if ok && now.Sub(cached.fetchedAt) < c.ttl {
		return cached.price, nil
	}

	price, err := c.feed.Fetch(ctx, key)
	if err != nil {
		if ok {
			return cached.price, fmt.Errorf("using stale cache: %w", err)
		}
		return 0, err
	}

	c.mu.Lock()
	c.entries[key] = cachedEntry{price: price, fetchedAt: now}
	c.mu.Unlock()

	return price, nil
}

// validatePrice ensures the offer price is within toleranceBps of the feed price.
func validatePrice(offerPrice, feedPrice float64, toleranceBps uint32) bool {
	margin := feedPrice * float64(toleranceBps) / 10000
	return offerPrice >= feedPrice-margin && offerPrice <= feedPrice+margin
}
