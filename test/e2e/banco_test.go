package e2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	sdktypes "github.com/arkade-os/go-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/arkade-os/solver/pkg/banco"
	"github.com/arkade-os/solver/pkg/banco/contract"
)

const mockPriceFeedURL = "http://mock-price-feed"

// TestBancoAssetToBTC: maker deposits asset, wants BTC.
// Mock price feed returns 1.0. With BaseDecimals=0, QuoteDecimals=0:
//
//	price = depositAmount/wantAmount = 500/500 = 1.0 ✓
func TestBancoAssetToBTC(t *testing.T) {
	ctx := t.Context()

	// Create maker, fund with offchain BTC, issue asset.
	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.0005)
	assetID := issueAsset(t, maker, 500)

	// Configure taker pair: asset/BTC. We write directly to pairRepo
	// (instead of going through takerSvc.AddPair) so we can pin
	// BaseDecimals=QuoteDecimals=0; AddPair would resolve BTC's quote
	// decimals to 8 and the mock price feed (1.0) would no longer match
	// the 500/500 offer ratio.
	pair := banco.Pair{
		Pair:          assetID + "/BTC",
		MinAmount:     1,
		MaxAmount:     100000000,
		BaseDecimals:  0,
		QuoteDecimals: 0,
		PriceFeed:     mockPriceFeedURL,
	}
	require.NoError(t, pairRepo.Add(ctx, pair))
	t.Cleanup(func() { _ = pairRepo.Remove(ctx, pair.Pair) })

	// Maker creates offer: deposit asset, want 500 sats BTC.
	intro := newIntroClient(t)
	offerResult, err := contract.CreateOffer(ctx, contract.CreateOfferParams{
		WantAmount: 500,
	}, maker, intro)
	require.NoError(t, err)
	require.NotEmpty(t, offerResult.SwapAddress)

	// Subscribe to the maker's vtxo events BEFORE funding the swap so we
	// catch the fulfill VTXO that lands at the maker's address after the
	// taker bot picks up the offer.
	makerVtxoCh := maker.GetVtxoEventChannel(ctx)

	// Fund swap address with asset + offer packet. Deposit 500 units of
	// asset; the BTC amount (450) is just a dust carrier. The solver reads
	// DepositAmount from the asset packet (=500).
	sendOffChainWithExtension(t, maker, clientTypes.Receiver{
		To:     offerResult.SwapAddress,
		Amount: 450,
		Assets: []clientTypes.Asset{{AssetId: assetID, Amount: 500}},
	}, offerResult.Packet)

	// Wait for taker bot to fulfill — the BTC payout lands as a new VTXO
	// at the maker's makerPkScript.
	requireFulfillment(t, ctx, makerVtxoCh, 60*time.Second)
}

// TestBancoBTCToAsset: maker deposits BTC, wants asset.
// Mock price feed returns 1.0. With BaseDecimals=0, QuoteDecimals=0:
//
//	price = depositAmount/wantAmount = 500/500 = 1.0 ✓
func TestBancoBTCToAsset(t *testing.T) {
	ctx := t.Context()

	// Issue asset on a temp wallet and send it to the taker bot, so the
	// taker has asset balance to fulfill the maker's offer.
	tempClient := setupArkClient(t)
	faucetOffchain(t, tempClient, 0.001)
	assetID := issueAsset(t, tempClient, 1000)

	takerAddr, err := takerSvc.GetAddress(ctx)
	require.NoError(t, err)

	takerVtxoCh := takerClient.GetVtxoEventChannel(ctx)
	_, err = tempClient.SendOffChain(ctx, []clientTypes.Receiver{{
		To:     takerAddr.OffchainAddress,
		Amount: 1000,
		Assets: []clientTypes.Asset{{AssetId: assetID, Amount: 1000}},
	}})
	require.NoError(t, err)
	// Wait for the taker wallet to see the incoming asset VTXO.
	waitForVtxoAdded(t, ctx, takerVtxoCh, 30*time.Second)

	// Configure pair: BTC/asset with both decimals=0.
	pair := banco.Pair{
		Pair:          "BTC/" + assetID,
		MinAmount:     1,
		MaxAmount:     100000000,
		BaseDecimals:  0,
		QuoteDecimals: 0,
		PriceFeed:     mockPriceFeedURL,
	}
	require.NoError(t, pairRepo.Add(ctx, pair))
	t.Cleanup(func() { _ = pairRepo.Remove(ctx, pair.Pair) })

	// Maker creates offer: deposit BTC, want 500 units of asset.
	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.0005)

	intro := newIntroClient(t)
	wantAssetID, err := asset.NewAssetIdFromString(assetID)
	require.NoError(t, err)
	offerResult, err := contract.CreateOffer(ctx, contract.CreateOfferParams{
		WantAmount: 500,
		WantAsset:  wantAssetID,
	}, maker, intro)
	require.NoError(t, err)

	// Subscribe to maker vtxo events before funding the swap.
	makerVtxoCh := maker.GetVtxoEventChannel(ctx)

	// Fund swap address with 500 sats BTC + offer packet. Deposit must
	// equal WantAmount for price=1.0 with decimals=0.
	sendOffChainWithExtension(t, maker, clientTypes.Receiver{
		To:     offerResult.SwapAddress,
		Amount: 500,
	}, offerResult.Packet)

	// Wait for the taker's fulfill: the maker should observe an asset
	// VTXO landing at their address.
	requireAssetFulfillment(t, ctx, makerVtxoCh, assetID, 60*time.Second)
}

