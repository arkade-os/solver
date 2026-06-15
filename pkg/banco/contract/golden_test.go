package contract

import (
	"encoding/hex"
	"testing"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/stretchr/testify/require"
)

// privFromByte derives a deterministic secp256k1 keypair from a 32-byte scalar
// filled with the given byte, so the same keys can be reproduced in the TS test.
func privFromByte(b byte) (*btcec.PrivateKey, *btcec.PublicKey) {
	var buf [32]byte
	for i := range buf {
		buf[i] = b
	}
	priv, pub := btcec.PrivKeyFromBytes(buf[:])
	return priv, pub
}

// goldenSwapAddress is the swap address for a fixed full-fill offer (no ratio).
// It is locked here and asserted identically by the TS test
// wallet/src/lib/banco/offer.golden.test.ts to prove client/solver parity.
const goldenSwapAddress = "tark1qp8n2k7uklxq4aegau7vawtptkgxsja4kt99lpv6krctwpq8tpc65v3ewwuvc2gxfcjnfmuykarp4vz3a80szwsunrg6rwe7nnuyqxh75cpzw0"

// TestSwapAddress_Golden_FullFill builds a full-fill (no ratio) offer from fixed
// keys and asserts the derived swap address matches the golden value. The TS SDK
// must produce the same string for the same inputs.
func TestSwapAddress_Golden_FullFill(t *testing.T) {
	_, serverPub := privFromByte(0x11)
	_, emulatorPub := privFromByte(0x22)
	_, makerPub := privFromByte(0x33)

	makerPkScript, err := script.P2TRScript(makerPub)
	require.NoError(t, err)

	// Log the fixed inputs (x-only hex) for the TS counterpart.
	t.Logf("serverPub   (x-only) = %s", hex.EncodeToString(schnorr.SerializePubKey(serverPub)))
	t.Logf("emulatorPub (x-only) = %s", hex.EncodeToString(schnorr.SerializePubKey(emulatorPub)))
	t.Logf("makerPub    (x-only) = %s", hex.EncodeToString(schnorr.SerializePubKey(makerPub)))
	t.Logf("makerPkScript        = %s", hex.EncodeToString(makerPkScript))

	offer := &Offer{
		WantAmount:     50_000,
		MakerPkScript:  makerPkScript,
		MakerPublicKey: makerPub,
		EmulatorPubkey: emulatorPub,
		ExitDelay:      &arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 144},
	}

	vtxoScript, err := offer.VtxoScript(serverPub)
	require.NoError(t, err)
	taprootKey, _, err := vtxoScript.TapTree()
	require.NoError(t, err)

	swapAddr := &arklib.Address{
		HRP:        arklib.BitcoinRegTest.Addr,
		Signer:     serverPub,
		VtxoTapKey: taprootKey,
	}
	got, err := swapAddr.EncodeV0()
	require.NoError(t, err)

	t.Logf("swap address         = %s", got)
	require.Equal(t, goldenSwapAddress, got)
}
