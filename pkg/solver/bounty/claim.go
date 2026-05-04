package bounty

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ArkLabsHQ/introspector/pkg/arkade"
	introclient "github.com/ArkLabsHQ/introspector/pkg/client"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
)

// BuildBatchClaim assembles the unmined claim tx for a batch of bounties.
// Each entry's arkade enforcement script is built from its OWN difficulty —
// mixed-difficulty batching is supported. The returned tx still carries an
// all-zero placeholder nonce in its extension OP_RETURN; call Mine with the
// max difficulty across the batch (a higher-difficulty txid satisfies all
// lower per-entry checks).
//
// baseExt is the extension content (introspector packet only) that Mine should
// append the MiningNoncePacket to. extOutputIdx is the index of the OP_RETURN
// output in the tx that Mine should mutate.
func BuildBatchClaim(
	batch []*MatchedBounty,
	takerPkScript []byte,
	checkpointTapscriptBytes []byte,
) (
	arkTx *psbt.Packet,
	checkpoints []*psbt.Packet,
	baseExt extension.Extension,
	extOutputIdx int,
	err error,
) {
	if len(batch) == 0 {
		return nil, nil, nil, 0, fmt.Errorf("empty batch")
	}
	if len(batch) > 0xFFFF {
		return nil, nil, nil, 0, fmt.Errorf("batch too large: %d entries (max %d)", len(batch), 0xFFFF)
	}
	if _, err := p2trWitnessProgram(takerPkScript); err != nil {
		return nil, nil, nil, 0, fmt.Errorf("invalid taker pkScript: %w", err)
	}

	// Build per-input introspector entries — each uses its own difficulty.
	entries := make([]arkade.IntrospectorEntry, len(batch))
	for i, m := range batch {
		s, err := payToAndPoW(m.ReceiverPkScript, m.Difficulty)
		if err != nil {
			return nil, nil, nil, 0, fmt.Errorf("entry %d enforcement script: %w", i, err)
		}
		entries[i] = arkade.IntrospectorEntry{
			Vin:     uint16(i),
			Script:  s,
			Witness: wire.TxWitness{},
		}
	}
	introPacket, err := arkade.NewPacket(entries...)
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("build introspector packet: %w", err)
	}

	// arkade.IntrospectorPacket already implements extension.Packet — no need
	// to re-wrap as UnknownPacket.
	baseExt, err = extension.NewExtensionFromPackets(introPacket)
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("build base extension: %w", err)
	}

	// Placeholder mining nonce for the initial tx; Mine will overwrite the OP_RETURN.
	placeholderNonce := &MiningNoncePacket{Nonce: make([]byte, NonceSize)}
	placeholderNoncePkt, err := placeholderNonce.ToExtensionPacket()
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("build placeholder nonce packet: %w", err)
	}
	placeholderExt, err := extension.NewExtensionFromPackets(introPacket, placeholderNoncePkt)
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("build placeholder extension: %w", err)
	}
	extTxOut, err := placeholderExt.TxOut()
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("build extension TxOut: %w", err)
	}

	// Build outputs: receiver_i, then bot tip, then OP_RETURN.
	outputs := make([]*wire.TxOut, 0, len(batch)+2)
	totalFee := int64(MinerFeeSats) * int64(len(batch))
	for _, m := range batch {
		outputs = append(outputs, &wire.TxOut{
			Value:    int64(m.Amount - MinerFeeSats),
			PkScript: slices.Clone(m.ReceiverPkScript),
		})
	}
	outputs = append(outputs, &wire.TxOut{Value: totalFee, PkScript: slices.Clone(takerPkScript)})
	extOutputIdx = len(outputs)
	outputs = append(outputs, extTxOut)

	// Build inputs.
	vtxoInputs := make([]offchain.VtxoInput, len(batch))
	for i, m := range batch {
		input, err := buildBountyInput(m)
		if err != nil {
			return nil, nil, nil, 0, fmt.Errorf("build input %d: %w", i, err)
		}
		vtxoInputs[i] = input
	}

	arkTx, checkpoints, err = offchain.BuildTxs(vtxoInputs, outputs, checkpointTapscriptBytes)
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("build offchain txs: %w", err)
	}

	return arkTx, checkpoints, baseExt, extOutputIdx, nil
}

