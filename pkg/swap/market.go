package swap

import (
	"context"
	"math"
)

// Market is a bidirectional trading market for a base/quote asset pair.
// Each direction is gated by the want-side bounds; a zero Max*Amount disables it.
type Market struct {
	BaseAsset     string `json:"baseAsset"`     // asset id, or "BTC"
	QuoteAsset    string `json:"quoteAsset"`    // asset id, or "BTC"
	BaseDecimals  int    `json:"baseDecimals"`  // decimal precision of the base asset
	QuoteDecimals int    `json:"quoteDecimals"` // decimal precision of the quote asset

	// Sell-base direction (offer deposits base, wants quote).
	// MaxQuoteAmount == 0 disables this direction.
	MinQuoteAmount uint64 `json:"minQuoteAmount"`
	MaxQuoteAmount uint64 `json:"maxQuoteAmount"`

	// Buy-base direction (offer deposits quote, wants base).
	// MaxBaseAmount == 0 disables this direction.
	MinBaseAmount uint64 `json:"minBaseAmount"`
	MaxBaseAmount uint64 `json:"maxBaseAmount"`

	PriceFeed string `json:"priceFeed"` // returns quote-per-base
	// JSON pointer to the price in the feed response, e.g. "/bitcoin/usd".
	// Empty = guess from the feed host (Binance / CoinGecko).
	PricePath   string `json:"pricePath"`
	SlippageBps uint32 `json:"slippageBps"` // 0 = DefaultSlippageBps
	FeeBps      uint32 `json:"feeBps"`      // solver margin, shifted into the price; 0 = no fee
}

const DefaultSlippageBps uint32 = 10

// Direction is the side an offer trades on this market.
type Direction int

const (
	NoMatch Direction = iota // offer does not match this market
	Sell                     // deposit base, want quote
	Buy                      // deposit quote, want base
)

// ID is the canonical "base/quote" identifier.
func (m Market) ID() string { return m.BaseAsset + "/" + m.QuoteAsset }

// EffectiveSlippageBps returns the market's slippage, falling back to the default.
func (m Market) EffectiveSlippageBps() uint32 {
	if m.SlippageBps == 0 {
		return DefaultSlippageBps
	}
	return m.SlippageBps
}

// Match resolves an offer's direction on this market, range-checking the want
// amount against that direction's bounds. A disabled direction (Max == 0) never
// matches. Returns NoMatch when the assets don't fit or the amount is out of range.
func (m Market) Match(depositAsset, wantAsset string, wantAmount uint64) Direction {
	switch {
	case depositAsset == m.BaseAsset && wantAsset == m.QuoteAsset:
		if inRange(wantAmount, m.MinQuoteAmount, m.MaxQuoteAmount) {
			return Sell
		}
	case depositAsset == m.QuoteAsset && wantAsset == m.BaseAsset:
		if inRange(wantAmount, m.MinBaseAmount, m.MaxBaseAmount) {
			return Buy
		}
	}
	return NoMatch
}

// inRange is true only when max != 0 (direction enabled) and min <= v <= max.
func inRange(v, min, max uint64) bool {
	return max != 0 && v >= min && v <= max
}

// ComputePrice returns the offer price as quote-per-base for the given
// direction, with the solver's FeeBps folded in: the price is nudged in the
// solver's favor (up for Sell, down for Buy) so an offer must beat the feed by
// the fee to clear the tolerance check. FeeBps == 0 returns the raw price.
// Returns 0, false for zero amounts or NoMatch.
func (m Market) ComputePrice(depositAmount, wantAmount uint64, dir Direction) (float64, bool) {
	if depositAmount == 0 || wantAmount == 0 {
		return 0, false
	}
	fee := float64(m.FeeBps) / 10000
	var baseAmt, quoteAmt uint64
	var feeFactor float64
	switch dir {
	case Sell: // deposit base, want quote — inflate so the maker must ask less
		baseAmt, quoteAmt, feeFactor = depositAmount, wantAmount, 1+fee
	case Buy: // deposit quote, want base — deflate so the maker must give more
		baseAmt, quoteAmt, feeFactor = wantAmount, depositAmount, 1-fee
	default:
		return 0, false
	}
	base := float64(baseAmt) / math.Pow10(m.BaseDecimals)
	quote := float64(quoteAmt) / math.Pow10(m.QuoteDecimals)
	return quote / base * feeFactor, true
}

// MarketRepository is the read-only view of configured markets used by the swap plugin.
type MarketRepository interface {
	List(ctx context.Context) ([]Market, error)
}
