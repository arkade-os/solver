package swap

import (
	"context"
	"testing"

	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// findMatchingMarket tests
// ---------------------------------------------------------------------------

func TestFindMatchingMarket(t *testing.T) {
	wantAsset := testAssetId(t)

	markets := []Market{{
		BaseAsset: "BTC", QuoteAsset: wantAsset.String(),
		MinQuoteAmount: 1, MaxQuoteAmount: 1_000_000,
		MinBaseAmount: 1, MaxBaseAmount: 1_000_000,
	}}

	// sell-base match: BTC deposit, wants the market's quote asset.
	sell := &Offer{DepositAsset: nil}
	sell.WantAsset = wantAsset
	sell.WantAmount = 500
	m, dir := findMatchingMarket(markets, sell)
	require.NotNil(t, m)
	require.Equal(t, Sell, dir)

	// no-match asset: BTC deposit, wants an unrelated asset.
	none := &Offer{DepositAsset: nil}
	none.WantAsset = testOtherAssetId(t)
	none.WantAmount = 500
	m, dir = findMatchingMarket(markets, none)
	require.Nil(t, m)
	require.Equal(t, NoMatch, dir)
}

// ---------------------------------------------------------------------------
// Plugin tests
// ---------------------------------------------------------------------------

func TestPlugin_Match_NonSwapTx(t *testing.T) {
	p := NewPlugin(Config{
		MarketsRepository: &fakeMarkets{markets: nil},
	})
	intent, ok := p.Match(context.Background(), emptyPSBT(t))
	require.False(t, ok)
	require.Nil(t, intent)
}

func TestPlugin_Solve_NilMatchedOffer(t *testing.T) {
	p := NewPlugin(Config{})
	// Solve should return cleanly on nil/wrong-type intent without panicking.
	require.NotPanics(t, func() {
		p.Solve(context.Background(), nil)
	})
}

// emptyPSBT returns a *psbt.Packet wrapping an empty MsgTx — has no extension,
// so Match should return (nil, false, nil).
func emptyPSBT(t *testing.T) *psbt.Packet {
	t.Helper()
	tx := wire.NewMsgTx(2)
	pkt, err := psbt.NewFromUnsignedTx(tx)
	require.NoError(t, err)
	return pkt
}

// fakeMarkets is a minimal MarketRepository for testing.
type fakeMarkets struct {
	markets []Market
	err     error
}

func (f *fakeMarkets) List(ctx context.Context) ([]Market, error) {
	return f.markets, f.err
}
