package banco

import (
	"context"
	"strings"
)

// Pair defines a trading pair and its price-feed constraints for the banco plugin.
// The Pair field uses the format "{base}/{quote}" where each side is either
// "BTC" (for native bitcoin) or the hex asset ID (for arkade assets).
// Examples: "a1b2c3.../BTC", "BTC/d4e5f6...", "a1b2c3.../d4e5f6..."
type Pair struct {
	Pair          string `json:"pair"`          // e.g. "a1b2c3.../BTC"
	MinAmount     uint64 `json:"minAmount"`     // satoshis
	MaxAmount     uint64 `json:"maxAmount"`     // satoshis
	BaseDecimals  int    `json:"baseDecimals"`  // decimal precision of the base asset
	QuoteDecimals int    `json:"quoteDecimals"` // decimal precision of the quote asset
	PriceFeed     string `json:"priceFeed"`     // price API URL
	InvertPrice   bool   `json:"invertPrice"`   // if true, use 1/feedPrice for comparison
}

// Base returns the base asset of the pair (e.g. "BTC" from "BTC/USDT").
func (p Pair) Base() string {
	return strings.SplitN(p.Pair, "/", 2)[0] // SplitN always returns at least one element
}

// Quote returns the quote asset of the pair (e.g. "USDT" from "BTC/USDT").
func (p Pair) Quote() string {
	parts := strings.SplitN(p.Pair, "/", 2)
	if len(parts) > 1 {
		return parts[1]
	}
	return ""
}

// PairRepository is the read-only view of configured trading pairs used by the banco plugin.
type PairRepository interface {
	List(ctx context.Context) ([]Pair, error)
}
