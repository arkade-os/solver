package swap

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMarketMatch(t *testing.T) {
	m := Market{
		BaseAsset: "BTC", QuoteAsset: "usd",
		MinQuoteAmount: 10, MaxQuoteAmount: 100,
		MinBaseAmount: 2, MaxBaseAmount: 5,
	}
	// sell match
	require.Equal(t, Sell, m.Match("BTC", "usd", 50))
	// sell but wantAmount < MinQuoteAmount
	require.Equal(t, NoMatch, m.Match("BTC", "usd", 5))
	// sell but wantAmount > MaxQuoteAmount
	require.Equal(t, NoMatch, m.Match("BTC", "usd", 500))

	// buy match
	require.Equal(t, Buy, m.Match("usd", "BTC", 3))
	// buy but wantAmount < MinBaseAmount
	require.Equal(t, NoMatch, m.Match("usd", "BTC", 1))
	// buy but wantAmount > MaxBaseAmount
	require.Equal(t, NoMatch, m.Match("usd", "BTC", 9))

	// "eur" isn't associated to the market
	require.Equal(t, NoMatch, m.Match("BTC", "eur", 50))

	// disabled buy
	m = Market{
		BaseAsset: "BTC", QuoteAsset: "usd",
		MinQuoteAmount: 10, MaxQuoteAmount: 100,
		MaxBaseAmount: 0, // 0 base
	}
	require.Equal(t, NoMatch, m.Match("usd", "BTC", 1))

	// disabled sell
	m = Market{
		BaseAsset: "BTC", QuoteAsset: "usd",
		MinBaseAmount: 10, MaxBaseAmount: 100,
		MaxQuoteAmount: 0, // 0 quote
	}

	require.Equal(t, NoMatch, m.Match("BTC", "usd", 1))
}

func TestMarket_ComputePrice(t *testing.T) {
	m := Market{BaseDecimals: 8, QuoteDecimals: 2}
	// sell: base=deposit, quote=want. 1 BTC (1e8) for 30000.00 usd (3_000_000).
	p, ok := m.ComputePrice(100_000_000, 3_000_000, Sell)
	require.True(t, ok)
	require.InDelta(t, 30000.0, p, 1e-6)
	// buy: base=want, quote=deposit. same price.
	p, ok = m.ComputePrice(3_000_000, 100_000_000, Buy)
	require.True(t, ok)
	require.InDelta(t, 30000.0, p, 1e-6)
	// zero deposit amount
	_, ok = m.ComputePrice(0, 1, Sell)
	require.False(t, ok)
	// zero base amount
	_, ok = m.ComputePrice(1, 0, Buy)
	require.False(t, ok)
	// invalid direction
	_, ok = m.ComputePrice(1, 1, NoMatch)
	require.False(t, ok)
}

func TestMarketComputePrice_Fee(t *testing.T) {
	// 1% fee. Raw price for these amounts is 30000 quote-per-base.
	m := Market{BaseDecimals: 8, QuoteDecimals: 2, FeeBps: 100}
	// sell inflates the price by the fee (maker must ask less to clear it)
	p, ok := m.ComputePrice(100_000_000, 3_000_000, Sell)
	require.True(t, ok)
	require.InDelta(t, 30000.0*1.01, p, 1e-6)
	// buy deflates the price by the fee (maker must give more)
	p, ok = m.ComputePrice(3_000_000, 100_000_000, Buy)
	require.True(t, ok)
	require.InDelta(t, 30000.0*0.99, p, 1e-6)
}

func TestEffectivePriceTTL(t *testing.T) {
	// unset falls back to the server default
	require.Equal(t, DefaultPriceTTL, Market{}.EffectivePriceTTL())

	// an explicit value is used verbatim
	require.Equal(t, 5*time.Second, Market{PriceTTLSeconds: 5}.EffectivePriceTTL())
	require.Equal(t, 3600*time.Second, Market{PriceTTLSeconds: 3600}.EffectivePriceTTL())
}
