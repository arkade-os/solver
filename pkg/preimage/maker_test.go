package preimage_test

import (
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/txscript"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arkade-os/solver/pkg/preimage"
)

func freshP2TR(t *testing.T) []byte {
	t.Helper()
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	out, err := txscript.NewScriptBuilder().
		AddOp(txscript.OP_1).
		AddData(priv.PubKey().SerializeCompressed()[1:]).
		Script()
	require.NoError(t, err)
	require.Len(t, out, 34)
	return out
}

func TestBuildPacket_RoundTrip(t *testing.T) {
	solver, err := btcec.NewPrivateKey()
	require.NoError(t, err)

	preimg := make([]byte, 32)
	for i := range preimg {
		preimg[i] = byte(i)
	}
	receiver := freshP2TR(t)

	pkt, err := preimage.BuildPacket(preimg, solver.PubKey(), receiver)
	require.NoError(t, err)
	require.Equal(t, preimage.PacketType, pkt.Type())

	body, err := pkt.Serialize()
	require.NoError(t, err)

	decoded, err := preimage.DeserializeClaim(body)
	require.NoError(t, err)

	plaintext, err := preimage.Decrypt(solver, decoded.Ciphertext)
	require.NoError(t, err)
	assert.Equal(t, preimg, plaintext)

	expectedScript, err := preimage.EnforcePayTo(receiver)
	require.NoError(t, err)
	assert.Equal(t, expectedScript, decoded.ArkadeScript)
}

func TestBuildPacket_ValidatesInputs(t *testing.T) {
	solver, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	receiver := freshP2TR(t)

	t.Run("preimage wrong length", func(t *testing.T) {
		_, err := preimage.BuildPacket(make([]byte, 31), solver.PubKey(), receiver)
		assert.Error(t, err)
	})
	t.Run("solver pubkey nil", func(t *testing.T) {
		_, err := preimage.BuildPacket(make([]byte, 32), nil, receiver)
		assert.Error(t, err)
	})
	t.Run("receiver wrong length", func(t *testing.T) {
		_, err := preimage.BuildPacket(make([]byte, 32), solver.PubKey(), []byte{0x01})
		assert.Error(t, err)
	})
}
