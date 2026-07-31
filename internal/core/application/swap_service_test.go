package application

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/arkade-os/solver/pkg/swap"
)

func validMarket() swap.Market {
	return swap.Market{
		BaseAsset:      "BTC",
		QuoteAsset:     "aabbcc",
		MinQuoteAmount: 1000,
		MaxQuoteAmount: 100000,
		PriceFeed:      "https://example.com/price",
	}
}

func TestValidateMarket(t *testing.T) {
	// valid market (sell direction only, as configured by validMarket)
	require.NoError(t, validateMarket(validMarket()))

	// slippage at the cap is ok, above it is rejected
	m := validMarket()
	m.SlippageBps = 5000
	require.NoError(t, validateMarket(m))
	m.SlippageBps = 5001
	require.ErrorContains(t, validateMarket(m), "slippage_bps must be at most 5000")

	// base and quote are both required
	m = validMarket()
	m.BaseAsset = ""
	require.ErrorContains(t, validateMarket(m), "base and quote assets are required")
	m = validMarket()
	m.QuoteAsset = ""
	require.ErrorContains(t, validateMarket(m), "base and quote assets are required")

	// base and quote must differ
	m = validMarket()
	m.QuoteAsset = m.BaseAsset
	require.ErrorContains(t, validateMarket(m), "base and quote assets must differ")

	// both directions disabled (max == 0 on each) is rejected
	m = validMarket()
	m.MaxQuoteAmount = 0
	m.MaxBaseAmount = 0
	require.ErrorContains(t, validateMarket(m), "at least one direction must be enabled")

	// a single enabled direction (buy only) is accepted
	m = validMarket()
	m.MinQuoteAmount, m.MaxQuoteAmount = 0, 0
	m.MinBaseAmount, m.MaxBaseAmount = 10, 1000
	require.NoError(t, validateMarket(m))

	// min must be <= max, checked per enabled direction
	m = validMarket()
	m.MinQuoteAmount = 200000
	require.ErrorContains(t, validateMarket(m), "min_quote_amount must be <= max_quote_amount")
	m = validMarket()
	m.MaxQuoteAmount = 0
	m.MinBaseAmount, m.MaxBaseAmount = 2000, 1000
	require.ErrorContains(t, validateMarket(m), "min_base_amount must be <= max_base_amount")

	// price feed is required
	m = validMarket()
	m.PriceFeed = ""
	require.ErrorContains(t, validateMarket(m), "price_feed is required")

	// fee at the cap is ok, above it is rejected
	m = validMarket()
	m.FeeBps = 5000
	require.NoError(t, validateMarket(m))
	m.FeeBps = 5001
	require.ErrorContains(t, validateMarket(m), "fee_bps must be at most 5000")

	// price ttl at the cap is ok, above it is rejected
	m = validMarket()
	m.PriceTTLSeconds = 3600
	require.NoError(t, validateMarket(m))
	m.PriceTTLSeconds = 3601
	require.ErrorContains(t, validateMarket(m), "price_ttl_seconds must be at most 3600")
}
