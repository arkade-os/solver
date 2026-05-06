package banco

import (
	"testing"

	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arkade-os/bancod/pkg/banco/contract"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func testAssetId(t *testing.T) *asset.AssetId {
	t.Helper()
	assetBytes := make([]byte, 34)
	assetBytes[0] = 0xAB
	for i := 1; i < 32; i++ {
		assetBytes[i] = byte(i)
	}
	id, err := asset.NewAssetIdFromBytes(assetBytes)
	require.NoError(t, err)
	return id
}

func testKeyPair(t *testing.T) (*btcec.PrivateKey, *btcec.PublicKey) {
	t.Helper()
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	return priv, priv.PubKey()
}

func testP2TRScript(t *testing.T, pub *btcec.PublicKey) []byte {
	t.Helper()
	s, err := script.P2TRScript(pub)
	require.NoError(t, err)
	return s
}

// buildTestTxWithOffer builds a wire.MsgTx that embeds the given contract.Offer
// as an extension OP_RETURN output, and adds a swap output matching offer.SwapPkScript.
func buildTestTxWithOffer(t *testing.T, offer *contract.Offer) *wire.MsgTx {
	t.Helper()

	pkt, err := offer.ToPacket()
	require.NoError(t, err)

	ext := extension.Extension{pkt}
	extOut, err := ext.TxOut()
	require.NoError(t, err)

	tx := wire.NewMsgTx(2)

	// Add dummy input
	dummyHash := chainhash.Hash{}
	tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&dummyHash, 0), nil, nil))

	// Add the swap output matching offer.SwapPkScript with positive value
	tx.AddTxOut(wire.NewTxOut(100000, offer.SwapPkScript))

	// Add extension OP_RETURN output
	tx.AddTxOut(extOut)

	return tx
}

// buildMinimalOffer builds a valid contract.Offer with required fields populated.
func buildMinimalOffer(t *testing.T) *contract.Offer {
	t.Helper()
	_, makerPub := testKeyPair(t)
	_, introPub := testKeyPair(t)

	makerPkScript := testP2TRScript(t, makerPub)
	swapPkScript := testP2TRScript(t, makerPub)

	return &contract.Offer{
		SwapPkScript:       swapPkScript,
		WantAmount:         1000,
		MakerPkScript:      makerPkScript,
		IntrospectorPubkey: introPub,
	}
}

// ---------------------------------------------------------------------------
// IsBTCDeposit tests
// ---------------------------------------------------------------------------

func TestOffer_IsBTCDeposit(t *testing.T) {
	t.Run("nil DepositAsset returns true", func(t *testing.T) {
		o := &Offer{DepositAsset: nil}
		assert.True(t, o.IsBTCDeposit())
	})

	t.Run("non-nil DepositAsset returns false", func(t *testing.T) {
		o := &Offer{DepositAsset: testAssetId(t)}
		assert.False(t, o.IsBTCDeposit())
	})
}

// ---------------------------------------------------------------------------
// DepositAssetStr tests
// ---------------------------------------------------------------------------

func TestOffer_DepositAssetStr(t *testing.T) {
	t.Run("nil DepositAsset returns BTC", func(t *testing.T) {
		o := &Offer{DepositAsset: nil}
		assert.Equal(t, "BTC", o.DepositAssetStr())
	})

	t.Run("non-nil DepositAsset returns non-empty non-BTC string", func(t *testing.T) {
		o := &Offer{DepositAsset: testAssetId(t)}
		s := o.DepositAssetStr()
		assert.NotEmpty(t, s)
		assert.NotEqual(t, "BTC", s)
	})
}

// ---------------------------------------------------------------------------
// WantAssetStr tests
// ---------------------------------------------------------------------------

func TestOffer_WantAssetStr(t *testing.T) {
	t.Run("nil WantAsset returns BTC", func(t *testing.T) {
		o := &Offer{}
		// WantAsset lives on embedded contract.Offer; nil is the zero value
		assert.Equal(t, "BTC", o.WantAssetStr())
	})

	t.Run("non-nil WantAsset returns asset id", func(t *testing.T) {
		o := &Offer{}
		o.WantAsset = testAssetId(t)
		s := o.WantAssetStr()
		assert.NotEmpty(t, s)
		assert.NotEqual(t, "BTC", s)
	})
}

