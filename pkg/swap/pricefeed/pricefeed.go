package pricefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client fetches asset prices from an HTTP feed URL. The price is located in the
// JSON response by an RFC 6901 pointer ("/bitcoin/usd"); with an empty pointer
// the pointer is derived from the feed URL for the known providers.
type Client struct {
	httpClient *http.Client
}

func New() *Client {
	return &Client{
		&http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (f *Client) Fetch(ctx context.Context, url, pricePath string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := f.httpClient.Do(req)
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

	if pricePath == "" {
		pricePath, err = DefaultPricePath(url)
		if err != nil {
			return 0, err
		}
	}
	return resolve(body, pricePath)
}

// DefaultPricePath is the pointer for a known provider
// binance is always "/price"
// coingecko is computed from the query parameters
func DefaultPricePath(feedURL string) (string, error) {
	if strings.Contains(feedURL, "binance") {
		return "/price", nil
	}
	u, err := url.Parse(feedURL)
	if err != nil {
		return "", fmt.Errorf("invalid feed url: %w", err)
	}
	q := u.Query()
	ids, currencies := q.Get("ids"), q.Get("vs_currencies")
	if ids == "" || currencies == "" {
		return "", fmt.Errorf("price_path is required for feed %q", feedURL)
	}
	id, _, _ := strings.Cut(ids, ",")
	currency, _, _ := strings.Cut(currencies, ",")
	return "/" + id + "/" + currency, nil
}

// ValidatePricePath rejects a non-empty pointer that is not RFC 6901 shaped
func ValidatePricePath(pointer string) error {
	if pointer == "" {
		return nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return fmt.Errorf("price_path must be a JSON pointer starting with %q", "/")
	}
	if strings.Contains(strings.NewReplacer("~0", "", "~1", "").Replace(pointer), "~") {
		return fmt.Errorf("price_path: %q must be escaped as %q or %q", "~", "~0", "~1")
	}
	return nil
}

// resolve walks an RFC 6901 JSON pointer into the response and reads the
// value as a number, accepting both JSON numbers and numeric strings.
func resolve(body []byte, pointer string) (float64, error) {
	var node any
	if err := json.Unmarshal(body, &node); err != nil {
		return 0, fmt.Errorf("failed to parse response: %w", err)
	}
	for token := range strings.SplitSeq(strings.TrimPrefix(pointer, "/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch n := node.(type) {
		case map[string]any:
			v, ok := n[token]
			if !ok {
				return 0, fmt.Errorf("price path %q: no key %q in response", pointer, token)
			}
			node = v
		case []any:
			i, err := strconv.Atoi(token)
			if err != nil || i < 0 || i >= len(n) {
				return 0, fmt.Errorf("price path %q: out of range index %q", pointer, token)
			}
			node = n[i]
		default:
			return 0, fmt.Errorf("price path %q: cannot descend into %q", pointer, token)
		}
	}
	switch n := node.(type) {
	case float64:
		return n, nil
	case string:
		price, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, fmt.Errorf("failed to parse price %q: %w", n, err)
		}
		return price, nil
	}
	return 0, fmt.Errorf("price path %q: value is not a number", pointer)
}
