package e2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	sdktypes "github.com/arkade-os/go-sdk/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	swapv1 "github.com/arkade-os/solver/api-spec/protobuf/gen/go/solverd/v1"
	"github.com/arkade-os/solver/pkg/swap"
	"github.com/arkade-os/solver/pkg/swap/contract"
)

const (
	mockAssetBTCPriceFeed   = "http://solverd-pricefeed/asset-btc"
	mockBTCAssetPriceFeed   = "http://solverd-pricefeed/btc-asset"
	mockAssetAssetPriceFeed = "http://solverd-pricefeed/asset-asset"
)

// TestSwapAssetToBTC: maker deposits asset, wants BTC.
func TestSwapAssetToBTC(t *testing.T) {
	ctx := t.Context()

	// Create maker, fund with offchain BTC, issue asset.
	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.0005)
	assetID := issueAsset(t, maker, 500)

	market := swap.Market{
		BaseAsset:      assetID,
		QuoteAsset:     "BTC",
		MinQuoteAmount: 1,
		MaxQuoteAmount: 100000000,
		MinBaseAmount:  1,
		MaxBaseAmount:  100000000,
		PriceFeed:      mockAssetBTCPriceFeed,
	}
	addMarket(t, market)

	// Maker creates offer: deposit asset, want 500 sats BTC.
	emulator := newEmulatorClient(t)
	offerResult, err := contract.CreateOffer(ctx, contract.CreateOfferParams{
		WantAmount: 500,
	}, maker, emulator)
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

// TestSwapBTCToAsset: maker deposits BTC, wants asset.
func TestSwapBTCToAsset(t *testing.T) {
	ctx := t.Context()

	// Issue asset on a temp wallet and send it to the taker bot, so the
	// taker has asset balance to fulfill the maker's offer.
	tempClient := setupArkClient(t)
	faucetOffchain(t, tempClient, 0.001)
	assetID := issueAsset(t, tempClient, 1000)

	fundSolverAsset(t, ctx, tempClient, assetID, 1000)

	market := swap.Market{
		BaseAsset:      "BTC",
		QuoteAsset:     assetID,
		MinQuoteAmount: 1,
		MaxQuoteAmount: 100000000,
		MinBaseAmount:  1,
		MaxBaseAmount:  100000000,
		PriceFeed:      mockBTCAssetPriceFeed,
	}
	addMarket(t, market)

	// Maker creates offer: deposit BTC, want 500 units of asset.
	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.0005)

	emulator := newEmulatorClient(t)
	wantAssetID, err := asset.NewAssetIdFromString(assetID)
	require.NoError(t, err)
	offerResult, err := contract.CreateOffer(ctx, contract.CreateOfferParams{
		WantAmount: 500,
		WantAsset:  wantAssetID,
	}, maker, emulator)
	require.NoError(t, err)

	// Subscribe to maker vtxo events before funding the swap.
	makerVtxoCh := maker.GetVtxoEventChannel(ctx)

	// Fund swap address with 500 sats BTC + offer packet.
	sendOffChainWithExtension(t, maker, clientTypes.Receiver{
		To:     offerResult.SwapAddress,
		Amount: 500,
	}, offerResult.Packet)

	// Wait for the taker's fulfill: the maker should observe an asset
	// VTXO landing at their address.
	requireAssetFulfillment(t, ctx, makerVtxoCh, assetID, 60*time.Second)
}

