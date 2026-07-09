package application

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/arkade-os/solver/pkg/banco"
)

func validPair() banco.Pair {
	return banco.Pair{
		Pair:      "BTC/aabbcc",
		MinAmount: 1000,
		MaxAmount: 100000,
		PriceFeed: "https://example.com/price",
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
