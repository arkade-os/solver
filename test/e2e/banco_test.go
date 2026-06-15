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

	bancov1 "github.com/arkade-os/solver/api-spec/protobuf/gen/go/solverd/v1"
	"github.com/arkade-os/solver/pkg/banco"
	"github.com/arkade-os/solver/pkg/banco/contract"
)

const (
	mockAssetBTCPriceFeed   = "http://solverd-pricefeed/asset-btc"
	mockBTCAssetPriceFeed   = "http://solverd-pricefeed/btc-asset"
	mockAssetAssetPriceFeed = "http://solverd-pricefeed/asset-asset"
)

// TestBancoAssetToBTC: maker deposits asset, wants BTC.
func TestBancoAssetToBTC(t *testing.T) {
	ctx := t.Context()

	// Create maker, fund with offchain BTC, issue asset.
	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.0005)
	assetID := issueAsset(t, maker, 500)

	pair := banco.Pair{
		Pair:      assetID + "/BTC",
		MinAmount: 1,
		MaxAmount: 100000000,
		PriceFeed: mockAssetBTCPriceFeed,
	}
	addPair(t, pair)

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

// TestBancoBTCToAsset: maker deposits BTC, wants asset.
func TestBancoBTCToAsset(t *testing.T) {
	ctx := t.Context()

	// Issue asset on a temp wallet and send it to the taker bot, so the
	// taker has asset balance to fulfill the maker's offer.
	tempClient := setupArkClient(t)
	faucetOffchain(t, tempClient, 0.001)
	assetID := issueAsset(t, tempClient, 1000)

	solverAddr := getAddress(t)

	_, err := tempClient.SendOffChain(ctx, []clientTypes.Receiver{{
		To:     solverAddr.OffchainAddress,
		Amount: 1000,
		Assets: []clientTypes.Asset{{AssetId: assetID, Amount: 1000}},
	}})
	require.NoError(t, err)
	// Wait for the dockerized solver to report the incoming asset balance.
	pollSolverAssetBalance(t, ctx, assetID, 1000, 30*time.Second)

	pair := banco.Pair{
		Pair:      "BTC/" + assetID,
		MinAmount: 1,
		MaxAmount: 100000000,
		PriceFeed: mockBTCAssetPriceFeed,
	}
	addPair(t, pair)

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

// TestBancoAssetToAsset: maker deposits assetA, wants assetB.
func TestBancoAssetToAsset(t *testing.T) {
	ctx := t.Context()

	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.0005)
	assetA := issueAsset(t, maker, 500)

	// Fund taker bot with assetB.
	tempClient := setupArkClient(t)
	faucetOffchain(t, tempClient, 0.001)
	assetB := issueAsset(t, tempClient, 1000)

	solverAddr := getAddress(t)

	_, err := tempClient.SendOffChain(ctx, []clientTypes.Receiver{{
		To:     solverAddr.OffchainAddress,
		Amount: 1000,
		Assets: []clientTypes.Asset{{AssetId: assetB, Amount: 1000}},
	}})
	require.NoError(t, err)
	pollSolverAssetBalance(t, ctx, assetB, 1000, 30*time.Second)

	pair := banco.Pair{
		Pair:      assetA + "/" + assetB,
		MinAmount: 1,
		MaxAmount: 100000000,
		PriceFeed: mockAssetAssetPriceFeed,
	}
	addPair(t, pair)

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

func dialBancoClient(t *testing.T) bancov1.BancoServiceClient {
	t.Helper()
	conn, err := grpc.NewClient(e2eGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return bancov1.NewBancoServiceClient(conn)
}

func addPair(t *testing.T, pair banco.Pair) {
	t.Helper()
	_, err := dialBancoClient(t).AddPair(t.Context(), &bancov1.AddPairRequest{
		Pair: &bancov1.PairInfo{
			Pair:        pair.Pair,
			MinAmount:   pair.MinAmount,
			MaxAmount:   pair.MaxAmount,
			PriceFeed:   pair.PriceFeed,
			InvertPrice: pair.InvertPrice,
		},
	})
	require.NoError(t, err)
}

func getAddress(t *testing.T) *bancov1.GetAddressResponse {
	t.Helper()
	resp, err := dialWalletClient(t).GetAddress(t.Context(), &bancov1.GetAddressRequest{})
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