// TestSwapAssetToAsset: maker deposits assetA, wants assetB.
func TestSwapAssetToAsset(t *testing.T) {
	ctx := t.Context()

	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.0005)
	assetA := issueAsset(t, maker, 500)

	// Fund taker bot with assetB.
	tempClient := setupArkClient(t)
	faucetOffchain(t, tempClient, 0.001)
	assetB := issueAsset(t, tempClient, 1000)

	fundSolverAsset(t, ctx, tempClient, assetB, 1000)

	market := swap.Market{
		BaseAsset:      assetA,
		QuoteAsset:     assetB,
		MinQuoteAmount: 1,
		MaxQuoteAmount: 100000000,
		MinBaseAmount:  1,
		MaxBaseAmount:  100000000,
		PriceFeed:      mockAssetAssetPriceFeed,
	}
	addMarket(t, market)

	emulator := newEmulatorClient(t)
	wantAssetID, err := asset.NewAssetIdFromString(assetB)
	require.NoError(t, err)
	offerResult, err := contract.CreateOffer(ctx, contract.CreateOfferParams{
		WantAmount: 500,
		WantAsset:  wantAssetID,
	}, maker, emulator)
	require.NoError(t, err)

	makerVtxoCh := maker.GetVtxoEventChannel(ctx)

	sendOffChainWithExtension(t, maker, clientTypes.Receiver{
		To:     offerResult.SwapAddress,
		Amount: 450,
		Assets: []clientTypes.Asset{{AssetId: assetA, Amount: 500}},
	}, offerResult.Packet)

	requireAssetFulfillment(t, ctx, makerVtxoCh, assetB, 60*time.Second)
}

// TestSwapSingleMarketBothDirections: a single bidirectional market serves
// both a sell-base offer (deposit asset, want BTC) and a buy-base offer
// (deposit BTC, want asset).
func TestSwapSingleMarketBothDirections(t *testing.T) {
	ctx := t.Context()

	// Issuer mints the asset and keeps half; the other half goes to the taker
	// bot so it can pay the asset side of the buy-base direction.
	issuer := setupArkClient(t)
	faucetOffchain(t, issuer, 0.001)
	assetID := issueAsset(t, issuer, 2000)

	fundSolverAsset(t, ctx, issuer, assetID, 1000)

	// ONE market, both directions enabled.
	addMarket(t, swap.Market{
		BaseAsset:      assetID,
		QuoteAsset:     "BTC",
		MinQuoteAmount: 1,
		MaxQuoteAmount: 100000000,
		MinBaseAmount:  1,
		MaxBaseAmount:  100000000,
		PriceFeed:      mockAssetBTCPriceFeed,
	})

	// Direction 1 — sell-base: issuer deposits 500 asset, wants 500 sats BTC.
	sellOffer, err := contract.CreateOffer(ctx, contract.CreateOfferParams{
		WantAmount: 500,
	}, issuer, newEmulatorClient(t))
	require.NoError(t, err)
	require.NotEmpty(t, sellOffer.SwapAddress)

	issuerVtxoCh := issuer.GetVtxoEventChannel(ctx)
	sendOffChainWithExtension(t, issuer, clientTypes.Receiver{
		To:     sellOffer.SwapAddress,
		Amount: 450,
		Assets: []clientTypes.Asset{{AssetId: assetID, Amount: 500}},
	}, sellOffer.Packet)
	requireFulfillment(t, ctx, issuerVtxoCh, 60*time.Second)

	// Direction 2 — buy-base: a fresh maker deposits 500 sats BTC, wants 500 asset.
	buyMaker := setupArkClient(t)
	faucetOffchain(t, buyMaker, 0.0005)
	wantAssetID, err := asset.NewAssetIdFromString(assetID)
	require.NoError(t, err)
	buyOffer, err := contract.CreateOffer(ctx, contract.CreateOfferParams{
		WantAmount: 500,
		WantAsset:  wantAssetID,
	}, buyMaker, newEmulatorClient(t))
	require.NoError(t, err)

	buyMakerVtxoCh := buyMaker.GetVtxoEventChannel(ctx)
	sendOffChainWithExtension(t, buyMaker, clientTypes.Receiver{
		To:     buyOffer.SwapAddress,
		Amount: 500,
	}, buyOffer.Packet)
	requireAssetFulfillment(t, ctx, buyMakerVtxoCh, assetID, 60*time.Second)
}