// ---------------------------------------------------------------------------
// ComputePrice tests
// ---------------------------------------------------------------------------

func TestOffer_ComputePrice(t *testing.T) {
	t.Run("normal: deposit=200000 want=100000 both 8 decimals -> price=2.0", func(t *testing.T) {
		o := &Offer{DepositAmount: 200000}
		o.WantAmount = 100000
		pair := &Pair{BaseDecimals: 8, QuoteDecimals: 8}
		price, ok := o.ComputePrice(pair)
		require.True(t, ok)
		assert.InDelta(t, 2.0, price, 1e-9)
	})

	t.Run("different decimals: base=8 quote=2 deposit=100000000 want=100 -> price=1.0", func(t *testing.T) {
		o := &Offer{DepositAmount: 100_000_000}
		o.WantAmount = 100
		pair := &Pair{BaseDecimals: 8, QuoteDecimals: 2}
		price, ok := o.ComputePrice(pair)
		require.True(t, ok)
		assert.InDelta(t, 1.0, price, 1e-9)
	})

	t.Run("zero deposit returns false", func(t *testing.T) {
		o := &Offer{DepositAmount: 0}
		o.WantAmount = 100000
		pair := &Pair{BaseDecimals: 8, QuoteDecimals: 8}
		_, ok := o.ComputePrice(pair)
		assert.False(t, ok)
	})

	t.Run("zero want returns false", func(t *testing.T) {
		o := &Offer{DepositAmount: 200000}
		o.WantAmount = 0
		pair := &Pair{BaseDecimals: 8, QuoteDecimals: 8}
		_, ok := o.ComputePrice(pair)
		assert.False(t, ok)
	})
}

// ---------------------------------------------------------------------------
// NewOffer tests
// ---------------------------------------------------------------------------

func TestNewOffer_ValidTx(t *testing.T) {
	contractOffer := buildMinimalOffer(t)
	tx := buildTestTxWithOffer(t, contractOffer)

	offer, err := NewOffer(tx)
	require.NoError(t, err)
	require.NotNil(t, offer, "expected non-nil Offer for a valid tx")

	assert.Equal(t, uint64(100000), offer.DepositAmount)
	assert.True(t, offer.IsBTCDeposit(), "should be BTC deposit (no asset packet)")
}

func TestNewOffer_NoExtensionOutput(t *testing.T) {
	tx := wire.NewMsgTx(2)
	dummyHash := chainhash.Hash{}
	tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&dummyHash, 0), nil, nil))
	// plain output, no OP_RETURN / extension
	tx.AddTxOut(wire.NewTxOut(50000, []byte{0x51, 0x20}))

	offer, err := NewOffer(tx)
	require.NoError(t, err)
	assert.Nil(t, offer, "tx without extension should return nil")
}

func TestNewOffer_ExtensionButNoBancoOffer(t *testing.T) {
	// Build an extension with a non-banco packet type
	pkt := extension.UnknownPacket{PacketType: 0xFF, Data: []byte{0xDE, 0xAD}}
	ext := extension.Extension{pkt}
	extOut, err := ext.TxOut()
	require.NoError(t, err)

	tx := wire.NewMsgTx(2)
	dummyHash := chainhash.Hash{}
	tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&dummyHash, 0), nil, nil))
	tx.AddTxOut(wire.NewTxOut(50000, []byte{0x51, 0x20}))
	tx.AddTxOut(extOut)

	offer, err := NewOffer(tx)
	require.NoError(t, err)
	assert.Nil(t, offer, "extension without banco packet should return nil")
}

func TestNewOffer_OfferButNoMatchingSwapOutput(t *testing.T) {
	contractOffer := buildMinimalOffer(t)

	pkt, err := contractOffer.ToPacket()
	require.NoError(t, err)
	ext := extension.Extension{pkt}
	extOut, err := ext.TxOut()
	require.NoError(t, err)

	tx := wire.NewMsgTx(2)
	dummyHash := chainhash.Hash{}
	tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&dummyHash, 0), nil, nil))

	// Output with a *different* pkscript — won't match SwapPkScript
	tx.AddTxOut(wire.NewTxOut(50000, []byte{0x51, 0x20}))
	tx.AddTxOut(extOut)

	offer, err := NewOffer(tx)
	require.NoError(t, err)
	assert.Nil(t, offer, "offer with no matching swap output should return nil")
}
