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
	Pair string `json:"pair"` // e.g. "a1b2c3.../BTC"
	// Min/MaxBaseAmount bound the trade size on the base side of the trade —
	// the maker's deposit — in base-asset atomic units (sats only when the
	// base is BTC). Non-BTC-base pairs are bounded in their own units.
	MinBaseAmount uint64 `json:"minBaseAmount"`
	MaxBaseAmount uint64 `json:"maxBaseAmount"`
	BaseDecimals  int    `json:"baseDecimals"`  // decimal precision of the base asset
	QuoteDecimals int    `json:"quoteDecimals"` // decimal precision of the quote asset
	// Base/QuoteName and Base/QuoteTicker are display metadata for the
	// discovery card's asset descriptors. The BTC side is auto-filled with
	// "Bitcoin"/"BTC"; asset sides fall back to the indexer's metadata.
	BaseName    string `json:"baseName"`
	BaseTicker  string `json:"baseTicker"`
	QuoteName   string `json:"quoteName"`
	QuoteTicker string `json:"quoteTicker"`
	PriceFeed   string `json:"priceFeed"` // price API URL
	// PriceDecimals is the number of decimal places encoded in the feed's
	// value: the price is feedValue / 10^PriceDecimals. 0 means the feed
	// returns the price directly. Published in the discovery card so makers
	// normalize the same way the solver does.
	PriceDecimals int    `json:"priceDecimals"`
	InvertPrice   bool   `json:"invertPrice"`  // if true, use 1/feedPrice for comparison
	ToleranceBps  uint32 `json:"toleranceBps"` // internal fill-time max price deviation in basis points; 0 = DefaultToleranceBps
	FeeBps        uint32 `json:"feeBps"`       // published spread; must be lower than the effective tolerance
}

// DefaultToleranceBps is the price tolerance applied when a pair doesn't set one.
const DefaultToleranceBps uint32 = 100

// EffectiveToleranceBps returns the pair's fill-time price tolerance, falling
// back to the default. The tolerance is solver-internal: it is never published
// in the discovery card, unlike FeeBps which is the promise made to makers.
func (p Pair) EffectiveToleranceBps() uint32 {
	if p.ToleranceBps == 0 {
		return DefaultToleranceBps
	}
	return p.ToleranceBps
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