// TestSwapMarketDisabledDirection: a market with the buy-base direction
// disabled (MaxBaseAmount == 0) must NOT fulfill a buy-base offer, even though
// the taker holds enough asset to do so.
func TestSwapMarketDisabledDirection(t *testing.T) {
	ctx := t.Context()

	// Fund the taker bot with the asset so lack-of-funds cannot explain the
	// rejection — the only reason the offer must fail is the disabled direction.
	issuer := setupArkClient(t)
	faucetOffchain(t, issuer, 0.001)
	assetID := issueAsset(t, issuer, 1000)

	fundSolverAsset(t, ctx, issuer, assetID, 1000)

	// Sell-base enabled, buy-base disabled (zero base bounds).
	addMarket(t, swap.Market{
		BaseAsset:      assetID,
		QuoteAsset:     "BTC",
		MinQuoteAmount: 1,
		MaxQuoteAmount: 100000000,
		MinBaseAmount:  0,
		MaxBaseAmount:  0,
		PriceFeed:      mockAssetBTCPriceFeed,
	})

	// Buy-base offer (deposit BTC, want asset) — the disabled direction.
	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.0005)
	wantAssetID, err := asset.NewAssetIdFromString(assetID)
	require.NoError(t, err)
	offer, err := contract.CreateOffer(ctx, contract.CreateOfferParams{
		WantAmount: 500,
		WantAsset:  wantAssetID,
	}, maker, newEmulatorClient(t))
	require.NoError(t, err)

	makerVtxoCh := maker.GetVtxoEventChannel(ctx)
	sendOffChainWithExtension(t, maker, clientTypes.Receiver{
		To:     offer.SwapAddress,
		Amount: 500,
	}, offer.Packet)

	// The solver must never fulfill: no asset VTXO should land at the maker.
	requireNoAssetFulfillment(t, ctx, makerVtxoCh, assetID, 30*time.Second)
}

// requireNoAssetFulfillment asserts that no VTXO carrying assetID lands at the
// maker within timeout. BTC change VTXOs from funding are ignored, so a false
// negative from settlement noise can't occur — only an asset payout fails it.
func requireNoAssetFulfillment(
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
			return
		case <-deadline.C:
			return // success: the disabled direction was never fulfilled
		case ev, ok := <-vtxoCh:
			if !ok {
				return
			}
			if ev.Type != sdktypes.VtxosAdded {
				continue
			}
			for _, v := range ev.Vtxos {
				for _, a := range v.Assets {
					if a.AssetId == assetID {
						t.Fatalf("disabled direction was fulfilled: asset %s VTXO landed at maker", assetID)
					}
				}
			}
		}
	}
}

func dialSwapClient(t *testing.T) swapv1.SwapServiceClient {
	t.Helper()
	conn, err := grpc.NewClient(e2eGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return swapv1.NewSwapServiceClient(conn)
}

func addMarket(t *testing.T, market swap.Market) {
	t.Helper()
	_, err := dialSwapClient(t).AddMarket(t.Context(), &swapv1.AddMarketRequest{
		Market: &swapv1.MarketInfo{
			BaseAsset:      market.BaseAsset,
			QuoteAsset:     market.QuoteAsset,
			MinQuoteAmount: market.MinQuoteAmount,
			MaxQuoteAmount: market.MaxQuoteAmount,
			MinBaseAmount:  market.MinBaseAmount,
			MaxBaseAmount:  market.MaxBaseAmount,
			PriceFeed:      market.PriceFeed,
			SlippageBps:    market.SlippageBps,
		},
	})
	require.NoError(t, err)
}

func getAddress(t *testing.T) *swapv1.GetAddressResponse {
	t.Helper()
	resp, err := dialWalletClient(t).GetAddress(t.Context(), &swapv1.GetAddressRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, resp.OffchainAddress)
	return resp
}

// waitForVtxoAdded blocks until vtxoCh delivers a VtxosAdded event or the
// timeout expires.
func waitForVtxoAdded(
	t *testing.T,
	ctx context.Context,
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
