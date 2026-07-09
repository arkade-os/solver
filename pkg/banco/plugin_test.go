package banco

import (
	"context"
	"testing"

	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// validatePrice tests
// ---------------------------------------------------------------------------

func TestValidatePrice(t *testing.T) {
	tests := []struct {
		name         string
		offerPrice   float64
		feedPrice    float64
		toleranceBps uint32
		want         bool
	}{
		{
			name:         "exact match within 1% margin",
			offerPrice:   100.0,
			feedPrice:    100.0,
			toleranceBps: 100,
			want:         true,
		},
		{
			name:         "at lower bound (99% of feed)",
			offerPrice:   99.0,
			feedPrice:    100.0,
			toleranceBps: 100,
			want:         true,
		},
		{
			name:         "at upper bound (101% of feed)",
			offerPrice:   101.0,
			feedPrice:    100.0,
			toleranceBps: 100,
			want:         true,
		},
		{
			name:         "below lower bound",
			offerPrice:   98.9,
			feedPrice:    100.0,
			toleranceBps: 100,
			want:         false,
		},
		{
			name:         "above upper bound",
			offerPrice:   101.1,
			feedPrice:    100.0,
			toleranceBps: 100,
			want:         false,
		},
		{
			name:         "wider tolerance accepts larger deviation",
			offerPrice:   95.0,
			feedPrice:    100.0,
			toleranceBps: 500,
			want:         true,
		},
		{
			name:         "tighter tolerance rejects deviation 1% would allow",
			offerPrice:   99.5,
			feedPrice:    100.0,
			toleranceBps: 10,
			want:         false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := validatePrice(tc.offerPrice, tc.feedPrice, tc.toleranceBps)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestEffectiveToleranceBps(t *testing.T) {
	assert.Equal(t, uint32(100), Pair{}.EffectiveToleranceBps())
	assert.Equal(t, uint32(250), Pair{ToleranceBps: 250}.EffectiveToleranceBps())
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

func TestPlugin_Match_NonBancoTx(t *testing.T) {
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

// ---------------------------------------------------------------------------
// Match: tolerance + base-denominated limits (via real offer txs)
// ---------------------------------------------------------------------------

// fakeFeed is a PriceFeed returning a fixed price.
type fakeFeed struct{ price float64 }

func (f *fakeFeed) Fetch(_ context.Context, _ string) (float64, error) {
	return f.price, nil
}

// btcDepositOfferTx builds a tx depositing `deposit` sats of BTC into the swap
// output, wanting `want` units of the given asset.
func btcDepositOfferTx(t *testing.T, deposit int64, want uint64, wantAsset *asset.AssetId) *psbt.Packet {
	t.Helper()
	offer := buildMinimalOffer(t)
	offer.WantAmount = want
	offer.WantAsset = wantAsset

	pkt, err := offer.ToPacket()
	require.NoError(t, err)
	ext := extension.Extension{pkt}
	extOut, err := ext.TxOut()
	require.NoError(t, err)

	tx := wire.NewMsgTx(2)
	dummyHash := chainhash.Hash{}
	tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&dummyHash, 0), nil, nil))
	tx.AddTxOut(wire.NewTxOut(deposit, offer.SwapPkScript))
	tx.AddTxOut(extOut)

	p, err := psbt.NewFromUnsignedTx(tx)
	require.NoError(t, err)
	return p
}

// assetDepositOfferTx builds a tx depositing `depositUnits` of depositAsset
// (carried by an asset packet on the swap output) wanting `want` of wantAsset.
func assetDepositOfferTx(
	t *testing.T, depositUnits, want uint64, depositAsset, wantAsset *asset.AssetId,
) *psbt.Packet {
	t.Helper()
	offer := buildMinimalOffer(t)
	offer.WantAmount = want
	offer.WantAsset = wantAsset

	offerPkt, err := offer.ToPacket()
	require.NoError(t, err)
	assetPkt := asset.Packet{{
		AssetId: depositAsset,
		Outputs: []asset.AssetOutput{{
			Type:   asset.AssetOutputTypeLocal,
			Vout:   0, // the swap output below
			Amount: depositUnits,
		}},
	}}
	ext, err := extension.NewExtensionFromPackets(assetPkt, offerPkt)
	require.NoError(t, err)
	extOut, err := ext.TxOut()
	require.NoError(t, err)

	tx := wire.NewMsgTx(2)
	dummyHash := chainhash.Hash{}
	tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&dummyHash, 0), nil, nil))
	tx.AddTxOut(wire.NewTxOut(330, offer.SwapPkScript)) // dust carrier
	tx.AddTxOut(extOut)

	p, err := psbt.NewFromUnsignedTx(tx)
	require.NoError(t, err)
	return p
}

