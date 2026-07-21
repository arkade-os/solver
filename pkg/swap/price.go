package swap

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/arkade-os/solver/pkg/swap/pricefeed"
)

// TODO make it configurable
const priceCacheTTL = time.Minute

type priceCache struct {
	client  *pricefeed.Client
	mu      sync.RWMutex
	ttl     time.Duration
	entries map[string]cachedEntry
}

type cachedEntry struct {
	price     float64
	fetchedAt time.Time
}

func newPriceCache() *priceCache {
	return &priceCache{
		client:  pricefeed.New(),
		ttl:     priceCacheTTL,
		entries: make(map[string]cachedEntry),
	}
}

// get returns the price for the given feed URL, using the cache when fresh.
func (c *priceCache) get(ctx context.Context, feedURL, pricePath string) (float64, error) {
	url := strings.TrimSpace(feedURL)
	key := url + pricePath
	now := time.Now()

	c.mu.RLock()
	cached, ok := c.entries[key]
	c.mu.RUnlock()

	if ok && now.Sub(cached.fetchedAt) < c.ttl {
		return cached.price, nil
	}

	price, err := c.client.Fetch(ctx, url, pricePath)
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

func validatePrice(offerPrice, feedPrice float64, slippageBps uint32, dir Direction) bool {
	margin := feedPrice * float64(slippageBps) / 10000
	switch dir {
	case Sell:
		return offerPrice <= feedPrice+margin
	case Buy:
		return offerPrice >= feedPrice-margin
	default:
		return false
	}
}
