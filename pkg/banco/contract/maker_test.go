package contract

import (
	"context"
	"encoding/hex"
	"testing"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/client-lib/identity"
	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	clientTypes "github.com/arkade-os/arkd/pkg/client-lib/types"
	emulatorclient "github.com/arkade-os/emulator/pkg/client"
	arksdk "github.com/arkade-os/go-sdk"
	sdkcontract "github.com/arkade-os/go-sdk/contract"
	"github.com/arkade-os/go-sdk/contract/handlers"
	sdktypes "github.com/arkade-os/go-sdk/types"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock: emulatorclient.TransportClient
// ---------------------------------------------------------------------------

type mockEmulatorClient struct {
	emulatorclient.TransportClient // embed for unimplemented methods
	getInfoFn                      func(ctx context.Context) (*emulatorclient.Info, error)
}

func (m *mockEmulatorClient) GetInfo(ctx context.Context) (*emulatorclient.Info, error) {
	return m.getInfoFn(ctx)
}

// ---------------------------------------------------------------------------
// Mock: arksdk.Wallet (only methods used by CreateOffer)
// ---------------------------------------------------------------------------

type mockArkClient struct {
	arksdk.Wallet     // embed for unimplemented methods
	getConfigDataFn   func(ctx context.Context) (*clientTypes.Config, error)
	contractManagerFn func() sdkcontract.Manager
}

func (m *mockArkClient) GetConfigData(ctx context.Context) (*clientTypes.Config, error) {
	return m.getConfigDataFn(ctx)
}

func (m *mockArkClient) ContractManager() sdkcontract.Manager {
	return m.contractManagerFn()
}

// ---------------------------------------------------------------------------
// Mock: contract.Manager + handlers.Handler (only methods used by CreateOffer)
// ---------------------------------------------------------------------------

type mockContractManager struct {
	sdkcontract.Manager // embed for unimplemented methods
	newContractFn       func(ctx context.Context, t sdktypes.ContractType, opts ...sdkcontract.ContractOption) (*sdktypes.Contract, error)
	getHandlerFn        func(ctx context.Context, c sdktypes.Contract) (handlers.Handler, error)
}

func (m *mockContractManager) NewContract(
	ctx context.Context, t sdktypes.ContractType, opts ...sdkcontract.ContractOption,
) (*sdktypes.Contract, error) {
	return m.newContractFn(ctx, t, opts...)
}

func (m *mockContractManager) GetHandler(
	ctx context.Context, c sdktypes.Contract,
) (handlers.Handler, error) {
	return m.getHandlerFn(ctx, c)
}

type mockHandler struct {
	handlers.Handler // embed for unimplemented methods
	getKeyRefFn      func(c sdktypes.Contract) (*identity.KeyRef, error)
}

func (m *mockHandler) GetKeyRef(c sdktypes.Contract) (*identity.KeyRef, error) {
	return m.getKeyRefFn(c)
}

// makerContractManager returns a mock contract manager that yields a default
// contract at the given address whose owner key is makerPub.
func makerContractManager(address string, makerPub *btcec.PublicKey) sdkcontract.Manager {
	return &mockContractManager{
		newContractFn: func(ctx context.Context, t sdktypes.ContractType, opts ...sdkcontract.ContractOption) (*sdktypes.Contract, error) {
			return &sdktypes.Contract{Type: t, Address: address}, nil
		},
		getHandlerFn: func(ctx context.Context, c sdktypes.Contract) (handlers.Handler, error) {
			return &mockHandler{
				getKeyRefFn: func(c sdktypes.Contract) (*identity.KeyRef, error) {
					return &identity.KeyRef{Id: "0", PubKey: makerPub}, nil
				},
			}, nil
		},
	}
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

func TestCreateOffer_HappyPath(t *testing.T) {
	// Generate keys for the mock services
	_, signerPub := testKeyPair(t)
	_, emulatorPub := testKeyPair(t)
	_, vtxoPub := testKeyPair(t)
	_, makerPub := testKeyPair(t)

	// Build a valid Ark address that the maker contract resolves to
	addr := &arklib.Address{
		HRP:        arklib.BitcoinRegTest.Addr,
		Signer:     signerPub,
		VtxoTapKey: vtxoPub,
	}
	encodedAddr, err := addr.EncodeV0()
	require.NoError(t, err)

	emulatorClient := &mockEmulatorClient{
		getInfoFn: func(ctx context.Context) (*emulatorclient.Info, error) {
			// Return compressed (33-byte) pubkey hex — btcec.ParsePubKey expects compressed
			return &emulatorclient.Info{
				SignerPublicKey: hex.EncodeToString(emulatorPub.SerializeCompressed()),
			}, nil
		},
	}

	arkClient := &mockArkClient{
		contractManagerFn: func() sdkcontract.Manager {
			return makerContractManager(encodedAddr, makerPub)
		},
		getConfigDataFn: func(ctx context.Context) (*clientTypes.Config, error) {
			return &clientTypes.Config{
				SignerPubKey:        signerPub,
				Network:             arklib.BitcoinRegTest,
				UnilateralExitDelay: arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 144},
			}, nil
		},
	}

	params := CreateOfferParams{WantAmount: 50_000}
	result, err := CreateOffer(context.Background(), params, arkClient, emulatorClient)
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
		schnorr.SerializePubKey(emulatorPub),
		schnorr.SerializePubKey(decoded.EmulatorPubkey),
	)
	// cancel path is mandatory: makerPublicKey must be present
	require.NotNil(t, decoded.MakerPublicKey)
	assert.Equal(t, schnorr.SerializePubKey(makerPub), schnorr.SerializePubKey(decoded.MakerPublicKey))
	// exit path is always set from the server's unilateral exit delay
	require.NotNil(t, decoded.ExitDelay)
	assert.Equal(t, uint32(144), decoded.ExitDelay.Value)
}

