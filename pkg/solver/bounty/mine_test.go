package bounty

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// freshClaimTx builds a minimal claim-shaped tx with a single input, one output
// to a P2TR receiver, one tip output, and an extension OP_RETURN at index 2.
// The extension carries an introspector packet only (no nonce yet) so the test
// matches what the plugin's BuildBatchClaim produces (a placeholder nonce slot
// that Mine overwrites).
func freshClaimTx(t *testing.T) (*psbt.Packet, extension.Extension, int) {
	t.Helper()

	receiver := freshP2TR(t)

	intro := extension.UnknownPacket{PacketType: 0xab, Data: []byte{0x01}} // stand-in introspector packet
	noncePkt, err := (&MiningNoncePacket{Nonce: make([]byte, NonceSize)}).ToExtensionPacket()
	require.NoError(t, err)

	baseExt, err := extension.NewExtensionFromPackets(intro)
	require.NoError(t, err)

	placeholderExt, err := extension.NewExtensionFromPackets(intro, noncePkt)
	require.NoError(t, err)
	extTxOut, err := placeholderExt.TxOut()
	require.NoError(t, err)

	tx := wire.NewMsgTx(wire.TxVersion)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: chainhash.Hash{0x01}, Index: 0},
	})
	tx.AddTxOut(&wire.TxOut{Value: 9_900, PkScript: receiver})
	tx.AddTxOut(&wire.TxOut{Value: 100, PkScript: receiver})
	tx.AddTxOut(extTxOut)

	pkt, err := psbt.NewFromUnsignedTx(tx)
	require.NoError(t, err)

	return pkt, baseExt, 2
}

func TestMine_DifficultyOne(t *testing.T) {
	pkt, baseExt, idx := freshClaimTx(t)
	nonce, err := Mine(t.Context(), pkt, baseExt, idx, 1)
	require.NoError(t, err)
	require.Len(t, nonce, NonceSize)

	hash := pkt.UnsignedTx.TxHash()
	assert.Equal(t, byte(0x00), hash[0], "txid first byte must be zero for difficulty=1")
}

func TestMine_DifficultyTwo(t *testing.T) {
	pkt, baseExt, idx := freshClaimTx(t)
	nonce, err := Mine(t.Context(), pkt, baseExt, idx, 2)
	require.NoError(t, err)
	require.Len(t, nonce, NonceSize)

	hash := pkt.UnsignedTx.TxHash()
	assert.Equal(t, []byte{0x00, 0x00}, hash[:2], "txid first two bytes must be zero for difficulty=2")
}

func TestMine_OnlyMutatesExtensionOutput(t *testing.T) {
	pkt, baseExt, idx := freshClaimTx(t)

	// Snapshot every output other than the OP_RETURN.
	type snap struct {
		pkScript []byte
		value    int64
	}
	pre := make([]snap, len(pkt.UnsignedTx.TxOut))
	for i, out := range pkt.UnsignedTx.TxOut {
		s := snap{pkScript: append([]byte(nil), out.PkScript...), value: out.Value}
		pre[i] = s
	}
	preInputs := make([]wire.TxIn, len(pkt.UnsignedTx.TxIn))
	for i, in := range pkt.UnsignedTx.TxIn {
		preInputs[i] = *in
	}

	_, err := Mine(t.Context(), pkt, baseExt, idx, 1)
	require.NoError(t, err)

	// Inputs unchanged.
	require.Len(t, pkt.UnsignedTx.TxIn, len(preInputs))
	for i, in := range pkt.UnsignedTx.TxIn {
		assert.Equal(t, preInputs[i].PreviousOutPoint, in.PreviousOutPoint, "input %d outpoint", i)
		assert.Equal(t, preInputs[i].Sequence, in.Sequence, "input %d sequence", i)
	}

	// Receiver + tip outputs unchanged.
	for i, out := range pkt.UnsignedTx.TxOut {
		if i == idx {
			continue
		}
		assert.Equal(t, pre[i].value, out.Value, "output %d value", i)
		assert.True(t, bytes.Equal(pre[i].pkScript, out.PkScript), "output %d pkScript", i)
	}

	// Extension output changed.
	assert.False(t, bytes.Equal(pre[idx].pkScript, pkt.UnsignedTx.TxOut[idx].PkScript),
		"extension OP_RETURN must have been mutated by mining")
}

func TestMine_HonorsContextCancellation(t *testing.T) {
	pkt, baseExt, idx := freshClaimTx(t)

	// Pre-cancelled context returns ctx.Err() immediately, before any iteration.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Mine(ctx, pkt, baseExt, idx, 8) // would otherwise take ages
	assert.ErrorIs(t, err, context.Canceled)

	// Cancellation mid-loop also returns within a bounded time.
	pkt2, baseExt2, idx2 := freshClaimTx(t)
	ctx2, cancel2 := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel2()
	start := time.Now()
	_, err = Mine(ctx2, pkt2, baseExt2, idx2, 32) // 2^256 expected — impossible
	elapsed := time.Since(start)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, 2*time.Second, "Mine must abort within ~timeout, took %v", elapsed)
}

func TestMine_Validates(t *testing.T) {
	pkt, baseExt, idx := freshClaimTx(t)
	_, err := Mine(t.Context(), pkt, baseExt, idx, 0)
	assert.Error(t, err)
	_, err = Mine(t.Context(), pkt, baseExt, idx, 33)
	assert.Error(t, err)
	_, err = Mine(t.Context(), pkt, baseExt, -1, 1)
	assert.Error(t, err)
	_, err = Mine(t.Context(), pkt, baseExt, len(pkt.UnsignedTx.TxOut), 1)
	assert.Error(t, err)
	_, err = Mine(t.Context(), nil, baseExt, idx, 1)
	assert.Error(t, err)
}

// silence unused import warning if we ever drop txscript usage in this file
var _ = txscript.OP_RETURN
