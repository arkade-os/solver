package swap

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/arkade-os/solver/pkg/swap/pricefeed"
)

// staleFactor bounds how far past its TTL a cached price may be served when the feed is unreachable.
const staleFactor = 6

type priceCache struct {
	client  *pricefeed.Client
	mu      sync.RWMutex
	entries map[string]cachedEntry
}

type cachedEntry struct {
	price     float64
	fetchedAt time.Time
}

func newPriceCache() *priceCache {
	return &priceCache{
		client:  pricefeed.New(),
		entries: make(map[string]cachedEntry),
	}
}

// get returns the price for the given feed URL, using the cache when fresher
// than ttl. On a fetch error a cached price is served until it is staleFactor
// times older than ttl, after which the error stands alone.
func (c *priceCache) get(
	ctx context.Context, feedURL, pricePath string, ttl time.Duration,
) (float64, error) {
	url := strings.TrimSpace(feedURL)
	key := url + pricePath
	now := time.Now()

	c.mu.RLock()
	cached, ok := c.entries[key]
	c.mu.RUnlock()

	age := now.Sub(cached.fetchedAt)
	if ok && age < ttl {
		return cached.price, nil
	}

	price, err := c.client.Fetch(ctx, url, pricePath)
	if err != nil {
		if ok && age < ttl*staleFactor {
			return cached.price, fmt.Errorf("using stale cache: %w", err)
		}
		if ok {
			return 0, fmt.Errorf("cached price too stale (age %s): %w", age.Round(time.Second), err)
		}
		return 0, err
	}

	c.mu.Lock()
	c.entries[key] = cachedEntry{price: price, fetchedAt: now}
	c.mu.Unlock()

	return price, nil
}

func validatePrice(offerPrice, feedPrice float64, slippageBps uint32, dir Direction) bool {
	// a non-positive feed price zeroes the margin, which would make Buy accept anything
	if feedPrice <= 0 {
		return false
	}
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