// TestPlugin_Match_ToleranceRejectsWhatOnePercentAccepted: an offer priced
// ~0.8% away from the feed passes the default 100 bps tolerance but is
// rejected once the pair's tolerance is tightened to 50 bps.
func TestPlugin_Match_ToleranceRejectsWhatOnePercentAccepted(t *testing.T) {
	wantAsset := testAssetId(t)
	// deposit 100_000 sats, want 100_800 asset units, both sides 8 decimals:
	// offer price = 100000/100800 = 0.99206... => ~0.79% below a feed of 1.0.
	tx := btcDepositOfferTx(t, 100_000, 100_800, wantAsset)

	pairFor := func(toleranceBps uint32) []Pair {
		return []Pair{{
			Pair:          "BTC/" + wantAsset.String(),
			MinBaseAmount: 1,
			MaxBaseAmount: 10_000_000,
			BaseDecimals:  8,
			QuoteDecimals: 8,
			PriceFeed:     "test-feed",
			ToleranceBps:  toleranceBps,
		}}
	}

	// Old behavior (1% band): accepted.
	p := NewPlugin(Config{
		PairsRepository: &fakePairs{pairs: pairFor(100)},
		PriceFeed:       &fakeFeed{price: 1.0},
	})
	intent, ok := p.Match(context.Background(), tx)
	require.True(t, ok, "0.8%% deviation must pass a 100 bps tolerance")
	require.NotNil(t, intent)

	// Tightened band: rejected.
	p = NewPlugin(Config{
		PairsRepository: &fakePairs{pairs: pairFor(50)},
		PriceFeed:       &fakeFeed{price: 1.0},
	})
	_, ok = p.Match(context.Background(), tx)
	require.False(t, ok, "0.8%% deviation must fail a 50 bps tolerance")
}

// TestPlugin_Match_BaseLimitsNonBTCBase: a non-BTC-base pair enforces min/max
// against the deposited asset amount (base units), not the want side.
func TestPlugin_Match_BaseLimitsNonBTCBase(t *testing.T) {
	depositAsset := testAssetId(t)
	wantAsset := func() *asset.AssetId {
		// a second, distinct asset id
		raw := make([]byte, 34)
		raw[0] = 0xCD
		for i := 1; i < 32; i++ {
			raw[i] = byte(0xFF - i)
		}
		id, err := asset.NewAssetIdFromBytes(raw)
		require.NoError(t, err)
		return id
	}()

	pairs := []Pair{{
		Pair:          depositAsset.String() + "/" + wantAsset.String(),
		MinBaseAmount: 1000,
		MaxBaseAmount: 2000,
		BaseDecimals:  8,
		QuoteDecimals: 8,
		PriceFeed:     "test-feed",
	}}
	p := NewPlugin(Config{
		PairsRepository: &fakePairs{pairs: pairs},
		PriceFeed:       &fakeFeed{price: 1.0},
	})

	// Deposit below the base-side minimum: rejected regardless of want amount.
	tx := assetDepositOfferTx(t, 500, 500, depositAsset, wantAsset)
	_, ok := p.Match(context.Background(), tx)
	require.False(t, ok, "deposit of 500 base units must fail min_base_amount=1000")

	// Deposit within bounds (and priced at the feed): accepted.
	tx = assetDepositOfferTx(t, 1500, 1500, depositAsset, wantAsset)
	intent, ok := p.Match(context.Background(), tx)
	require.True(t, ok, "deposit of 1500 base units must pass [1000, 2000]")
	require.NotNil(t, intent)

	// Deposit above the base-side maximum: rejected.
	tx = assetDepositOfferTx(t, 2500, 2500, depositAsset, wantAsset)
	_, ok = p.Match(context.Background(), tx)
	require.False(t, ok, "deposit of 2500 base units must fail max_base_amount=2000")
}
