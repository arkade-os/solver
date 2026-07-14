package pricefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Feed fetches asset prices from an HTTP feed URL. The response format is
// picked from the URL host: Binance for *.binance.* hosts, CoinGecko otherwise.
type Feed struct {
	*http.Client
}

func New() *Feed {
	return &Feed{
		&http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (f *Feed) Fetch(ctx context.Context, feedURL string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := f.Do(req)
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

	if strings.Contains(feedURL, "binance") {
		return parseBinance(body)
	}
	return parseCoinGecko(body)
}

// CoinGecko simple/price format: {"bitcoin":{"usd":50000}}
func parseCoinGecko(body []byte) (float64, error) {
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

// Binance ticker/price format: {"symbol":"BTCUSDT","price":"50000.00"}
func parseBinance(body []byte) (float64, error) {
	var result struct {
		Price string `json:"price"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("failed to parse response: %w", err)
	}
	if result.Price == "" {
		return 0, fmt.Errorf("no price found in response")
	}
	price, err := strconv.ParseFloat(result.Price, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse price %q: %w", result.Price, err)
	}
	return price, nil
}
