package pricefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type CoinGecko struct {
	*http.Client
}

func NewCoinGecko() *CoinGecko {
	return &CoinGecko{
		&http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Fetch fetches the price from the given feed URL.
// Example response: {"bitcoin":{"usd":50000}}
func (cg *CoinGecko) Fetch(ctx context.Context, feedURL string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := cg.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch price: %w", err)
	}
	// nolint:errcheck
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse the CoinGecko simple/price format: {"coin":{"currency": price}}
	var result map[string]map[string]float64
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("failed to parse response: %w", err)
	}

	for _, currencies := range result {
		for _, price := range currencies {
			return price, nil
		}
	}

	return 0, fmt.Errorf("no price found in response")
}
