package swap

import (
	"context"
	"testing"

	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// validatePrice tests
// ---------------------------------------------------------------------------

func TestValidatePrice(t *testing.T) {
	tests := []struct {
		name        string
		offerPrice  float64
		feedPrice   float64
		slippageBps uint32
		want        bool
	}{
		{
			name:        "exact match within 1% margin",
			offerPrice:  100.0,
			feedPrice:   100.0,
			slippageBps: 100,
			want:        true,
		},
		{
			name:        "at lower bound (99% of feed)",
			offerPrice:  99.0,
			feedPrice:   100.0,
			slippageBps: 100,
			want:        true,
		},
		{
			name:        "at upper bound (101% of feed)",
			offerPrice:  101.0,
			feedPrice:   100.0,
			slippageBps: 100,
			want:        true,
		},
		{
			name:        "below lower bound",
			offerPrice:  98.9,
			feedPrice:   100.0,
			slippageBps: 100,
			want:        false,
		},
		{
			name:        "above upper bound",
			offerPrice:  101.1,
			feedPrice:   100.0,
			slippageBps: 100,
			want:        false,
		},
		{
			name:        "wider slippage accepts larger deviation",
			offerPrice:  95.0,
			feedPrice:   100.0,
			slippageBps: 500,
			want:        true,
		},
		{
			name:        "tighter slippage rejects deviation 1% would allow",
			offerPrice:  99.5,
			feedPrice:   100.0,
			slippageBps: 10,
			want:        false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := validatePrice(tc.offerPrice, tc.feedPrice, tc.slippageBps)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestEffectiveSlippageBps(t *testing.T) {
	assert.Equal(t, uint32(100), Pair{}.EffectiveSlippageBps())
	assert.Equal(t, uint32(250), Pair{SlippageBps: 250}.EffectiveSlippageBps())
}

// ---------------------------------------------------------------------------
// findMatchingPair-equivalent tests (via exported types)
// ---------------------------------------------------------------------------

func TestFindMatchingPair_Match(t *testing.T) {
	// BTC deposit, want = BTC -> pair "BTC/BTC"
	offer := &Offer{DepositAsset: nil}
	// offer.WantAsset is nil, so WantAssetStr() == "BTC"

	pairs := []Pair{
		{Pair: "BTC/BTC"}, // Base="BTC", Quote="BTC"
		{Pair: "OTHER/X"}, // won't match
	}

	result := findMatchingPair(pairs, offer)
	require.NotNil(t, result)
	assert.Equal(t, "BTC/BTC", result.Pair)
}

func TestFindMatchingPair_NoMatchWrongBase(t *testing.T) {
	assetId := testAssetId(t)
	offer := &Offer{DepositAsset: assetId}

	pairs := []Pair{
		{Pair: "BTC/BTC"}, // Base="BTC", but offer deposits an asset
	}

	result := findMatchingPair(pairs, offer)
	assert.Nil(t, result)
}

func TestFindMatchingPair_NoMatchWrongQuote(t *testing.T) {
	assetId := testAssetId(t)
	offer := &Offer{DepositAsset: nil} // BTC deposit
	offer.WantAsset = assetId          // wants an asset, so WantAssetStr() != "BTC"

	pairs := []Pair{
		{Pair: "BTC/BTC"}, // Quote="BTC" but offer wants an asset
	}

	result := findMatchingPair(pairs, offer)
	assert.Nil(t, result)
}

func TestFindMatchingPair_EmptyPairs(t *testing.T) {
	offer := &Offer{DepositAsset: nil}
	result := findMatchingPair([]Pair{}, offer)
	assert.Nil(t, result)
}

func TestFindMatchingPair_AssetDepositAssetWant(t *testing.T) {
	depositAsset := testAssetId(t)
	wantAsset := testAssetId(t)

	offer := &Offer{DepositAsset: depositAsset}
	offer.WantAsset = wantAsset

	depositStr := offer.DepositAssetStr()
	wantStr := offer.WantAssetStr()

	pairs := []Pair{
		{Pair: depositStr + "/" + wantStr},
	}

	result := findMatchingPair(pairs, offer)
	require.NotNil(t, result)
	assert.Equal(t, depositStr+"/"+wantStr, result.Pair)
}

// ---------------------------------------------------------------------------
// Plugin tests
// ---------------------------------------------------------------------------

// emptyPSBT returns a *psbt.Packet wrapping an empty MsgTx — has no extension,
// so Match should return (nil, false, nil).
func emptyPSBT(t *testing.T) *psbt.Packet {
	t.Helper()
	tx := wire.NewMsgTx(2)
	pkt, err := psbt.NewFromUnsignedTx(tx)
	require.NoError(t, err)
	return pkt
}

func TestPlugin_Match_NonSwapTx(t *testing.T) {
	p := NewPlugin(Config{
		PairsRepository: &fakePairs{pairs: nil},
	})
	intent, ok := p.Match(context.Background(), emptyPSBT(t))
	require.False(t, ok)
	require.Nil(t, intent)
}

// fakePairs is a minimal PairRepository for testing.
type fakePairs struct {
	pairs []Pair
	err   error
}

func (f *fakePairs) List(ctx context.Context) ([]Pair, error) {
	return f.pairs, f.err
}

func TestPlugin_Solve_NilMatchedOffer(t *testing.T) {
	p := NewPlugin(Config{})
	// Solve should return cleanly on nil/wrong-type intent without panicking.
	require.NotPanics(t, func() {
		p.Solve(context.Background(), nil)
	})
}
