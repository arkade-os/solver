package e2e_test

import (
	"sync"
	"testing"
	"time"

	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	"github.com/stretchr/testify/require"

	"github.com/arkade-os/bancod/pkg/banco"
	"github.com/arkade-os/bancod/pkg/contract"
)

const mockPriceFeedURL = "http://mock-price-feed"

// TestBancoAssetToBTC: maker deposits asset, wants BTC.
// Mock price feed returns 1.0. With BaseDecimals=0, QuoteDecimals=0:
//
//	price = depositAmount/wantAmount = 500/500 = 1.0 ✓
func TestBancoAssetToBTC(t *testing.T) {
	ctx := t.Context()

	// Create maker, fund with offchain BTC, issue asset
	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.0005)
	assetID := issueAsset(t, maker, 500)
	time.Sleep(3 * time.Second)

	// Record maker BTC balance before
	balBefore, err := maker.Balance(ctx)
	require.NoError(t, err)
	btcBefore := balBefore.OffchainBalance.Total

	// Configure taker pair: asset/BTC. Both BTC deposit and BTC want now
	// stringify as the literal "BTC", so findMatchingPair lines up against
	// WantAssetStr(). We still write directly to pairRepo (instead of going
	// through takerSvc.AddPair) so we can pin BaseDecimals=QuoteDecimals=0;
	// AddPair would resolve BTC's quote decimals to 8 and the mock price
	// feed (1.0) would no longer match the 500/500 offer ratio.
	pair := banco.Pair{
		Pair:          assetID + "/BTC",
		MinAmount:     1,
		MaxAmount:     100000000,
		BaseDecimals:  0,
		QuoteDecimals: 0,
		PriceFeed:     mockPriceFeedURL,
	}
	err = pairRepo.Add(ctx, pair)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pairRepo.Remove(ctx, pair.Pair) })

	// Maker creates offer: deposit asset, want 500 sats BTC
	intro := newIntroClient(t)
	offerResult, err := contract.CreateOffer(ctx, contract.CreateOfferParams{
		WantAmount: 500,
	}, maker, intro)
	require.NoError(t, err)
	require.NotEmpty(t, offerResult.SwapAddress)

	// Fund swap address with asset + offer packet
	// Deposit 500 units of asset. The BTC amount (450) is just a dust carrier.
	// The solver reads DepositAmount from the asset packet (=500).
	wg := &sync.WaitGroup{}
	wg.Add(1)
	var incomingErr error
	go func() {
		defer wg.Done()
		_, incomingErr = maker.NotifyIncomingFunds(ctx, offerResult.SwapAddress)
	}()

	sendOffChainWithExtension(t, maker, clientTypes.Receiver{
		To:     offerResult.SwapAddress,
		Amount: 450,
		Assets: []clientTypes.Asset{{AssetId: assetID, Amount: 500}},
	}, offerResult.Packet)
	wg.Wait()
	require.NoError(t, incomingErr)

	// Wait for taker bot to fulfill — maker's BTC balance should increase
	waitForCondition(t, 30*time.Second, 2*time.Second, func() bool {
		bal, err := maker.Balance(ctx)
		if err != nil {
			return false
		}
		return bal.OffchainBalance.Total > btcBefore
	})
	t.Log("asset->BTC: taker fulfilled successfully")
}

