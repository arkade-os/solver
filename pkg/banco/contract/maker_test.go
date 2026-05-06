package contract

import (
	"context"
	"encoding/hex"
	"testing"

	introclient "github.com/ArkLabsHQ/introspector/pkg/client"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock: introclient.TransportClient
// ---------------------------------------------------------------------------

type mockIntroClient struct {
	introclient.TransportClient // embed for unimplemented methods
	getInfoFn                   func(ctx context.Context) (*introclient.Info, error)
}

func (m *mockIntroClient) GetInfo(ctx context.Context) (*introclient.Info, error) {
	return m.getInfoFn(ctx)
}

// ---------------------------------------------------------------------------
// Mock: arksdk.ArkClient (only methods used by CreateOffer)
// ---------------------------------------------------------------------------

type mockArkClient struct {
	arksdk.ArkClient     // embed for unimplemented methods
	getConfigDataFn      func(ctx context.Context) (*clientTypes.Config, error)
	newOffchainAddressFn func(ctx context.Context) (string, error)
}

func (m *mockArkClient) GetConfigData(ctx context.Context) (*clientTypes.Config, error) {
	return m.getConfigDataFn(ctx)
}

func (m *mockArkClient) NewOffchainAddress(ctx context.Context) (string, error) {
	return m.newOffchainAddressFn(ctx)
}

// ---------------------------------------------------------------------------
// Mock: indexer.Indexer (only methods used by GetOffers)
// ---------------------------------------------------------------------------

type mockIndexer struct {
	indexer.Indexer // embed for unimplemented methods
	getVtxosFn      func(ctx context.Context, opts ...indexer.GetVtxosOption) (*indexer.VtxosResponse, error)
}

func (m *mockIndexer) GetVtxos(ctx context.Context, opts ...indexer.GetVtxosOption) (*indexer.VtxosResponse, error) {
	return m.getVtxosFn(ctx, opts...)
}

// ---------------------------------------------------------------------------
// CreateOffer tests
// ---------------------------------------------------------------------------

func TestCreateOffer_CancelNotSupported(t *testing.T) {
	params := CreateOfferParams{WantAmount: 100_000, CancelAt: 1700000000}
	_, err := CreateOffer(context.Background(), params, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancel path not supported")
}

func TestCreateOffer_ExitNotSupported(t *testing.T) {
	params := CreateOfferParams{
		WantAmount: 100_000,
		ExitDelay:  &arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 144},
	}
	_, err := CreateOffer(context.Background(), params, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit not supported")
}

func TestCreateOffer_HappyPath(t *testing.T) {
	// Generate keys for the mock services
	_, signerPub := testKeyPair(t)
	_, introPub := testKeyPair(t)
	_, vtxoPub := testKeyPair(t)

	// Build a valid Ark address that NewOffchainAddress will return
	addr := &arklib.Address{
		HRP:        arklib.BitcoinRegTest.Addr,
		Signer:     signerPub,
		VtxoTapKey: vtxoPub,
	}
	encodedAddr, err := addr.EncodeV0()
	require.NoError(t, err)

	introClient := &mockIntroClient{
		getInfoFn: func(ctx context.Context) (*introclient.Info, error) {
			// Return compressed (33-byte) pubkey hex — btcec.ParsePubKey expects compressed
			return &introclient.Info{
				SignerPublicKey: hex.EncodeToString(introPub.SerializeCompressed()),
			}, nil
		},
	}

	arkClient := &mockArkClient{
		newOffchainAddressFn: func(ctx context.Context) (string, error) {
			return encodedAddr, nil
		},
		getConfigDataFn: func(ctx context.Context) (*clientTypes.Config, error) {
			return &clientTypes.Config{
				SignerPubKey: signerPub,
				Network:      arklib.BitcoinRegTest,
			}, nil
		},
	}

	params := CreateOfferParams{WantAmount: 50_000}
	result, err := CreateOffer(context.Background(), params, arkClient, introClient)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.NotEmpty(t, result.OfferHex)
	assert.NotEmpty(t, result.SwapAddress)
	assert.NotNil(t, result.Packet)
	assert.Equal(t, PacketType, result.Packet.Type())

	// Verify the offer can be deserialized from the hex
	offerBytes, err := hex.DecodeString(result.OfferHex)
	require.NoError(t, err)
	decoded, err := DeserializeOffer(offerBytes)
	require.NoError(t, err)
	assert.Equal(t, uint64(50_000), decoded.WantAmount)
	assert.Len(t, decoded.MakerPkScript, 34)
	assert.Equal(t,
		schnorr.SerializePubKey(introPub),
		schnorr.SerializePubKey(decoded.IntrospectorPubkey),
	)
}

func TestCreateOffer_IntroClientError(t *testing.T) {
	introClient := &mockIntroClient{
		getInfoFn: func(ctx context.Context) (*introclient.Info, error) {
			return nil, assert.AnError
		},
	}

	params := CreateOfferParams{WantAmount: 50_000}
	_, err := CreateOffer(context.Background(), params, nil, introClient)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get introspector info")
}

func TestCreateOffer_BadIntroPubkeyHex(t *testing.T) {
	introClient := &mockIntroClient{
		getInfoFn: func(ctx context.Context) (*introclient.Info, error) {
			return &introclient.Info{SignerPublicKey: "not-hex"}, nil
		},
	}

	params := CreateOfferParams{WantAmount: 50_000}
	_, err := CreateOffer(context.Background(), params, nil, introClient)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode introspector pubkey")
}

func TestCreateOffer_InvalidIntroPubkey(t *testing.T) {
	introClient := &mockIntroClient{
		getInfoFn: func(ctx context.Context) (*introclient.Info, error) {
			return &introclient.Info{SignerPublicKey: hex.EncodeToString([]byte{0x01, 0x02})}, nil
		},
	}

	params := CreateOfferParams{WantAmount: 50_000}
	_, err := CreateOffer(context.Background(), params, nil, introClient)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse introspector pubkey")
}

func TestCreateOffer_ArkClientAddressError(t *testing.T) {
	_, introPub := testKeyPair(t)

	introClient := &mockIntroClient{
		getInfoFn: func(ctx context.Context) (*introclient.Info, error) {
			return &introclient.Info{
				SignerPublicKey: hex.EncodeToString(introPub.SerializeCompressed()),
			}, nil
		},
	}

	arkClient := &mockArkClient{
		newOffchainAddressFn: func(ctx context.Context) (string, error) {
			return "", assert.AnError
		},
	}

	params := CreateOfferParams{WantAmount: 50_000}
	_, err := CreateOffer(context.Background(), params, arkClient, introClient)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get maker address")
}

func TestCreateOffer_GetConfigError(t *testing.T) {
	_, signerPub := testKeyPair(t)
	_, introPub := testKeyPair(t)
	_, vtxoPub := testKeyPair(t)

	addr := &arklib.Address{
		HRP:        arklib.BitcoinRegTest.Addr,
		Signer:     signerPub,
		VtxoTapKey: vtxoPub,
	}
	encodedAddr, err := addr.EncodeV0()
	require.NoError(t, err)

	introClient := &mockIntroClient{
		getInfoFn: func(ctx context.Context) (*introclient.Info, error) {
			return &introclient.Info{
				SignerPublicKey: hex.EncodeToString(introPub.SerializeCompressed()),
			}, nil
		},
	}

	arkClient := &mockArkClient{
		newOffchainAddressFn: func(ctx context.Context) (string, error) {
			return encodedAddr, nil
		},
		getConfigDataFn: func(ctx context.Context) (*clientTypes.Config, error) {
			return nil, assert.AnError
		},
	}

	params := CreateOfferParams{WantAmount: 50_000}
	_, err = CreateOffer(context.Background(), params, arkClient, introClient)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// GetOffers tests
// ---------------------------------------------------------------------------

func TestGetOffers_HappyPath(t *testing.T) {
	_, signerPub := testKeyPair(t)
	_, vtxoPub := testKeyPair(t)

	// Build a valid swap address
	addr := &arklib.Address{
		HRP:        arklib.BitcoinRegTest.Addr,
		Signer:     signerPub,
		VtxoTapKey: vtxoPub,
	}
	swapAddress, err := addr.EncodeV0()
	require.NoError(t, err)

	idx := &mockIndexer{
		getVtxosFn: func(ctx context.Context, opts ...indexer.GetVtxosOption) (*indexer.VtxosResponse, error) {
			return &indexer.VtxosResponse{
				Vtxos: []clientTypes.Vtxo{
					{
						Outpoint: clientTypes.Outpoint{Txid: "aabb", VOut: 0},
						Amount:   100_000,
						Spent:    false,
					},
					{
						Outpoint: clientTypes.Outpoint{Txid: "ccdd", VOut: 1},
						Amount:   50_000,
						Spent:    true,
					},
				},
			}, nil
		},
	}

	statuses, err := GetOffers(context.Background(), swapAddress, idx)
	require.NoError(t, err)
	require.Len(t, statuses, 2)

	assert.Equal(t, "aabb", statuses[0].Txid)
	assert.Equal(t, uint32(0), statuses[0].VOut)
	assert.Equal(t, uint64(100_000), statuses[0].Value)
	assert.True(t, statuses[0].Spendable)

	assert.Equal(t, "ccdd", statuses[1].Txid)
	assert.Equal(t, uint32(1), statuses[1].VOut)
	assert.Equal(t, uint64(50_000), statuses[1].Value)
	assert.False(t, statuses[1].Spendable)
}

func TestGetOffers_InvalidAddress(t *testing.T) {
	idx := &mockIndexer{}
	_, err := GetOffers(context.Background(), "not-a-valid-address", idx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode swap address")
}

func TestGetOffers_IndexerError(t *testing.T) {
	_, signerPub := testKeyPair(t)
	_, vtxoPub := testKeyPair(t)

	addr := &arklib.Address{
		HRP:        arklib.BitcoinRegTest.Addr,
		Signer:     signerPub,
		VtxoTapKey: vtxoPub,
	}
	swapAddress, err := addr.EncodeV0()
	require.NoError(t, err)

	idx := &mockIndexer{
		getVtxosFn: func(ctx context.Context, opts ...indexer.GetVtxosOption) (*indexer.VtxosResponse, error) {
			return nil, assert.AnError
		},
	}

	_, err = GetOffers(context.Background(), swapAddress, idx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get vtxos")
}

func TestGetOffers_EmptyVtxos(t *testing.T) {
	_, signerPub := testKeyPair(t)
	_, vtxoPub := testKeyPair(t)

	addr := &arklib.Address{
		HRP:        arklib.BitcoinRegTest.Addr,
		Signer:     signerPub,
		VtxoTapKey: vtxoPub,
	}
	swapAddress, err := addr.EncodeV0()
	require.NoError(t, err)

	idx := &mockIndexer{
		getVtxosFn: func(ctx context.Context, opts ...indexer.GetVtxosOption) (*indexer.VtxosResponse, error) {
			return &indexer.VtxosResponse{Vtxos: []clientTypes.Vtxo{}}, nil
		},
	}

	statuses, err := GetOffers(context.Background(), swapAddress, idx)
	require.NoError(t, err)
	assert.Empty(t, statuses)
}
