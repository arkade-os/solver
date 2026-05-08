package preimage_test

import (
	"testing"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/txscript"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arkade-os/bancod/pkg/preimage"
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

func TestCreateClaim_RoundTrip(t *testing.T) {
	solver, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	server, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	intro, err := btcec.NewPrivateKey()
	require.NoError(t, err)

	preimg := make([]byte, 32)
	for i := range preimg {
		preimg[i] = byte(i)
	}
	receiver := freshP2TR(t)

	res, err := preimage.CreateClaim(preimage.CreateClaimParams{
		Preimage:     preimg,
		ReceiverPk:   receiver,
		SolverPubKey: solver.PubKey(),
		ServerPubKey: server.PubKey(),
		IntroPubKey:  intro.PubKey(),
		Network:      arklib.BitcoinRegTest,
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.ClaimAddress)
	require.NotNil(t, res.Packet)
	assert.Equal(t, preimage.PacketType, res.Packet.Type())

	body, err := res.Packet.Serialize()
	require.NoError(t, err)
	pkt, err := preimage.DeserializeClaim(body)
	require.NoError(t, err)

	plaintext, err := preimage.Decrypt(solver, pkt.Ciphertext)
	require.NoError(t, err)

	gotPreimage, gotScript, err := preimage.SplitSecretPayload(plaintext)
	require.NoError(t, err)
	assert.Equal(t, preimg, gotPreimage)
	expectedScript, err := preimage.EnforcePayTo(receiver)
	require.NoError(t, err)
	assert.Equal(t, expectedScript, gotScript)

	require.Greater(t, len(pkt.Taptree), 0)
}
