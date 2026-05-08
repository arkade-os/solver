package preimage

import (
	"bytes"
	"context"
	"testing"

	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arkade-os/bancod/pkg/solver/builder"
)

// fixture bundles every key + the artifacts a maker would produce.
type fixture struct {
	t            *testing.T
	solverPriv   *btcec.PrivateKey
	serverPriv   *btcec.PrivateKey
	introPriv    *btcec.PrivateKey
	preimg       []byte
	receiverPk   []byte
	arkadeScript []byte
	taptree      []string
	expectedPk   []byte
	plugin       *plugin
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	solverPriv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	serverPriv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	introPriv, err := btcec.NewPrivateKey()
	require.NoError(t, err)

	preimg := bytes.Repeat([]byte{0xab}, 32)
	receiverPk := freshTaprootScript(t)

	arkadeScript, err := EnforcePayTo(receiverPk)
	require.NoError(t, err)

	preimageHash := btcutil.Hash160(preimg)
	vtxoScript, err := VtxoScript(preimageHash, receiverPk, serverPriv.PubKey(), introPriv.PubKey())
	require.NoError(t, err)
	taptree, err := vtxoScript.Encode()
	require.NoError(t, err)
	tapKey, _, err := vtxoScript.TapTree()
	require.NoError(t, err)
	expectedPk, err := script.P2TRScript(tapKey)
	require.NoError(t, err)

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	p := &plugin{
		cfg: Config{
			SolverPrivKey:      solverPriv,
			IntrospectorPubKey: introPriv.PubKey(),
			ServerPubKey:       serverPriv.PubKey(),
			Log:                log,
		},
		log: log,
	}

	return &fixture{
		t:            t,
		solverPriv:   solverPriv,
		serverPriv:   serverPriv,
		introPriv:    introPriv,
		preimg:       preimg,
		receiverPk:   receiverPk,
		arkadeScript: arkadeScript,
		taptree:      taptree,
		expectedPk:   expectedPk,
		plugin:       p,
	}
}

func freshTaprootScript(t *testing.T) []byte {
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

func (f *fixture) makeClaimTx(packet extension.Packet) (*psbt.Packet, extension.Extension) {
	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: 1234, PkScript: bytes.Repeat([]byte{0x99}, 34)})
	tx.AddTxOut(&wire.TxOut{Value: 5000, PkScript: f.expectedPk})
	p, err := psbt.NewFromUnsignedTx(tx)
	require.NoError(f.t, err)
	ext := extension.Extension{}
	if packet != nil {
		ext = append(ext, packet)
	}
	return p, ext
}

func (f *fixture) goodPacket() extension.Packet {
	plaintext := buildSecretPayload(f.preimg, f.arkadeScript)
	ct, err := Encrypt(f.solverPriv.PubKey(), plaintext)
	require.NoError(f.t, err)
	pkt, err := (&ClaimPacket{Ciphertext: ct, Taptree: f.taptree}).ToPacket()
	require.NoError(f.t, err)
	return pkt
}

func TestPluginDecode_Match(t *testing.T) {
	f := newFixture(t)
	tx, ext := f.makeClaimTx(f.goodPacket())
	matched, err := f.plugin.decode(context.Background(), tx, ext)
	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, uint32(1), matched.Outpoint.Index)
	assert.Equal(t, uint64(5000), matched.Amount)
	assert.Equal(t, f.preimg, matched.Credentials.Preimage)
	assert.Equal(t, f.arkadeScript, matched.Credentials.ArkadeScript)
	assert.Equal(t, f.expectedPk, matched.Credentials.PkScript)
}

func TestPluginDecode_NoExtensionPacket(t *testing.T) {
	f := newFixture(t)
	tx, ext := f.makeClaimTx(nil)
	_, err := f.plugin.decode(context.Background(), tx, ext)
	require.ErrorIs(t, err, builder.ErrSkip)
}

func TestPluginDecode_WrongPacketType(t *testing.T) {
	f := newFixture(t)
	other := extension.UnknownPacket{PacketType: 0xff, Data: []byte{0x00}}
	tx, ext := f.makeClaimTx(other)
	_, err := f.plugin.decode(context.Background(), tx, ext)
	require.ErrorIs(t, err, builder.ErrSkip)
}

func TestPluginDecode_DecryptFails(t *testing.T) {
	f := newFixture(t)
	junk, err := (&ClaimPacket{Ciphertext: bytes.Repeat([]byte{0x00}, 80), Taptree: f.taptree}).ToPacket()
	require.NoError(t, err)
	tx, ext := f.makeClaimTx(junk)
	_, err = f.plugin.decode(context.Background(), tx, ext)
	require.ErrorIs(t, err, builder.ErrSkip)
}

func TestPluginDecode_OutputMismatch(t *testing.T) {
	f := newFixture(t)
	pkt := f.goodPacket()
	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: 1234, PkScript: bytes.Repeat([]byte{0x99}, 34)})
	p, err := psbt.NewFromUnsignedTx(tx)
	require.NoError(t, err)
	_, err = f.plugin.decode(context.Background(), p, extension.Extension{pkt})
	require.ErrorIs(t, err, builder.ErrSkip)
}

func TestPluginDecode_ArkadeScriptMismatch(t *testing.T) {
	f := newFixture(t)
	bad := append([]byte{}, f.arkadeScript...)
	bad = append(bad, txscript.OP_NOP)
	plaintext := buildSecretPayload(f.preimg, bad)
	ct, err := Encrypt(f.solverPriv.PubKey(), plaintext)
	require.NoError(t, err)
	pkt, err := (&ClaimPacket{Ciphertext: ct, Taptree: f.taptree}).ToPacket()
	require.NoError(t, err)
	tx, ext := f.makeClaimTx(pkt)
	_, err = f.plugin.decode(context.Background(), tx, ext)
	require.ErrorIs(t, err, builder.ErrSkip)
}

func TestPluginDecode_TaptreeMismatch(t *testing.T) {
	f := newFixture(t)
	plaintext := buildSecretPayload(f.preimg, f.arkadeScript)
	ct, err := Encrypt(f.solverPriv.PubKey(), plaintext)
	require.NoError(t, err)
	wrongIntro, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	wrongVtxo, err := VtxoScript(
		btcutil.Hash160(f.preimg), f.receiverPk,
		f.serverPriv.PubKey(), wrongIntro.PubKey(),
	)
	require.NoError(t, err)
	wrongTaptree, err := wrongVtxo.Encode()
	require.NoError(t, err)
	pkt, err := (&ClaimPacket{Ciphertext: ct, Taptree: wrongTaptree}).ToPacket()
	require.NoError(t, err)
	tx, ext := f.makeClaimTx(pkt)
	_, err = f.plugin.decode(context.Background(), tx, ext)
	require.ErrorIs(t, err, builder.ErrSkip)
}
