package preimage

import (
	"bytes"
	"context"
	"testing"

	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arkade-os/solver/pkg/solver/builder"
)

type fixture struct {
	t            *testing.T
	solverPriv   *btcec.PrivateKey
	serverPriv   *btcec.PrivateKey
	emulatorPriv *btcec.PrivateKey
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
	emulatorPriv, err := btcec.NewPrivateKey()
	require.NoError(t, err)

	preimg := bytes.Repeat([]byte{0xab}, 32)
	receiverPk := freshTaprootScript(t)

	arkadeScript, err := EnforcePayTo(receiverPk)
	require.NoError(t, err)

	preimageHash := btcutil.Hash160(preimg)
	closure, err := CovenantClaimClosure(preimageHash, receiverPk, serverPriv.PubKey(), emulatorPriv.PubKey())
	require.NoError(t, err)
	vtxoScript := &script.TapscriptsVtxoScript{Closures: []script.Closure{closure}}
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
			SolverPrivKey:  solverPriv,
			EmulatorPubKey: emulatorPriv.PubKey(),
			ServerPubKey:   serverPriv.PubKey(),
			Log:            log,
		},
		log: log,
	}

	return &fixture{
		t:            t,
		solverPriv:   solverPriv,
		serverPriv:   serverPriv,
		emulatorPriv: emulatorPriv,
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

func (f *fixture) goodPacket() extension.Packet {
	ct, err := Encrypt(f.solverPriv.PubKey(), f.preimg)
	require.NoError(f.t, err)
	pkt, err := (&ClaimPacket{Ciphertext: ct, ArkadeScript: f.arkadeScript}).ToPacket()
	require.NoError(f.t, err)
	return pkt
}

func (f *fixture) makeClaimTx(packet extension.Packet) (*psbt.Packet, extension.Extension) {
	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: 1234, PkScript: bytes.Repeat([]byte{0x99}, 34)})
	tx.AddTxOut(&wire.TxOut{Value: 5000, PkScript: f.expectedPk})
	p, err := psbt.NewFromUnsignedTx(tx)
	require.NoError(f.t, err)
	encoded, err := txutils.TapTree(f.taptree).Encode()
	require.NoError(f.t, err)
	p.Outputs[1].TaprootTapTree = encoded
	ext := extension.Extension{}
	if packet != nil {
		ext = append(ext, packet)
	}
	return p, ext
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
	junk, err := (&ClaimPacket{
		Ciphertext:   bytes.Repeat([]byte{0x00}, 80),
		ArkadeScript: f.arkadeScript,
	}).ToPacket()
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
	encoded, err := txutils.TapTree(f.taptree).Encode()
	require.NoError(t, err)
	p.Outputs[0].TaprootTapTree = encoded
	_, err = f.plugin.decode(context.Background(), p, extension.Extension{pkt})
	require.ErrorIs(t, err, builder.ErrSkip)
}

func TestPluginDecode_ArkadeScriptMismatch(t *testing.T) {
	f := newFixture(t)
	bad := append([]byte{}, f.arkadeScript...)
	bad = append(bad, txscript.OP_NOP)
	ct, err := Encrypt(f.solverPriv.PubKey(), f.preimg)
	require.NoError(t, err)
	pkt, err := (&ClaimPacket{Ciphertext: ct, ArkadeScript: bad}).ToPacket()
	require.NoError(t, err)
	tx, ext := f.makeClaimTx(pkt)
	_, err = f.plugin.decode(context.Background(), tx, ext)
	require.ErrorIs(t, err, builder.ErrSkip)
}

func TestPluginDecode_TaptreeMismatch(t *testing.T) {
	f := newFixture(t)
	ct, err := Encrypt(f.solverPriv.PubKey(), f.preimg)
	require.NoError(t, err)
	pkt, err := (&ClaimPacket{Ciphertext: ct, ArkadeScript: f.arkadeScript}).ToPacket()
	require.NoError(t, err)
	wrongIntro, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	wrongClosure, err := CovenantClaimClosure(
		btcutil.Hash160(f.preimg), f.receiverPk,
		f.serverPriv.PubKey(), wrongIntro.PubKey(),
	)
	require.NoError(t, err)
	wrongVtxo := &script.TapscriptsVtxoScript{Closures: []script.Closure{wrongClosure}}
	wrongTaptree, err := wrongVtxo.Encode()
	require.NoError(t, err)
	wrongEncoded, err := txutils.TapTree(wrongTaptree).Encode()
	require.NoError(t, err)
	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: 1234, PkScript: bytes.Repeat([]byte{0x99}, 34)})
	tx.AddTxOut(&wire.TxOut{Value: 5000, PkScript: f.expectedPk})
	p, err := psbt.NewFromUnsignedTx(tx)
	require.NoError(t, err)
	p.Outputs[1].TaprootTapTree = wrongEncoded
	_, err = f.plugin.decode(context.Background(), p, extension.Extension{pkt})
	require.ErrorIs(t, err, builder.ErrSkip)
}

func TestPluginDecode_MissingTaprootTapTree(t *testing.T) {
	f := newFixture(t)
	pkt := f.goodPacket()
	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: 5000, PkScript: f.expectedPk})
	p, err := psbt.NewFromUnsignedTx(tx)
	require.NoError(t, err)
	_, err = f.plugin.decode(context.Background(), p, extension.Extension{pkt})
	require.ErrorIs(t, err, builder.ErrSkip)
}
