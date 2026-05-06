package contract

import (
	"testing"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/stretchr/testify/require"
)

// testKeyPair generates a secp256k1 keypair for use in tests.
func testKeyPair(t *testing.T) (*btcec.PrivateKey, *btcec.PublicKey) {
	t.Helper()
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	return priv, priv.PubKey()
}

// testP2TRScript builds a 34-byte P2TR pkscript from a public key.
func testP2TRScript(t *testing.T, pub *btcec.PublicKey) []byte {
	t.Helper()
	s, err := script.P2TRScript(pub)
	require.NoError(t, err)
	require.Len(t, s, 34)
	return s
}

// testXOnlyBytes returns the 32-byte x-only serialization of a public key.
func testXOnlyBytes(pub *btcec.PublicKey) []byte {
	return schnorr.SerializePubKey(pub)
}

// testMinimalOffer returns a valid Offer with only required fields set.
func testMinimalOffer(t *testing.T) *Offer {
	t.Helper()
	_, makerPub := testKeyPair(t)
	_, introPub := testKeyPair(t)

	makerPkScript := testP2TRScript(t, makerPub)
	swapPkScript := testP2TRScript(t, makerPub)

	return &Offer{
		SwapPkScript:       swapPkScript,
		WantAmount:         1000,
		MakerPkScript:      makerPkScript,
		IntrospectorPubkey: introPub,
	}
}

// testFullOffer returns a valid Offer with all optional fields set.
func testFullOffer(t *testing.T) *Offer {
	t.Helper()
	_, makerPub := testKeyPair(t)
	_, makerPub2 := testKeyPair(t)
	_, introPub := testKeyPair(t)

	makerPkScript := testP2TRScript(t, makerPub)
	swapPkScript := testP2TRScript(t, makerPub)

	return &Offer{
		SwapPkScript:       swapPkScript,
		WantAmount:         5000,
		MakerPkScript:      makerPkScript,
		IntrospectorPubkey: introPub,
		CancelAt:           1700000000,
		RatioNum:           3,
		RatioDen:           4,
		ExitDelay: &arklib.RelativeLocktime{
			Type:  arklib.LocktimeTypeBlock,
			Value: 144,
		},
		MakerPublicKey: makerPub2,
	}
}