// TestBancoAssetToAsset: maker deposits assetA, wants assetB.
// Mock price feed returns 1.0. With BaseDecimals=0, QuoteDecimals=0:
//
//	price = depositAmount/wantAmount = 500/500 = 1.0 ✓
func TestBancoAssetToAsset(t *testing.T) {
	ctx := t.Context()

	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.0005)
	assetA := issueAsset(t, maker, 500)

	// Fund taker bot with assetB.
	tempClient := setupArkClient(t)
	faucetOffchain(t, tempClient, 0.001)
	assetB := issueAsset(t, tempClient, 1000)

	takerAddr, err := takerSvc.GetAddress(ctx)
	require.NoError(t, err)

	takerVtxoCh := takerClient.GetVtxoEventChannel(ctx)
	_, err = tempClient.SendOffChain(ctx, []clientTypes.Receiver{{
		To:     takerAddr.OffchainAddress,
		Amount: 1000,
		Assets: []clientTypes.Asset{{AssetId: assetB, Amount: 1000}},
	}})
	require.NoError(t, err)
	waitForVtxoAdded(t, ctx, takerVtxoCh, 30*time.Second)

	pair := banco.Pair{
		Pair:          assetA + "/" + assetB,
		MinAmount:     1,
		MaxAmount:     100000000,
		BaseDecimals:  0,
		QuoteDecimals: 0,
		PriceFeed:     mockPriceFeedURL,
	}
	require.NoError(t, pairRepo.Add(ctx, pair))
	t.Cleanup(func() { _ = pairRepo.Remove(ctx, pair.Pair) })

	intro := newIntroClient(t)
	wantAssetID, err := asset.NewAssetIdFromString(assetB)
	require.NoError(t, err)
	offerResult, err := contract.CreateOffer(ctx, contract.CreateOfferParams{
		WantAmount: 500,
		WantAsset:  wantAssetID,
	}, maker, intro)
	require.NoError(t, err)

	makerVtxoCh := maker.GetVtxoEventChannel(ctx)

	sendOffChainWithExtension(t, maker, clientTypes.Receiver{
		To:     offerResult.SwapAddress,
		Amount: 450,
		Assets: []clientTypes.Asset{{AssetId: assetA, Amount: 500}},
	}, offerResult.Packet)

	requireAssetFulfillment(t, ctx, makerVtxoCh, assetB, 60*time.Second)
}

// waitForVtxoAdded blocks until vtxoCh delivers a VtxosAdded event or the
// timeout expires.
func waitForVtxoAdded(
	t *testing.T,
	ctx context.Context, // see below
	vtxoCh <-chan sdktypes.VtxoEvent,
	timeout time.Duration,
) []clientTypes.Vtxo {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled while waiting for VtxosAdded: %v", ctx.Err())
			return nil
		case <-deadline.C:
			t.Fatalf("timed out waiting for VtxosAdded within %v", timeout)
			return nil
		case ev, ok := <-vtxoCh:
			if !ok {
				t.Fatal("vtxo event channel closed while waiting for VtxosAdded")
				return nil
			}
			if ev.Type == sdktypes.VtxosAdded && len(ev.Vtxos) > 0 {
				return ev.Vtxos
			}
		}
	}
}

// requireFulfillment waits for ANY VtxosAdded event on the maker's vtxo
// channel — used for asset->BTC where the maker's wanted side is plain BTC.
func requireFulfillment(
	t *testing.T,
	ctx context.Context,
	vtxoCh <-chan sdktypes.VtxoEvent,
	timeout time.Duration,
) {
	t.Helper()
	vtxos := waitForVtxoAdded(t, ctx, vtxoCh, timeout)
	require.NotEmpty(t, vtxos, "expected fulfilled VTXO at maker")
}

// requireAssetFulfillment waits for a VtxosAdded event on the maker's vtxo
// channel whose VTXOs carry the given asset ID. Used when the maker is
// receiving an asset (not plain BTC).
func requireAssetFulfillment(
	t *testing.T,
	ctx context.Context,
	vtxoCh <-chan sdktypes.VtxoEvent,
	assetID string,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled while waiting for asset %s fulfillment: %v", assetID, ctx.Err())
			return
		case <-deadline.C:
			t.Fatalf("timed out waiting for asset %s fulfillment within %v", assetID, timeout)
			return
		case ev, ok := <-vtxoCh:
			if !ok {
				t.Fatalf("vtxo event channel closed while waiting for asset %s", assetID)
				return
			}
			if ev.Type != sdktypes.VtxosAdded {
				continue
			}
			for _, v := range ev.Vtxos {
				for _, a := range v.Assets {
					if a.AssetId == assetID {
						return
					}
				}
			}
		}
	}
}
