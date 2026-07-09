package application

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/arkade-os/solver/pkg/banco"
)

func validPair() banco.Pair {
	return banco.Pair{
		Pair:          "BTC/aabbcc",
		MinBaseAmount: 1000,
		MaxBaseAmount: 100000,
		PriceFeed:     "https://example.com/price",
	}
}

func TestValidatePairTolerance(t *testing.T) {
	p := validPair()
	assert.NoError(t, validatePair(p))

	p.ToleranceBps = 5000
	assert.NoError(t, validatePair(p))

	p.ToleranceBps = 5001
	assert.ErrorContains(t, validatePair(p), "tolerance_bps must be at most 5000")
}

func TestValidatePairFeeVsTolerance(t *testing.T) {
	p := validPair()
	p.FeeBps = 30
	assert.NoError(t, validatePair(p), "fee below the default tolerance is valid")

	// fee equal to the effective tolerance cannot fill.
	p.ToleranceBps = 0 // effective 100
	p.FeeBps = 100
	assert.ErrorContains(t, validatePair(p), "tolerance_bps (100) must be greater than fee_bps (100)")

	p.ToleranceBps = 50
	p.FeeBps = 80
	assert.ErrorContains(t, validatePair(p), "must be greater than fee_bps")

	p.ToleranceBps = 100
	p.FeeBps = 99
	assert.NoError(t, validatePair(p))
}

func TestFillSideMetaBTC(t *testing.T) {
	svc := &Service{} // BTC path never touches the indexer

	var name, ticker string
	var decimals int
	err := svc.fillSideMeta(t.Context(), "BTC", &name, &ticker, &decimals)
	assert.NoError(t, err)
	assert.Equal(t, "Bitcoin", name)
	assert.Equal(t, "BTC", ticker)
	assert.Equal(t, 8, decimals)

	// operator-supplied metadata is preserved, decimals stay authoritative.
	name, ticker, decimals = "Custom Bitcoin", "XBT", 0
	err = svc.fillSideMeta(t.Context(), "BTC", &name, &ticker, &decimals)
	assert.NoError(t, err)
	assert.Equal(t, "Custom Bitcoin", name)
	assert.Equal(t, "XBT", ticker)
	assert.Equal(t, 8, decimals)
}

func TestValidatePairTickers(t *testing.T) {
	p := validPair()
	p.BaseTicker = "BTC"
	p.QuoteTicker = "USDT"
	assert.NoError(t, validatePair(p))

	p.QuoteTicker = "US/DT"
	assert.ErrorContains(t, validatePair(p), "must not contain")

	p.QuoteTicker = "US DT"
	assert.ErrorContains(t, validatePair(p), "must not contain")
}
