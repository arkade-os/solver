package preimage

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	arkade "github.com/ArkLabsHQ/introspector/pkg/arkade"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
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

func fixedReceiver(t *testing.T) []byte {
	t.Helper()
	pub := fixedPub(t, strings.Repeat("01", 32))
	pkScript, err := txscript.PayToTaprootScript(pub)
	require.NoError(t, err)
	require.Len(t, pkScript, 34)
	return pkScript
}

func TestEnforcePayTo_Determinism(t *testing.T) {
	receiver := fixedReceiver(t)
	a, err := EnforcePayTo(receiver)
	require.NoError(t, err)
	b, err := EnforcePayTo(receiver)
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

func TestEnforcePayTo_RejectsNonP2TR(t *testing.T) {
	t.Run("wrong length", func(t *testing.T) {
		_, err := EnforcePayTo([]byte{0x51, 0x20})
		assert.Error(t, err)
	})
	t.Run("wrong prefix", func(t *testing.T) {
		bad := make([]byte, 34)
		bad[0] = txscript.OP_0
		bad[1] = 0x20
		_, err := EnforcePayTo(bad)
		assert.Error(t, err)
	})
}

func TestPreimageCondition_Shape(t *testing.T) {
	preimage := bytes.Repeat([]byte{0x42}, 32)
	hash := btcutil.Hash160(preimage)

	cond, err := preimageCondition(hash)
	require.NoError(t, err)

	// Shape: OP_HASH160 OP_DATA_20 <hash[20]> OP_EQUAL = 23 bytes.
	require.Len(t, cond, 23)
	assert.Equal(t, byte(txscript.OP_HASH160), cond[0])
	assert.Equal(t, byte(txscript.OP_DATA_20), cond[1])
	assert.Equal(t, hash, cond[2:22])
	assert.Equal(t, byte(txscript.OP_EQUAL), cond[22])
}

func TestPreimageCondition_RejectsBadLength(t *testing.T) {
	_, err := preimageCondition([]byte{0x01})
	assert.Error(t, err)
	_, err = preimageCondition(make([]byte, 32))
	assert.Error(t, err)
}

func TestCovenantClaimClosure_Determinism(t *testing.T) {
	receiver := fixedReceiver(t)
	server := fixedPub(t, strings.Repeat("02", 32))
	intro := fixedPub(t, strings.Repeat("03", 32))
	preimageHash := btcutil.Hash160(bytes.Repeat([]byte{0x42}, 32))

	a, err := CovenantClaimClosure(preimageHash, receiver, server, intro)
	require.NoError(t, err)
	b, err := CovenantClaimClosure(preimageHash, receiver, server, intro)
	require.NoError(t, err)

	scriptA, err := a.Script()
	require.NoError(t, err)
	scriptB, err := b.Script()
	require.NoError(t, err)
	assert.Equal(t, scriptA, scriptB)
}

func TestCovenantClaimClosure_NilPubkeyRejected(t *testing.T) {
	receiver := fixedReceiver(t)
	preimageHash := btcutil.Hash160(bytes.Repeat([]byte{0x42}, 32))

	_, err := CovenantClaimClosure(preimageHash, receiver, nil, fixedPub(t, strings.Repeat("03", 32)))
	assert.Error(t, err)
	_, err = CovenantClaimClosure(preimageHash, receiver, fixedPub(t, strings.Repeat("02", 32)), nil)
	assert.Error(t, err)
}

func TestCovenantClaimClosure_Shape(t *testing.T) {
	receiver := fixedReceiver(t)
	server := fixedPub(t, strings.Repeat("02", 32))
	intro := fixedPub(t, strings.Repeat("03", 32))
	preimageHash := btcutil.Hash160(bytes.Repeat([]byte{0x42}, 32))

	c, err := CovenantClaimClosure(preimageHash, receiver, server, intro)
	require.NoError(t, err)

	cmc, ok := c.(*script.ConditionMultisigClosure)
	require.True(t, ok, "expected ConditionMultisigClosure, got %T", c)
	require.Len(t, cmc.PubKeys, 2)

	enforcement, err := EnforcePayTo(receiver)
	require.NoError(t, err)
	expectedTweaked := arkade.ComputeArkadeScriptPublicKey(
		intro, arkade.ArkadeScriptHash(enforcement),
	)
	assert.True(t, cmc.PubKeys[0].IsEqual(server))
	assert.True(t, cmc.PubKeys[1].IsEqual(expectedTweaked))

	expectedCondition, err := preimageCondition(preimageHash)
	require.NoError(t, err)
	assert.Equal(t, expectedCondition, cmc.Condition)
}

func TestValidateArkadeScript_RoundTrip(t *testing.T) {
	receiver := fixedReceiver(t)
	arkadeScript, err := EnforcePayTo(receiver)
	require.NoError(t, err)

	parsed, err := ValidateArkadeScript(arkadeScript)
	require.NoError(t, err)
	assert.Equal(t, receiver, parsed)
}

func TestValidateArkadeScript_RejectsBadInput(t *testing.T) {
	t.Run("empty script", func(t *testing.T) {
		_, err := ValidateArkadeScript(nil)
		assert.Error(t, err)
	})

	t.Run("no OP_DATA_32 push", func(t *testing.T) {
		_, err := ValidateArkadeScript([]byte{txscript.OP_HASH160, txscript.OP_EQUAL})
		assert.Error(t, err)
	})

	t.Run("two OP_DATA_32 pushes", func(t *testing.T) {
		var bad bytes.Buffer
		bad.WriteByte(txscript.OP_DATA_32)
		bad.Write(bytes.Repeat([]byte{0x01}, 32))
		bad.WriteByte(txscript.OP_DATA_32)
		bad.Write(bytes.Repeat([]byte{0x02}, 32))
		_, err := ValidateArkadeScript(bad.Bytes())
		assert.Error(t, err)
	})

	t.Run("right OP_DATA_32 push but other bytes differ", func(t *testing.T) {
		good, err := EnforcePayTo(fixedReceiver(t))
		require.NoError(t, err)
		corrupted := append([]byte{}, good...)
		// Flip the last byte (OP_GREATERTHANOREQUAL) — corrupting a trailing
		// opcode can't accidentally restructure the script's tokenizer parse,
		// so the rejection must come from the bytes-equal check, not from a
		// secondary failure path.
		corrupted[len(corrupted)-1] ^= 0xFF
		_, err = ValidateArkadeScript(corrupted)
		assert.Error(t, err)
	})

	t.Run("hand-crafted two-output script — documents v1 restriction", func(t *testing.T) {
		// Plausible "two-output" arkade script: pin output[0] AND output[1] to
		// the same receiver. Contains exactly one OP_DATA_32 push (so
		// parseReceiverFromArkadeScript would succeed in isolation), but the
		// surrounding opcodes don't match EnforcePayTo's exact byte sequence.
		// ValidateArkadeScript must reject it.
		receiver := fixedReceiver(t)
		witnessProgram := receiver[2:]

		b := txscript.NewScriptBuilder()
		b.AddInt64(0)
		b.AddOp(txscript.OP_DUP)
		b.AddOp(arkade.OP_INSPECTOUTPUTSCRIPTPUBKEY)
		b.AddOp(arkade.OP_1)
		b.AddOp(arkade.OP_EQUALVERIFY)
		b.AddData(witnessProgram)
		b.AddOp(arkade.OP_EQUALVERIFY)
		b.AddOp(arkade.OP_INSPECTOUTPUTVALUE)
		b.AddInt64(1)
		fakeTwoOutputScript, err := b.Script()
		require.NoError(t, err)

		_, err = ValidateArkadeScript(fakeTwoOutputScript)
		assert.Error(t, err, "two-output arkade scripts must be rejected by v1")
	})
}