// TestBancoBTCToAsset: maker deposits BTC, wants asset.
// Mock price feed returns 1.0. With BaseDecimals=0, QuoteDecimals=0:
//
//	price = depositAmount/wantAmount = 500/500 = 1.0 ✓
func TestBancoBTCToAsset(t *testing.T) {
	ctx := t.Context()

	// Issue asset and send to taker bot
	tempClient := setupArkClient(t)
	faucetOffchain(t, tempClient, 0.001)
	assetID := issueAsset(t, tempClient, 1000)
	time.Sleep(3 * time.Second)

	// Send asset to taker's wallet and notify
	takerAddr, err := takerSvc.GetAddress(ctx)
	require.NoError(t, err)

	wgFund := &sync.WaitGroup{}
	wgFund.Add(1)
	go func() {
		defer wgFund.Done()
		_, _ = takerClient.NotifyIncomingFunds(ctx, takerAddr.OffchainAddress)
	}()

	_, err = tempClient.SendOffChain(ctx, []clientTypes.Receiver{{
		To:     takerAddr.OffchainAddress,
		Amount: 1000,
		Assets: []clientTypes.Asset{{AssetId: assetID, Amount: 1000}},
	}})
	require.NoError(t, err)
	wgFund.Wait()
	time.Sleep(3 * time.Second)

	// Configure pair: BTC/asset with both decimals=0
	pair := banco.Pair{
		Pair:          "BTC/" + assetID,
		MinAmount:     1,
		MaxAmount:     100000000,
		BaseDecimals:  0,
		QuoteDecimals: 0,
		PriceFeed:     mockPriceFeedURL,
	}
	err = pairRepo.Add(ctx, pair)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pairRepo.Remove(ctx, pair.Pair) })

	// Maker creates offer: deposit BTC, want 500 units of asset
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

	// Fund swap address with 500 sats BTC + offer packet.
	// Deposit must equal WantAmount for price=1.0 with decimals=0.
	wg := &sync.WaitGroup{}
	wg.Add(1)
	var incomingErr error
	go func() {
		defer wg.Done()
		_, incomingErr = maker.NotifyIncomingFunds(ctx, offerResult.SwapAddress)
	}()

	sendOffChainWithExtension(t, maker, clientTypes.Receiver{
		To:     offerResult.SwapAddress,
		Amount: 500,
	}, offerResult.Packet)
	wg.Wait()
	require.NoError(t, incomingErr)

	// Wait for maker to receive asset
	waitForCondition(t, 30*time.Second, 2*time.Second, func() bool {
		vtxos := listVtxosWithAsset(t, maker, assetID)
		return len(vtxos) > 0
	})
	t.Log("BTC->asset: taker fulfilled successfully")
}

// TestBancoAssetToAsset: maker deposits assetA, wants assetB.
// Mock price feed returns 1.0. With BaseDecimals=0, QuoteDecimals=0:
//
//	price = depositAmount/wantAmount = 500/500 = 1.0 ✓
func TestBancoAssetToAsset(t *testing.T) {
	ctx := t.Context()

	// Create maker, fund, issue assetA
	maker := setupArkClient(t)
	faucetOffchain(t, maker, 0.0005)
	assetA := issueAsset(t, maker, 500)

	// Issue assetB and send to taker
	tempClient := setupArkClient(t)
	faucetOffchain(t, tempClient, 0.001)
	assetB := issueAsset(t, tempClient, 1000)
	time.Sleep(3 * time.Second)

	takerAddr, err := takerSvc.GetAddress(ctx)
	require.NoError(t, err)

	wgFund := &sync.WaitGroup{}
	wgFund.Add(1)
	go func() {
		defer wgFund.Done()
		_, _ = takerClient.NotifyIncomingFunds(ctx, takerAddr.OffchainAddress)
	}()

	_, err = tempClient.SendOffChain(ctx, []clientTypes.Receiver{{
		To:     takerAddr.OffchainAddress,
		Amount: 1000,
		Assets: []clientTypes.Asset{{AssetId: assetB, Amount: 1000}},
	}})
	require.NoError(t, err)
	wgFund.Wait()
	time.Sleep(3 * time.Second)

	// Configure pair: assetA/assetB
	pair := banco.Pair{
		Pair:          assetA + "/" + assetB,
		MinAmount:     1,
		MaxAmount:     100000000,
		BaseDecimals:  0,
		QuoteDecimals: 0,
		PriceFeed:     mockPriceFeedURL,
	}
	err = pairRepo.Add(ctx, pair)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pairRepo.Remove(ctx, pair.Pair) })

	// Maker creates offer: deposit assetA, want assetB
	intro := newIntroClient(t)
	wantAssetID, err := asset.NewAssetIdFromString(assetB)
	require.NoError(t, err)
	offerResult, err := contract.CreateOffer(ctx, contract.CreateOfferParams{
		WantAmount: 500,
		WantAsset:  wantAssetID,
	}, maker, intro)
	require.NoError(t, err)

	// Fund swap address with assetA + offer packet
	wg := &sync.WaitGroup{}
	wg.Add(1)
	var incomingErr error
	go func() {
		defer wg.Done()
		_, incomingErr = maker.NotifyIncomingFunds(ctx, offerResult.SwapAddress)
	}()

	sendOffChainWithExtension(t, maker, clientTypes.Receiver{
		To:     offerResult.SwapAddress,
		Amount: 450,
		Assets: []clientTypes.Asset{{AssetId: assetA, Amount: 500}},
	}, offerResult.Packet)
	wg.Wait()
	require.NoError(t, incomingErr)

	// Wait for maker to receive assetB
	waitForCondition(t, 30*time.Second, 2*time.Second, func() bool {
		vtxos := listVtxosWithAsset(t, maker, assetB)
		return len(vtxos) > 0
	})
	t.Log("assetA->assetB: taker fulfilled successfully")
}
