package application

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/arkade-os/solver/pkg/swap"
)

func validPair() swap.Pair {
	return swap.Pair{
		Pair:      "BTC/aabbcc",
		MinAmount: 1000,
		MaxAmount: 100000,
		PriceFeed: "https://example.com/price",
	}
}

func TestValidatePairSlippage(t *testing.T) {
	p := validPair()
	assert.NoError(t, validatePair(p))

	p.SlippageBps = 5000
	assert.NoError(t, validatePair(p))

	p.SlippageBps = 5001
	assert.ErrorContains(t, validatePair(p), "slippage_bps must be at most 5000")
}
