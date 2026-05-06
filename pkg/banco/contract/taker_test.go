package contract

import (
	"testing"

	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSecondTestAssetId builds a different 34-byte asset ID for use in error-path tests.
func newSecondTestAssetId(t *testing.T) *asset.AssetId {
	t.Helper()
	assetBytes := make([]byte, 34)
	assetBytes[0] = 0xCD
	for i := 1; i < 32; i++ {
		assetBytes[i] = byte(i + 100)
	}
	id, err := asset.NewAssetIdFromBytes(assetBytes)
	require.NoError(t, err)
	return id
}

// TestBuildFulfillAssetPacket_NoAssets verifies that when neither the swap VTXO
// nor taker VTXOs carry any assets, the function returns nil, nil.
func TestBuildFulfillAssetPacket_NoAssets(t *testing.T) {
	swapVtxo := clientTypes.Vtxo{
		Amount: 10_000,
		// Assets is nil / empty
	}
	takerVtxos := []clientTypes.VtxoWithTapTree{
		{Vtxo: clientTypes.Vtxo{Amount: 5_000}},
	}
	offer := *testMinimalOffer(t)
	offer.WantAsset = nil

	pkt, err := buildFulfillAssetPacket(swapVtxo, takerVtxos, offer)
	require.NoError(t, err)
	assert.Nil(t, pkt)
}

// TestBuildFulfillAssetPacket_WantAssetRouting verifies that when offer.WantAsset
// is set and the asset exists in inputs, a non-nil packet is returned and the
// wanted asset's amount is routed to output 0.
func TestBuildFulfillAssetPacket_WantAssetRouting(t *testing.T) {
	assetId := newTestAssetId(t)

	// Swap VTXO holds 500 units of the asset.
	swapVtxo := clientTypes.Vtxo{
		Amount: 1_000,
		Assets: []clientTypes.Asset{
			{AssetId: assetId.String(), Amount: 500},
		},
	}
	// Taker also holds 200 units of the same asset.
	takerVtxos := []clientTypes.VtxoWithTapTree{
		{
			Vtxo: clientTypes.Vtxo{
				Amount: 5_000,
				Assets: []clientTypes.Asset{
					{AssetId: assetId.String(), Amount: 200},
				},
			},
		},
	}
	offer := *testMinimalOffer(t)
	offer.WantAsset = assetId
	offer.WantAmount = 300

	pkt, err := buildFulfillAssetPacket(swapVtxo, takerVtxos, offer)
	require.NoError(t, err)
	assert.NotNil(t, pkt)
}

// TestBuildFulfillAssetPacket_ChangesToTaker verifies that when swap VTXO holds
// more asset than the maker wants, the remainder is routed to the taker output
// (output index 1) and the returned packet is non-nil.
func TestBuildFulfillAssetPacket_ChangesToTaker(t *testing.T) {
	assetId := newTestAssetId(t)

	// Swap VTXO holds 1000 units; maker wants only 400 → 600 should go to taker.
	swapVtxo := clientTypes.Vtxo{
		Amount: 2_000,
		Assets: []clientTypes.Asset{
			{AssetId: assetId.String(), Amount: 1000},
		},
	}
	// No taker VTXOs with assets.
	takerVtxos := []clientTypes.VtxoWithTapTree{
		{Vtxo: clientTypes.Vtxo{Amount: 5_000}},
	}
	offer := *testMinimalOffer(t)
	offer.WantAsset = assetId
	offer.WantAmount = 400

	pkt, err := buildFulfillAssetPacket(swapVtxo, takerVtxos, offer)
	require.NoError(t, err)
	assert.NotNil(t, pkt, "expected non-nil packet when change goes to taker output")
}

// TestBuildFulfillAssetPacket_WantAssetNotInInputs verifies that requesting an
// asset that does not exist in any input returns an error containing "not found in inputs".
func TestBuildFulfillAssetPacket_WantAssetNotInInputs(t *testing.T) {
	presentAsset := newTestAssetId(t)
	missingAsset := newSecondTestAssetId(t)

	// Swap VTXO holds the first asset, but the offer asks for the second.
	swapVtxo := clientTypes.Vtxo{
		Amount: 1_000,
		Assets: []clientTypes.Asset{
			{AssetId: presentAsset.String(), Amount: 500},
		},
	}
	takerVtxos := []clientTypes.VtxoWithTapTree{
		{Vtxo: clientTypes.Vtxo{Amount: 5_000}},
	}
	offer := *testMinimalOffer(t)
	offer.WantAsset = missingAsset
	offer.WantAmount = 100

	_, err := buildFulfillAssetPacket(swapVtxo, takerVtxos, offer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in inputs")
}