func TestCreateOffer_EmulatorClientError(t *testing.T) {
	emulatorClient := &mockEmulatorClient{
		getInfoFn: func(ctx context.Context) (*emulatorclient.Info, error) {
			return nil, assert.AnError
		},
	}

	params := CreateOfferParams{WantAmount: 50_000}
	_, err := CreateOffer(context.Background(), params, nil, emulatorClient)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get emulator info")
}

func TestCreateOffer_BadIntroPubkeyHex(t *testing.T) {
	emulatorClient := &mockEmulatorClient{
		getInfoFn: func(ctx context.Context) (*emulatorclient.Info, error) {
			return &emulatorclient.Info{SignerPublicKey: "not-hex"}, nil
		},
	}

	params := CreateOfferParams{WantAmount: 50_000}
	_, err := CreateOffer(context.Background(), params, nil, emulatorClient)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode emulator pubkey")
}

func TestCreateOffer_InvalidIntroPubkey(t *testing.T) {
	emulatorClient := &mockEmulatorClient{
		getInfoFn: func(ctx context.Context) (*emulatorclient.Info, error) {
			return &emulatorclient.Info{SignerPublicKey: hex.EncodeToString([]byte{0x01, 0x02})}, nil
		},
	}

	params := CreateOfferParams{WantAmount: 50_000}
	_, err := CreateOffer(context.Background(), params, nil, emulatorClient)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse emulator pubkey")
}

func TestCreateOffer_ContractError(t *testing.T) {
	_, emulatorPub := testKeyPair(t)

	emulatorClient := &mockEmulatorClient{
		getInfoFn: func(ctx context.Context) (*emulatorclient.Info, error) {
			return &emulatorclient.Info{
				SignerPublicKey: hex.EncodeToString(emulatorPub.SerializeCompressed()),
			}, nil
		},
	}

	arkClient := &mockArkClient{
		contractManagerFn: func() sdkcontract.Manager {
			return &mockContractManager{
				newContractFn: func(ctx context.Context, tp sdktypes.ContractType, opts ...sdkcontract.ContractOption) (*sdktypes.Contract, error) {
					return nil, assert.AnError
				},
			}
		},
	}

	params := CreateOfferParams{WantAmount: 50_000}
	_, err := CreateOffer(context.Background(), params, arkClient, emulatorClient)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create maker contract")
}

func TestCreateOffer_GetConfigError(t *testing.T) {
	_, signerPub := testKeyPair(t)
	_, emulatorPub := testKeyPair(t)
	_, vtxoPub := testKeyPair(t)

	addr := &arklib.Address{
		HRP:        arklib.BitcoinRegTest.Addr,
		Signer:     signerPub,
		VtxoTapKey: vtxoPub,
	}
	encodedAddr, err := addr.EncodeV0()
	require.NoError(t, err)

	emulatorClient := &mockEmulatorClient{
		getInfoFn: func(ctx context.Context) (*emulatorclient.Info, error) {
			return &emulatorclient.Info{
				SignerPublicKey: hex.EncodeToString(emulatorPub.SerializeCompressed()),
			}, nil
		},
	}

	_, makerPub := testKeyPair(t)
	arkClient := &mockArkClient{
		contractManagerFn: func() sdkcontract.Manager {
			return makerContractManager(encodedAddr, makerPub)
		},
		getConfigDataFn: func(ctx context.Context) (*clientTypes.Config, error) {
			return nil, assert.AnError
		},
	}

	params := CreateOfferParams{WantAmount: 50_000}
	_, err = CreateOffer(context.Background(), params, arkClient, emulatorClient)
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
