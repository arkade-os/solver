package bounty

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedPub returns a deterministic public key from a hex secret. Used for
// golden-output tests that should not change unless the script encoding does.
func fixedPub(t *testing.T, hexSecret string) *btcec.PublicKey {
	t.Helper()
	raw, err := hex.DecodeString(hexSecret)
	require.NoError(t, err)
	priv, _ := btcec.PrivKeyFromBytes(raw)
	return priv.PubKey()
}

// fixedReceiver returns a deterministic 34-byte P2TR pkScript.
func fixedReceiver(t *testing.T) []byte {
	t.Helper()
	pub := fixedPub(t, strings.Repeat("01", 32))
	pkScript, err := txscript.PayToTaprootScript(pub)
	require.NoError(t, err)
	require.Len(t, pkScript, 34)
	return pkScript
}

func TestPayToAndPoW_Determinism(t *testing.T) {
	receiver := fixedReceiver(t)
	a, err := payToAndPoW(receiver, 4)
	require.NoError(t, err)
	b, err := payToAndPoW(receiver, 4)
	require.NoError(t, err)
	assert.Equal(t, a, b, "payToAndPoW must be deterministic for fixed inputs")
}

func TestPayToAndPoW_OpcodeShape(t *testing.T) {
	receiver := fixedReceiver(t)
	scriptBytes, err := payToAndPoW(receiver, 3)
	require.NoError(t, err)
	// Sanity: decode and walk the opcodes; no full equality check, just shape.
	tokenizer := txscript.MakeScriptTokenizer(0, scriptBytes)
	var ops []byte
	for tokenizer.Next() {
		ops = append(ops, tokenizer.Opcode())
	}
	require.NoError(t, tokenizer.Err())
	require.NotEmpty(t, ops)
	// First opcode must be OP_TXID (the PoW gate).
	assert.Equal(t, byte(0xf3), ops[0], "first opcode must be OP_TXID")
}

func TestPayToAndPoW_RejectsBadInput(t *testing.T) {
	receiver := fixedReceiver(t)
	_, err := payToAndPoW(receiver, 0)
	assert.Error(t, err)
	_, err = payToAndPoW(receiver, 33)
	assert.Error(t, err)
	_, err = payToAndPoW([]byte{0x00, 0x20}, 4)
	assert.Error(t, err)
}

func TestVtxoScript_Determinism(t *testing.T) {
	receiver := fixedReceiver(t)
	server := fixedPub(t, strings.Repeat("02", 32))
	intro := fixedPub(t, strings.Repeat("03", 32))

	a, err := VtxoScript(4, receiver, server, intro)
	require.NoError(t, err)
	b, err := VtxoScript(4, receiver, server, intro)
	require.NoError(t, err)

	tapA, _, err := a.TapTree()
	require.NoError(t, err)
	tapB, _, err := b.TapTree()
	require.NoError(t, err)
	assert.True(t, tapA.IsEqual(tapB), "tap key must be deterministic")

	require.Len(t, a.Closures, 1, "FHTLC must have exactly one closure")
	_, ok := a.Closures[0].(*script.MultisigClosure)
	assert.True(t, ok, "FHTLC closure must be MultisigClosure (no Condition)")
}

func TestVtxoScript_DiffersByDifficulty(t *testing.T) {
	receiver := fixedReceiver(t)
	server := fixedPub(t, strings.Repeat("02", 32))
	intro := fixedPub(t, strings.Repeat("03", 32))

	a, err := VtxoScript(2, receiver, server, intro)
	require.NoError(t, err)
	b, err := VtxoScript(4, receiver, server, intro)
	require.NoError(t, err)

	tapA, _, err := a.TapTree()
	require.NoError(t, err)
	tapB, _, err := b.TapTree()
	require.NoError(t, err)

	assert.False(t, tapA.IsEqual(tapB), "different difficulty must yield different tap keys")
}

func TestAddress_RoundTrip(t *testing.T) {
	receiver := fixedReceiver(t)
	server := fixedPub(t, strings.Repeat("02", 32))
	intro := fixedPub(t, strings.Repeat("03", 32))
	network := arklib.Network{Addr: "tark"}

	addrStr, err := Address(4, receiver, server, intro, network)
	require.NoError(t, err)

	decoded, err := arklib.DecodeAddressV0(addrStr)
	require.NoError(t, err)

	vtxoScript, err := VtxoScript(4, receiver, server, intro)
	require.NoError(t, err)
	tapKey, _, err := vtxoScript.TapTree()
	require.NoError(t, err)
	// Address encoding strips parity (x-only), so compare schnorr-serialized forms.
	assert.True(t, bytes.Equal(
		schnorr.SerializePubKey(decoded.VtxoTapKey),
		schnorr.SerializePubKey(tapKey),
	))
}

func TestMinerFeeSats_Constant(t *testing.T) {
	// Pinned to the dust limit so a singleton claim's tip output is itself
	// non-dust; batched claims (N>1) scale the bot's reward linearly.
	assert.Equal(t, DustLimitSats, MinerFeeSats)
}

func TestP2TRWitnessProgram(t *testing.T) {
	good := fixedReceiver(t)
	prog, err := p2trWitnessProgram(good)
	require.NoError(t, err)
	assert.Len(t, prog, 32)
	assert.Equal(t, good[2:], prog)

	_, err = p2trWitnessProgram([]byte{0x00})
	assert.Error(t, err)

	bad := make([]byte, 34)
	bad[0] = txscript.OP_0 // not OP_1
	bad[1] = txscript.OP_DATA_32
	_, err = p2trWitnessProgram(bad)
	assert.Error(t, err)
}