// SubmitBatchClaim signs Alice's leaves (no-op for bounty closures with no
// taker key) and submits the bundle via the introspector. Returns the
// finalized ark txid.
func SubmitBatchClaim(
	ctx context.Context,
	arkClient arksdk.ArkClient,
	introClient introclient.TransportClient,
	arkTx *psbt.Packet,
	checkpoints []*psbt.Packet,
) (string, error) {
	signedArk, err := signB64(ctx, arkClient, arkTx)
	if err != nil {
		return "", fmt.Errorf("sign ark tx: %w", err)
	}
	signedCheckpoints := make([]string, len(checkpoints))
	for i, cp := range checkpoints {
		s, err := signB64(ctx, arkClient, cp)
		if err != nil {
			return "", fmt.Errorf("sign checkpoint %d: %w", i, err)
		}
		signedCheckpoints[i] = s
	}
	finalArkB64, _, err := introClient.SubmitTx(ctx, signedArk, signedCheckpoints)
	if err != nil {
		return "", fmt.Errorf("introspector submit: %w", err)
	}
	finalPtx, err := psbt.NewFromRawBytes(strings.NewReader(finalArkB64), true)
	if err != nil {
		return "", fmt.Errorf("decode finalized claim tx: %w", err)
	}
	return finalPtx.UnsignedTx.TxHash().String(), nil
}

// signB64 base64-encodes a psbt and runs it through arkClient.SignTransaction.
// The bounty closures contain no taker key so signing is a no-op for our inputs;
// the call is kept for uniformity (introspector requires non-arkd signatures
// to be present before forwarding, which is harmless when none exist).
func signB64(ctx context.Context, arkClient arksdk.ArkClient, p *psbt.Packet) (string, error) {
	b64, err := p.B64Encode()
	if err != nil {
		return "", err
	}
	return arkClient.SignTransaction(ctx, b64)
}

// buildBountyInput constructs the offchain.VtxoInput for a single bounty's
// claim closure (the only closure in the bounty vtxo script).
func buildBountyInput(m *MatchedBounty) (offchain.VtxoInput, error) {
	if m.VtxoScript == nil || len(m.VtxoScript.Closures) == 0 {
		return offchain.VtxoInput{}, fmt.Errorf("nil or empty vtxo script")
	}
	closureScript, err := m.VtxoScript.Closures[0].Script()
	if err != nil {
		return offchain.VtxoInput{}, fmt.Errorf("build closure script: %w", err)
	}
	_, tapTree, err := m.VtxoScript.TapTree()
	if err != nil {
		return offchain.VtxoInput{}, fmt.Errorf("build taptree: %w", err)
	}
	leafHash := txscript.NewBaseTapLeaf(closureScript).TapHash()
	merkleProof, err := tapTree.GetTaprootMerkleProof(leafHash)
	if err != nil {
		return offchain.VtxoInput{}, fmt.Errorf("merkle proof: %w", err)
	}
	controlBlock, err := txscript.ParseControlBlock(merkleProof.ControlBlock)
	if err != nil {
		return offchain.VtxoInput{}, fmt.Errorf("parse control block: %w", err)
	}
	revealed, err := m.VtxoScript.Encode()
	if err != nil {
		return offchain.VtxoInput{}, fmt.Errorf("encode tapscripts: %w", err)
	}
	op := m.Outpoint // chainhash.Hash is [32]byte: struct copy is a deep copy.
	return offchain.VtxoInput{
		Outpoint:           &op,
		Amount:             int64(m.Amount),
		Tapscript:          &waddrmgr.Tapscript{ControlBlock: controlBlock, RevealedScript: merkleProof.Script},
		RevealedTapscripts: revealed,
	}, nil
}
